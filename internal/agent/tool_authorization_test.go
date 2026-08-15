package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/auth"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

// bindDiagnosisProfileForTest 把测试 Catalog 的 test 工具绑定为固定 Diagnosis
// Profile，供 Middleware/并发测试使用；ToolsFor 的 v1 语义不受影响。
func bindDiagnosisProfileForTest(t *testing.T, catalog *ToolCatalog) {
	t.Helper()
	profile := mustToolProfileForTest(t, agentruntime.ToolProfileDiagnosis, []string{
		testToolReadCase, testToolGitHub, testToolReadSQL, testToolLabSQL, testToolKnowledge,
	})
	if err := catalog.BindProfile(profile, []string{ToolSkill}); err != nil {
		t.Fatalf("BindProfile: %v", err)
	}
}

func TestToolAuthorizationMiddlewareRequiresRunAccessAndReplacesStaticTools(t *testing.T) {
	catalog := newToolCatalogForTest(t)
	bindDiagnosisProfileForTest(t, catalog)
	middleware, err := NewToolAuthorizationMiddleware(catalog, agentruntime.ToolProfileDiagnosis)
	if err != nil {
		t.Fatalf("NewToolAuthorizationMiddleware: %v", err)
	}
	bypass := newNamedToolForTest(t, "test_bypass")
	runCtx := &adk.ChatModelAgentContext{
		Tools: []tool.BaseTool{bypass}, ReturnDirectly: map[string]bool{"test_bypass": true, testToolReadCase: true},
	}
	if _, _, err = middleware.BeforeAgent(context.Background(), runCtx); !errors.Is(err, ErrRunAccessRequired) {
		t.Fatalf("missing RunAccess error = %v, want ErrRunAccessRequired", err)
	}
	scope := mustTaskScope(t, auth.RoleAnalyst, TaskTypeDiagnosis, []ScopedDataSource{{
		ID: uuid.New(), Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly,
	}}, ToolDependencyExternalCase)
	_, got, err := middleware.BeforeAgent(WithTaskScope(context.Background(), scope), runCtx)
	if err != nil {
		t.Fatalf("BeforeAgent: %v", err)
	}
	if names := toolNamesForTest(t, got.Tools); !slices.Equal(names, []string{
		testToolGitHub, testToolKnowledge, testToolLabSQL, testToolReadCase, testToolReadSQL,
	}) {
		t.Fatalf("authorized tools = %v", names)
	}
	if got.ReturnDirectly["test_bypass"] || !got.ReturnDirectly[testToolReadCase] {
		t.Fatalf("ReturnDirectly = %v", got.ReturnDirectly)
	}
}

type toolProviderStub struct {
	resolved ResolvedToolProfile
}

func (s toolProviderStub) ResolveProfile(context.Context, agentruntime.ToolProfileID) (ResolvedToolProfile, error) {
	return s.resolved, nil
}

func TestToolAuthorizationMiddlewareRejectsInvalidProviderOutput(t *testing.T) {
	duplicate := newNamedToolForTest(t, "test_provider_duplicate")
	middleware, err := NewToolAuthorizationMiddleware(toolProviderStub{
		resolved: ResolvedToolProfile{
			ID:                agentruntime.ToolProfileDiagnosis,
			Tools:             []tool.BaseTool{duplicate, duplicate},
			ModelVisibleNames: []string{"test_provider_duplicate"},
		},
	}, agentruntime.ToolProfileDiagnosis)
	if err != nil {
		t.Fatalf("NewToolAuthorizationMiddleware: %v", err)
	}
	scope := mustTaskScope(t, auth.RoleAnalyst, TaskTypeDiagnosis, []ScopedDataSource{{
		ID: uuid.New(), Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly,
	}})
	_, _, err = middleware.BeforeAgent(
		WithTaskScope(context.Background(), scope),
		&adk.ChatModelAgentContext{},
	)
	if err == nil {
		t.Fatal("BeforeAgent accepted duplicate provider output")
	}
}

func TestToolAuthorizationMiddlewareKeepsBlockedToolsInSchema(t *testing.T) {
	readTool := newNamedToolForTest(t, testToolReadCase)
	knowledgeTool := newNamedToolForTest(t, ToolSearchKnowledge)
	middleware, err := NewToolAuthorizationMiddleware(toolProviderStub{
		resolved: ResolvedToolProfile{
			ID: agentruntime.ToolProfileDiagnosis,
			Tools: []tool.BaseTool{readTool, knowledgeTool},
			ModelVisibleNames: []string{testToolReadCase, ToolSearchKnowledge},
		},
	}, agentruntime.ToolProfileDiagnosis)
	if err != nil {
		t.Fatal(err)
	}
	scope := mustTaskScope(t, auth.RoleAnalyst, TaskTypeDiagnosis, []ScopedDataSource{{
		ID: uuid.New(), Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly,
	}})
	ctx := WithTaskScope(context.Background(), scope)
	ctx = withAgentToolRunPolicy(ctx, newAgentToolRunPolicy([]string{ToolSearchKnowledge}, nil))
	_, got, err := middleware.BeforeAgent(ctx, &adk.ChatModelAgentContext{})
	if err != nil {
		t.Fatal(err)
	}
	// 调用次数限制/blocked 状态不能从下一轮模型上下文中删除 Tool Schema。
	if names := toolNamesForTest(t, got.Tools); !slices.Equal(names, []string{testToolReadCase, ToolSearchKnowledge}) {
		t.Fatalf("blocked state changed model schema: %v", names)
	}
}

