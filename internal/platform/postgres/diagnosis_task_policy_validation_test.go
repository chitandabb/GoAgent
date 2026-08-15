package postgres

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/diagnosis"

	"github.com/google/uuid"
)

func mustValidFrozenPolicyBytes(t *testing.T) (json.RawMessage, int) {
	t.Helper()
	permissions, err := agentruntime.NewPermissionSet(agentruntime.PermissionCaseRead)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := agentruntime.NewResourceGrants(agentruntime.ResourceGrantsConfig{
		ExternalCaseIDs: []uuid.UUID{uuid.New()},
	})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := agentruntime.NewInvestigationPolicy(1, permissions, grants)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := agentruntime.MarshalInvestigationPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	return encoded, policy.SchemaVersion()
}

func TestValidateTaskInvestigationPolicyAcceptsFrozenPolicy(t *testing.T) {
	payload, version := mustValidFrozenPolicyBytes(t)
	if err := validateTaskInvestigationPolicy(payload, version); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
}

func TestValidateTaskInvestigationPolicyRejectsMissingPayload(t *testing.T) {
	_, version := mustValidFrozenPolicyBytes(t)
	if err := validateTaskInvestigationPolicy(nil, version); !errors.Is(err, diagnosis.ErrInvalidTask) {
		t.Fatalf("nil payload error = %v, want ErrInvalidTask", err)
	}
	if err := validateTaskInvestigationPolicy(json.RawMessage(` `), version); !errors.Is(err, diagnosis.ErrInvalidTask) {
		t.Fatalf("blank payload error = %v, want ErrInvalidTask", err)
	}
}

func TestValidateTaskInvestigationPolicyRejectsNonPositiveSchemaVersion(t *testing.T) {
	payload, _ := mustValidFrozenPolicyBytes(t)
	for _, version := range []int{0, -1} {
		if err := validateTaskInvestigationPolicy(payload, version); !errors.Is(err, diagnosis.ErrInvalidTask) {
			t.Fatalf("schema version %d error = %v, want ErrInvalidTask", version, err)
		}
	}
}

func TestValidateTaskInvestigationPolicyRejectsPayloadVersionMismatch(t *testing.T) {
	payload, version := mustValidFrozenPolicyBytes(t)
	if err := validateTaskInvestigationPolicy(payload, version+1); !errors.Is(err, diagnosis.ErrInvalidTask) {
		t.Fatalf("mismatched column version error = %v, want ErrInvalidTask", err)
	}
}

func TestValidateTaskInvestigationPolicyRejectsCorruptPayload(t *testing.T) {
	corrupt := json.RawMessage(`{"schemaVersion":1,"permissions":["case.read"],"grants":{},"unknownField":true}`)
	if err := validateTaskInvestigationPolicy(corrupt, 1); !errors.Is(err, diagnosis.ErrInvalidTask) {
		t.Fatalf("corrupt payload error = %v, want ErrInvalidTask", err)
	}
	malformed := json.RawMessage(`{"schemaVersion":1,"permissions":[`)
	if err := validateTaskInvestigationPolicy(malformed, 1); !errors.Is(err, diagnosis.ErrInvalidTask) {
		t.Fatalf("malformed payload error = %v, want ErrInvalidTask", err)
	}
}

// TestValidateTaskInvestigationPolicyNeverFallsBack 证明新任务缺失 Policy 时
// 没有任何降级路径：全空输入同样被拒绝，Repository 不会把它转换成 legacy
// 任务写入。
func TestValidateTaskInvestigationPolicyNeverFallsBack(t *testing.T) {
	if err := validateTaskInvestigationPolicy(nil, 0); !errors.Is(err, diagnosis.ErrInvalidTask) {
		t.Fatalf("empty input error = %v, want ErrInvalidTask", err)
	}
}
