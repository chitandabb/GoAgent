package agentruntime

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func mustPolicyForCodecTest(t *testing.T, permissions []Permission, config ResourceGrantsConfig) InvestigationPolicy {
	t.Helper()
	permissionSet, err := NewPermissionSet(permissions...)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := NewResourceGrants(config)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := NewInvestigationPolicy(3, permissionSet, grants)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func TestInvestigationPolicyCodecRoundTripsDeterministically(t *testing.T) {
	first := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	second := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	third := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	// 输入顺序刻意打乱；规范化顺序必须稳定。
	policy := mustPolicyForCodecTest(t,
		[]Permission{PermissionSQLRead, PermissionCaseRead, PermissionKnowledgeRead},
		ResourceGrantsConfig{
			DataSourceIDs:   []uuid.UUID{second, first},
			ExternalCaseIDs: []uuid.UUID{second},
			AttachmentIDs:   []uuid.UUID{third},
		},
	)
	firstEncoded, err := MarshalInvestigationPolicy(policy)
	if err != nil {
		t.Fatalf("MarshalInvestigationPolicy: %v", err)
	}
	secondEncoded, err := MarshalInvestigationPolicy(policy)
	if err != nil {
		t.Fatalf("MarshalInvestigationPolicy: %v", err)
	}
	if !bytes.Equal(firstEncoded, secondEncoded) {
		t.Fatalf("encoding is not deterministic:\n%s\n%s", firstEncoded, secondEncoded)
	}
	var payload struct {
		SchemaVersion int      `json:"schemaVersion"`
		Permissions   []string `json:"permissions"`
		Grants        struct {
			DataSourceIDs   []string `json:"dataSourceIds"`
			ExternalCaseIDs []string `json:"externalCaseIds"`
			AttachmentIDs   []string `json:"attachmentIds"`
		} `json:"grants"`
	}
	if err := json.Unmarshal(firstEncoded, &payload); err != nil {
		t.Fatalf("decode policy JSON: %v", err)
	}
	if payload.SchemaVersion != 3 {
		t.Fatalf("schemaVersion = %d, want 3", payload.SchemaVersion)
	}
	wantPermissions := []string{"case.read", "knowledge.read", "sql.read"}
	if strings.Join(payload.Permissions, ",") != strings.Join(wantPermissions, ",") {
		t.Fatalf("permissions = %v, want %v", payload.Permissions, wantPermissions)
	}
	wantDataSources := []string{first.String(), second.String()}
	if strings.Join(payload.Grants.DataSourceIDs, ",") != strings.Join(wantDataSources, ",") {
		t.Fatalf("dataSourceIds = %v, want %v", payload.Grants.DataSourceIDs, wantDataSources)
	}

	decoded, err := UnmarshalInvestigationPolicy(firstEncoded)
	if err != nil {
		t.Fatalf("UnmarshalInvestigationPolicy: %v", err)
	}
	if decoded.SchemaVersion() != policy.SchemaVersion() {
		t.Fatalf("schemaVersion = %d, want %d", decoded.SchemaVersion(), policy.SchemaVersion())
	}
	if !decoded.Grants().AllowsDataSource(first) || !decoded.Grants().AllowsDataSource(second) ||
		!decoded.Grants().AllowsExternalCase(second) || !decoded.Grants().AllowsAttachment(third) {
		t.Fatalf("decoded grants = %v", decoded.Grants())
	}
	for _, permission := range wantPermissions {
		if !decoded.Permissions().Has(Permission(permission)) {
			t.Fatalf("decoded permissions missing %q", permission)
		}
	}
	// 重编码必须与第一次逐字节一致。
	reEncoded, err := MarshalInvestigationPolicy(decoded)
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	if !bytes.Equal(firstEncoded, reEncoded) {
		t.Fatalf("round-trip encoding drifted:\n%s\n%s", firstEncoded, reEncoded)
	}
}

func TestInvestigationPolicyCodecRejectsUnknownFields(t *testing.T) {
	raw := `{"schemaVersion":1,"permissions":["case.read"],"grants":{},"extra":"nope"}`
	if _, err := UnmarshalInvestigationPolicy([]byte(raw)); err == nil {
		t.Fatal("UnmarshalInvestigationPolicy accepted an unknown field")
	}
}

func TestInvestigationPolicyCodecRejectsTrailingValues(t *testing.T) {
	raw := `{"schemaVersion":1,"permissions":["case.read"],"grants":{}} {"schemaVersion":1}`
	if _, err := UnmarshalInvestigationPolicy([]byte(raw)); err == nil {
		t.Fatal("UnmarshalInvestigationPolicy accepted multiple JSON values")
	}
}

func TestInvestigationPolicyCodecRejectsInvalidPermission(t *testing.T) {
	raw := `{"schemaVersion":1,"permissions":["case.read","shell.exec"],"grants":{}}`
	if _, err := UnmarshalInvestigationPolicy([]byte(raw)); err == nil {
		t.Fatal("UnmarshalInvestigationPolicy accepted an invalid permission")
	}
}

func TestInvestigationPolicyCodecRejectsDuplicatePermissions(t *testing.T) {
	raw := `{"schemaVersion":1,"permissions":["case.read","case.read"],"grants":{}}`
	if _, err := UnmarshalInvestigationPolicy([]byte(raw)); err == nil {
		t.Fatal("UnmarshalInvestigationPolicy accepted duplicate permissions")
	}
}

func TestInvestigationPolicyCodecRejectsEmptyAndNilUUIDs(t *testing.T) {
	raw := `{"schemaVersion":1,"permissions":["case.read"],"grants":{"externalCaseIds":[""]}}`
	if _, err := UnmarshalInvestigationPolicy([]byte(raw)); err == nil {
		t.Fatal("UnmarshalInvestigationPolicy accepted an empty UUID")
	}
	raw = `{"schemaVersion":1,"permissions":["case.read"],"grants":{"externalCaseIds":["00000000-0000-0000-0000-000000000000"]}}`
	if _, err := UnmarshalInvestigationPolicy([]byte(raw)); err == nil {
		t.Fatal("UnmarshalInvestigationPolicy accepted a nil UUID")
	}
}

func TestInvestigationPolicyCodecRejectsInvalidVersionsAndEmptyPermissions(t *testing.T) {
	raw := `{"schemaVersion":0,"permissions":["case.read"],"grants":{}}`
	if _, err := UnmarshalInvestigationPolicy([]byte(raw)); err == nil {
		t.Fatal("UnmarshalInvestigationPolicy accepted schemaVersion 0")
	}
	raw = `{"schemaVersion":1,"permissions":[],"grants":{}}`
	if _, err := UnmarshalInvestigationPolicy([]byte(raw)); err == nil {
		t.Fatal("UnmarshalInvestigationPolicy accepted an empty permission set")
	}
}

func TestInvestigationPolicyCodecRejectsDuplicatedGrants(t *testing.T) {
	id := "11111111-1111-1111-1111-111111111111"
	raw := `{"schemaVersion":1,"permissions":["case.read"],"grants":{"externalCaseIds":["` + id + `","` + id + `"]}}`
	if _, err := UnmarshalInvestigationPolicy([]byte(raw)); err == nil {
		t.Fatal("UnmarshalInvestigationPolicy accepted duplicate grants")
	}
}

func TestInvestigationPolicyCodecRejectsNonObjectPayload(t *testing.T) {
	for _, raw := range []string{`[]`, `"policy"`, `null`, ``, `   `} {
		if _, err := UnmarshalInvestigationPolicy([]byte(raw)); err == nil {
			t.Fatalf("UnmarshalInvestigationPolicy accepted %q", raw)
		}
	}
}