type authorizationCaptureModel struct {
	mu       sync.Mutex
	captured map[string][]string
}

func (m *authorizationCaptureModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *authorizationCaptureModel) Generate(
	_ context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.Message, error) {
	query := ""
	for _, message := range input {
		if message.Role == schema.User {
			query = message.Content
		}
	}
	if query == "" {
		return nil, errors.New("user query is missing")
	}
	common := model.GetCommonOptions(nil, opts...)
	names := make([]string, 0, len(common.Tools))
	for _, info := range common.Tools {
		names = append(names, info.Name)
	}
	sort.Strings(names)
	m.mu.Lock()
	m.captured[query] = names
	m.mu.Unlock()
	return schema.AssistantMessage("done", nil), nil
}

func (m *authorizationCaptureModel) Stream(
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

func TestToolAuthorizationMiddlewareIsolatesConcurrentADKRuns(t *testing.T) {
	ctx := context.Background()
	catalog := newToolCatalogForTest(t)
	bindDiagnosisProfileForTest(t, catalog)
	middleware, err := NewToolAuthorizationMiddleware(catalog, agentruntime.ToolProfileDiagnosis)
	if err != nil {
		t.Fatalf("NewToolAuthorizationMiddleware: %v", err)
	}
	modelState := &authorizationCaptureModel{captured: make(map[string][]string)}
	bypass := newNamedToolForTest(t, "test_static_bypass")
	newRunner := func() (*adk.Runner, error) {
		// Eino v0.9.13 的 ChatModelAgent 在 Run 初始化时会写入内部配置，不能被并发复用。
		// Catalog、Middleware 和模型保持共享；每次 Run 只创建一个轻量的 Agent 实例承载请求状态。
		agentInstance, buildErr := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
			Name: "authorization-test", Model: modelState,
			ToolsConfig:   adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{bypass}}},
			MaxIterations: 2, Handlers: []adk.ChatModelAgentMiddleware{middleware},
		})
		if buildErr != nil {
			return nil, buildErr
		}
		return adk.NewRunner(ctx, adk.RunnerConfig{Agent: agentInstance}), nil
	}

	wantTools := []string{testToolGitHub, testToolKnowledge, testToolLabSQL, testToolReadCase, testToolReadSQL}
	sort.Strings(wantTools)
	runs := make([]string, 0, 60)
	for i := 0; i < 20; i++ {
		runs = append(runs,
			fmt.Sprintf("case-%02d", i),
			fmt.Sprintf("lab-%02d", i),
			fmt.Sprintf("production-%02d", i),
		)
	}
	scopes := []TaskScope{
		mustTaskScope(t, auth.RoleAnalyst, TaskTypeDiagnosis, []ScopedDataSource{{
			ID: uuid.New(), Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly,
		}}, ToolDependencyExternalCase),
		mustTaskScope(t, auth.RoleAdmin, TaskTypeDiagnosis, []ScopedDataSource{{
			ID: uuid.New(), Role: DataSourceRoleProductReplica, SafetyMode: DataSourceSafetyBoundedLab,
		}}, ToolDependencySQLServer),
		mustTaskScope(t, auth.RoleAdmin, TaskTypeDiagnosis, []ScopedDataSource{{
			ID: uuid.New(), Role: DataSourceRoleProduction, SafetyMode: DataSourceSafetyReadOnly,
		}}, ToolDependencySQLServer),
	}

	var wg sync.WaitGroup
	errorsFound := make(chan error, len(runs))
	for index, query := range runs {
		query := query
		scope := scopes[index%len(scopes)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			runner, buildErr := newRunner()
			if buildErr != nil {
				errorsFound <- fmt.Errorf("%s: build runner: %w", query, buildErr)
				return
			}
			runCtx := WithTaskScope(context.Background(), scope)
			iterator := runner.Query(runCtx, query)
			for {
				event, ok := iterator.Next()
				if !ok {
					return
				}
				if event.Err != nil {
					errorsFound <- fmt.Errorf("%s: %w", query, event.Err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errorsFound)
	for runErr := range errorsFound {
		t.Error(runErr)
	}
	if t.Failed() {
		return
	}

	modelState.mu.Lock()
	defer modelState.mu.Unlock()
	for _, query := range runs {
		got, ok := modelState.captured[query]
		if !ok {
			t.Errorf("query %q was not captured", query)
			continue
		}
		// 固定 Profile 下所有 Run 必须看到完全相同的 Tool Schema。
		if !slices.Equal(got, wantTools) {
			t.Errorf("query %q tools = %v, want stable %v", query, got, wantTools)
		}
		if slices.Contains(got, "test_static_bypass") {
			t.Errorf("query %q received static bypass tool", query)
		}
	}
}
