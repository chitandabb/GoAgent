// Command mesguard-agentic-retrieval-eval evaluates the Evidence Gate's bounded
// second-pass knowledge retrieval decision with a real ChatModel and a fixed
// in-memory knowledge Tool fixture.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/chitandabb/GoAgent/internal/externalcase"
	"github.com/chitandabb/GoAgent/internal/knowledge"
	platformchatmodel "github.com/chitandabb/GoAgent/internal/platform/chatmodel"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformlogger "github.com/chitandabb/GoAgent/internal/platform/logger"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const agenticEvaluationCaseID = "44444444-4444-4444-4444-444444444444"

type commandOptions struct {
	datasetPath     string
	outputPath      string
	summaryPath     string
	caseID          string
	maxCases        int
	maxTotalTokens  int
	timeout         time.Duration
	executeProvider bool
}

type agenticEvaluationCaseGetter struct{}

func (agenticEvaluationCaseGetter) Get(_ context.Context, id uuid.UUID) (*externalcase.ExternalCase, error) {
	if id != uuid.MustParse(agenticEvaluationCaseID) {
		return nil, errors.New("Agentic retrieval evaluation case not found")
	}
	now := time.Date(2026, 8, 7, 8, 0, 0, 0, time.UTC)
	return &externalcase.ExternalCase{
		ID: id, ExternalCaseKey: "AGENTIC-EVAL-001", CaseType: "incident",
		Title:       "报工完成后状态同步延迟",
		Description: "操作员完成报工后，页面状态没有及时更新，需要核对企业内部处理规范。",
		Category:    "workflow", Module: "work-reporting", Status: externalcase.StatusOpen,
		Priority: externalcase.PriorityMedium, ReportedAt: now, SourceUpdatedAt: now,
		SourceFingerprint: "agentic-retrieval-evaluation-v1",
	}, nil
}

type fixtureKnowledgeSearcher struct{}

func (fixtureKnowledgeSearcher) Search(
	ctx context.Context,
	actorID uuid.UUID,
	query string,
	limit int,
) (knowledge.HybridSearch, error) {
	if err := ctx.Err(); err != nil {
		return knowledge.HybridSearch{}, err
	}
	if actorID == uuid.Nil || strings.TrimSpace(query) == "" {
		return knowledge.HybridSearch{}, errors.New("fixture knowledge search request is invalid")
	}
	if limit < 1 {
		limit = 1
	}
	plan, err := knowledge.OriginalQueryPlan(query)
	if err != nil {
		return knowledge.HybridSearch{}, err
	}
	documentID := uuid.MustParse("55555555-5555-5555-5555-555555555555")
	versionID := uuid.MustParse("66666666-6666-6666-6666-666666666666")
	chunkID := uuid.MustParse("77777777-7777-7777-7777-777777777777")
	content := "企业报工状态同步规范要求：服务端确认报工事务提交后，异步状态更新应在网关超时预算内完成；超过预算时应保留请求编号并记录可追溯日志。"
	return knowledge.HybridSearch{
		Results: []knowledge.SearchResult{{
			DocumentID: documentID, DocumentVersionID: versionID, ChunkID: chunkID,
			Title: "报工状态同步规范", Scope: knowledge.ScopeGlobal, Ordinal: 0,
			ElementType: knowledge.ElementText, SectionPath: []string{"状态同步"},
			ContentText: content, ContentSHA256: knowledge.SHA256Hex(content),
			Score: 1, FTSRank: 1, VectorRank: 1, FusedScore: 1,
		}},
		QueryPlan: plan, Sources: []string{"fixture"}, QueryRewriteStatus: knowledge.QueryRewriteDisabled,
	}, nil
}

type seededAgentInvoker struct {
	delegate mesagent.AgentInvoker
	seed     mesagent.RunResult
	calls    int
}

func (i *seededAgentInvoker) Invoke(ctx context.Context, request mesagent.RunRequest) (mesagent.RunResult, error) {
	if i.calls == 0 {
		i.calls++
		return i.seed, nil
	}
	i.calls++
	return i.delegate.Invoke(ctx, request)
}

