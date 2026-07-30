// Command mesguard-agent-smoke 使用合成工单验证真实 ReAct、Tool 和多轮 usage 聚合。
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/externalcase"
	platformchatmodel "github.com/chitandabb/GoAgent/internal/platform/chatmodel"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformlogger "github.com/chitandabb/GoAgent/internal/platform/logger"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

var smokeCaseID = uuid.MustParse("11111111-1111-1111-1111-111111111111")

type syntheticCaseGetter struct{}

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
	if len(args) != 0 {
		return errors.New("usage: mesguard-agent-smoke")
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	definitions, err := mesagent.LoadSkillDefinitions(cfg.Agent.SkillsDirectory)
	if err != nil {
		return fmt.Errorf("load skills: %w", err)
	}
	chatModel, err := platformchatmodel.NewStepFun(ctx, cfg.Models.Chat)
	if err != nil {
		return fmt.Errorf("build StepFun model: %w", err)
	}
	runner, err := mesagent.NewDefaultRunner(ctx, mesagent.DefaultRunnerDependencies{
		ChatModel: chatModel, ExternalCases: syntheticCaseGetter{},
		SkillDefinitions: definitions, Logger: log.Named("runner"),
	})
	if err != nil {
		return fmt.Errorf("build Agent runner: %w", err)
	}
	result, err := runner.Invoke(ctx, mesagent.RunRequest{
		UserQuery:      "请读取工单，并用一句话概括最可能的故障方向。",
		ExternalCaseID: smokeCaseID.String(),
	})
	if err != nil {
		return fmt.Errorf("invoke Agent: %w", err)
	}
	toolNames := make([]string, 0, len(result.ToolExecutions))
	for _, execution := range result.ToolExecutions {
		toolNames = append(toolNames, execution.Name)
	}
	fmt.Printf(
		"skill=%s version=%s modelCalls=%d tools=%s promptTokens=%d completionTokens=%d totalTokens=%d cachedTokens=%d reasoningTokens=%d answerRunes=%d\n",
		result.SkillID, result.SkillVersion, result.Usage.ModelCalls, strings.Join(toolNames, ","),
		result.Usage.PromptTokens, result.Usage.CompletionTokens, result.Usage.TotalTokens,
		result.Usage.CachedTokens, result.Usage.ReasoningTokens, utf8.RuneCountInString(result.Answer),
	)
	return nil
}
