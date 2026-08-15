// Command mesguard-agent-paired-eval 对同一版本化样本顺序运行 baseline 和 experiment。
// 输出是可交给 mesguard-agent-eval 汇总的真实 EvaluationObservation JSONL。
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/chitandabb/GoAgent/internal/evaluationidentity"
	"github.com/chitandabb/GoAgent/internal/externalcase"
	platformchatmodel "github.com/chitandabb/GoAgent/internal/platform/chatmodel"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/platform/githubmcp"
	platformlogger "github.com/chitandabb/GoAgent/internal/platform/logger"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	platformsqlserver "github.com/chitandabb/GoAgent/internal/platform/sqlserver"
	"github.com/chitandabb/GoAgent/internal/resilience"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

var pairedEvalCaseID = uuid.MustParse("11111111-1111-1111-1111-111111111111")

const evidenceGateReviewedCaseTargetForProviderRun = 30

type pairedEvalCaseGetter struct{}

func (pairedEvalCaseGetter) Get(_ context.Context, id uuid.UUID) (*externalcase.ExternalCase, error) {
	if id != pairedEvalCaseID {
		return nil, errors.New("paired evaluation synthetic case not found")
	}
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	return &externalcase.ExternalCase{
		ID: id, ExternalCaseKey: "EVAL-001", CaseType: "incident",
		Title:       "工位报工后状态未更新",
		Description: "操作员完成报工，ERP 工单仍显示处理中，现场网络正常。",
		Category:    "workflow", Module: "work-reporting",
		Status: externalcase.StatusOpen, Priority: externalcase.PriorityMedium,
		ReportedAt: now, SourceUpdatedAt: now,
		SourceFingerprint: "agent-evaluation-real-v1",
	}, nil
}

func main() {
	log := platformlogger.NewBootstrapFor("mesguard-agent-paired-eval")
	defer platformlogger.Sync(log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], log); err != nil {
		log.Error("Agent paired evaluation failed", zap.Error(err))
		platformlogger.Sync(log)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, log *zap.Logger) error {
	return runWithDependencies(ctx, args, log, defaultPairedEvalDependencies())
}

// pairedEvalDependencies 是 runWithDependencies 的注入点：离线测试用 stub
// 替换所有真实 Provider/远端连接工厂，证明两臂可比性 preflight 发生在任何
// Provider 创建之前（factory.calls == 0）。
type pairedEvalDependencies struct {
	loadConfig                func() (config.Config, error)
	newChatModel              func(context.Context, config.ChatModelConfig) (model.ToolCallingChatModel, error)
	connectGitHub             func(context.Context, config.GitHubMCPConfig, *zap.Logger) (*githubmcp.Connection, error)
	verifyPairedComparability func([]*schema.ToolInfo, []*schema.ToolInfo) (mesagent.ToolSelectionComparability, error)
}

// pairedEvaluationAssembly is the exact startup-Epoch assembly accepted by
// the comparability preflight. Runtime Runners reuse these Catalog and Skill
// objects and re-check their arm-specific Schema fingerprint before invoking
// any model, so preflight cannot validate one contract and execute another.
type pairedEvaluationAssembly struct {
	skillRuntime                *mesagent.NativeSkillRuntime
	productionCatalog           *mesagent.ToolCatalog
	wideCatalog                 *mesagent.ToolCatalog
	productionSchemaFingerprint string
	wideSchemaFingerprint       string
	comparability               mesagent.ToolSelectionComparability
}

func defaultPairedEvalDependencies() pairedEvalDependencies {
	return pairedEvalDependencies{
		loadConfig: config.Load,
		newChatModel: func(ctx context.Context, models config.ChatModelConfig) (model.ToolCallingChatModel, error) {
			instance, err := platformchatmodel.NewActive(ctx, models)
			if err != nil {
				return nil, err
			}
			return instance.Model, nil
		},
		connectGitHub:             githubmcp.Connect,
		verifyPairedComparability: mesagent.VerifyToolSelectionComparability,
	}
}

