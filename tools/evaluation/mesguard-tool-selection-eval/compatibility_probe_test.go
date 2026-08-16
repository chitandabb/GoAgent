package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/evaluationidentity"
	platformchatmodel "github.com/chitandabb/GoAgent/internal/platform/chatmodel"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/platform/githubmcp"

	openai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// probeGenerateRecord 记录一次 Generate 的全部单变量输入：绑定时的
// ToolInfo、messages 与解析后的 options（temperature、maxTokens、toolChoice）。
type probeGenerateRecord struct {
	toolInfos    []*schema.ToolInfo
	messagesJSON string
	temperature  *float32
	maxTokens    *int
	toolChoice   *schema.ToolChoice
}

// compatibilityProbeModelStub 是兼容性探针的脚本化模型：顺序记录每次
// Generate 的输入，errors 按调用序号返回（nil 或越界时成功）。成功响应带
// 单个 read_external_case Tool 调用与 Provider usage。
type compatibilityProbeModelStub struct {
	mu        sync.Mutex
	errors    []error
	records   []probeGenerateRecord
	bound     []*schema.ToolInfo
	calls     atomic.Int32
	usageCall int32
}

func (m *compatibilityProbeModelStub) Generate(_ context.Context, messages []*schema.Message, options ...model.Option) (*schema.Message, error) {
	index := int(m.calls.Add(1)) - 1
	encoded, _ := json.Marshal(messages)
	applied := model.GetCommonOptions(&model.Options{}, options...)
	record := probeGenerateRecord{
		messagesJSON: string(encoded),
		temperature:  applied.Temperature,
		maxTokens:    applied.MaxTokens,
		toolChoice:   applied.ToolChoice,
	}
	m.mu.Lock()
	record.toolInfos = append([]*schema.ToolInfo(nil), m.bound...)
	m.records = append(m.records, record)
	m.mu.Unlock()
	if index < len(m.errors) && m.errors[index] != nil {
		return nil, m.errors[index]
	}
	return &schema.Message{
		Role:    schema.Assistant,
		Content: "ok",
		ToolCalls: []schema.ToolCall{{
			Index: intPtr(0),
			Function: schema.FunctionCall{
				Name: mesagent.ToolReadExternalCase, Arguments: "{}",
			},
		}},
		ResponseMeta: &schema.ResponseMeta{
			FinishReason: "stop",
			Usage: &schema.TokenUsage{
				PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110,
			},
		},
	}, nil
}

func (m *compatibilityProbeModelStub) Stream(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, messages, options...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (m *compatibilityProbeModelStub) WithTools(infos []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bound = append([]*schema.ToolInfo(nil), infos...)
	return m, nil
}

func (m *compatibilityProbeModelStub) snapshotRecords() []probeGenerateRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]probeGenerateRecord(nil), m.records...)
}

// compatibilityProbeObservationForTest 是探针 JSONL 的测试侧投影。
type compatibilityProbeObservationForTest struct {
	ObservationSchemaVersion string               `json:"observationSchemaVersion"`
	Scenario                 string               `json:"scenario"`
	ModelProvider            string               `json:"modelProvider"`
	ModelID                  string               `json:"modelId"`
	ModelProfileFingerprint  string               `json:"modelProfileFingerprint"`
	ImplementationRevision   string               `json:"implementationRevision"`
	ImplementationDirty      bool                 `json:"implementationDirty"`
	ToolCount                int                  `json:"toolCount"`
	ToolNames                []string             `json:"toolNames"`
	ToolSchemaFingerprint    string               `json:"toolSchemaFingerprint"`
	ToolChoiceMode           string               `json:"toolChoiceMode"`
	Success                  bool                 `json:"success"`
	ErrorCategory            string               `json:"errorCategory"`
	HTTPStatusCode           int                  `json:"httpStatusCode"`
	ProviderType             string               `json:"providerType"`
	ProviderCode             string               `json:"providerCode"`
	ProviderParam            string               `json:"providerParam"`
	FinishReason             string               `json:"finishReason"`
	ToolCallCount            int                  `json:"toolCallCount"`
	SelectedTool             string               `json:"selectedTool"`
	DurationMillis           int64                `json:"durationMillis"`
	Usage                    *mesagent.ModelUsage `json:"usage"`
}

