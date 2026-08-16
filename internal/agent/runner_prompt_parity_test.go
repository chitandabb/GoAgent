package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agentruntime"

	"github.com/google/uuid"
)

// buildParityTaskContextForTest 用生产深模块 BuildDiagnosisRunContext 构造一次
// 生产等价的诊断运行上下文（含 SQL 授权数据源），与 paired 评测的 Case 派生
// 逻辑一致：case.read + sql.read 冻结 Policy、Analyst actor、真实诊断 Profile
// 名单 ceiling，输出非空 task_context。
func buildParityTaskContextForTest(t *testing.T) DiagnosisRunContext {
	t.Helper()
	sqlSource := uuid.MustParse("44444444-4444-4444-4444-444444444444")
	policy := mustDiagnosisPolicyForTest(t,
		[]agentruntime.Permission{agentruntime.PermissionCaseRead, agentruntime.PermissionSQLRead},
		agentruntime.ResourceGrantsConfig{
			ExternalCaseIDs: []uuid.UUID{runnerTestCaseID},
			DataSourceIDs:   []uuid.UUID{sqlSource},
		},
	)
	runContext, err := BuildDiagnosisRunContext(DiagnosisRunContextInput{
		Policy: policy,
		Actor:  agentruntime.Actor{UserID: uuid.MustParse("22222222-2222-2222-2222-222222222222"), Role: "analyst"},
		ProfileToolNames: []string{
			ToolReadExternalCase, ToolSearchSchemaCatalog, ToolExecuteReadonlyQuery,
			ToolDatabaseObjectDefinition, ToolReadSkillReference, ToolSkill,
		},
		ExternalCaseID: runnerTestCaseID,
		DataSources: []DiagnosisCeilingDataSource{{
			ID: sqlSource, Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly,
		}},
	})
	if err != nil {
		t.Fatalf("BuildDiagnosisRunContext: %v", err)
	}
	if strings.TrimSpace(runContext.TaskContext()) == "" {
		t.Fatal("BuildDiagnosisRunContext must produce a non-empty task_context")
	}
	return runContext
}

// TestBaselineAndExperimentSystemMessagesByteIdentical 是 Generic paired
// 评测的单变量契约测试：baseline（RunnerModeBaseline + evaluation-wide-v2
// wide 臂）与 experiment（RunnerModeExperiment + diagnosis-default 生产臂）
// 对同一 Case 的最终 system message 必须字节级一致——相同的生产
// SystemInstruction、相同的 ticket-diagnosis 入口 Skill 全文、相同的
// task_context，以及相同的模型参数与运行预算。唯一允许差异是最终 Tool
// Profile/Schema。模型请求按真实装配捕获，直接断言两臂 system message
// 相等。
func TestBaselineAndExperimentSystemMessagesByteIdentical(t *testing.T) {
	experimentState := &runnerModelState{baseline: true}
	baselineState := &runnerModelState{baseline: true}
	experimentRunner := newRunnerTestWithMode(t, experimentState, RunnerModeExperiment)
	baselineRunner := newRunnerTestWithMode(t, baselineState, RunnerModeBaseline)

	access := runnerTestAccess(t, agentruntime.PermissionCaseRead)
	request := RunRequest{UserQuery: "读取并诊断工单", ExternalCaseID: runnerTestCaseID.String()}

	if _, err := experimentRunner.Invoke(withRunnerTestRunAccess(context.Background(), access), request); err != nil {
		t.Fatalf("experiment Invoke: %v", err)
	}
	if _, err := baselineRunner.Invoke(withRunnerTestRunAccess(context.Background(), access), request); err != nil {
		t.Fatalf("baseline Invoke: %v", err)
	}

	if len(experimentState.prompts) == 0 {
		t.Fatal("experiment captured no system messages")
	}
	if len(baselineState.prompts) == 0 {
		t.Fatal("baseline captured no system messages")
	}
	if len(experimentState.prompts) != len(baselineState.prompts) {
		t.Fatalf("prompt turn counts differ: experiment=%d baseline=%d",
			len(experimentState.prompts), len(baselineState.prompts))
	}
	for turn := range experimentState.prompts {
		experimentSystem := strings.Join(experimentState.prompts[turn], "\n")
		baselineSystem := strings.Join(baselineState.prompts[turn], "\n")
		if experimentSystem != baselineSystem {
			t.Fatalf("turn %d system messages differ\n--- experiment ---\n%s\n--- baseline ---\n%s",
				turn, experimentSystem, baselineSystem)
		}
	}

	firstBaseline := strings.Join(baselineState.prompts[0], "\n")
	if !strings.Contains(firstBaseline, runnerTestSystemInstruction) {
		t.Fatalf("baseline system message misses the production SystemInstruction")
	}
	if !strings.Contains(firstBaseline, `<entry_skill name="ticket-diagnosis">`) {
		t.Fatalf("baseline system message must embed the full ticket-diagnosis entry Skill, got:\n%s", firstBaseline)
	}
	if strings.Contains(firstBaseline, "<entry_task>") {
		t.Fatalf("baseline system message must not use the legacy entry_task marker:\n%s", firstBaseline)
	}
	if strings.Contains(firstBaseline, runnerTestBaselineInstruction) {
		t.Fatalf("baseline system message must use the production SystemInstruction, not BaselineInstruction:\n%s", firstBaseline)
	}
}