func runWithDependencies(ctx context.Context, args []string, log *zap.Logger, deps pairedEvalDependencies) error {
	flags := flag.NewFlagSet("mesguard-agent-paired-eval", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	datasetPath := flags.String("dataset", "testdata/agent-evaluation.real-v1.jsonl", "versioned JSONL evaluation cases")
	outputPath := flags.String("output", "testdata/agent-evaluation.real-v3.observations.jsonl", "output JSONL observations")
	reasoningEffort := flags.String("reasoning-effort", "", "provider-supported effort; defaults to config")
	maxTotalTokens := flags.Int("max-total-tokens", 0, "override the Evidence Gate total token budget; defaults to config")
	comparison := flags.String("comparison", "tool-selection", "paired variable: tool-selection or evidence-gate")
	allowProviderCalls := flags.Bool("allow-provider-calls", false, "explicitly authorize Provider calls for evidence-gate comparison")
	maxCases := flags.Int("max-cases", 0, "maximum evidence-gate cases authorized for this Provider run")
	maxProviderCalls := flags.Int("max-provider-calls", 0, "maximum estimated Provider calls authorized for this evidence-gate run")
	maxProviderTokens := flags.Int("max-provider-tokens", 0, "maximum total Token budget authorized across both evidence-gate arms")
	allowDirty := flags.Bool("allow-dirty", false, "accept a dirty/unknown implementation revision for local smoke; results are NOT formal metrics")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("usage: mesguard-agent-paired-eval [-dataset path] [-output path] [-reasoning-effort provider-value]: %w", err)
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *comparison != "tool-selection" && *comparison != "evidence-gate" {
		return errors.New("comparison must be tool-selection or evidence-gate")
	}

	cases, err := readEvaluationCases(*datasetPath)
	if err != nil {
		return fmt.Errorf("read evaluation dataset: %w", err)
	}
	cfg, err := deps.loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !cfg.Models.Chat.Enabled {
		return errors.New("chat model is disabled")
	}
	if !cfg.GitHubMCP.Enabled {
		return errors.New("github MCP must be enabled for paired evaluation")
	}
	profile, err := cfg.Models.Chat.ActiveProfile()
	if err != nil {
		return err
	}
	if strings.TrimSpace(*reasoningEffort) != "" {
		profile.ReasoningEffort = strings.ToLower(strings.TrimSpace(*reasoningEffort))
	}
	if _, err := parseReasoningEffort(profile.ReasoningEffort); err != nil {
		return err
	}
	cfg.Models.Chat.Profiles[cfg.Models.Chat.ActiveProfileName] = profile
	// 身份校验必须在调用任何收费 Provider 之前完成：先解析实现 revision 与
	// 最终模型 Profile 指纹，再决定是否允许继续。
	identity, identityErr := evaluationidentity.ResolveImplementationIdentity()
	if identityErr != nil && !*allowDirty {
		return fmt.Errorf("resolve implementation revision: %w (pass -allow-dirty for local smoke)", identityErr)
	}
	identity, decisionErr := evaluationidentity.EvaluateImplementationIdentity(identity, *allowDirty)
	if decisionErr != nil {
		return decisionErr
	}
	if identityErr != nil || identity.Dirty || identity.Revision == "unknown" {
		log.Warn("dirty or unknown implementation revision accepted for local smoke only; observations are NOT formal metrics",
			zap.String("revision", identity.Revision), zap.Bool("dirty", identity.Dirty))
	}
	// 指纹基于写回后的最终 Profile 计算，保证记录的指纹与实际模型调用配置一致。
	modelProfileFingerprint, fingerprintErr := profile.PromptProfileFingerprint(cfg.Models.Chat.ActiveProfileName)
	if fingerprintErr != nil {
		return fmt.Errorf("compute model profile fingerprint: %w", fingerprintErr)
	}
	if *maxTotalTokens != 0 {
		if *maxTotalTokens < 1000 || *maxTotalTokens > 1_000_000 {
			return errors.New("max-total-tokens must be between 1000 and 1000000")
		}
		cfg.Agent.MaxTotalTokens = *maxTotalTokens
	}
	if *comparison == "evidence-gate" {
		budget, budgetErr := validateEvidenceGateProviderBudget(
			len(cases), cfg.Agent.MaxAgentRuns, cfg.Agent.MaxToolCalls, cfg.Agent.MaxTotalTokens,
			*allowProviderCalls, *maxCases, *maxProviderCalls, *maxProviderTokens,
		)
		if budgetErr != nil {
			return budgetErr
		}
		log.Info("Evidence Gate Provider paired run authorized",
			zap.Int("cases", budget.Cases),
			zap.Int("estimated_provider_call_upper_bound", budget.ProviderCalls),
			zap.Int("embedding_request_upper_bound", 0),
			zap.Int("rerank_request_upper_bound", 0),
			zap.Int("total_token_budget_upper_bound", budget.TotalTokens),
		)
	}
	prompts, err := cfg.Agent.LoadPrompts()
	if err != nil {
		return fmt.Errorf("load Agent prompts: %w", err)
	}

	githubConnection, err := deps.connectGitHub(ctx, cfg.GitHubMCP, log.Named("github_mcp"))
	if err != nil {
		return fmt.Errorf("connect GitHub MCP: %w", err)
	}
	defer githubConnection.Close()

	var sqlDB *sql.DB
	var sqlObjectDefinition tool.BaseTool
	var schemaCatalog tool.BaseTool
	var readonlyQuery tool.BaseTool
	var closePostgres func() error
	var sqlCatalogFixture *sqlCatalogEvaluationFixture
	sqlEnabled := evaluationDatasetHasTag(cases, "sql-enabled") || evaluationDatasetHasTag(cases, "sql-query-enabled")
	if sqlEnabled {
		if !cfg.SQLServer.Enabled {
			return errors.New("SQL Server must be enabled for SQL evaluation cases")
		}
		sqlDB, err = platformsqlserver.Open(ctx, cfg.SQLServer)
		if err != nil {
			return fmt.Errorf("open SQL Server for paired evaluation: %w", err)
		}
		defer sqlDB.Close()
		if err := sqlDB.PingContext(ctx); err != nil {
			return fmt.Errorf("ping SQL Server for paired evaluation: %w", err)
		}
		reader, readerErr := platformsqlserver.NewObjectDefinitionReader(sqlDB, cfg.SQLServer, log.Named("sqlserver_investigation"))
		if readerErr != nil {
			return fmt.Errorf("build SQL object definition reader: %w", readerErr)
		}
		sqlObjectDefinition, err = mesagent.NewDatabaseObjectDefinitionTool(reader)
		if err != nil {
			return fmt.Errorf("build SQL object definition Tool: %w", err)
		}
	}
	if evaluationDatasetHasTag(cases, "sql-query-enabled") {
		postgresDB, postgresClose, postgresErr := platformpostgres.Open(ctx, cfg.Postgres, log.Named("postgres_eval"))
		if postgresErr != nil {
			return fmt.Errorf("open PostgreSQL for SQL Catalog evaluation: %w", postgresErr)
		}
		closePostgres = postgresClose
		defer func() { _ = closePostgres() }()
		sqlDataSourceID, parseErr := uuid.Parse(cfg.SQLServer.ID)
		if parseErr != nil {
			return fmt.Errorf("parse SQL data source id for Catalog evaluation: %w", parseErr)
		}
		sqlCatalogFixture, err = beginSQLCatalogEvaluationFixture(ctx, postgresDB, sqlDataSourceID)
		if err != nil {
			return fmt.Errorf("begin SQL Catalog evaluation fixture: %w", err)
		}
		defer func() { _ = sqlCatalogFixture.Rollback() }()
		catalog := platformpostgres.NewSchemaCatalogRepository(sqlCatalogFixture.DB())
		schemaCatalog, err = mesagent.NewSearchSchemaCatalogTool(catalog)
		if err != nil {
			return fmt.Errorf("build SQL Catalog search Tool: %w", err)
		}
		readonlyExecutor, executorErr := platformsqlserver.NewReadonlyQueryExecutor(
			sqlDB, cfg.SQLServer, catalog, log.Named("sqlserver_readonly_query"),
		)
		if executorErr != nil {
			return fmt.Errorf("build readonly query executor: %w", executorErr)
		}
		readonlyQuery, err = mesagent.NewExecuteReadonlyQueryTool(readonlyExecutor)
		if err != nil {
			return fmt.Errorf("build readonly query Tool: %w", err)
		}
	}

	// 两臂可比性 preflight 必须在创建任何收费 Provider 之前完成：production
	// （diagnosis-default）与 baseline（evaluation-wide-v2）经过同一个真实
	// Eino Skill Middleware，VerifyToolSelectionComparability 校验共享 Schema
	// 一致性与严格超集；不一致直接 fail-closed，不发起任何模型调用。
	assembly, err := verifyPairedArmsComparability(
		ctx, cfg, githubConnection.Tools, sqlObjectDefinition, schemaCatalog, readonlyQuery,
		deps.verifyPairedComparability,
	)
	if err != nil {
		return fmt.Errorf("paired arms comparability preflight: %w", err)
	}

	chatModel, err := deps.newChatModel(ctx, cfg.Models.Chat)
	if err != nil {
		return fmt.Errorf("build chat model: %w", err)
	}

	var output *os.File
	if *comparison == "evidence-gate" {
		output, err = os.OpenFile(*outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	} else {
		output, err = os.Create(*outputPath)
	}
	if err != nil {
		return fmt.Errorf("create observations output: %w", err)
	}
	defer output.Close()
	encoder := json.NewEncoder(output)

	for _, definition := range cases {
		if definition.TaskType != "diagnosis" {
			return fmt.Errorf("case %q uses unsupported paired evaluation task type %q", definition.CaseID, definition.TaskType)
		}
		access, accessErr := newPairedEvaluationRunAccess(definition, cfg)
		if accessErr != nil {
			return fmt.Errorf("build run access for case %q: %w", definition.CaseID, accessErr)
		}
		pairingFingerprint, fingerprintErr := evidenceGatePairingFingerprint(definition, cfg)
		if fingerprintErr != nil {
			return fmt.Errorf("fingerprint case %q: %w", definition.CaseID, fingerprintErr)
		}
		for _, variant := range []mesagent.EvaluationVariant{mesagent.EvaluationBaseline, mesagent.EvaluationExperiment} {
			if err := ctx.Err(); err != nil {
				return err
			}
			orchestrator, toolSchemaFingerprint, buildErr := buildPairedEvaluationRun(
				ctx, cfg, prompts, chatModel, log, variant, *comparison, assembly,
			)
			if buildErr != nil {
				return fmt.Errorf("build %s run for case %q: %w", variant, definition.CaseID, buildErr)
			}
			startedAt := time.Now()
			result, invokeErr := orchestrator.Invoke(
				agentruntime.WithRunAccess(ctx, access),
				mesagent.RunRequest{
					UserQuery: definition.UserQuery, ExternalCaseID: pairedEvalCaseID.String(),
				},
			)
			if invokeErr != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if *comparison != "evidence-gate" {
					return fmt.Errorf("invoke %s run for case %q: %w", variant, definition.CaseID, invokeErr)
				}
			}
			duration := time.Since(startedAt)
			if *comparison == "evidence-gate" {
				observation := evidenceGateObservationFromResult(
					definition, variant, cfg, pairingFingerprint, result, duration, invokeErr,
				)
				if err := observation.Validate(); err != nil {
					return fmt.Errorf("validate %s Evidence Gate observation for case %q: %w", variant, definition.CaseID, err)
				}
				if err := encoder.Encode(observation); err != nil {
					return fmt.Errorf("write %s Evidence Gate observation for case %q: %w", variant, definition.CaseID, err)
				}
				if invokeErr != nil {
					log.Warn("Evidence Gate paired arm failed and was recorded",
						zap.String("case_id", definition.CaseID),
						zap.String("variant", string(variant)),
						zap.String("error_type", observation.ErrorType),
					)
					continue
				}
			} else {
				observation := observationFromResult(
					definition, variant, cfg, result, duration, toolSchemaFingerprint,
					assembly.comparability, identity, modelProfileFingerprint,
				)
				if err := observation.Validate(); err != nil {
					return fmt.Errorf("validate %s observation for case %q: %w", variant, definition.CaseID, err)
				}
				if err := encoder.Encode(observation); err != nil {
					return fmt.Errorf("write %s observation for case %q: %w", variant, definition.CaseID, err)
				}
			}
			log.Info("Agent paired evaluation run completed",
				zap.String("case_id", definition.CaseID), zap.String("variant", string(variant)),
				zap.Int("total_tokens", result.Usage.TotalTokens), zap.Int("tool_calls", len(result.ToolExecutions)),
				zap.Duration("duration", time.Since(startedAt)),
			)
		}
	}
	return nil
}

func buildPairedEvaluationRun(
	ctx context.Context,
	cfg config.Config,
	prompts config.AgentPrompts,
	chatModel model.ToolCallingChatModel,
	log *zap.Logger,
	variant mesagent.EvaluationVariant,
	comparison string,
	assembly pairedEvaluationAssembly,
) (*mesagent.EvidenceOrchestrator, string, error) {
	catalog := assembly.productionCatalog
	mode := mesagent.RunnerModeExperiment
	expectedSchemaFingerprint := assembly.productionSchemaFingerprint
	if comparison == "tool-selection" && variant == mesagent.EvaluationBaseline {
		catalog = assembly.wideCatalog
		mode = mesagent.RunnerModeBaseline
		expectedSchemaFingerprint = assembly.wideSchemaFingerprint
	}
	if catalog == nil || assembly.skillRuntime == nil || expectedSchemaFingerprint == "" {
		return nil, "", errors.New("paired evaluation preflight assembly is incomplete")
	}
	runner, err := mesagent.NewRunner(mesagent.RunnerConfig{
		ChatModel: chatModel, ToolCatalog: catalog, SkillRuntime: assembly.skillRuntime,
		SystemInstruction:     prompts.SystemInstruction,
		BaselineInstruction:   prompts.BaselineInstruction,
		Mode:                  mode,
		GitHubArgumentRewrite: githubmcp.NewArgumentRewriter(),
		Logger:                log.Named(string(variant)),
	})
	if err != nil {
		return nil, "", fmt.Errorf("build %s Agent runner: %w", variant, err)
	}
	// toolSchemaFingerprint 是实验臂特有合同：由本 Runner 绑定 Profile 的
	// 模型可见 Tool Schema 规范指纹（启动 Epoch 内固定），同 variant 跨样本
	// 必须一致。
	toolSchemaFingerprint, fingerprintErr := runner.ProfileToolSchemaFingerprint(ctx)
	if fingerprintErr != nil {
		return nil, "", fmt.Errorf("compute %s Tool Schema fingerprint: %w", variant, fingerprintErr)
	}
	if toolSchemaFingerprint != expectedSchemaFingerprint {
		return nil, "", fmt.Errorf(
			"%s runtime Tool Schema fingerprint %q differs from preflight Tool Schema %q",
			variant, toolSchemaFingerprint, expectedSchemaFingerprint,
		)
	}
	orchestrator, err := mesagent.NewEvidenceOrchestrator(ctx, mesagent.EvidenceOrchestratorConfig{
		Runner: runner, Logger: log.Named("evidence_" + string(variant)),
		ReportPolicy:     resilience.PolicyRepairThenFail,
		DisableEarlyExit: comparison == "evidence-gate" && variant == mesagent.EvaluationBaseline,
		MaxAgentRuns:     cfg.Agent.MaxAgentRuns, MaxToolCalls: cfg.Agent.MaxToolCalls,
		MaxEvidenceItems: cfg.Agent.MaxEvidenceItems, MaxTotalTokens: cfg.Agent.MaxTotalTokens,
		Timeout:                   time.Duration(cfg.Agent.TimeoutMillis) * time.Millisecond,
		ReportContractInstruction: prompts.ReportContractInstruction,
	})
	if err != nil {
		return nil, "", fmt.Errorf("build Evidence orchestrator: %w", err)
	}
	return orchestrator, toolSchemaFingerprint, nil
}

// pairedArmToolInfos 把一臂装配成最终模型可见的 ToolInfo 列表：与 Runner
// 相同的真实装配链（ToolAuthorizationMiddleware -> Eino Skill Middleware），
// 不伪造任何 Schema。
func pairedArmToolInfos(
	ctx context.Context,
	authorization *mesagent.ToolAuthorizationMiddleware,
	skillMiddleware adk.ChatModelAgentMiddleware,
	access agentruntime.RunAccess,
) ([]*schema.ToolInfo, string, error) {
	_, authorizedCtx, authErr := authorization.BeforeAgent(
		agentruntime.WithRunAccess(ctx, access),
		&adk.ChatModelAgentContext{Tools: nil},
	)
	if authErr != nil {
		return nil, "", fmt.Errorf("assemble Tool schema: %w", authErr)
	}
	_, finalCtx, skillErr := skillMiddleware.BeforeAgent(
		agentruntime.WithRunAccess(ctx, access),
		authorizedCtx,
	)
	if skillErr != nil {
		return nil, "", fmt.Errorf("append skill Tool: %w", skillErr)
	}
	fingerprint, fingerprintErr := mesagent.CanonicalToolContractFingerprint(ctx, finalCtx.Tools)
	if fingerprintErr != nil {
		return nil, "", fmt.Errorf("fingerprint final Tool schema: %w", fingerprintErr)
	}
	infos := make([]*schema.ToolInfo, 0, len(finalCtx.Tools))
	for _, current := range finalCtx.Tools {
		info, infoErr := current.Info(ctx)
		if infoErr != nil {
			return nil, "", infoErr
		}
		infos = append(infos, info)
	}
	return infos, fingerprint, nil
}

// pairedPreflightRunAccess 构造 preflight 装配用的最小合法诊断 RunAccess：
// 装配层只校验 RunAccess 存在与 Profile 匹配，preflight 不执行任何 Tool。
func pairedPreflightRunAccess() (agentruntime.RunAccess, error) {
	permissions, err := agentruntime.NewPermissionSet(agentruntime.PermissionCaseRead)
	if err != nil {
		return agentruntime.RunAccess{}, err
	}
	grants, err := agentruntime.NewResourceGrants(agentruntime.ResourceGrantsConfig{
		ExternalCaseIDs: []uuid.UUID{pairedEvalCaseID},
	})
	if err != nil {
		return agentruntime.RunAccess{}, err
	}
	policy, err := agentruntime.NewInvestigationPolicy(diagnosis.InvestigationPolicySchemaVersion, permissions, grants)
	if err != nil {
		return agentruntime.RunAccess{}, err
	}
	return agentruntime.DeriveDiagnosisRunAccess(
		policy,
		agentruntime.Actor{UserID: uuid.MustParse("22222222-2222-2222-2222-222222222222"), Role: auth.RoleAnalyst},
		agentruntime.AccessCeiling{Permissions: permissions, Grants: grants},
	)
}

// verifyPairedArmsComparability 在创建任何收费 Provider 之前装配两臂并执行
// 可比性 preflight：production（diagnosis-default）与 baseline
// （evaluation-wide-v2）使用同一个真实 Eino Skill Middleware 与同一组注册
// Tool；VerifyToolSelectionComparability 校验名字集合、共享 Schema 与严格
// 超集，任何漂移直接 fail-closed。
func verifyPairedArmsComparability(
	ctx context.Context,
	cfg config.Config,
	githubTools []tool.BaseTool,
	sqlObjectDefinition, schemaCatalog, readonlyQuery tool.BaseTool,
	verify func([]*schema.ToolInfo, []*schema.ToolInfo) (mesagent.ToolSelectionComparability, error),
) (pairedEvaluationAssembly, error) {
	skillRuntime, err := mesagent.NewNativeSkillRuntime(ctx, cfg.Agent.SkillsDirectory)
	if err != nil {
		return pairedEvaluationAssembly{}, fmt.Errorf("build native Skill runtime: %w", err)
	}
	dependencies := mesagent.DefaultToolCatalogDependencies{
		ExternalCases:        pairedEvalCaseGetter{},
		SkillReference:       skillRuntime.ReferenceTool,
		GitHubTools:          githubTools,
		SQLObjectDefinitions: sqlObjectDefinition,
		SchemaCatalog:        schemaCatalog,
		ReadonlyQuery:        readonlyQuery,
	}
	productionCatalog, err := mesagent.NewDiagnosisDefaultToolCatalog(ctx, dependencies)
	if err != nil {
		return pairedEvaluationAssembly{}, fmt.Errorf("build production Tool catalog: %w", err)
	}
	wideCatalog, err := mesagent.NewEvaluationWideDefaultToolCatalog(ctx, dependencies)
	if err != nil {
		return pairedEvaluationAssembly{}, fmt.Errorf("build wide Tool catalog: %w", err)
	}
	productionAuthorization, err := mesagent.NewToolAuthorizationMiddleware(productionCatalog, agentruntime.ToolProfileDiagnosis)
	if err != nil {
		return pairedEvaluationAssembly{}, fmt.Errorf("build production Tool authorization middleware: %w", err)
	}
	wideAuthorization, err := mesagent.NewToolAuthorizationMiddleware(wideCatalog, agentruntime.ToolProfileEvaluationWide)
	if err != nil {
		return pairedEvaluationAssembly{}, fmt.Errorf("build wide Tool authorization middleware: %w", err)
	}
	access, err := pairedPreflightRunAccess()
	if err != nil {
		return pairedEvaluationAssembly{}, err
	}
	productionInfos, productionFingerprint, err := pairedArmToolInfos(
		ctx, productionAuthorization, skillRuntime.Middleware, access,
	)
	if err != nil {
		return pairedEvaluationAssembly{}, fmt.Errorf("assemble production arm: %w", err)
	}
	wideInfos, wideFingerprint, err := pairedArmToolInfos(
		ctx, wideAuthorization, skillRuntime.Middleware, access,
	)
	if err != nil {
		return pairedEvaluationAssembly{}, fmt.Errorf("assemble wide arm: %w", err)
	}
	comparability, err := verify(productionInfos, wideInfos)
	if err != nil {
		return pairedEvaluationAssembly{}, err
	}
	return pairedEvaluationAssembly{
		skillRuntime:                skillRuntime,
		productionCatalog:           productionCatalog,
		wideCatalog:                 wideCatalog,
		productionSchemaFingerprint: productionFingerprint,
		wideSchemaFingerprint:       wideFingerprint,
		comparability:               comparability,
	}, nil
}

type evidenceGateProviderBudget struct {
	Cases         int
	ProviderCalls int
	TotalTokens   int
}

func validateEvidenceGateProviderBudget(
	cases int,
	maxAgentRuns int,
	maxToolCalls int,
	maxTotalTokens int,
	allowed bool,
	caseLimit int,
	providerCallLimit int,
	totalTokenLimit int,
) (evidenceGateProviderBudget, error) {
	if !allowed {
		return evidenceGateProviderBudget{}, errors.New("evidence-gate Provider run requires -allow-provider-calls")
	}
	if cases < 1 || caseLimit < 1 || providerCallLimit < 1 || totalTokenLimit < 1 {
		return evidenceGateProviderBudget{}, errors.New("evidence-gate Provider run requires positive -max-cases, -max-provider-calls, and -max-provider-tokens")
	}
	if cases > evidenceGateReviewedCaseTargetForProviderRun || caseLimit > evidenceGateReviewedCaseTargetForProviderRun {
		return evidenceGateProviderBudget{}, fmt.Errorf("evidence-gate Provider run is capped at %d reviewed cases", evidenceGateReviewedCaseTargetForProviderRun)
	}
	if cases > caseLimit {
		return evidenceGateProviderBudget{}, fmt.Errorf("dataset has %d cases, exceeds authorized max-cases %d", cases, caseLimit)
	}
	if maxAgentRuns < 1 || maxToolCalls < 1 || maxTotalTokens < 1 {
		return evidenceGateProviderBudget{}, errors.New("effective Agent run, Tool call, and Token budgets must be positive")
	}
	budget := evidenceGateProviderBudget{
		Cases: cases,
		// One model turn produces either a final answer or one Tool call. This is a
		// conservative upper bound across both paired arms.
		ProviderCalls: cases * 2 * (maxAgentRuns + maxToolCalls),
		TotalTokens:   cases * 2 * maxTotalTokens,
	}
	if budget.ProviderCalls > providerCallLimit {
		return evidenceGateProviderBudget{}, fmt.Errorf("estimated Provider call upper bound %d exceeds authorized max-provider-calls %d", budget.ProviderCalls, providerCallLimit)
	}
	if budget.TotalTokens > totalTokenLimit {
		return evidenceGateProviderBudget{}, fmt.Errorf("total Token budget upper bound %d exceeds authorized max-provider-tokens %d", budget.TotalTokens, totalTokenLimit)
	}
	return budget, nil
}

func evidenceGatePairingFingerprint(definition mesagent.EvaluationCase, cfg config.Config) (string, error) {
	profile, err := cfg.Models.Chat.ActiveProfile()
	if err != nil {
		return "", err
	}
	payload := struct {
		Case             mesagent.EvaluationCase `json:"case"`
		ModelProvider    string                  `json:"modelProvider"`
		ModelID          string                  `json:"modelId"`
		ModelProfile     string                  `json:"modelProfile"`
		ReasoningEffort  string                  `json:"reasoningEffort"`
		PromptVersion    string                  `json:"promptVersion"`
		MaxAgentRuns     int                     `json:"maxAgentRuns"`
		MaxToolCalls     int                     `json:"maxToolCalls"`
		MaxEvidenceItems int                     `json:"maxEvidenceItems"`
		MaxTotalTokens   int                     `json:"maxTotalTokens"`
		TimeoutMillis    int                     `json:"timeoutMillis"`
	}{
		Case: definition, ModelProvider: profile.Provider, ModelID: profile.Model,
		ModelProfile: cfg.Models.Chat.ActiveProfileName, ReasoningEffort: profile.ReasoningEffort,
		PromptVersion: cfg.Agent.PromptVersion, MaxAgentRuns: cfg.Agent.MaxAgentRuns,
		MaxToolCalls: cfg.Agent.MaxToolCalls, MaxEvidenceItems: cfg.Agent.MaxEvidenceItems,
		MaxTotalTokens: cfg.Agent.MaxTotalTokens, TimeoutMillis: cfg.Agent.TimeoutMillis,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("sha256:%x", digest[:]), nil
}

func evidenceGateObservationFromResult(
	definition mesagent.EvaluationCase,
	variant mesagent.EvaluationVariant,
	cfg config.Config,
	pairingFingerprint string,
	result mesagent.OrchestrationResult,
	duration time.Duration,
	invokeErr error,
) mesagent.EvidenceGateEvaluationObservation {
	profile, _ := cfg.Models.Chat.ActiveProfile()
	reasoningEffort := strings.TrimSpace(profile.ReasoningEffort)
	if reasoningEffort == "" {
		reasoningEffort = "none"
	}
	errorType := ""
	var degradationReasons []string
	if invokeErr != nil {
		errorType = evidenceGateInvocationErrorType(invokeErr)
		degradationReasons = []string{"invoke_failed_before_report"}
	} else if result.Partial {
		errorType = strings.TrimSpace(result.StopReason)
		if errorType == "" {
			errorType = "evidence_gate_partial"
		}
		degradationReasons = append(degradationReasons, result.MissingEvidence...)
	}
	return mesagent.EvidenceGateEvaluationObservation{
		DatasetVersion: definition.DatasetVersion, CaseID: definition.CaseID,
		Variant: variant, RunID: fmt.Sprintf("%s-%s-%s", definition.CaseID, variant, uuid.NewString()),
		EarlyExitEnabled:   variant == mesagent.EvaluationExperiment,
		PairingFingerprint: pairingFingerprint, ModelProvider: profile.Provider, ModelID: profile.Model,
		ModelProfile: cfg.Models.Chat.ActiveProfileName, PromptVersion: cfg.Agent.PromptVersion,
		ReasoningEffort: reasoningEffort, AgentRuns: result.AgentRuns,
		Completed:       invokeErr == nil && !result.Partial,
		QualityReviewed: false, Usage: result.Usage, ToolCalls: len(result.ToolExecutions),
		DurationMillis: duration.Milliseconds(), ErrorType: errorType,
		DegradationReasons: degradationReasons,
	}
}

func evidenceGateInvocationErrorType(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "provider_or_orchestration_error"
	}
}

// newPairedEvaluationRunAccess 构造评测用的诊断 RunAccess：授权事实直接来自
// 冻结 Policy 与 ceiling（case.read + 按 tag 附加 code/sql），不再经过旧
// TaskScope。Diagnosis 入口 Skill 由 Runner 固定为 ticket-diagnosis。
func newPairedEvaluationRunAccess(definition mesagent.EvaluationCase, cfg config.Config) (agentruntime.RunAccess, error) {
	permissions := []agentruntime.Permission{agentruntime.PermissionCaseRead}
	grantsConfig := agentruntime.ResourceGrantsConfig{
		ExternalCaseIDs: []uuid.UUID{pairedEvalCaseID},
		DataSourceIDs: []uuid.UUID{
			uuid.MustParse("33333333-3333-3333-3333-333333333333"),
		},
	}
	if containsString(definition.Tags, "github-enabled") {
		permissions = append(permissions, agentruntime.PermissionCodeRead)
	}
	if containsString(definition.Tags, "sql-enabled") || containsString(definition.Tags, "sql-query-enabled") {
		dataSourceID, err := uuid.Parse(cfg.SQLServer.ID)
		if err != nil {
			return agentruntime.RunAccess{}, fmt.Errorf("parse SQL data source id: %w", err)
		}
		permissions = append(permissions, agentruntime.PermissionSQLRead)
		grantsConfig.DataSourceIDs = append(grantsConfig.DataSourceIDs, dataSourceID)
	}
	permissionSet, err := agentruntime.NewPermissionSet(permissions...)
	if err != nil {
		return agentruntime.RunAccess{}, err
	}
	grants, err := agentruntime.NewResourceGrants(grantsConfig)
	if err != nil {
		return agentruntime.RunAccess{}, err
	}
	policy, err := agentruntime.NewInvestigationPolicy(diagnosis.InvestigationPolicySchemaVersion, permissionSet, grants)
	if err != nil {
		return agentruntime.RunAccess{}, err
	}
	return agentruntime.DeriveDiagnosisRunAccess(
		policy,
		agentruntime.Actor{UserID: uuid.MustParse("22222222-2222-2222-2222-222222222222"), Role: auth.RoleAnalyst},
		agentruntime.AccessCeiling{Permissions: permissionSet, Grants: grants},
	)
}

func evaluationDatasetHasTag(cases []mesagent.EvaluationCase, tag string) bool {
	for _, definition := range cases {
		if containsString(definition.Tags, tag) {
			return true
		}
	}
	return false
}

func observationFromResult(
	definition mesagent.EvaluationCase,
	variant mesagent.EvaluationVariant,
	cfg config.Config,
	result mesagent.OrchestrationResult,
	duration time.Duration,
	toolSchemaFingerprint string,
	comparability mesagent.ToolSelectionComparability,
	identity evaluationidentity.Identity,
	modelProfileFingerprint string,
) mesagent.EvaluationObservation {
	profile, _ := cfg.Models.Chat.ActiveProfile()
	actualTools := make([]string, 0, len(result.ToolExecutions))
	for _, execution := range result.ToolExecutions {
		actualTools = append(actualTools, execution.Name)
	}
	evidence := make([]string, 0, len(result.EvidenceItems))
	for _, item := range result.EvidenceItems {
		value := string(item.SourceType)
		if value != "" && !containsString(evidence, value) {
			evidence = append(evidence, value)
		}
	}
	// ToolProfileID 是实验臂特有合同：baseline 固定 evaluation-wide-v2（评测
	// wide 合同），experiment 固定生产 diagnosis-default。
	toolProfileID := string(agentruntime.ToolProfileDiagnosis)
	if variant == mesagent.EvaluationBaseline {
		toolProfileID = string(agentruntime.ToolProfileEvaluationWide)
	}
	return mesagent.EvaluationObservation{
		DatasetVersion:           definition.DatasetVersion,
		CaseID:                   definition.CaseID,
		Variant:                  variant,
		RunID:                    fmt.Sprintf("%s-%s-%s", definition.CaseID, variant, uuid.NewString()),
		ObservationSchemaVersion: mesagent.EvaluationObservationV3,
		Model:                    profile.Provider,
		ModelVersion:             profile.Model,
		ReasoningEffort:          profile.ReasoningEffort,
		PromptVersion:            cfg.Agent.PromptVersion,
		ToolProfileID:            toolProfileID,
		ToolSchemaFingerprint:    toolSchemaFingerprint,
		ModelProfileFingerprint:  modelProfileFingerprint,
		ImplementationRevision:   identity.Revision,
		ImplementationDirty:      identity.Dirty,
		ComparisonFingerprint:    comparability.ComparisonFingerprint,
		SharedToolNames:          append([]string(nil), comparability.SharedToolNames...),
		BaselineOnlyToolNames:    append([]string(nil), comparability.BaselineOnlyToolNames...),
		SelectedSkill:            result.SelectedSkill,
		ActualToolCalls:          actualTools,
		AllowedTools:             result.AllowedTools,
		Evidence:                 evidence,
		Limitations:              result.Report.Limitations,
		ConclusionStatus:         result.Report.ConclusionStatus,
		Partial:                  result.Partial,
		Usage:                    result.Usage,
		DurationMillis:           duration.Milliseconds(),
		ErrorType:                result.StopReason,
	}
}

func readEvaluationCases(path string) ([]mesagent.EvaluationCase, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var result []mesagent.EvaluationCase
	line := 0
	for scanner.Scan() {
		line++
		contents := bytes.TrimSpace(scanner.Bytes())
		if len(contents) == 0 {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(contents))
		decoder.DisallowUnknownFields()
		var current mesagent.EvaluationCase
		if err := decoder.Decode(&current); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if err := ensureEvaluationJSONEOF(decoder); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if err := current.Validate(); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		result = append(result, current)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, errors.New("evaluation dataset contains no cases")
	}
	return result, nil
}

func ensureEvaluationJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("multiple JSON values on one line")
}

func parseReasoningEffort(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "", "low", "medium", "high", "xhigh", "max":
		return normalized, nil
	default:
		return "", errors.New("reasoning-effort must be empty, low, medium, high, xhigh, or max")
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