// runCompatibilityProbeForTest 用给定模型桩与可选身份注入跑探针模式，
// 返回探针输出路径与正式输出路径（用于断言互不污染）。
func runCompatibilityProbeForTest(
	t *testing.T,
	modelStub model.ToolCallingChatModel,
	extraArgs []string,
	identityOverride func() (evaluationidentity.Identity, error),
) (string, string) {
	t.Helper()
	dataset := writeSelectionDatasetForTest(t)
	outputDir := t.TempDir()
	output := filepath.Join(outputDir, "formal-obs.jsonl")
	summary := filepath.Join(outputDir, "formal-summary.json")
	probeOutput := filepath.Join(outputDir, "probe-obs.jsonl")

	var modelFactoryCalls atomic.Int32
	var githubCalls atomic.Int32
	deps := defaultSelectionEvalDependencies()
	deps.loadConfig = func() (config.Config, error) { return selectionTestConfig(), nil }
	deps.connectGitHub = func(context.Context, config.GitHubMCPConfig, *zap.Logger) (*githubmcp.Connection, error) {
		githubCalls.Add(1)
		return &githubmcp.Connection{}, nil
	}
	deps.newChatModel = func(context.Context, string, config.ChatModelProfileConfig) (model.ToolCallingChatModel, error) {
		modelFactoryCalls.Add(1)
		return modelStub, nil
	}
	if identityOverride != nil {
		deps.resolveIdentity = identityOverride
	}
	args := append([]string{
		"-dataset", dataset, "-output", output, "-summary", summary,
		"-probe-output", probeOutput,
		"-concurrency", "1", "-allow-dirty",
		"-case-id", "case-1",
		"-compatibility-probe",
		"-allow-provider-calls", "-max-cases", "1",
		"-max-provider-calls", "5", "-max-provider-tokens", "5000000",
	}, extraArgs...)
	if err := runWithDependencies(context.Background(), args, zap.NewNop(), deps); err != nil {
		t.Fatalf("runWithDependencies(compatibility probe): %v", err)
	}
	if modelFactoryCalls.Load() != 1 {
		t.Fatalf("newChatModel called %d times, want exactly 1", modelFactoryCalls.Load())
	}
	if githubCalls.Load() != 1 {
		t.Fatalf("connectGitHub called %d times, want exactly 1", githubCalls.Load())
	}
	return probeOutput, output
}

func readCompatibilityProbeObservations(t *testing.T, path string) []compatibilityProbeObservationForTest {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read probe observations: %v", err)
	}
	var observations []compatibilityProbeObservationForTest
	for _, line := range strings.Split(strings.TrimSpace(string(contents)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var observation compatibilityProbeObservationForTest
		if err := json.Unmarshal([]byte(line), &observation); err != nil {
			t.Fatalf("decode probe observation %q: %v", line, err)
		}
		observations = append(observations, observation)
	}
	return observations
}

func probeInfosJSON(t *testing.T, infos []*schema.ToolInfo) string {
	t.Helper()
	encoded, err := json.Marshal(infos)
	if err != nil {
		t.Fatalf("marshal tool infos: %v", err)
	}
	return string(encoded)
}

