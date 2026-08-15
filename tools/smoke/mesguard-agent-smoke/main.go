// Command mesguard-agent-smoke 使用合成工单验证真实 ReAct、Tool 和多轮 usage 聚合。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/chitandabb/GoAgent/internal/externalcase"
	platformchatmodel "github.com/chitandabb/GoAgent/internal/platform/chatmodel"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformlogger "github.com/chitandabb/GoAgent/internal/platform/logger"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

var smokeCaseID = uuid.MustParse("11111111-1111-1111-1111-111111111111")

// buildSmokeRunAccess 构造 smoke 用诊断 RunAccess：授权事实直接来自冻结
// Policy 与 ceiling（case.read + smoke 工单 Grant）。
func buildSmokeRunAccess(caseID uuid.UUID) (agentruntime.RunAccess, error) {
	permissions, err := agentruntime.NewPermissionSet(agentruntime.PermissionCaseRead)
	if err != nil {
		return agentruntime.RunAccess{}, err
	}
	grants, err := agentruntime.NewResourceGrants(agentruntime.ResourceGrantsConfig{
		ExternalCaseIDs: []uuid.UUID{caseID},
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
		agentruntime.Actor{UserID: uuid.New(), Role: auth.RoleAnalyst},
		agentruntime.AccessCeiling{Permissions: permissions, Grants: grants},
	)
}

type syntheticCaseGetter struct{}

type modelCallMetric struct {
	Duration         time.Duration
	ReasoningRunes   int
	AnswerRunes      int
	ToolCalls        int
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	Err              error
}

type modelCallTrace struct {
	mu    sync.Mutex
	calls []modelCallMetric
}

type modelCallUsage struct {
	ModelCalls       int
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

func (t *modelCallTrace) append(metric modelCallMetric) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls = append(t.calls, metric)
}

func (t *modelCallTrace) snapshot() []modelCallMetric {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]modelCallMetric(nil), t.calls...)
}

func (t *modelCallTrace) usage() modelCallUsage {
	usage := modelCallUsage{}
	for _, call := range t.snapshot() {
		usage.ModelCalls++
		usage.PromptTokens += call.PromptTokens
		usage.CompletionTokens += call.CompletionTokens
		usage.TotalTokens += call.TotalTokens
	}
	return usage
}

type tracedChatModel struct {
	inner model.ToolCallingChatModel
	trace *modelCallTrace
}

// IsCallbacksEnabled 告诉 Eino：包装器不需要再套一层自动回调，避免内层模型和包装器重复统计。
func (m *tracedChatModel) IsCallbacksEnabled() bool { return true }

func (m *tracedChatModel) Generate(
	ctx context.Context,
	messages []*schema.Message,
	opts ...model.Option,
) (*schema.Message, error) {
	startedAt := time.Now()
	message, err := m.inner.Generate(ctx, messages, opts...)
	metric := modelCallMetric{Duration: time.Since(startedAt), Err: err}
	if message != nil {
		metric.ReasoningRunes = utf8.RuneCountInString(message.ReasoningContent)
		metric.AnswerRunes = utf8.RuneCountInString(message.Content)
		metric.ToolCalls = len(message.ToolCalls)
		if message.ResponseMeta != nil && message.ResponseMeta.Usage != nil {
			usage := message.ResponseMeta.Usage
			metric.PromptTokens = usage.PromptTokens
			metric.CompletionTokens = usage.CompletionTokens
			metric.TotalTokens = usage.TotalTokens
		}
	}
	m.trace.append(metric)
	return message, err
}

func (m *tracedChatModel) Stream(
	ctx context.Context,
	messages []*schema.Message,
	opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return m.inner.Stream(ctx, messages, opts...)
}

func (m *tracedChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	bound, err := m.inner.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &tracedChatModel{inner: bound, trace: m.trace}, nil
}

func (syntheticCaseGetter) Get(_ context.Context, id uuid.UUID) (*externalcase.ExternalCase, error) {
	if id != smokeCaseID {
		return nil, errors.New("synthetic case not found")
	}
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	return &externalcase.ExternalCase{
		ID: id, ExternalCaseKey: "SMOKE-001", CaseType: "incident",
		Title:       "工位报工后状态未更新",
		Description: "操作员完成报工，ERP 工单仍显示处理中，现场网络正常。",
		Category:    "workflow", Module: "work-reporting",
		Status: externalcase.StatusOpen, Priority: externalcase.PriorityMedium,
		ReportedAt: now, SourceUpdatedAt: now,
		SourceFingerprint: "synthetic-smoke-case",
	}, nil
}

