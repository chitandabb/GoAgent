package postgres

import (
	"errors"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/diagnosis"

	"github.com/google/uuid"
)

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

func intPtr(value int) *int { return &value }

func workerPolicyRecord(payload []byte, version *int) workerTaskRecord {
	return workerTaskRecord{
		InvestigationPolicy:              payload,
		InvestigationPolicySchemaVersion: version,
	}
}

// TestDecodeWorkerInvestigationPolicyReturnsPolicy 证明 Worker 没有 legacy
// fallback：只有两列同时存在、严格 codec 通过且版本一致时才返回 Policy。
func TestDecodeWorkerInvestigationPolicyReturnsPolicy(t *testing.T) {
	payload, version := mustWorkerFrozenPolicy(t)
	policy, err := decodeWorkerInvestigationPolicy(workerPolicyRecord(payload, intPtr(version)))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if policy.SchemaVersion() != version || !policy.Permissions().Has(agentruntime.PermissionCaseRead) {
		t.Fatalf("decoded policy = %v", policy)
	}
}

// TestDecodeWorkerInvestigationPolicyRejectsMissingColumns 证明缺失 Policy
// 的任务无法执行：payload/版本任一缺失都 fail-closed，绝不退回 legacy 派生。
func TestDecodeWorkerInvestigationPolicyRejectsMissingColumns(t *testing.T) {
	payload, version := mustWorkerFrozenPolicy(t)
	cases := []workerTaskRecord{
		workerPolicyRecord(nil, nil),
		workerPolicyRecord(payload, nil),
		workerPolicyRecord(nil, intPtr(version)),
	}
	for _, record := range cases {
		if _, err := decodeWorkerInvestigationPolicy(record); !errors.Is(err, diagnosis.ErrInvalidTask) {
			t.Fatalf("missing-column record error = %v, want ErrInvalidTask", err)
		}
	}
}

func TestDecodeWorkerInvestigationPolicyRejectsCorruptJSON(t *testing.T) {
	_, version := mustWorkerFrozenPolicy(t)
	for _, corrupt := range [][]byte{
		[]byte(`{"schemaVersion":1,"permissions":["case.read"],"grants":{},"unknownField":true}`),
		[]byte(`{"schemaVersion":1,"permissions":[`),
		[]byte(`not-json`),
	} {
		if _, err := decodeWorkerInvestigationPolicy(
			workerPolicyRecord(corrupt, intPtr(version)),
		); !errors.Is(err, diagnosis.ErrInvalidTask) {
			t.Fatalf("corrupt payload error = %v, want ErrInvalidTask", err)
		}
	}
}

func TestDecodeWorkerInvestigationPolicyRejectsVersionMismatch(t *testing.T) {
	payload, version := mustWorkerFrozenPolicy(t)
	if _, err := decodeWorkerInvestigationPolicy(
		workerPolicyRecord(payload, intPtr(version+1)),
	); !errors.Is(err, diagnosis.ErrInvalidTask) {
		t.Fatalf("column/json version mismatch error = %v, want ErrInvalidTask", err)
	}
}