// TestCompatibilityProbeScenarioMatrixOrderAndSingleVariable 证明探针固定
// 5 个场景、顺序固定，且单变量合同成立：
//   - 场景 1/2 除 tool_choice 外（messages、ToolInfo、temperature、maxTokens）
//     完全一致；
//   - 4 个 no_choice 场景完全不发送 ToolChoice option；
//   - many_simple 数量等于 production 最终模型可见 Tool 数且 Schema 形状一致
//     （仅名称不同）；
//   - one_real 使用真实 read_external_case ToolInfo；
//   - full_production 使用真实最终 production Tool 集。
func TestCompatibilityProbeScenarioMatrixOrderAndSingleVariable(t *testing.T) {
	stub := &compatibilityProbeModelStub{}
	probeOutput, formalOutput := runCompatibilityProbeForTest(t, stub, nil, nil)
	observations := readCompatibilityProbeObservations(t, probeOutput)

	wantScenarios := []string{
		"one_simple_no_choice",
		"one_simple_required",
		"many_simple_no_choice",
		"one_real_no_choice",
		"full_production_no_choice",
	}
	if len(observations) != 5 {
		t.Fatalf("probe observations = %d, want exactly 5", len(observations))
	}
	for index, want := range wantScenarios {
		if observations[index].Scenario != want {
			t.Fatalf("observation %d scenario = %q, want %q (fixed order)", index, observations[index].Scenario, want)
		}
		if observations[index].ObservationSchemaVersion != "tool-compatibility-probe-v1" {
			t.Fatalf("observation %d schema version = %q", index, observations[index].ObservationSchemaVersion)
		}
	}
	for index, want := range []string{"absent", "required", "absent", "absent", "absent"} {
		if observations[index].ToolChoiceMode != want {
			t.Fatalf("observation %d toolChoiceMode = %q, want %q", index, observations[index].ToolChoiceMode, want)
		}
	}
	if _, err := os.Stat(formalOutput); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("probe mode must not write the formal observations output: %v", err)
	}

	records := stub.snapshotRecords()
	if len(records) != 5 {
		t.Fatalf("Generate called %d times, want exactly 5", len(records))
	}
	for index, record := range records {
		if record.temperature == nil || *record.temperature != 0 {
			t.Fatalf("record %d temperature = %v, want 0", index, record.temperature)
		}
		if record.maxTokens == nil || *record.maxTokens != toolSelectionMaxTokens {
			t.Fatalf("record %d maxTokens = %v, want %d", index, record.maxTokens, toolSelectionMaxTokens)
		}
		if record.messagesJSON == "" || record.messagesJSON != records[0].messagesJSON {
			t.Fatalf("record %d messages must be byte-identical across scenarios", index)
		}
	}
	// 4 个 no_choice 场景完全不传 ToolChoice（不是显式 auto）。
	for _, index := range []int{0, 2, 3, 4} {
		if records[index].toolChoice != nil {
			t.Fatalf("record %d (no_choice) must omit ToolChoice entirely, got %v", index, *records[index].toolChoice)
		}
	}
	if records[1].toolChoice == nil || *records[1].toolChoice != schema.ToolChoiceForced {
		t.Fatalf("record 1 toolChoice = %v, want ToolChoiceForced", records[1].toolChoice)
	}
	// 场景 1/2 除 tool_choice 外完全一致。
	if probeInfosJSON(t, records[0].toolInfos) != probeInfosJSON(t, records[1].toolInfos) {
		t.Fatal("scenario 1 and 2 must bind byte-identical tool infos")
	}
	if len(records[0].toolInfos) != 1 {
		t.Fatalf("one_simple must bind exactly 1 tool, got %d", len(records[0].toolInfos))
	}

	// 真实 production 最终 Tool 集（与探针同一装配源）。
	skillRuntime := skillRuntimeForSelectionTest(t)
	assembly, err := assembleSelectionEval(context.Background(), nil, skillRuntime, mesagent.VerifyToolSelectionComparability)
	if err != nil {
		t.Fatalf("assembleSelectionEval: %v", err)
	}
	productionInfos, _, err := selectionArmTools(context.Background(), assembly.productionAuthorization, skillRuntime.Middleware)
	if err != nil {
		t.Fatalf("selectionArmTools(production): %v", err)
	}
	productionJSON := probeInfosJSON(t, productionInfos)

	// many_simple：数量等于 production 且 Schema 形状一致（仅名称唯一）。
	if len(records[2].toolInfos) != len(productionInfos) {
		t.Fatalf("many_simple bound %d tools, want production count %d", len(records[2].toolInfos), len(productionInfos))
	}
	if records[2].toolInfos[0].Name != records[0].toolInfos[0].Name {
		t.Fatalf("many_simple first tool = %q, want the one_simple tool %q",
			records[2].toolInfos[0].Name, records[0].toolInfos[0].Name)
	}
	if observations[2].ToolCount != len(productionInfos) {
		t.Fatalf("many_simple toolCount = %d, want %d", observations[2].ToolCount, len(productionInfos))
	}
	names := map[string]bool{}
	baseSchema := ""
	for _, info := range records[2].toolInfos {
		if names[info.Name] {
			t.Fatalf("many_simple tool names must be unique, duplicate %q", info.Name)
		}
		names[info.Name] = true
		withoutName, marshalErr := json.Marshal(struct {
			Desc        string
			ParamsOneOf *schema.ParamsOneOf
		}{info.Desc, info.ParamsOneOf})
		if marshalErr != nil {
			t.Fatalf("marshal many_simple schema shape: %v", marshalErr)
		}
		if baseSchema == "" {
			baseSchema = string(withoutName)
		} else if string(withoutName) != baseSchema {
			t.Fatalf("many_simple schemas must share one shape, %q differs", info.Name)
		}
	}

	// one_real：真实 read_external_case ToolInfo。
	var realInfo *schema.ToolInfo
	for _, info := range productionInfos {
		if info.Name == mesagent.ToolReadExternalCase {
			realInfo = info
		}
	}
	if realInfo == nil {
		t.Fatal("production infos must contain read_external_case")
	}
	if len(records[3].toolInfos) != 1 || records[3].toolInfos[0].Name != mesagent.ToolReadExternalCase {
		t.Fatalf("one_real must bind exactly the real read_external_case tool, got %d infos", len(records[3].toolInfos))
	}
	if probeInfosJSON(t, records[3].toolInfos) != probeInfosJSON(t, []*schema.ToolInfo{realInfo}) {
		t.Fatal("one_real must bind the byte-identical production read_external_case ToolInfo")
	}

	// full_production：真实最终 production Tool 集（含 skill），按名排序后逐字节一致。
	if probeInfosJSON(t, records[4].toolInfos) != productionJSON {
		t.Fatal("full_production must bind the byte-identical production final tool infos")
	}
	if observations[4].ToolCount != len(productionInfos) {
		t.Fatalf("full_production toolCount = %d, want %d", observations[4].ToolCount, len(productionInfos))
	}
}

