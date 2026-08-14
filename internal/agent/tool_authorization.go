package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
)

var ErrTaskScopeRequired = errors.New("task scope is required")

// ErrRunAccessRequired 是执行期第二层 Guard 的 fail-closed 错误：
// Tool 可见（Schema 层已通过）但本次执行没有合法 RunAccess 时返回。
var ErrRunAccessRequired = errors.New("run access is required")

// ToolAuthorizationMiddleware 必须放在 Skill Middleware 之前：它先注入业务 Tool，
// Skill Middleware 再追加框架自己的 skill Tool，二者不会重复注册。
type ToolAuthorizationMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	provider AgentToolProvider
}

func NewToolAuthorizationMiddleware(provider AgentToolProvider) (*ToolAuthorizationMiddleware, error) {
	if provider == nil {
		return nil, errors.New("agent tool provider is required")
	}
	return &ToolAuthorizationMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		provider:                     provider,
	}, nil
}

func (m *ToolAuthorizationMiddleware) BeforeAgent(
	ctx context.Context,
	runCtx *adk.ChatModelAgentContext,
) (context.Context, *adk.ChatModelAgentContext, error) {
	if runCtx == nil {
		return ctx, nil, errors.New("chat model agent context is nil")
	}
	scope, ok := TaskScopeFromContext(ctx)
	if !ok {
		return ctx, runCtx, ErrTaskScopeRequired
	}
	tools, err := m.provider.ToolsFor(ctx, scope)
	if err != nil {
		return ctx, runCtx, fmt.Errorf("resolve authorized tools: %w", err)
	}
	tools, err = filterAgentToolsForRun(ctx, tools)
	if err != nil {
		return ctx, runCtx, fmt.Errorf("apply run tool policy: %w", err)
	}
	next := *runCtx
	next.Tools = append([]tool.BaseTool(nil), tools...)

	allowedNames := make(map[string]struct{}, len(tools))
	for _, current := range tools {
		if current == nil {
			return ctx, runCtx, errors.New("agent tool provider returned a nil tool")
		}
		info, infoErr := current.Info(ctx)
		if infoErr != nil {
			return ctx, runCtx, fmt.Errorf("read authorized tool info: %w", infoErr)
		}
		if info == nil {
			return ctx, runCtx, errors.New("agent tool provider returned nil tool info")
		}
		if !toolNamePattern.MatchString(info.Name) {
			return ctx, runCtx, fmt.Errorf("agent tool provider returned invalid tool name %q", info.Name)
		}
		if _, duplicate := allowedNames[info.Name]; duplicate {
			return ctx, runCtx, fmt.Errorf("agent tool provider returned duplicate tool %q", info.Name)
		}
		allowedNames[info.Name] = struct{}{}
	}
	next.ReturnDirectly = make(map[string]bool, len(runCtx.ReturnDirectly))
	for name, enabled := range runCtx.ReturnDirectly {
		if _, allowed := allowedNames[name]; allowed {
			next.ReturnDirectly[name] = enabled
		}
	}
	return ctx, &next, nil
}
