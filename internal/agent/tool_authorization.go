package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/chitandabb/GoAgent/internal/agentruntime"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
)

// ErrRunAccessRequired 是执行期第二层 Guard 的 fail-closed 错误：
// Tool 可见（Schema 层已通过）但本次执行没有合法 RunAccess 时返回。
var ErrRunAccessRequired = errors.New("run access is required")

// ToolAuthorizationMiddleware 把固定 ToolProfile 解析为本次 Agent Run 的模型
// 可见 Schema。它必须放在 Skill Middleware 之前：它先注入业务 Tool，
// Skill Middleware 再追加框架自己的 skill Tool，二者不会重复注册。
type ToolAuthorizationMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	provider  AgentToolProvider
	profileID agentruntime.ToolProfileID
}

func NewToolAuthorizationMiddleware(
	provider AgentToolProvider,
	profileID agentruntime.ToolProfileID,
) (*ToolAuthorizationMiddleware, error) {
	if provider == nil {
		return nil, errors.New("agent tool provider is required")
	}
	if !profileID.Valid() {
		return nil, fmt.Errorf("tool profile id %q is invalid", profileID)
	}
	return &ToolAuthorizationMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		provider:                     provider,
		profileID:                    profileID,
	}, nil
}

func (m *ToolAuthorizationMiddleware) BeforeAgent(
	ctx context.Context,
	runCtx *adk.ChatModelAgentContext,
) (context.Context, *adk.ChatModelAgentContext, error) {
	if runCtx == nil {
		return ctx, nil, errors.New("chat model agent context is nil")
	}
	// Profile 决定 Schema 可见性；RunAccess 决定能否执行。BeforeAgent 只做
	// 装配，不按 RunAccess 裁剪 Schema。缺失 RunAccess 时 fail-closed。
	if _, ok := agentruntime.RunAccessFromContext(ctx); !ok {
		return ctx, runCtx, ErrRunAccessRequired
	}
	resolved, err := m.provider.ResolveProfile(ctx, m.profileID)
	if err != nil {
		return ctx, runCtx, fmt.Errorf("resolve tool profile %q: %w", m.profileID, err)
	}
	tools := resolved.Tools
	if tools == nil {
		tools = []tool.BaseTool{}
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