// TestCompatibilityProbeFailClosedBeforeProvider 证明缺授权、预算不足、
// dirty revision、Case 数不为一时 Provider factory 与 GitHub 连接调用数均为 0，
// 且不创建探针输出。
func TestCompatibilityProbeFailClosedBeforeProvider(t *testing.T) {
	scenarios := []struct {
		name          string
		twoCases      bool
		omitCaseID    bool
		dirtyIdentity bool
		args          []string
		wantErr       string
	}{
		{name: "missing authorization", args: []string{"-max-cases", "1", "-max-provider-calls", "5", "-max-provider-tokens", "5000000"}, wantErr: "allow-provider-calls"},
		{name: "call bound below fixed matrix", args: []string{"-allow-provider-calls", "-max-cases", "1", "-max-provider-calls", "4", "-max-provider-tokens", "5000000"}, wantErr: "max-provider-calls"},
		{name: "token bound exceeded", args: []string{"-allow-provider-calls", "-max-cases", "1", "-max-provider-calls", "5", "-max-provider-tokens", "1"}, wantErr: "max-provider-tokens"},
		{name: "two cases without case-id", twoCases: true, omitCaseID: true, args: []string{"-allow-provider-calls", "-max-cases", "2", "-max-provider-calls", "5", "-max-provider-tokens", "5000000"}, wantErr: "exactly one case"},
		{name: "dirty revision without allow-dirty", dirtyIdentity: true, args: []string{"-allow-provider-calls", "-max-cases", "1", "-max-provider-calls", "5", "-max-provider-tokens", "5000000"}, wantErr: "allow-dirty"},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			dataset := writeSelectionDatasetForTest(t)
			if scenario.twoCases {
				dataset = writeSelectionTwoCaseDatasetForTest(t)
			}
			outputDir := t.TempDir()
			probeOutput := filepath.Join(outputDir, "probe-obs.jsonl")

			var modelCalls atomic.Int32
			var githubCalls atomic.Int32
			deps := defaultSelectionEvalDependencies()
			deps.loadConfig = func() (config.Config, error) { return selectionTestConfig(), nil }
			deps.newChatModel = func(context.Context, string, config.ChatModelProfileConfig) (model.ToolCallingChatModel, error) {
				modelCalls.Add(1)
				return nil, errors.New("factory must not be reached before probe gates")
			}
			deps.connectGitHub = func(context.Context, config.GitHubMCPConfig, *zap.Logger) (*githubmcp.Connection, error) {
				githubCalls.Add(1)
				return nil, errors.New("GitHub must not be reached before probe gates")
			}
			if scenario.dirtyIdentity {
				deps.resolveIdentity = func() (evaluationidentity.Identity, error) {
					return evaluationidentity.Identity{Revision: "abc123", Dirty: true}, nil
				}
			}
			args := append([]string{
				"-dataset", dataset, "-output", filepath.Join(outputDir, "formal.jsonl"),
				"-summary", filepath.Join(outputDir, "formal-summary.json"),
				"-probe-output", probeOutput, "-concurrency", "1",
				"-compatibility-probe",
			}, scenario.args...)
			if !scenario.dirtyIdentity {
				args = append(args, "-allow-dirty")
			}
			if !scenario.omitCaseID {
				args = append(args, "-case-id", "case-1")
			}
			err := runWithDependencies(context.Background(), args, zap.NewNop(), deps)
			if err == nil || !strings.Contains(err.Error(), scenario.wantErr) {
				t.Fatalf("runWithDependencies error = %v, want %q", err, scenario.wantErr)
			}
			if modelCalls.Load() != 0 {
				t.Fatalf("newChatModel called %d times, want 0", modelCalls.Load())
			}
			if githubCalls.Load() != 0 {
				t.Fatalf("connectGitHub called %d times, want 0", githubCalls.Load())
			}
			if _, statErr := os.Stat(probeOutput); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("probe output must not be created on gate rejection: %v", statErr)
			}
		})
	}
}

