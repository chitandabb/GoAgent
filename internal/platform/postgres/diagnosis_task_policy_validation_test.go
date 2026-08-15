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
	if err := validateTaskInvestigationPolicy(diagnosis.InvestigationPolicyModeFrozen, payload, version); err != nil {
		t.Fatalf("valid frozen policy rejected: %v", err)
	}
}

func TestValidateTaskInvestigationPolicyRejectsMissingPayload(t *testing.T) {
	_, version := mustValidFrozenPolicyBytes(t)
	if err := validateTaskInvestigationPolicy(diagnosis.InvestigationPolicyModeFrozen, nil, version); !errors.Is(err, diagnosis.ErrInvalidTask) {
		t.Fatalf("nil payload error = %v, want ErrInvalidTask", err)
	}
	if err := validateTaskInvestigationPolicy(diagnosis.InvestigationPolicyModeFrozen, json.RawMessage(` `), version); !errors.Is(err, diagnosis.ErrInvalidTask) {
		t.Fatalf("blank payload error = %v, want ErrInvalidTask", err)
	}
}

func TestValidateTaskInvestigationPolicyRejectsNonPositiveSchemaVersion(t *testing.T) {
	payload, _ := mustValidFrozenPolicyBytes(t)
	for _, version := range []int{0, -1} {
		if err := validateTaskInvestigationPolicy(diagnosis.InvestigationPolicyModeFrozen, payload, version); !errors.Is(err, diagnosis.ErrInvalidTask) {
			t.Fatalf("schema version %d error = %v, want ErrInvalidTask", version, err)
		}
	}
}

func TestValidateTaskInvestigationPolicyRejectsPayloadVersionMismatch(t *testing.T) {
	payload, version := mustValidFrozenPolicyBytes(t)
	if err := validateTaskInvestigationPolicy(diagnosis.InvestigationPolicyModeFrozen, payload, version+1); !errors.Is(err, diagnosis.ErrInvalidTask) {
		t.Fatalf("mismatched column version error = %v, want ErrInvalidTask", err)
	}
}

func TestValidateTaskInvestigationPolicyRejectsCorruptPayload(t *testing.T) {
	corrupt := json.RawMessage(`{"schemaVersion":1,"permissions":["case.read"],"grants":{},"unknownField":true}`)
	if err := validateTaskInvestigationPolicy(diagnosis.InvestigationPolicyModeFrozen, corrupt, 1); !errors.Is(err, diagnosis.ErrInvalidTask) {
		t.Fatalf("corrupt payload error = %v, want ErrInvalidTask", err)
	}
	malformed := json.RawMessage(`{"schemaVersion":1,"permissions":[`)
	if err := validateTaskInvestigationPolicy(diagnosis.InvestigationPolicyModeFrozen, malformed, 1); !errors.Is(err, diagnosis.ErrInvalidTask) {
		t.Fatalf("malformed payload error = %v, want ErrInvalidTask", err)
	}
}

func TestValidateTaskInvestigationPolicyRejectsNonFrozenMode(t *testing.T) {
	payload, version := mustValidFrozenPolicyBytes(t)
	for _, mode := range []diagnosis.InvestigationPolicyMode{
		diagnosis.InvestigationPolicyModeLegacy, "", "FROZEN", "froze", "unknown",
	} {
		if err := validateTaskInvestigationPolicy(mode, payload, version); !errors.Is(err, diagnosis.ErrInvalidTask) {
			t.Fatalf("mode %q error = %v, want ErrInvalidTask", mode, err)
		}
	}
}

func TestValidateTaskInvestigationPolicyNeverConvertsLegacy(t *testing.T) {
	// Repository 绝不能把缺失 Policy 的新任务自动转换成 legacy：即使三列
	// 全空，写入也必须被拒绝而不是静默降级。
	if err := validateTaskInvestigationPolicy(diagnosis.InvestigationPolicyModeLegacy, nil, 0); !errors.Is(err, diagnosis.ErrInvalidTask) {
		t.Fatalf("legacy-looking input error = %v, want ErrInvalidTask", err)
	}
	if err := validateTaskInvestigationPolicy("", nil, 0); !errors.Is(err, diagnosis.ErrInvalidTask) {
		t.Fatalf("empty-mode input error = %v, want ErrInvalidTask", err)
	}
}
