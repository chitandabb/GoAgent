package agent

import (
	"context"
	"strings"
	"testing"
)

type stubExecutor struct {
	answer  string
	handoff *HandoffRequest
	usage   ModelUsage
}

func (s stubExecutor) Execute(context.Context, RunRequest, SkillDefinition) (RunResult, error) {
	return RunResult{Answer: s.answer, Handoff: s.handoff, Usage: s.usage}, nil
}

func TestRunnerRoutesThroughEinoGraph(t *testing.T) {
	ctx := context.Background()
	registry, err := NewRegistry(testSkills()...)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	router, err := NewRuleRouter(registry)
	if err != nil {
		t.Fatalf("NewRuleRouter: %v", err)
	}
	runner, err := NewRunner(ctx, router, registry, map[SkillID]SkillExecutor{
		SkillTicketDiagnosis:   stubExecutor{answer: "ticket"},
		SkillCodeInvestigation: stubExecutor{answer: "code"},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	ticketResult, err := runner.Invoke(ctx, RunRequest{UserQuery: "分析根因", ExternalCaseID: "case-id"})
	if err != nil {
		t.Fatalf("ticket Invoke: %v", err)
	}
	if ticketResult.SkillID != SkillTicketDiagnosis || ticketResult.Answer != "ticket" {
		t.Fatalf("unexpected ticket result: %+v", ticketResult)
	}
	if ticketResult.SkillVersion != "test-v1" {
		t.Fatalf("ticket skill version = %q", ticketResult.SkillVersion)
	}
	if len(ticketResult.AllowedTools) != 2 || ticketResult.AllowedTools[0] != ToolReadExternalCase {
		t.Fatalf("ticket tools = %v", ticketResult.AllowedTools)
	}

	codeResult, err := runner.Invoke(ctx, RunRequest{UserQuery: "帮我搜索相关代码提交"})
	if err != nil {
		t.Fatalf("code Invoke: %v", err)
	}
	if codeResult.SkillID != SkillCodeInvestigation || codeResult.Answer != "code" {
		t.Fatalf("unexpected code result: %+v", codeResult)
	}
}

func TestRunnerHandsTicketDiagnosisToCodeInvestigation(t *testing.T) {
	ctx := context.Background()
	registry, err := NewRegistry(testSkills()...)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	router, err := NewRuleRouter(registry)
	if err != nil {
		t.Fatalf("NewRuleRouter: %v", err)
	}
	runner, err := NewRunner(ctx, router, registry, map[SkillID]SkillExecutor{
		SkillTicketDiagnosis: stubExecutor{
			answer: "工单显示 InventoryService 超时",
			handoff: &HandoffRequest{
				TargetSkill: SkillCodeInvestigation, Reason: "需要代码证据",
				Query: "搜索 InventoryService 超时处理", Clues: []string{"InventoryService"},
			},
			usage: ModelUsage{ModelCalls: 2, TotalTokens: 100},
		},
		SkillCodeInvestigation: stubExecutor{
			answer: "提交 abc 修复了超时", usage: ModelUsage{ModelCalls: 3, TotalTokens: 200},
		},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	result, err := runner.Invoke(ctx, RunRequest{UserQuery: "诊断工单", ExternalCaseID: "case-id"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if len(result.ExecutedSkills) != 2 || result.ExecutedSkills[0] != SkillTicketDiagnosis ||
		result.ExecutedSkills[1] != SkillCodeInvestigation {
		t.Fatalf("executed skills = %v", result.ExecutedSkills)
	}
	if len(result.Handoffs) != 1 || result.Handoffs[0].ToSkill != SkillCodeInvestigation {
		t.Fatalf("handoffs = %+v", result.Handoffs)
	}
	if result.Usage.ModelCalls != 5 || result.Usage.TotalTokens != 300 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if !strings.Contains(result.Answer, "InventoryService") || !strings.Contains(result.Answer, "提交 abc") {
		t.Fatalf("answer = %q", result.Answer)
	}
}

func TestRunnerReportsUnavailableGitHubMCPWithoutDiscardingTicketFinding(t *testing.T) {
	ctx := context.Background()
	ticketDefinition := testSkills()[0]
	registry, err := NewRegistry(ticketDefinition)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	router, err := NewRuleRouter(registry)
	if err != nil {
		t.Fatalf("NewRuleRouter: %v", err)
	}
	runner, err := NewRunner(ctx, router, registry, map[SkillID]SkillExecutor{
		SkillTicketDiagnosis: stubExecutor{
			answer: "已经确认工单中的错误模块",
			handoff: &HandoffRequest{
				TargetSkill: SkillCodeInvestigation, Reason: "需要代码证据", Query: "搜索错误模块",
			},
		},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	result, err := runner.Invoke(ctx, RunRequest{UserQuery: "诊断工单", ExternalCaseID: "case-id"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(result.Answer, "已经确认工单") || !strings.Contains(result.Answer, "GitHub MCP 工具暂时不可用") {
		t.Fatalf("answer = %q", result.Answer)
	}
}

func TestRunnerDispatcherSupportsConfiguredSkillWithoutHardCodedPath(t *testing.T) {
	ctx := context.Background()
	skills := testSkills()
	sqlSkill := skills[1]
	sqlSkill.ID = SkillID("sql-investigation")
	sqlSkill.Description = "sql investigation"
	registry, err := NewRegistry(skills[0], sqlSkill)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	router, err := NewRuleRouter(registry)
	if err != nil {
		t.Fatalf("NewRuleRouter: %v", err)
	}
	runner, err := NewRunner(ctx, router, registry, map[SkillID]SkillExecutor{
		SkillTicketDiagnosis: stubExecutor{
			answer: "ticket", handoff: &HandoffRequest{
				TargetSkill: sqlSkill.ID, Reason: "需要数据库证据", Query: "检查工单状态流转",
			},
		},
		sqlSkill.ID: stubExecutor{answer: "sql evidence"},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	result, err := runner.Invoke(ctx, RunRequest{UserQuery: "诊断工单", ExternalCaseID: "case-id"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if len(result.ExecutedSkills) != 2 || result.ExecutedSkills[1] != sqlSkill.ID {
		t.Fatalf("executed skills = %v", result.ExecutedSkills)
	}
}

func TestRunnerRejectsHandoffCycle(t *testing.T) {
	ctx := context.Background()
	registry, err := NewRegistry(testSkills()...)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	router, err := NewRuleRouter(registry)
	if err != nil {
		t.Fatalf("NewRuleRouter: %v", err)
	}
	runner, err := NewRunner(ctx, router, registry, map[SkillID]SkillExecutor{
		SkillTicketDiagnosis: stubExecutor{answer: "ticket", handoff: &HandoffRequest{
			TargetSkill: SkillCodeInvestigation, Reason: "code", Query: "code",
		}},
		SkillCodeInvestigation: stubExecutor{answer: "code", handoff: &HandoffRequest{
			TargetSkill: SkillTicketDiagnosis, Reason: "ticket", Query: "ticket",
		}},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	_, err = runner.Invoke(ctx, RunRequest{UserQuery: "诊断工单", ExternalCaseID: "case-id"})
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle error = %v", err)
	}
}
