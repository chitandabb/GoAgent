package agent

import (
	"context"
	"errors"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
)

// ErrResourceNotGranted 是运行时通用 Tool 边界的 fail-closed 错误：
// RunAccess 权限已放行（case.read/attachment.read/...），但被访问的具体
// 资源不在本轮 ResourceGrants 中。底层 getter/reader/executor 在收到该
// 错误前不会发生任何调用。
var ErrResourceNotGranted = errors.New("resource is not granted in the current run")

// ErrConversationResourceNotGranted 是通用 Guard 收敛前的历史错误名，
// 保留为别名以避免破坏既有调用方与测试的错误识别。
var ErrConversationResourceNotGranted = ErrResourceNotGranted

// requireRuntimeResourceGrant 是所有 Runtime 共用的具体资源 Grant 校验：
//   - 无 RunAccess -> ErrRunAccessRequired（fail-closed）；
//   - 资源不在 Grant -> ErrResourceNotGranted。
//
// Conversation 与 Diagnosis 都必须先在底层调用前通过本边界；Conversation
// 的 CommandContext/owner 校验与 Diagnosis 的任务归属校验保留为第二层。
func requireRuntimeResourceGrant(
	ctx context.Context,
	granted func(grants agentruntime.ResourceGrants) bool,
) error {
	access, ok := agentruntime.RunAccessFromContext(ctx)
	if !ok {
		return ErrRunAccessRequired
	}
	if !granted(access.Grants()) {
		return ErrResourceNotGranted
	}
	return nil
}