// TestCompatibilityProbeErrorClassificationAndSanitization 证明 400/429/5xx/
// timeout 稳定分类到独立探针 observation，原始错误消息绝不进入 JSON，Usage
// 每次成功只累计一次、失败不估算 Token。
func TestCompatibilityProbeErrorClassificationAndSanitization(t *testing.T) {
	secret := "sensitive provider body with prompt and credential details"
	stub := &compatibilityProbeModelStub{errors: []error{
		nil,
		&openai.APIError{Message: secret, Type: "invalid_request_error", Code: "bad_param", HTTPStatusCode: 400},
		&openai.APIError{Message: secret, Type: "rate_limit_error", HTTPStatusCode: 429},
		&openai.APIError{Message: secret, Type: "server_error", HTTPStatusCode: 503},
		context.DeadlineExceeded,
	}}
	probeOutput, _ := runCompatibilityProbeForTest(t, stub, nil, nil)
	observations := readCompatibilityProbeObservations(t, probeOutput)
	if len(observations) != 5 {
		t.Fatalf("probe observations = %d, want 5", len(observations))
	}
	wantCategories := []string{
		"", platformchatmodel.ProviderErrorCategoryBadRequest,
		platformchatmodel.ProviderErrorCategoryRateLimited,
		platformchatmodel.ProviderErrorCategoryServer,
		platformchatmodel.ProviderErrorCategoryTimeout,
	}
	for index, want := range wantCategories {
		if observations[index].ErrorCategory != want {
			t.Fatalf("observation %d errorCategory = %q, want %q", index, observations[index].ErrorCategory, want)
		}
		if want == "" && !observations[index].Success {
			t.Fatalf("observation %d must be success", index)
		}
		if want != "" && observations[index].Success {
			t.Fatalf("observation %d must be failure", index)
		}
	}
	if observations[1].HTTPStatusCode != 400 || observations[1].ProviderType != "invalid_request_error" || observations[1].ProviderCode != "bad_param" {
		t.Fatalf("observation 1 provider details = %+v", observations[1])
	}
	contents, err := os.ReadFile(probeOutput)
	if err != nil {
		t.Fatalf("read probe output: %v", err)
	}
	if strings.Contains(string(contents), secret) || strings.Contains(string(contents), "credential") {
		t.Fatal("probe observations must not leak the provider error message")
	}
	// Usage：成功恰好一次（ModelCalls=1，不估算），失败无 Usage。
	if observations[0].Usage == nil || observations[0].Usage.ModelCalls != 1 ||
		observations[0].Usage.PromptTokens != 100 || observations[0].Usage.TotalTokens != 110 {
		t.Fatalf("success usage = %+v, want one reported usage 100/110", observations[0].Usage)
	}
	for _, index := range []int{1, 2, 3, 4} {
		if observations[index].Usage != nil {
			t.Fatalf("failed observation %d must not carry usage: %+v", index, observations[index].Usage)
		}
	}
	if observations[0].FinishReason != "stop" || observations[0].ToolCallCount != 1 ||
		observations[0].SelectedTool != mesagent.ToolReadExternalCase {
		t.Fatalf("success observation outcome fields = %+v", observations[0])
	}
	if observations[0].DurationMillis < 0 {
		t.Fatalf("duration must be non-negative, got %d", observations[0].DurationMillis)
	}
}

