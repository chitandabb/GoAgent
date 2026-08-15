package postgres

import (
	"encoding/json"
	"fmt"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/diagnosis"
)

// validateTaskInvestigationPolicy 是 DiagnosisTaskRepository 创建路径的
// fail-closed 校验：新任务必须显式携带 frozen mode、非空 Policy payload、
// 非零且与 payload 内 schemaVersion 一致的列版本，并且 payload 必须通过
// 严格 codec。任何一条不满足都返回 ErrInvalidTask 且不执行任何 INSERT。
//
// Repository 绝不把缺失 Policy 的新任务自动转换成 legacy：mode 必须由
// 调用方显式声明，legacy 只属于 migration 00034 之前的既有行。
func validateTaskInvestigationPolicy(
	mode diagnosis.InvestigationPolicyMode,
	payload json.RawMessage,
	schemaVersion int,
) error {
	if mode != diagnosis.InvestigationPolicyModeFrozen {
		return fmt.Errorf("%w: new diagnosis tasks must freeze an investigation policy (mode=%q)",
			diagnosis.ErrInvalidTask, mode)
	}
	if len(payload) == 0 {
		return fmt.Errorf("%w: investigation policy payload is required for frozen tasks",
			diagnosis.ErrInvalidTask)
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