// TestBaselineSystemContainsEntrySkillFullText 证明 baseline 的 system message
// 内嵌了与 experiment 完全相同的 ticket-diagnosis Skill 全文（不是占位符）。
// TestBaselineAndExperimentSystemMessagesByteIdenticalWithRealTaskContext 是
// Generic paired 单变量契约在"生产等价 task_context"下的测试：两臂注入由
// BuildDiagnosisRunContext 生成的同一个非空 task_context（含 SQL 授权数据源，
// 与真实 Diagnosis Worker 的投影一致），断言最终 system message 仍字节级一致、
// task_context 在其中恰好出现一次且位于应用侧指令尾部。
func TestBaselineAndExperimentSystemMessagesByteIdenticalWithRealTaskContext(t *testing.T) {
	experimentState := &runnerModelState{baseline: true}
	baselineState := &runnerModelState{baseline: true}
	experimentRunner := newRunnerTestWithMode(t, experimentState, RunnerModeExperiment)
	baselineRunner := newRunnerTestWithMode(t, baselineState, RunnerModeBaseline)

	runContext := buildParityTaskContextForTest(t)
	taskContext := runContext.TaskContext()
	baseCtx := agentruntime.WithRunAccess(context.Background(), runContext.Access())
	request := RunRequest{UserQuery: "读取并诊断工单", ExternalCaseID: runnerTestCaseID.String()}

	if _, err := experimentRunner.Invoke(WithDiagnosisTaskContext(baseCtx, taskContext), request); err != nil {
		t.Fatalf("experiment Invoke: %v", err)
	}
	if _, err := baselineRunner.Invoke(WithDiagnosisTaskContext(baseCtx, taskContext), request); err != nil {
		t.Fatalf("baseline Invoke: %v", err)
	}

	if len(experimentState.prompts) != len(baselineState.prompts) || len(experimentState.prompts) == 0 {
		t.Fatalf("prompt turn counts differ: experiment=%d baseline=%d",
			len(experimentState.prompts), len(baselineState.prompts))
	}
	for turn := range experimentState.prompts {
		experimentSystem := strings.Join(experimentState.prompts[turn], "\n")
		baselineSystem := strings.Join(baselineState.prompts[turn], "\n")
		if experimentSystem != baselineSystem {
			t.Fatalf("turn %d system messages differ with production task_context\n--- experiment ---\n%s\n--- baseline ---\n%s",
				turn, experimentSystem, baselineSystem)
		}
		if !strings.Contains(experimentSystem, taskContext) {
			t.Fatalf("turn %d system message must embed the generated task_context verbatim", turn)
		}
		if strings.Count(experimentSystem, "<task_context>") != 1 {
			t.Fatalf("task_context must appear exactly once, got %d:\n%s",
				strings.Count(experimentSystem, "<task_context>"), experimentSystem)
		}
		blockIndex := strings.Index(experimentSystem, "<task_context>")
		if blockIndex <= 0 || !strings.Contains(experimentSystem[:blockIndex], runnerTestSystemInstruction) {
			t.Fatalf("task_context must follow the base instruction at the tail:\n%s", experimentSystem)
		}
		if !strings.Contains(taskContext, `"externalCaseId":"`+runnerTestCaseID.String()+`"`) {
			t.Fatalf("generated task_context must carry the current external case id:\n%s", taskContext)
		}
		if !strings.Contains(taskContext, `"safetyMode":"read_only"`) {
			t.Fatalf("generated task_context must project the read_only SQL data source:\n%s", taskContext)
		}
	}
}

func TestBaselineSystemContainsEntrySkillFullText(t *testing.T) {
	experimentState := &runnerModelState{baseline: true}
	baselineState := &runnerModelState{baseline: true}
	experimentRunner := newRunnerTestWithMode(t, experimentState, RunnerModeExperiment)
	baselineRunner := newRunnerTestWithMode(t, baselineState, RunnerModeBaseline)

	skillContent, err := baselineRunner.skillRuntime.Instruction(context.Background(), SkillTicketDiagnosis)
	if err != nil {
		t.Fatalf("Instruction: %v", err)
	}
	request := RunRequest{UserQuery: "读取并诊断工单", ExternalCaseID: runnerTestCaseID.String()}
	access := runnerTestAccess(t, agentruntime.PermissionCaseRead)
	if _, err := experimentRunner.Invoke(withRunnerTestRunAccess(context.Background(), access), request); err != nil {
		t.Fatalf("experiment Invoke: %v", err)
	}
	if _, err := baselineRunner.Invoke(withRunnerTestRunAccess(context.Background(), access), request); err != nil {
		t.Fatalf("baseline Invoke: %v", err)
	}
	baselineSystem := strings.Join(baselineState.prompts[0], "\n")
	if !strings.Contains(baselineSystem, strings.TrimSpace(skillContent)) {
		t.Fatalf("baseline system message must contain the full entry Skill text verbatim; want:\n%s\ngot:\n%s",
			strings.TrimSpace(skillContent), baselineSystem)
	}
	if !strings.Contains(strings.Join(experimentState.prompts[0], "\n"), strings.TrimSpace(skillContent)) {
		t.Fatalf("experiment system message must contain the full entry Skill text verbatim")
	}
}
