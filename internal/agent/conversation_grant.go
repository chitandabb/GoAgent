package agent

import (
	"context"
	"errors"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
)

// ErrConversationResourceNotGranted 是 Conversation 运行时 Tool 边界的
// fail-closed 错误：RunAccess 权限已放行（case.read/attachment.read/...），
// 但被访问的具体资源不在本轮 ResourceGrants 中。底层 getter/reader/executor
// 在收到该错误前不会发生任何调用。
var ErrConversationResourceNotGranted = errors.New("resource is not granted in the current conversation run")

// requireConversationResourceGrant 在 Conversation 运行时检查具体资源的
// ResourceGrant；未授权在底层调用前拒绝。
//
// Diagnosis 兼容链路（InvestigationPolicy 尚未持久化，RunAccess 经 TaskScope
// 适配器构造且不携带 ExternalCase/Attachment/Task Grant）不适用本边界，由
// RuntimeKind 明确区分：RuntimeKindConversation 执行资源 Grant 校验，其余
// Runtime 继续沿用旧 TaskScope/CommandContext/owner 校验作为第二层防御。
// 缺失 RunAccess 时 fail-closed（ErrRunAccessRequired）。
func requireConversationResourceGrant(
	ctx context.Context,
	granted func(grants agentruntime.ResourceGrants) bool,
) error {
	access, ok := agentruntime.RunAccessFromContext(ctx)
	if !ok {
		return ErrRunAccessRequired
	}
	if access.RuntimeKind() != agentruntime.RuntimeKindConversation {
		return nil
	}
	if !granted(access.Grants()) {
		return ErrConversationResourceNotGranted
	}
	return nil
}
