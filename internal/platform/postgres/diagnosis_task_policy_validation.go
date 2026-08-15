package postgres

import (
	"encoding/json"
	"fmt"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/diagnosis"
)

// validateTaskInvestigationPolicy 是 DiagnosisTaskRepository 创建路径的
// fail-closed 校验：新任务必须携带非空 Policy payload、非零且与 payload 内
// schemaVersion 一致的列版本，并且 payload 必须通过严格 codec。任何一条
// 不满足都返回 ErrInvalidTask 且不执行任何 INSERT。
//
// 旧授权体系（legacy mode / request_scope 派生）已硬切删除：不存在把缺失
// Policy 的新任务降级执行的路径，Policy 是任务的唯一授权事实。
func validateTaskInvestigationPolicy(
	payload json.RawMessage,
	schemaVersion int,
) error {
	if len(payload) == 0 {
		return fmt.Errorf("%w: investigation policy payload is required", diagnosis.ErrInvalidTask)
	}
	if schemaVersion <= 0 {
		return fmt.Errorf("%w: investigation policy schema version must be positive (got %d)",
			diagnosis.ErrInvalidTask, schemaVersion)
	}
	policy, err := agentruntime.UnmarshalInvestigationPolicy(payload)
	if err != nil {
		return fmt.Errorf("%w: decode investigation policy: %v", diagnosis.ErrInvalidTask, err)
	}
	if policy.SchemaVersion() != schemaVersion {
		return fmt.Errorf("%w: investigation policy column version %d disagrees with payload schemaVersion %d",
			diagnosis.ErrInvalidTask, schemaVersion, policy.SchemaVersion())
	}
	return nil
}
