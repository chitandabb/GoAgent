package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"testing"

	"github.com/chitandabb/GoAgent/internal/auth"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

func TestToolAuthorizationMiddlewareRequiresScopeAndReplacesStaticTools(t *testing.T) {
	catalog := newToolCatalogForTest(t)
	middleware, err := NewToolAuthorizationMiddleware(catalog)
	if err != nil {
		t.Fatalf("NewToolAuthorizationMiddleware: %v", err)
	}
	bypass := newNamedToolForTest(t, "test_bypass")
	runCtx := &adk.ChatModelAgentContext{
		Tools: []tool.BaseTool{bypass}, ReturnDirectly: map[string]bool{"test_bypass": true, testToolReadCase: true},
	}
	if _, _, err = middleware.BeforeAgent(context.Background(), runCtx); !errors.Is(err, ErrTaskScopeRequired) {
		t.Fatalf("missing scope error = %v", err)
	}
	scope := mustTaskScope(t, auth.RoleAnalyst, TaskTypeDiagnosis, []ScopedDataSource{{
		ID: uuid.New(), Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly,
	}}, ToolDependencyExternalCase)
	_, got, err := middleware.BeforeAgent(WithTaskScope(context.Background(), scope), runCtx)
	if err != nil {
		t.Fatalf("BeforeAgent: %v", err)
	}
	if names := toolNamesForTest(t, got.Tools); !slices.Equal(names, []string{testToolReadCase}) {
		t.Fatalf("authorized tools = %v", names)
	}
	if got.ReturnDirectly["test_bypass"] || !got.ReturnDirectly[testToolReadCase] {
		t.Fatalf("ReturnDirectly = %v", got.ReturnDirectly)
	}
}

type toolProviderStub struct {
	tools []tool.BaseTool
}

func (s toolProviderStub) ToolsFor(context.Context, TaskScope) ([]tool.BaseTool, error) {
	return s.tools, nil
}

func TestToolAuthorizationMiddlewareRejectsInvalidProviderOutput(t *testing.T) {
	duplicate := newNamedToolForTest(t, "test_provider_duplicate")
	middleware, err := NewToolAuthorizationMiddleware(toolProviderStub{
		tools: []tool.BaseTool{duplicate, duplicate},
	})
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
	middleware, err := NewToolAuthorizationMiddleware(catalog)
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

	type runCase struct {
		query string
		scope TaskScope
		want  []string
	}
	runs := make([]runCase, 0, 60)
	for i := 0; i < 20; i++ {
		runs = append(runs,
			runCase{
				query: fmt.Sprintf("case-%02d", i),
				scope: mustTaskScope(t, auth.RoleAnalyst, TaskTypeDiagnosis, []ScopedDataSource{{
					ID: uuid.New(), Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly,
				}}, ToolDependencyExternalCase),
				want: []string{testToolReadCase},
			},
			runCase{
				query: fmt.Sprintf("lab-%02d", i),
				scope: mustTaskScope(t, auth.RoleAdmin, TaskTypeDiagnosis, []ScopedDataSource{{
					ID: uuid.New(), Role: DataSourceRoleProductReplica, SafetyMode: DataSourceSafetyBoundedLab,
				}}, ToolDependencySQLServer),
				want: []string{testToolLabSQL, testToolReadSQL},
			},
			runCase{
				query: fmt.Sprintf("production-%02d", i),
				scope: mustTaskScope(t, auth.RoleAdmin, TaskTypeDiagnosis, []ScopedDataSource{{
					ID: uuid.New(), Role: DataSourceRoleProduction, SafetyMode: DataSourceSafetyReadOnly,
				}}, ToolDependencySQLServer),
				want: []string{testToolReadSQL},
			},
		)
	}

	var wg sync.WaitGroup
	errorsFound := make(chan error, len(runs))
	for _, current := range runs {
		current := current
		wg.Add(1)
		go func() {
			defer wg.Done()
			runner, buildErr := newRunner()
			if buildErr != nil {
				errorsFound <- fmt.Errorf("%s: build runner: %w", current.query, buildErr)
				return
			}
			runCtx := WithTaskScope(context.Background(), current.scope)
			iterator := runner.Query(runCtx, current.query)
			for {
				event, ok := iterator.Next()
				if !ok {
					return
				}
				if event.Err != nil {
					errorsFound <- fmt.Errorf("%s: %w", current.query, event.Err)
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
	for _, current := range runs {
		got, ok := modelState.captured[current.query]
		if !ok {
			t.Errorf("query %q was not captured", current.query)
			continue
		}
		if !slices.Equal(got, current.want) {
			t.Errorf("query %q tools = %v, want %v", current.query, got, current.want)
		}
		if slices.Contains(got, "test_static_bypass") {
			t.Errorf("query %q received static bypass tool", current.query)
		}
	}
}