// TestCompatibilityProbeRequiresBothExplicitFlags 证明探针模式必须同时显式
// 传 -compatibility-probe 与 -allow-provider-calls：缺前者走正式路径（不产生
// 探针输出），缺后者 fail-closed。
func TestCompatibilityProbeRequiresBothExplicitFlags(t *testing.T) {
	// 缺 -allow-provider-calls 已由 fail-closed 表覆盖；这里验证不带
	// -compatibility-probe 时不产生探针输出（正式路径行为不变）。
	dataset := writeSelectionDatasetForTest(t)
	outputDir := t.TempDir()
	probeOutput := filepath.Join(outputDir, "probe-obs.jsonl")
	deps := defaultSelectionEvalDependencies()
	deps.loadConfig = func() (config.Config, error) { return selectionTestConfig(), nil }
	deps.connectGitHub = func(context.Context, config.GitHubMCPConfig, *zap.Logger) (*githubmcp.Connection, error) {
		return &githubmcp.Connection{}, nil
	}
	deps.newChatModel = func(context.Context, string, config.ChatModelProfileConfig) (model.ToolCallingChatModel, error) {
		return &selectionEvalModelStub{}, nil
	}
	err := runWithDependencies(context.Background(), []string{
		"-dataset", dataset, "-output", filepath.Join(outputDir, "formal.jsonl"),
		"-summary", filepath.Join(outputDir, "formal-summary.json"),
		"-probe-output", probeOutput, "-concurrency", "1", "-allow-dirty",
		"-allow-provider-calls", "-max-cases", "1",
		"-max-provider-calls", "10", "-max-provider-tokens", "5000000",
	}, zap.NewNop(), deps)
	if err != nil {
		t.Fatalf("formal mode must keep working unchanged: %v", err)
	}
	if _, statErr := os.Stat(probeOutput); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("probe output must not be created without -compatibility-probe: %v", statErr)
	}
}
