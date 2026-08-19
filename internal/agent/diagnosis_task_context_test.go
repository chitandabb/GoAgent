package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/contextgovernance"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// taskContextRecordingModel 记录传给模型的 system 消息内容，用于证明
// task_context 只追加到 system 指令最尾部。
type taskContextRecordingModel struct {
	mu     sync.Mutex
	system []string
	tools  []*schema.ToolInfo
}

func (m *taskContextRecordingModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return &taskContextRecordingModel{system: m.system, tools: append([]*schema.ToolInfo(nil), tools...)}, nil
}

func (m *taskContextRecordingModel) Generate(
	_ context.Context,
	input []*schema.Message,
	_ ...model.Option,
) (*schema.Message, error) {
	for _, message := range input {
		if message != nil && message.Role == schema.System {
			m.mu.Lock()
			m.system = append(m.system, message.Content)
			m.mu.Unlock()
		}
	}
	return schema.AssistantMessage("ok", nil), nil
}

func (m *taskContextRecordingModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (m *taskContextRecordingModel) systemSnapshot() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.system...)
}

func newTaskContextRunnerTest(t *testing.T, chatModel model.ToolCallingChatModel) *Runner {
	t.Helper()
	runner, err := NewDefaultRunner(context.Background(), DefaultRunnerDependencies{
		ChatModel: chatModel, ExternalCases: runnerTestCaseGetter{},
		SkillRoot:           filepath.Join("..", "..", "config", "skills"),
		SystemInstruction:   runnerTestSystemInstruction,
		BaselineInstruction: runnerTestBaselineInstruction,
		Logger:              zap.NewNop(),
	})
	if err != nil {
		t.Fatalf("NewDefaultRunner: %v", err)
	}
	return runner
}

const diagnosisTaskContextBlockForTest = "<task_context>\n{\"policySchemaVersion\":1,\"effectivePermissions\":[\"case.read\"],\"externalCaseId\":\"11111111-1111-1111-1111-111111111111\",\"attachmentCount\":0}\n</task_context>"

func TestRunnerAppendsDiagnosisTaskContextOnlyAtSystemTail(t *testing.T) {
	recording := &taskContextRecordingModel{}
	runner := newTaskContextRunnerTest(t, recording)
	ctx := withRunnerTestRunAccess(context.Background(), runnerTestAccess(t, agentruntime.PermissionCaseRead))
	ctx = WithDiagnosisTaskContext(ctx, diagnosisTaskContextBlockForTest)
	result, err := runner.Invoke(ctx, RunRequest{
		UserQuery: "诊断工单", ExternalCaseID: runnerTestCaseID.String(),
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if result.Answer != "ok" {
		t.Fatalf("answer = %q", result.Answer)
	}
	systems := recording.systemSnapshot()
	if len(systems) != 1 {
		t.Fatalf("system messages = %d, want 1", len(systems))
	}
	if strings.Count(systems[0], "<task_context>") != 1 {
		t.Fatalf("task_context appears %d times, want 1: %q", strings.Count(systems[0], "<task_context>"), systems[0])
	}
	// task_context 必须位于应用侧 system 指令最尾部：块之后只能紧跟 Eino
	// Skill Middleware 自己的静态 "# Skills System" 后缀（框架级、与任务无关）。
	blockIndex := strings.Index(systems[0], diagnosisTaskContextBlockForTest)
	if blockIndex <= 0 {
		t.Fatalf("task_context missing from the system instruction: %q", systems[0])
	}
	remainder := systems[0][blockIndex+len(diagnosisTaskContextBlockForTest):]
	if !strings.HasPrefix(remainder, "\n\n# Skills System") {
		t.Fatalf("task_context is not at the application instruction tail; remainder = %q", remainder[:min(len(remainder), 120)])
	}
	// entry_skill 与 authorized_data_sources 块必须在 task_context 之前。
	if !strings.Contains(systems[0][:blockIndex], "configured system instruction") {
		t.Fatalf("task_context must follow the base instruction: %q", systems[0][:blockIndex])
	}
}

func TestDiagnosisPreflightCountsIdenticalTaskContext(t *testing.T) {
	recording := &taskContextRecordingModel{}
	runner := newTaskContextRunnerTest(t, recording)
	planner := &diagnosisGuardPlanner{plans: []contextgovernance.TokenBudgetPlan{{
		AvailableInputTokens: 4000, EstimatedUpperBoundTokens: 800, ReservedTokens: 16,
		EstimationMethod: contextgovernance.EstimationMethodLocalCalibrated,
	}}}
	runner.contextPreflight = diagnosisContextPreflightForTest(planner)
	ctx := withRunnerTestRunAccess(context.Background(), runnerTestAccess(t, agentruntime.PermissionCaseRead))
	ctx = WithDiagnosisTaskContext(ctx, diagnosisTaskContextBlockForTest)
	result, err := runner.Invoke(ctx, RunRequest{
		UserQuery: "诊断工单", ExternalCaseID: runnerTestCaseID.String(),
		CaseSnapshot: `{"id":"11111111-1111-1111-1111-111111111111","title":"报工状态未更新"}`,
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if result.ContextObservation.PreflightCalls != 1 {
		t.Fatalf("preflight calls = %d, want 1", result.ContextObservation.PreflightCalls)
	}
	systems := recording.systemSnapshot()
	if len(systems) != 1 || !strings.Contains(systems[0], diagnosisTaskContextBlockForTest) {
		t.Fatalf("real system instruction = %v", systems)
	}
	requests := planner.requestSnapshot()
	if len(requests) != 1 {
		t.Fatalf("planner requests = %d, want 1", len(requests))
	}
	var preflightSystem string
	for _, segment := range requests[0].Prompt.Segments {
		if segment.Kind == contextgovernance.PromptSegmentSystem {
			preflightSystem = segment.Content
			break
		}
	}
	if preflightSystem == "" {
		t.Fatal("preflight segments contain no system segment")
	}
	// 预检把真实 system 消息投影为 JSON；解码后必须与真实指令逐字节一致，
	// 即 preflight 统计的 task_context 与真实调用完全相同。
	var messages []struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(preflightSystem), &messages); err != nil {
		t.Fatalf("decode preflight system segment: %v", err)
	}
	if len(messages) != 1 || messages[0].Content != systems[0] {
		t.Fatalf("preflight system content != real system instruction:\n%q\nvs\n%q", messages[0].Content, systems[0])
	}
}
