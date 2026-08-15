package postgres

import (
	"errors"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/diagnosis"

	"github.com/google/uuid"
)

func workerPolicyModeRecord(mode diagnosis.InvestigationPolicyMode, payload []byte, version *int) workerTaskRecord {
	return workerTaskRecord{
		InvestigationPolicyMode:          string(mode),
		InvestigationPolicy:              payload,
		InvestigationPolicySchemaVersion: version,
	}
}

func intPtr(value int) *int { return &value }

func mustWorkerFrozenPolicy(t *testing.T) ([]byte, int) {
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

func TestDecodeWorkerInvestigationPolicyLegacyDoubleNullReturnsNil(t *testing.T) {
	policy, err := decodeWorkerInvestigationPolicy(
		workerPolicyModeRecord(diagnosis.InvestigationPolicyModeLegacy, nil, nil),
	)
	if err != nil || policy != nil {
		t.Fatalf("legacy decode = %v, %v; want nil policy, nil error", policy, err)
	}
}

func TestDecodeWorkerInvestigationPolicyLegacyRejectsOneSidedColumns(t *testing.T) {
	payload, version := mustWorkerFrozenPolicy(t)
	cases := []workerTaskRecord{
		workerPolicyModeRecord(diagnosis.InvestigationPolicyModeLegacy, payload, nil),
		workerPolicyModeRecord(diagnosis.InvestigationPolicyModeLegacy, nil, intPtr(version)),
		workerPolicyModeRecord(diagnosis.InvestigationPolicyModeLegacy, payload, intPtr(version)),
	}
	for _, record := range cases {
		if _, err := decodeWorkerInvestigationPolicy(record); !errors.Is(err, diagnosis.ErrInvalidTask) {
			t.Fatalf("one-sided legacy record error = %v, want ErrInvalidTask", err)
		}
	}
}

func TestDecodeWorkerInvestigationPolicyFrozenRejectsMissingColumns(t *testing.T) {
	payload, version := mustWorkerFrozenPolicy(t)
	cases := []workerTaskRecord{
		workerPolicyModeRecord(diagnosis.InvestigationPolicyModeFrozen, nil, nil),
		workerPolicyModeRecord(diagnosis.InvestigationPolicyModeFrozen, payload, nil),
		workerPolicyModeRecord(diagnosis.InvestigationPolicyModeFrozen, nil, intPtr(version)),
	}
	for _, record := range cases {
		if _, err := decodeWorkerInvestigationPolicy(record); !errors.Is(err, diagnosis.ErrInvalidTask) {
			t.Fatalf("incomplete frozen record error = %v, want ErrInvalidTask", err)
		}
	}
}

func TestDecodeWorkerInvestigationPolicyFrozenReturnsPolicy(t *testing.T) {
	payload, version := mustWorkerFrozenPolicy(t)
	policy, err := decodeWorkerInvestigationPolicy(
		workerPolicyModeRecord(diagnosis.InvestigationPolicyModeFrozen, payload, intPtr(version)),
	)
	if err != nil {
		t.Fatalf("frozen decode: %v", err)
	}
	if policy == nil || policy.SchemaVersion() != version || !policy.Permissions().Has(agentruntime.PermissionCaseRead) {
		t.Fatalf("decoded policy = %v", policy)
	}
}

func TestDecodeWorkerInvestigationPolicyFrozenRejectsCorruptJSON(t *testing.T) {
	_, version := mustWorkerFrozenPolicy(t)
	for _, corrupt := range [][]byte{
		[]byte(`{"schemaVersion":1,"permissions":["case.read"],"grants":{},"unknownField":true}`),
		[]byte(`{"schemaVersion":1,"permissions":[`),
		[]byte(`not-json`),
	} {
		if _, err := decodeWorkerInvestigationPolicy(
			workerPolicyModeRecord(diagnosis.InvestigationPolicyModeFrozen, corrupt, intPtr(version)),
		); !errors.Is(err, diagnosis.ErrInvalidTask) {
			t.Fatalf("corrupt frozen payload error = %v, want ErrInvalidTask", err)
		}
	}
}

func TestDecodeWorkerInvestigationPolicyFrozenRejectsVersionMismatch(t *testing.T) {
	payload, version := mustWorkerFrozenPolicy(t)
	if _, err := decodeWorkerInvestigationPolicy(
		workerPolicyModeRecord(diagnosis.InvestigationPolicyModeFrozen, payload, intPtr(version+1)),
	); !errors.Is(err, diagnosis.ErrInvalidTask) {
		t.Fatalf("column/json version mismatch error = %v, want ErrInvalidTask", err)
	}
}

func TestDecodeWorkerInvestigationPolicyRejectsIllegalMode(t *testing.T) {
	payload, version := mustWorkerFrozenPolicy(t)
	for _, mode := range []diagnosis.InvestigationPolicyMode{"", "FROZEN", "Legacy", "froze", "unknown", "legacy "} {
		if _, err := decodeWorkerInvestigationPolicy(
			workerPolicyModeRecord(mode, payload, intPtr(version)),
		); !errors.Is(err, diagnosis.ErrInvalidTask) {
			t.Fatalf("mode %q error = %v, want ErrInvalidTask", mode, err)
		}
		if _, err := decodeWorkerInvestigationPolicy(
			workerPolicyModeRecord(mode, nil, nil),
		); !errors.Is(err, diagnosis.ErrInvalidTask) {
			t.Fatalf("mode %q with NULL columns error = %v, want ErrInvalidTask", mode, err)
		}
	}
}