func main() {
	log := platformlogger.NewBootstrapFor("mesguard-agentic-retrieval-eval")
	defer platformlogger.Sync(log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], log); err != nil {
		log.Error("Agentic retrieval evaluation failed", zap.Error(err))
		platformlogger.Sync(log)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, log *zap.Logger) error {
	options, err := parseOptions(args)
	if err != nil {
		return err
	}
	cases, err := readCases(options.datasetPath)
	if err != nil {
		return err
	}
	cases, err = selectCases(cases, options.caseID, options.maxCases)
	if err != nil {
		return err
	}
	if !options.executeProvider {
		return errors.New("provider execution is disabled; review the budget and add -execute-provider")
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !cfg.Models.Chat.Enabled {
		return errors.New("chat model is disabled")
	}
	if cfg.Agent.MaxAgentRuns < 2 {
		return errors.New("Agentic retrieval evaluation requires agent.maxAgentRuns >= 2")
	}
	if options.maxTotalTokens != 0 {
		cfg.Agent.MaxTotalTokens = options.maxTotalTokens
	}
	prompts, err := cfg.Agent.LoadPrompts()
	if err != nil {
		return fmt.Errorf("load Agent prompts: %w", err)
	}
	instance, err := platformchatmodel.NewActive(ctx, cfg.Models.Chat)
	if err != nil {
		return fmt.Errorf("build chat model: %w", err)
	}
	knowledgeTool, err := mesagent.NewSearchKnowledgeTool(fixtureKnowledgeSearcher{})
	if err != nil {
		return fmt.Errorf("build fixture knowledge Tool: %w", err)
	}
	runner, err := mesagent.NewDefaultRunner(ctx, mesagent.DefaultRunnerDependencies{
		ChatModel: instance.Model, ExternalCases: agenticEvaluationCaseGetter{},
		SkillRoot: cfg.Agent.SkillsDirectory, SystemInstruction: prompts.SystemInstruction,
		BaselineInstruction: prompts.BaselineInstruction, Mode: mesagent.RunnerModeExperiment,
		KnowledgeSearch: knowledgeTool, Logger: log.Named("runner"),
	})
	if err != nil {
		return fmt.Errorf("build Agent runner: %w", err)
	}
	profile, err := cfg.Models.Chat.ActiveProfile()
	if err != nil {
		return err
	}
	reportContract := prompts.ReportContractInstruction
	maxTotalTokens := cfg.Agent.MaxTotalTokens
	if maxTotalTokens < 1000 {
		maxTotalTokens = 16_000
	}
	output, err := os.OpenFile(options.outputPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create observations output: %w", err)
	}
	defer output.Close()
	encoder := json.NewEncoder(output)
	observations := make([]mesagent.AgenticRetrievalEvaluationObservation, 0, len(cases))
	for _, definition := range cases {
		if err := ctx.Err(); err != nil {
			return err
		}
		scope, err := evaluationRunAccess()
		if err != nil {
			return err
		}
		seed, err := seededRunResult(definition)
		if err != nil {
			return fmt.Errorf("seed case %q: %w", definition.CaseID, err)
		}
		invoker := &seededAgentInvoker{delegate: runner, seed: seed}
		orchestrator, err := mesagent.NewEvidenceOrchestrator(ctx, mesagent.EvidenceOrchestratorConfig{
			Runner: invoker, Logger: log.Named("evidence"), MaxAgentRuns: 2,
			MaxToolCalls: cfg.Agent.MaxToolCalls, MaxEvidenceItems: cfg.Agent.MaxEvidenceItems,
			MaxTotalTokens: maxTotalTokens, Timeout: options.timeout,
			ReportContractInstruction: reportContract,
		})
		if err != nil {
			return fmt.Errorf("build Evidence Gate for case %q: %w", definition.CaseID, err)
		}
		startedAt := time.Now()
		result, invokeErr := orchestrator.Invoke(
			agentruntime.WithRunAccess(ctx, scope),
			mesagent.RunRequest{
				UserQuery: definition.UserQuery, ExternalCaseID: agenticEvaluationCaseID,
			},
		)
		observation := observationFromResult(definition, profile, cfg.Agent.PromptVersion, result, time.Since(startedAt), invokeErr)
		if err := observation.Validate(); err != nil {
			return fmt.Errorf("validate observation for case %q: %w", definition.CaseID, err)
		}
		if err := encoder.Encode(observation); err != nil {
			return fmt.Errorf("write observation for case %q: %w", definition.CaseID, err)
		}
		observations = append(observations, observation)
		if invokeErr != nil {
			return fmt.Errorf("invoke Agent for case %q: %w", definition.CaseID, invokeErr)
		}
		log.Info("Agentic retrieval evaluation case completed", zap.String("case_id", definition.CaseID),
			zap.Bool("attempted", result.AgenticRetrievalAttempted), zap.Bool("added", result.AgenticRetrievalAddedEvidence),
			zap.String("stop_reason", result.AgenticRetrievalStopReason), zap.Int("total_tokens", result.Usage.TotalTokens))
	}
	summary, err := mesagent.EvaluateAgenticRetrieval(cases, observations)
	if err != nil {
		return fmt.Errorf("evaluate observations: %w", err)
	}
	if err := writeJSON(options.summaryPath, summary); err != nil {
		return err
	}
	fmt.Printf("dataset=%s cases=%d attempt_recall=%.4f added_evidence_rate=%.4f stop_reason_accuracy=%.4f total_tokens=%d\n",
		summary.DatasetVersion, summary.Cases, summary.AttemptRecall, summary.AddedEvidenceRate,
		summary.StopReasonAccuracy, summary.TotalTokens)
	return nil
}

func parseOptions(args []string) (commandOptions, error) {
	flags := flag.NewFlagSet("mesguard-agentic-retrieval-eval", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := commandOptions{}
	flags.StringVar(&options.datasetPath, "dataset", "testdata/agentic-retrieval-v1.jsonl", "versioned Agentic retrieval JSONL cases")
	flags.StringVar(&options.outputPath, "output", "output/evaluation/agentic-retrieval-v1.observations.jsonl", "observation JSONL output")
	flags.StringVar(&options.summaryPath, "summary", "output/evaluation/agentic-retrieval-v1.summary.json", "summary JSON output")
	flags.StringVar(&options.caseID, "case-id", "", "optional exact case id")
	flags.IntVar(&options.maxCases, "max-cases", 1, "maximum provider cases")
	flags.IntVar(&options.maxTotalTokens, "max-total-tokens", 16_000, "Evidence Gate total token budget per case")
	flags.DurationVar(&options.timeout, "timeout", 90*time.Second, "per-case timeout")
	flags.BoolVar(&options.executeProvider, "execute-provider", false, "allow the real ChatModel call")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return commandOptions{}, errors.New("usage: mesguard-agentic-retrieval-eval [-execute-provider] [-dataset path] [-output path] [-summary path] [-case-id id] [-max-cases n] [-max-total-tokens n] [-timeout duration]")
	}
	options.caseID = strings.TrimSpace(options.caseID)
	if options.maxCases < 1 || options.maxCases > 10 || options.maxTotalTokens < 1000 || options.maxTotalTokens > 1_000_000 ||
		options.timeout < time.Second || options.timeout > 10*time.Minute {
		return commandOptions{}, errors.New("evaluation limits are outside the safety bounds")
	}
	if filepath.Clean(options.outputPath) == filepath.Clean(options.summaryPath) {
		return commandOptions{}, errors.New("output and summary paths must be different")
	}
	return options, nil
}

func readCases(path string) ([]mesagent.AgenticRetrievalEvaluationCase, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open dataset: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 512*1024)
	var cases []mesagent.AgenticRetrievalEvaluationCase
	for line := 1; scanner.Scan(); line++ {
		contents := bytes.TrimSpace(scanner.Bytes())
		if len(contents) == 0 {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(contents))
		decoder.DisallowUnknownFields()
		var definition mesagent.AgenticRetrievalEvaluationCase
		if err := decoder.Decode(&definition); err != nil {
			return nil, fmt.Errorf("decode dataset line %d: %w", line, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("dataset line %d contains multiple JSON values", line)
		}
		if err := definition.Validate(); err != nil {
			return nil, fmt.Errorf("validate dataset line %d: %w", line, err)
		}
		cases = append(cases, definition)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(cases) == 0 {
		return nil, errors.New("Agentic retrieval dataset contains no cases")
	}
	return cases, nil
}

func selectCases(cases []mesagent.AgenticRetrievalEvaluationCase, caseID string, maxCases int) ([]mesagent.AgenticRetrievalEvaluationCase, error) {
	if caseID != "" {
		for _, definition := range cases {
			if definition.CaseID == caseID {
				return []mesagent.AgenticRetrievalEvaluationCase{definition}, nil
			}
		}
		return nil, fmt.Errorf("case-id %q was not found", caseID)
	}
	if len(cases) > maxCases {
		return append([]mesagent.AgenticRetrievalEvaluationCase(nil), cases[:maxCases]...), nil
	}
	return append([]mesagent.AgenticRetrievalEvaluationCase(nil), cases...), nil
}

// evaluationRunAccess 构造评测用的诊断 RunAccess：授权事实直接来自冻结
// Policy 与 ceiling（case.read + knowledge.read + 评测工单 Grant），不再经过
// 旧 TaskScope。入口 Skill 由 Runner 固定为 ticket-diagnosis。
func evaluationRunAccess() (agentruntime.RunAccess, error) {
	permissions, err := agentruntime.NewPermissionSet(
		agentruntime.PermissionCaseRead, agentruntime.PermissionKnowledgeRead,
	)
	if err != nil {
		return agentruntime.RunAccess{}, err
	}
	grants, err := agentruntime.NewResourceGrants(agentruntime.ResourceGrantsConfig{
		ExternalCaseIDs: []uuid.UUID{uuid.MustParse(agenticEvaluationCaseID)},
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

func seededRunResult(definition mesagent.AgenticRetrievalEvaluationCase) (mesagent.RunResult, error) {
	validReport := mesagent.StructuredReport{
		ConclusionStatus: mesagent.ConclusionProbable, RiskLevel: mesagent.RiskMedium,
		Conclusion: "需要结合企业规范确认状态同步链路。", BusinessSummary: "工单现象需要进一步核对。",
		TechnicalSummary: "首轮证据来自工单快照。", Limitations: []string{"尚未完成全部证据核对。"},
		Confidence: mesagent.ConfidenceMedium,
		Evidence: []mesagent.ReportEvidence{{
			Claim: "工单记录了状态同步延迟现象。", SourceTool: mesagent.ToolReadExternalCase,
			SourceRef: "evidence:seed-case", SupportType: mesagent.EvidenceSupports,
		}},
	}
	if definition.Scenario == mesagent.AgenticScenarioEvidenceGap {
		validReport.Evidence = nil
		return marshalSeedReport(validReport, mesagent.RunResult{
			SkillID: mesagent.SkillTicketDiagnosis, AllowedTools: []string{mesagent.ToolReadExternalCase},
		})
	}
	if definition.Scenario == mesagent.AgenticScenarioFormatOnly {
		validReport.Confidence = mesagent.ConfidenceLevel("certain")
	}
	answer, err := json.Marshal(validReport)
	if err != nil {
		return mesagent.RunResult{}, err
	}
	return mesagent.RunResult{
		SkillID: mesagent.SkillTicketDiagnosis, Answer: string(answer),
		AllowedTools:   []string{mesagent.ToolReadExternalCase},
		ToolExecutions: []mesagent.ToolExecution{{Name: mesagent.ToolReadExternalCase, Succeeded: true, EvidenceID: "evidence:seed-case"}},
		EvidenceItems: []mesagent.EvidenceItem{{
			ID: "evidence:seed-case", SourceType: mesagent.EvidenceSourceCaseSnapshot,
			SourceTool: mesagent.ToolReadExternalCase, SourceRef: "evidence:seed-case",
			CollectedAt: time.Date(2026, 8, 7, 8, 0, 0, 0, time.UTC), Summary: "固定工单快照",
			Snapshot: `{"externalCaseKey":"AGENTIC-EVAL-001"}`, ContentHash: "sha256:seed-case",
		}},
	}, nil
}

func marshalSeedReport(report mesagent.StructuredReport, result mesagent.RunResult) (mesagent.RunResult, error) {
	answer, err := json.Marshal(report)
	if err != nil {
		return mesagent.RunResult{}, err
	}
	result.Answer = string(answer)
	return result, nil
}

func observationFromResult(
	definition mesagent.AgenticRetrievalEvaluationCase,
	profile config.ChatModelProfileConfig,
	promptVersion string,
	result mesagent.OrchestrationResult,
	duration time.Duration,
	invokeErr error,
) mesagent.AgenticRetrievalEvaluationObservation {
	tools := make([]string, 0, len(result.ToolExecutions))
	for _, execution := range result.ToolExecutions {
		tools = append(tools, execution.Name)
	}
	reasoningEffort := strings.TrimSpace(profile.ReasoningEffort)
	if reasoningEffort == "" {
		reasoningEffort = "default"
	}
	observation := mesagent.AgenticRetrievalEvaluationObservation{
		DatasetVersion: definition.DatasetVersion, CaseID: definition.CaseID, Scenario: definition.Scenario,
		RunID: "agentic-" + definition.CaseID + "-" + uuid.NewString(),
		Model: profile.Provider, ModelVersion: profile.Model, ReasoningEffort: reasoningEffort,
		PromptVersion: promptVersion, AgentRuns: result.AgentRuns, ActualToolCalls: tools,
		AgenticRetrievalAttempted:     result.AgenticRetrievalAttempted,
		AgenticRetrievalAddedEvidence: result.AgenticRetrievalAddedEvidence,
		AgenticRetrievalStopReason:    result.AgenticRetrievalStopReason, Partial: result.Partial,
		Usage: result.Usage, DurationMillis: duration.Milliseconds(),
	}
	if invokeErr != nil {
		observation.ErrorType = "invoke_failed"
	}
	return observation
}

func writeJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
		return fmt.Errorf("write summary: %w", err)
	}
	return nil
}
