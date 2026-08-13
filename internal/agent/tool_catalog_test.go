package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/resilience"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/google/uuid"
)

const (
	testToolReadCase  = "test_read_case"
	testToolGitHub    = "test_github_search"
	testToolReadSQL   = "test_read_sql"
	testToolLabSQL    = "test_lab_sql"
	testToolKnowledge = "test_knowledge_search"
)

func TestToolCatalogFiltersByScope(t *testing.T) {
	catalog := newToolCatalogForTest(t)
	tests := []struct {
		name  string
		scope TaskScope
		want  []string
	}{
		{
			name: "analyst case with github",
			scope: mustTaskScope(t, auth.RoleAnalyst, TaskTypeDiagnosis, []ScopedDataSource{{
				ID: uuid.New(), Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly,
			}}, ToolDependencyExternalCase, ToolDependencyGitHubMCP),
			want: []string{testToolGitHub, testToolReadCase},
		},
		{
			name: "github capability denied while dependency is healthy",
			scope: mustTaskScopeWithCapabilities(t, auth.RoleAnalyst, TaskTypeDiagnosis, []ScopedDataSource{{
				ID: uuid.New(), Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly,
			}}, []ToolCapability{ToolCapabilityCase}, ToolDependencyExternalCase, ToolDependencyGitHubMCP),
			want: []string{testToolReadCase},
		},
		{
			name: "github dependency degraded",
			scope: mustTaskScope(t, auth.RoleAnalyst, TaskTypeDiagnosis, []ScopedDataSource{{
				ID: uuid.New(), Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly,
			}}, ToolDependencyExternalCase),
			want: []string{testToolReadCase},
		},
		{
			name: "diagnosis receives knowledge when dependency is healthy",
			scope: mustTaskScope(t, auth.RoleAnalyst, TaskTypeDiagnosis, []ScopedDataSource{{
				ID: uuid.New(), Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly,
			}}, ToolDependencyExternalCase, ToolDependencyKnowledge),
			want: []string{testToolKnowledge, testToolReadCase},
		},
		{
			name: "production admin cannot receive lab tool",
			scope: mustTaskScope(t, auth.RoleAdmin, TaskTypeDiagnosis, []ScopedDataSource{{
				ID: uuid.New(), Role: DataSourceRoleProduction, SafetyMode: DataSourceSafetyReadOnly,
			}}, ToolDependencySQLServer),
			want: []string{testToolReadSQL},
		},
		{
			name: "product replica admin receives bounded lab tool",
			scope: mustTaskScope(t, auth.RoleAdmin, TaskTypeDiagnosis, []ScopedDataSource{{
				ID: uuid.New(), Role: DataSourceRoleProductReplica, SafetyMode: DataSourceSafetyBoundedLab,
			}}, ToolDependencySQLServer),
			want: []string{testToolLabSQL, testToolReadSQL},
		},
		{
			name: "product replica analyst cannot receive admin lab tool",
			scope: mustTaskScope(t, auth.RoleAnalyst, TaskTypeDiagnosis, []ScopedDataSource{{
				ID: uuid.New(), Role: DataSourceRoleProductReplica, SafetyMode: DataSourceSafetyBoundedLab,
			}}, ToolDependencySQLServer),
			want: []string{testToolReadSQL},
		},
		{
			name:  "knowledge task receives only knowledge tool",
			scope: mustTaskScope(t, auth.RoleAnalyst, TaskTypeKnowledge, nil, ToolDependencyKnowledge),
			want:  []string{testToolKnowledge},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tools, err := catalog.ToolsFor(context.Background(), tt.scope)
			if err != nil {
				t.Fatalf("ToolsFor: %v", err)
			}
			if got := toolNamesForTest(t, tools); !slices.Equal(got, tt.want) {
				t.Fatalf("tool names = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestToolCatalogEvaluationBaselineUsesRoleAndTaskToolSet(t *testing.T) {
	catalog := newToolCatalogForTest(t)
	scope := mustTaskScope(t, auth.RoleAnalyst, TaskTypeDiagnosis, []ScopedDataSource{{
		ID: uuid.New(), Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly,
	}}, ToolDependencyExternalCase)
	tools, err := catalog.EvaluationBaselineToolsFor(context.Background(), scope)
	if err != nil {
		t.Fatalf("EvaluationBaselineToolsFor: %v", err)
	}
	want := []string{testToolGitHub, testToolKnowledge, testToolReadCase, testToolReadSQL}
	if got := toolNamesForTest(t, tools); !slices.Equal(got, want) {
		t.Fatalf("baseline tool names = %v, want %v", got, want)
	}
}

func TestToolCatalogRechecksScopeWhenToolExecutes(t *testing.T) {
	catalog := newToolCatalogForTest(t)
	authorized := mustTaskScopeWithCapabilities(t, auth.RoleAnalyst, TaskTypeDiagnosis, []ScopedDataSource{{
		ID: uuid.New(), Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly,
	}}, []ToolCapability{ToolCapabilityCase, ToolCapabilityCode}, ToolDependencyExternalCase, ToolDependencyGitHubMCP)
	tools, err := catalog.ToolsFor(context.Background(), authorized)
	if err != nil {
		t.Fatalf("ToolsFor: %v", err)
	}
	var github tool.InvokableTool
	for _, current := range tools {
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
	if _, err := github.InvokableRun(WithTaskScope(context.Background(), authorized), `{}`); err != nil {
		t.Fatalf("authorized InvokableRun: %v", err)
	}

	denied := mustTaskScopeWithCapabilities(t, auth.RoleAnalyst, TaskTypeDiagnosis, authorized.DataSources(),
		[]ToolCapability{ToolCapabilityCase}, ToolDependencyExternalCase, ToolDependencyGitHubMCP)
	if _, err := github.InvokableRun(WithTaskScope(context.Background(), denied), `{}`); !errors.Is(err, ErrToolNotAllowed) {
		t.Fatalf("denied InvokableRun error = %v, want ErrToolNotAllowed", err)
	}
	if _, err := github.InvokableRun(context.Background(), `{}`); !errors.Is(err, ErrTaskScopeRequired) {
		t.Fatalf("unscoped InvokableRun error = %v, want ErrTaskScopeRequired", err)
	}
}

func TestToolCatalogRequiresOneDataSourceToMatchWholeConstraint(t *testing.T) {
	conflictingTool := newNamedToolForTest(t, "test_conflicting_source")
	catalog, err := NewToolCatalog(context.Background(), ToolRegistration{
		Tool: conflictingTool, FailurePolicy: resilience.PolicyBestEffort,
		AllowedRoles:         []auth.Role{auth.RoleAdmin},
		AllowedTaskTypes:     []TaskType{TaskTypeDiagnosis},
		AllowedDataRoles:     []DataSourceRole{DataSourceRoleProduction},
		AllowedSafetyModes:   []DataSourceSafetyMode{DataSourceSafetyBoundedLab},
		RequiredCapabilities: []ToolCapability{ToolCapabilitySQL},
		RequiredDependencies: []ToolDependency{ToolDependencySQLServer},
	})
	if err != nil {
		t.Fatalf("NewToolCatalog: %v", err)
	}
	scope := mustTaskScope(t, auth.RoleAdmin, TaskTypeDiagnosis, []ScopedDataSource{
		{ID: uuid.New(), Role: DataSourceRoleProduction, SafetyMode: DataSourceSafetyReadOnly},
		{ID: uuid.New(), Role: DataSourceRoleProductReplica, SafetyMode: DataSourceSafetyBoundedLab},
	}, ToolDependencySQLServer)
	tools, err := catalog.ToolsFor(context.Background(), scope)
	if err != nil {
		t.Fatalf("ToolsFor: %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("cross-source constraints incorrectly authorized tools: %v", toolNamesForTest(t, tools))
	}
}

func TestToolCatalogRejectsDuplicateNamesAndInvalidPolicy(t *testing.T) {
	duplicateA := newNamedToolForTest(t, "test_duplicate")
	duplicateB := newNamedToolForTest(t, "test_duplicate")
	validPolicy := ToolRegistration{
		AllowedRoles: []auth.Role{auth.RoleAnalyst}, AllowedTaskTypes: []TaskType{TaskTypeDiagnosis},
		FailurePolicy: resilience.PolicyBestEffort,
	}
	first := validPolicy
	first.Tool = duplicateA
	second := validPolicy
	second.Tool = duplicateB
	if _, err := NewToolCatalog(context.Background(), first, second); err == nil {
		t.Fatal("NewToolCatalog accepted duplicate tool names")
	}
	if _, err := NewToolCatalog(context.Background(), ToolRegistration{
		Tool: duplicateA, AllowedRoles: []auth.Role{auth.RoleAnalyst, auth.RoleAnalyst},
		AllowedTaskTypes: []TaskType{TaskTypeDiagnosis},
	}); err == nil {
		t.Fatal("NewToolCatalog accepted a duplicated policy value")
	}
	if _, err := NewToolCatalog(context.Background(), ToolRegistration{
		Tool: duplicateA, AllowedRoles: []auth.Role{auth.RoleAnalyst},
		AllowedTaskTypes:     []TaskType{TaskTypeDiagnosis},
		RequiredCapabilities: []ToolCapability{ToolCapabilityCase, ToolCapabilityCase},
	}); err == nil {
		t.Fatal("NewToolCatalog accepted duplicated capabilities")
	}
}

func TestToolCatalogRequiresExplicitFailurePolicy(t *testing.T) {
	_, err := NewToolCatalog(context.Background(), ToolRegistration{
		Tool:         newNamedToolForTest(t, "test_missing_failure_policy"),
		AllowedRoles: []auth.Role{auth.RoleAnalyst}, AllowedTaskTypes: []TaskType{TaskTypeDiagnosis},
	})
	if err == nil {
		t.Fatal("NewToolCatalog accepted a tool without an explicit failure policy")
	}
}

func TestScopeGuardedToolKeepsStrictFailureAsError(t *testing.T) {
	want := errors.New("side effect failed")
	current := scopedFailingToolForTest(t, resilience.PolicyStrict, want, nil)
	ctx := WithTaskScope(context.Background(), mustTaskScopeWithCapabilities(
		t, auth.RoleAnalyst, TaskTypeDiagnosis, toolFailureDataSource(), []ToolCapability{ToolCapabilityCase},
	))
	if _, err := current.InvokableRun(ctx, `{}`); !errors.Is(err, want) {
		t.Fatalf("InvokableRun error = %v, want strict failure", err)
	}
}

func TestScopeGuardedToolReturnsStructuredBestEffortFailure(t *testing.T) {
	var observed []resilience.DegradationEvent
	current := scopedFailingToolForTest(
		t, resilience.PolicyBestEffort,
		resilience.RetryableFailure(errors.New("dial tcp secret.internal:1433")),
		resilience.ObserverFunc(func(event resilience.DegradationEvent) { observed = append(observed, event) }),
	)
	ctx := WithTaskScope(context.Background(), mustTaskScopeWithCapabilities(
		t, auth.RoleAnalyst, TaskTypeDiagnosis, toolFailureDataSource(), []ToolCapability{ToolCapabilityCase},
	))
	ctx = resilience.WithRunIdentity(ctx, resilience.RunIdentity{RunID: "run-1", TraceID: "trace-1"})
	raw, err := current.InvokableRun(ctx, `{}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
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
}

func TestScopeGuardedToolDistinguishesRejectedAndStrictFailures(t *testing.T) {
	ctx := WithTaskScope(context.Background(), mustTaskScopeWithCapabilities(
		t, auth.RoleAnalyst, TaskTypeDiagnosis, toolFailureDataSource(), []ToolCapability{ToolCapabilityCase},
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
		AllowedRoles: []auth.Role{auth.RoleAnalyst}, AllowedTaskTypes: []TaskType{TaskTypeDiagnosis},
		RequiredCapabilities: []ToolCapability{ToolCapabilityCase},
	})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := catalog.ToolsFor(context.Background(), mustTaskScopeWithCapabilities(
		t, auth.RoleAnalyst, TaskTypeDiagnosis, toolFailureDataSource(), []ToolCapability{ToolCapabilityCase},
	))
	if err != nil || len(tools) != 1 {
		t.Fatalf("ToolsFor = %d, %v", len(tools), err)
	}
	return tools[0].(tool.InvokableTool)
}

func toolFailureDataSource() []ScopedDataSource {
	return []ScopedDataSource{{
		ID: uuid.New(), Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly,
	}}
}

func newToolCatalogForTest(t *testing.T) *ToolCatalog {
	t.Helper()
	registrations := []ToolRegistration{
		{
			Tool:          newNamedToolForTest(t, testToolReadCase),
			FailurePolicy: resilience.PolicyBestEffort,
			AllowedRoles:  []auth.Role{auth.RoleAnalyst, auth.RoleAdmin}, AllowedTaskTypes: []TaskType{TaskTypeDiagnosis},
			AllowedDataRoles:     []DataSourceRole{DataSourceRoleCaseSource},
			AllowedSafetyModes:   []DataSourceSafetyMode{DataSourceSafetyReadOnly},
			RequiredCapabilities: []ToolCapability{ToolCapabilityCase},
			RequiredDependencies: []ToolDependency{ToolDependencyExternalCase},
		},
		{
			Tool:          newNamedToolForTest(t, testToolGitHub),
			FailurePolicy: resilience.PolicyBestEffort,
			AllowedRoles:  []auth.Role{auth.RoleAnalyst, auth.RoleAdmin}, AllowedTaskTypes: []TaskType{TaskTypeDiagnosis},
			RequiredCapabilities: []ToolCapability{ToolCapabilityCode},
			RequiredDependencies: []ToolDependency{ToolDependencyGitHubMCP},
		},
		{
			Tool:          newNamedToolForTest(t, testToolReadSQL),
			FailurePolicy: resilience.PolicyBestEffort,
			AllowedRoles:  []auth.Role{auth.RoleAnalyst, auth.RoleAdmin}, AllowedTaskTypes: []TaskType{TaskTypeDiagnosis},
			AllowedDataRoles:     []DataSourceRole{DataSourceRoleProduction, DataSourceRoleProductReplica},
			AllowedSafetyModes:   []DataSourceSafetyMode{DataSourceSafetyReadOnly, DataSourceSafetyBoundedLab},
			RequiredCapabilities: []ToolCapability{ToolCapabilitySQL},
			RequiredDependencies: []ToolDependency{ToolDependencySQLServer},
		},
		{
			Tool:          newNamedToolForTest(t, testToolLabSQL),
			FailurePolicy: resilience.PolicyBestEffort,
			AllowedRoles:  []auth.Role{auth.RoleAdmin}, AllowedTaskTypes: []TaskType{TaskTypeDiagnosis},
			AllowedDataRoles:     []DataSourceRole{DataSourceRoleProductReplica},
			AllowedSafetyModes:   []DataSourceSafetyMode{DataSourceSafetyBoundedLab},
			RequiredCapabilities: []ToolCapability{ToolCapabilitySQL},
			RequiredDependencies: []ToolDependency{ToolDependencySQLServer},
		},
		{
			Tool:          newNamedToolForTest(t, testToolKnowledge),
			FailurePolicy: resilience.PolicyBestEffort,
			AllowedRoles:  []auth.Role{auth.RoleAnalyst, auth.RoleAdmin}, AllowedTaskTypes: []TaskType{TaskTypeDiagnosis, TaskTypeKnowledge},
			RequiredCapabilities: []ToolCapability{ToolCapabilityKnowledge},
			RequiredDependencies: []ToolDependency{ToolDependencyKnowledge},
		},
	}
	catalog, err := NewToolCatalog(context.Background(), registrations...)
	if err != nil {
		t.Fatalf("NewToolCatalog: %v", err)
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

func mustTaskScope(
	t *testing.T,
	role auth.Role,
	taskType TaskType,
	dataSources []ScopedDataSource,
	dependencies ...ToolDependency,
) TaskScope {
	t.Helper()
	capabilities := []ToolCapability{ToolCapabilityCase, ToolCapabilityKnowledge}
	if taskType == TaskTypeKnowledge {
		capabilities = []ToolCapability{ToolCapabilityKnowledge}
	} else {
		for _, dependency := range dependencies {
			switch dependency {
			case ToolDependencyGitHubMCP:
				capabilities = append(capabilities, ToolCapabilityCode)
			case ToolDependencySQLServer:
				capabilities = append(capabilities, ToolCapabilitySQL)
			}
		}
	}
	return mustTaskScopeWithCapabilities(t, role, taskType, dataSources, capabilities, dependencies...)
}

func mustTaskScopeWithCapabilities(
	t *testing.T,
	role auth.Role,
	taskType TaskType,
	dataSources []ScopedDataSource,
	capabilities []ToolCapability,
	dependencies ...ToolDependency,
) TaskScope {
	t.Helper()
	scope, err := NewTaskScope(TaskScopeConfig{
		UserID: uuid.New(), Role: role, TaskType: taskType,
		DataSources: dataSources, AllowedCapabilities: capabilities, AvailableDependencies: dependencies,
	})
	if err != nil {
		t.Fatalf("NewTaskScope: %v", err)
	}
	return scope
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
