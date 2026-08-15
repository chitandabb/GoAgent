package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/resilience"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

const (
	testToolReadCase  = "test_read_case"
	testToolGitHub    = "test_github_search"
	testToolReadSQL   = "test_read_sql"
	testToolLabSQL    = "test_lab_sql"
	testToolKnowledge = "test_knowledge_search"
)

// TestToolCatalogSchemaComesOnlyFromBoundProfile 证明模型可见 Schema 只由
// Catalog 绑定的固定 Profile 决定：引用、权限、依赖健康或调用次数都不参与，
// 注册表也不再有角色/任务/能力/依赖过滤字段。
func TestToolCatalogSchemaComesOnlyFromBoundProfile(t *testing.T) {
	catalog := newToolCatalogForTest(t)
	profile, err := agentruntime.NewToolProfile(agentruntime.ToolProfileDiagnosis, []string{
		testToolReadCase, testToolKnowledge, ToolSkill,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.BindProfile(profile, []string{ToolSkill}); err != nil {
		t.Fatalf("BindProfile: %v", err)
	}
	resolved, err := catalog.ResolveProfile(context.Background(), agentruntime.ToolProfileDiagnosis)
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	if got := toolNamesForTest(t, resolved.Tools); !slices.Equal(got, []string{testToolKnowledge, testToolReadCase}) {
		t.Fatalf("tool names = %v", got)
	}
	if !slices.Equal(resolved.ModelVisibleNames, []string{ToolSkill, testToolKnowledge, testToolReadCase}) {
		t.Fatalf("model visible names = %v", resolved.ModelVisibleNames)
	}
	// 重复解析结果不变：Schema 与任何 per-run 状态无关。
	resolvedAgain, err := catalog.ResolveProfile(context.Background(), agentruntime.ToolProfileDiagnosis)
	if err != nil {
		t.Fatalf("second ResolveProfile: %v", err)
	}
	if !slices.Equal(toolNamesForTest(t, resolvedAgain.Tools), toolNamesForTest(t, resolved.Tools)) {
		t.Fatal("profile resolution changed between calls")
	}
}

// TestToolCatalogEvaluationWideProfileResolvesUnionOfProductionProfiles 证明
// evaluation-wide-v2 是独立且完整的固定宽 Profile：它解析两个生产 Profile
// 的并集（含 Middleware-owned skill 与 read_skill_reference，以及
// conversation-only 工具），且 Catalog 不会伪造 skill Tool。
func TestToolCatalogEvaluationWideProfileResolvesUnionOfProductionProfiles(t *testing.T) {
	wideCatalog := comparabilityWideCatalogForTest(t)
	resolved, err := wideCatalog.ResolveProfile(context.Background(), agentruntime.ToolProfileEvaluationWide)
	if err != nil {
		t.Fatalf("ResolveProfile(evaluation-wide-v2): %v", err)
	}
	names := resolved.ModelVisibleNames
	for _, required := range []string{
		"search_code", ToolSearchKnowledge, ToolDatabaseObjectDefinition, ToolReadExternalCase,
		ToolExecuteReadonlyQuery, ToolSkill, ToolReadSkillReference,
		ToolCreateDiagnosisTask, ToolGetDiagnosisTaskStatus, ToolReadConversationToolResult,
		ToolSearchSchemaCatalog, ToolReadConversationMemorySources,
	} {
		if !slices.Contains(names, required) {
			t.Fatalf("wide profile is missing %q: %v", required, names)
		}
	}
	if slices.Contains(toolNamesForTest(t, resolved.Tools), ToolSkill) {
		t.Fatal("catalog resolved a fake skill Tool")
	}
}

// TestToolCatalogRechecksRunAccessWhenToolExecutes 证明执行期 Guard 仍读取
// RunAccess：Schema 可见不等于可执行。
func TestToolCatalogRechecksRunAccessWhenToolExecutes(t *testing.T) {
	catalog := newToolCatalogForTest(t)
	profile, err := agentruntime.NewToolProfile(agentruntime.ToolProfileDiagnosis, []string{
		testToolGitHub, testToolReadCase, ToolSkill,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.BindProfile(profile, []string{ToolSkill}); err != nil {
		t.Fatalf("BindProfile: %v", err)
	}
	resolved, err := catalog.ResolveProfile(context.Background(), agentruntime.ToolProfileDiagnosis)
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	var github tool.InvokableTool
	for _, current := range resolved.Tools {
		info, infoErr := current.Info(context.Background())
		if infoErr != nil {
			t.Fatalf("Tool.Info: %v", infoErr)
		}
		if info.Name == testToolGitHub {
			github = current.(tool.InvokableTool)
			break
		}
	}
	if github == nil {
		t.Fatal("authorized GitHub tool is missing")
	}
	authorized := mustDiagnosisTestRunAccess(t, uuid.New(),
		[]agentruntime.Permission{agentruntime.PermissionCaseRead, agentruntime.PermissionCodeRead},
		agentruntime.ResourceGrantsConfig{},
	)
	if _, err := github.InvokableRun(withTestRunAccess(context.Background(), authorized), `{}`); err != nil {
		t.Fatalf("authorized InvokableRun: %v", err)
	}

	denied := mustDiagnosisTestRunAccess(t, uuid.New(),
		[]agentruntime.Permission{agentruntime.PermissionCaseRead},
		agentruntime.ResourceGrantsConfig{},
	)
	if _, err := github.InvokableRun(withTestRunAccess(context.Background(), denied), `{}`); !errors.Is(err, ErrToolNotAllowed) {
		t.Fatalf("denied InvokableRun error = %v, want ErrToolNotAllowed", err)
	}
	if _, err := github.InvokableRun(context.Background(), `{}`); !errors.Is(err, ErrRunAccessRequired) {
		t.Fatalf("unscoped InvokableRun error = %v, want ErrRunAccessRequired", err)
	}
}

func TestToolCatalogRegistrationValidatesRequiredPermissions(t *testing.T) {
	duplicate := newNamedToolForTest(t, "test_duplicate_permission")
	validPolicy := ToolRegistration{
		Tool: duplicate, FailurePolicy: resilience.PolicyBestEffort,
	}
	duplicated := validPolicy
	duplicated.RequiredPermissions = []agentruntime.Permission{
		agentruntime.PermissionSQLRead, agentruntime.PermissionSQLRead,
	}
	if _, err := NewToolCatalog(context.Background(), duplicated); err == nil {
		t.Fatal("NewToolCatalog accepted duplicated required permissions")
	}
	invalid := validPolicy
	invalid.RequiredPermissions = []agentruntime.Permission{"sql.write"}
	if _, err := NewToolCatalog(context.Background(), invalid); err == nil {
		t.Fatal("NewToolCatalog accepted an invalid required permission")
	}
}

func TestToolCatalogRejectsDuplicateNamesAndInvalidPolicy(t *testing.T) {
	duplicateA := newNamedToolForTest(t, "test_duplicate")
	duplicateB := newNamedToolForTest(t, "test_duplicate")
	validPolicy := ToolRegistration{FailurePolicy: resilience.PolicyBestEffort}
	first := validPolicy
	first.Tool = duplicateA
	second := validPolicy
	second.Tool = duplicateB
	if _, err := NewToolCatalog(context.Background(), first, second); err == nil {
		t.Fatal("NewToolCatalog accepted duplicate tool names")
	}
}

func TestToolCatalogRequiresExplicitFailurePolicy(t *testing.T) {
	_, err := NewToolCatalog(context.Background(), ToolRegistration{
		Tool: newNamedToolForTest(t, "test_missing_failure_policy"),
	})
	if err == nil {
		t.Fatal("NewToolCatalog accepted a tool without an explicit failure policy")
	}
}

func TestScopeGuardedToolKeepsStrictFailureAsError(t *testing.T) {
	want := errors.New("side effect failed")
	current := scopedFailingToolForTest(t, resilience.PolicyStrict, want, nil)
	ctx := withTestRunAccess(context.Background(), mustDiagnosisTestRunAccess(t, uuid.New(),
		[]agentruntime.Permission{agentruntime.PermissionCaseRead},
		agentruntime.ResourceGrantsConfig{},
	))
	if _, err := current.InvokableRun(ctx, `{}`); !errors.Is(err, want) {
		t.Fatalf("InvokableRun error = %v, want strict failure", err)
	}
}

func TestScopeGuardedToolReturnsStructuredBestEffortFailure(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(previous)
	})
	var observed []resilience.DegradationEvent
	current := scopedFailingToolForTest(
		t, resilience.PolicyBestEffort,
		resilience.RetryableFailure(errors.New("dial tcp secret.internal:1433")),
		resilience.ObserverFunc(func(event resilience.DegradationEvent) { observed = append(observed, event) }),
	)
	ctx := withTestRunAccess(context.Background(), mustDiagnosisTestRunAccess(t, uuid.New(),
		[]agentruntime.Permission{agentruntime.PermissionCaseRead},
		agentruntime.ResourceGrantsConfig{},
	))
	ctx = resilience.WithRunIdentity(ctx, resilience.RunIdentity{RunID: "run-1", TraceID: "trace-1"})
	endpoint := newToolObservabilityMiddleware().Invokable(func(
		ctx context.Context, input *compose.ToolInput,
	) (*compose.ToolOutput, error) {
		result, err := current.InvokableRun(ctx, input.Arguments)
		return &compose.ToolOutput{Result: result}, err
	})
	output, err := endpoint(ctx, &compose.ToolInput{
		Name: "test_failing_tool", CallID: "tool-call-1", Arguments: `{}`,
	})
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	raw := output.Result
	var result struct {
		OK           bool                          `json:"ok"`
		Error        string                        `json:"error"`
		ReasonCode   string                        `json:"reasonCode"`
		Degradations []resilience.DegradationEvent `json:"degradations"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode structured Tool error: %v", err)
	}
	if result.OK || result.Error != "tool_unavailable" || result.ReasonCode != "tool_execution_failed" ||
		len(result.Degradations) != 1 || result.Degradations[0].Policy != resilience.PolicyBestEffort ||
		result.Degradations[0].RunID != "run-1" || result.Degradations[0].TraceID != "trace-1" {
		t.Fatalf("structured Tool failure = %+v", result)
	}
	if strings.Contains(raw, "secret.internal") {
		t.Fatalf("structured Tool failure leaked provider details: %s", raw)
	}
	if len(observed) != 1 || observed[0] != result.Degradations[0] {
		t.Fatalf("observed degradations = %+v, result = %+v", observed, result.Degradations)
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Name != "tool.test_failing_tool" {
		t.Fatalf("unexpected Tool spans: %#v", spans)
	}
	callID := ""
	for _, item := range spans[0].Attributes {
		if string(item.Key) == "mesguard.tool_call.id" {
			callID = item.Value.AsString()
		}
	}
	if callID != "tool-call-1" {
		t.Fatalf("toolCallId = %q, want tool-call-1", callID)
	}
	if spans[0].Status.Code != codes.Error {
		t.Fatalf("degraded Tool status = %s, want Error", spans[0].Status.Code)
	}
	foundDegradation := false
	for _, event := range spans[0].Events {
		if event.Name == "mesguard.degradation" {
			foundDegradation = true
			break
		}
	}
	if !foundDegradation {
		t.Fatalf("Tool span events = %#v, want degradation", spans[0].Events)
	}
}

func TestToolResultDegradedRecognizesBothTruncationFormats(t *testing.T) {
	for _, result := range []string{
		"value\n[tool result truncated by MESGuard]",
		"value\n[tool result truncated by MESGuard; ref=result-1; original_bytes=999]",
		`{"ok":false,"error":"tool_unavailable","degradations":[{"reasonCode":"timeout"}]}`,
	} {
		if !inspectToolResult(result).Degraded {
			t.Fatalf("inspectToolResult(%q) did not report degradation", result)
		}
	}
	if inspectToolResult(`{"ok":true,"result":"normal"}`).Degraded {
		t.Fatal("normal Tool result must not be degraded")
	}
	structured := inspectToolResult(`{"truncated":true}`)
	if !structured.Truncated || structured.Status != "degraded" {
		t.Fatalf("structured truncation = %+v", structured)
	}
}

func TestScopeGuardedToolDistinguishesRejectedAndStrictFailures(t *testing.T) {
	ctx := withTestRunAccess(context.Background(), mustDiagnosisTestRunAccess(t, uuid.New(),
		[]agentruntime.Permission{agentruntime.PermissionCaseRead},
		agentruntime.ResourceGrantsConfig{},
	))
	ctx = resilience.WithRunIdentity(ctx, resilience.RunIdentity{RunID: "run-2"})
	rejected := scopedFailingToolForTest(t, resilience.PolicyBestEffort, errors.New("argument is invalid"), nil)
	raw, err := rejected.InvokableRun(ctx, `{}`)
	if err != nil || !strings.Contains(raw, `"error":"tool_call_rejected"`) ||
		!strings.Contains(raw, `"retryable":false`) || strings.Contains(raw, `"degradations"`) {
		t.Fatalf("rejected failure = %s, %v", raw, err)
	}
	strictCause := errors.New("readonly query rejected")
	strict := scopedFailingToolForTest(
		t, resilience.PolicyBestEffort, resilience.StrictFailure(strictCause), nil,
	)
	if _, err := strict.InvokableRun(ctx, `{}`); !errors.Is(err, strictCause) {
		t.Fatalf("classified strict failure = %v", err)
	}
}

func scopedFailingToolForTest(
	t *testing.T,
	policy resilience.Policy,
	want error,
	observer resilience.Observer,
) tool.InvokableTool {
	t.Helper()
	inner, err := toolutils.InferTool("test_failing_tool", "test tool", func(context.Context, struct{}) (string, error) {
		return "", want
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewToolCatalog(context.Background(), ToolRegistration{
		Tool: inner, FailurePolicy: policy, DegradationObserver: observer,
		RequiredPermissions: []agentruntime.Permission{agentruntime.PermissionCaseRead},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := agentruntime.NewToolProfile(agentruntime.ToolProfileDiagnosis, []string{
		"test_failing_tool", ToolSkill,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.BindProfile(profile, []string{ToolSkill}); err != nil {
		t.Fatal(err)
	}
	resolved, err := catalog.ResolveProfile(context.Background(), agentruntime.ToolProfileDiagnosis)
	if err != nil || len(resolved.Tools) != 1 {
		t.Fatalf("ResolveProfile = %d, %v", len(resolved.Tools), err)
	}
	return resolved.Tools[0].(tool.InvokableTool)
}

func newToolCatalogForTest(t *testing.T) *ToolCatalog {
	t.Helper()
	registrations := []ToolRegistration{
		{
			Tool:                newNamedToolForTest(t, testToolReadCase),
			FailurePolicy:       resilience.PolicyBestEffort,
			RequiredPermissions: []agentruntime.Permission{agentruntime.PermissionCaseRead},
		},
		{
			Tool:                newNamedToolForTest(t, testToolGitHub),
			FailurePolicy:       resilience.PolicyBestEffort,
			RequiredPermissions: []agentruntime.Permission{agentruntime.PermissionCodeRead},
		},
		{
			Tool:                newNamedToolForTest(t, testToolReadSQL),
			FailurePolicy:       resilience.PolicyBestEffort,
			RequiredPermissions: []agentruntime.Permission{agentruntime.PermissionSQLRead},
		},
		{
			Tool:                newNamedToolForTest(t, testToolLabSQL),
			FailurePolicy:       resilience.PolicyBestEffort,
			RequiredPermissions: []agentruntime.Permission{agentruntime.PermissionSQLRead},
		},
		{
			Tool:                newNamedToolForTest(t, testToolKnowledge),
			FailurePolicy:       resilience.PolicyBestEffort,
			RequiredPermissions: []agentruntime.Permission{agentruntime.PermissionKnowledgeRead},
		},
	}
	catalog, err := NewToolCatalog(context.Background(), registrations...)
	if err != nil {
		t.Fatalf("NewToolCatalog: %v", err)
	}
	return catalog
}

func newEvaluationWideToolCatalogForTest(t *testing.T) *ToolCatalog {
	t.Helper()
	catalog := newToolCatalogForTest(t)
	profile, err := agentruntime.NewToolProfile(agentruntime.ToolProfileEvaluationWide, []string{
		testToolGitHub, testToolKnowledge, testToolReadCase, testToolReadSQL, testToolLabSQL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.BindProfile(profile, []string{ToolSkill}); err != nil {
		t.Fatalf("BindProfile(evaluation-wide-v1): %v", err)
	}
	return catalog
}

func newNamedToolForTest(t *testing.T, name string) tool.InvokableTool {
	t.Helper()
	current, err := toolutils.InferTool(name, "test tool", func(context.Context, struct{}) (string, error) {
		return name, nil
	})
	if err != nil {
		t.Fatalf("InferTool(%s): %v", name, err)
	}
	return current
}

func toolNamesForTest(t *testing.T, tools []tool.BaseTool) []string {
	t.Helper()
	names := make([]string, 0, len(tools))
	for _, current := range tools {
		info, err := current.Info(context.Background())
		if err != nil {
			t.Fatalf("Tool.Info: %v", err)
		}
		names = append(names, info.Name)
	}
	return names
}