func main() {
	log := platformlogger.NewBootstrapFor("mesguard-agent-smoke")
	defer platformlogger.Sync(log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], log); err != nil {
		log.Error("Agent smoke test failed", zap.Error(err))
		platformlogger.Sync(log)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, log *zap.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	profile, err := cfg.Models.Chat.ActiveProfile()
	if err != nil {
		return err
	}
	reasoningEffort, err := parseReasoningEffort(args, profile.ReasoningEffort)
	if err != nil {
		return err
	}
	profile.ReasoningEffort = reasoningEffort
	prompts, err := cfg.Agent.LoadPrompts()
	if err != nil {
		return fmt.Errorf("load Agent prompts: %w", err)
	}
	instance, err := platformchatmodel.New(ctx, cfg.Models.Chat.ActiveProfileName, profile)
	if err != nil {
		return fmt.Errorf("build chat model: %w", err)
	}
	modelTrace := &modelCallTrace{}
	observedModel := &tracedChatModel{inner: instance.Model, trace: modelTrace}
	runner, err := mesagent.NewDefaultRunner(ctx, mesagent.DefaultRunnerDependencies{
		ChatModel: observedModel, ExternalCases: syntheticCaseGetter{},
		SkillRoot:           cfg.Agent.SkillsDirectory,
		SystemInstruction:   prompts.SystemInstruction,
		BaselineInstruction: prompts.BaselineInstruction,
		Logger:              log.Named("runner"),
	})
	if err != nil {
		return fmt.Errorf("build Agent runner: %w", err)
	}
	orchestrator, err := mesagent.NewEvidenceOrchestrator(ctx, mesagent.EvidenceOrchestratorConfig{
		Runner: runner, Logger: log.Named("evidence_orchestrator"),
		MaxAgentRuns: cfg.Agent.MaxAgentRuns, MaxToolCalls: cfg.Agent.MaxToolCalls,
		MaxEvidenceItems: cfg.Agent.MaxEvidenceItems, MaxTotalTokens: cfg.Agent.MaxTotalTokens,
		Timeout:                   time.Duration(cfg.Agent.TimeoutMillis) * time.Millisecond,
		ReportContractInstruction: prompts.ReportContractInstruction,
	})
	if err != nil {
		return fmt.Errorf("build Evidence orchestrator: %w", err)
	}
	// smoke RunAccess：授权事实直接来自冻结 Policy ∩ ceiling（case.read +
	// smoke 工单 Grant），不再经过旧 TaskScope。
	runAccess, err := buildSmokeRunAccess(smokeCaseID)
	if err != nil {
		return fmt.Errorf("build smoke run access: %w", err)
	}
	startedAt := time.Now()
	result, err := orchestrator.Invoke(agentruntime.WithRunAccess(ctx, runAccess), mesagent.RunRequest{
		UserQuery:      "请读取工单，分析最可能的故障方向并形成证据化报告。",
		ExternalCaseID: smokeCaseID.String(),
	})
	duration := time.Since(startedAt)
	if err != nil {
		return fmt.Errorf("invoke Agent: %w", err)
	}
	toolNames := make([]string, 0, len(result.ToolExecutions))
	for _, execution := range result.ToolExecutions {
		toolNames = append(toolNames, execution.Name)
	}
	observedUsage := modelTrace.usage()
	fmt.Printf(
		"partial=%t reasoningEffort=%s duration=%s agentRuns=%d modelCalls=%d tools=%s promptTokens=%d completionTokens=%d totalTokens=%d conclusionRunes=%d\n",
		result.Partial, reasoningEffort, duration.Round(time.Millisecond), result.AgentRuns,
		observedUsage.ModelCalls, strings.Join(toolNames, ","),
		observedUsage.PromptTokens, observedUsage.CompletionTokens, observedUsage.TotalTokens,
		utf8.RuneCountInString(result.Report.Conclusion),
	)
	for index, call := range modelTrace.snapshot() {
		stage := "final_answer"
		if call.ToolCalls > 0 {
			stage = "tool_decision"
		}
		fmt.Printf(
			"modelCall=%d stage=%s duration=%s toolCalls=%d reasoningRunes=%d answerRunes=%d promptTokens=%d completionTokens=%d totalTokens=%d error=%t\n",
			index+1, stage, call.Duration.Round(time.Millisecond), call.ToolCalls,
			call.ReasoningRunes, call.AnswerRunes, call.PromptTokens, call.CompletionTokens,
			call.TotalTokens, call.Err != nil,
		)
	}
	reportJSON, err := json.MarshalIndent(result.Report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode smoke report: %w", err)
	}
	fmt.Printf("report:\n%s\n", reportJSON)
	return nil
}

func parseReasoningEffort(args []string, defaultEffort string) (string, error) {
	flags := flag.NewFlagSet("mesguard-agent-smoke", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	effort := flags.String("reasoning-effort", defaultEffort, "provider-supported effort; empty keeps thinking control only")
	if err := flags.Parse(args); err != nil {
		return "", fmt.Errorf("usage: mesguard-agent-smoke [-reasoning-effort provider-value]: %w", err)
	}
	if flags.NArg() != 0 {
		return "", errors.New("usage: mesguard-agent-smoke [-reasoning-effort provider-value]")
	}
	normalized := strings.ToLower(strings.TrimSpace(*effort))
	switch normalized {
	case "", "low", "medium", "high", "xhigh", "max":
		return normalized, nil
	default:
		return "", errors.New("reasoning-effort must be empty, low, medium, high, xhigh, or max")
	}
}
