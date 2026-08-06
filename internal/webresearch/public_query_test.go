package webresearch

import (
	"errors"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/externalcase"
)

func TestQueryPolicySanitizesStructuredAndTaskSensitiveValues(t *testing.T) {
	policy, err := NewQueryPolicy(QueryPolicyConfig{SensitiveTerms: []string{"MESGuard Enterprise"}})
	if err != nil {
		t.Fatalf("NewQueryPolicy: %v", err)
	}
	item := externalcase.ExternalCase{
		ExternalCaseKey: "TKT-20260806", Customer: externalcase.CustomerContext{Code: "CUST-001", Name: "华东精密制造"},
		Production:  externalcase.ProductionContext{WorkOrderNo: "WO-889900", EquipmentCode: "EQ-42"},
		Environment: externalcase.EnvironmentContext{BusinessDatabaseAlias: "customer-prod-db"},
	}
	query, err := policy.SanitizeForExternalCase(
		"华东精密制造 MESGuard Enterprise TKT-20260806 在 192.168.10.8 上出现 SQL Server 连接池 timeout error 258，联系 ops@example.com",
		item,
	)
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}
	if query.String() != "在 上出现 SQL Server 连接池 timeout error 258，联系" {
		t.Fatalf("sanitized query = %q", query.String())
	}
	for _, secret := range []string{"华东精密制造", "MESGuard Enterprise", "TKT-20260806", "192.168.10.8", "ops@example.com"} {
		if strings.Contains(query.String(), secret) {
			t.Fatalf("sanitized query leaked %q: %q", secret, query.String())
		}
	}
	if !query.Redacted() || len(query.Findings()) < 4 {
		t.Fatalf("findings = %+v", query.Findings())
	}
}

func TestQueryPolicyPreservesPublicTechnicalSignals(t *testing.T) {
	policy, err := NewQueryPolicy(QueryPolicyConfig{})
	if err != nil {
		t.Fatalf("NewQueryPolicy: %v", err)
	}
	query, err := policy.Sanitize("SQL Server 2022 error 258 connection pool timeout dotnet 8.0", nil)
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}
	if query.String() != "SQL Server 2022 error 258 connection pool timeout dotnet 8.0" || query.Redacted() {
		t.Fatalf("public query = %q findings=%+v", query.String(), query.Findings())
	}
}

func TestQueryPolicyBlocksSecretsBeforeReplacement(t *testing.T) {
	policy, err := NewQueryPolicy(QueryPolicyConfig{})
	if err != nil {
		t.Fatalf("NewQueryPolicy: %v", err)
	}
	for _, value := range []string{
		"SQL Server timeout Authorization: Bearer abcdefghijklmnopqrstuvwxyz",
		"连接失败 password=x SQL Server timeout",
		"Server=10.0.0.1;Database=ERP;User ID=sa;Password=secret SQL Server error",
		"Integrated Security=true;Data Source=customer-db SQL Server timeout",
		"-----BEGIN PRIVATE KEY----- secret material",
		"JWT eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijklmnopqrstuvwxyz",
	} {
		if _, err := policy.Sanitize(value, nil); !errors.Is(err, ErrSensitiveSecretDetected) {
			t.Fatalf("Sanitize(%q) error = %v", value, err)
		}
	}
}

func TestQueryPolicyDoesNotTreatPublicTechnicalWordsAsBusinessIDs(t *testing.T) {
	policy, err := NewQueryPolicy(QueryPolicyConfig{})
	if err != nil {
		t.Fatalf("NewQueryPolicy: %v", err)
	}
	input := "SQL Server serializable isolation and CASEWHEN parser behavior"
	query, err := policy.Sanitize(input, nil)
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}
	if query.String() != input || query.Redacted() {
		t.Fatalf("public query = %q findings=%+v", query.String(), query.Findings())
	}
}

func TestQueryPolicyFailsClosedForStructuredOrOverRedactedInput(t *testing.T) {
	policy, err := NewQueryPolicy(QueryPolicyConfig{})
	if err != nil {
		t.Fatalf("NewQueryPolicy: %v", err)
	}
	if _, err := policy.Sanitize("2026-08-06 ERROR customer-db\nstack trace line 2", nil); !errors.Is(err, ErrStructuredContentBlocked) {
		t.Fatalf("multiline error = %v", err)
	}
	for _, value := range []string{
		"SELECT CustomerName, PasswordHash FROM dbo.Users WHERE CustomerID = 42",
		`{"customer":"ACME","host":"prod-db-01"}`,
		"2026-08-06 12:30:01 ERROR customer-db connection refused",
		"panic at company.private.Service.Run(service.go:42)",
	} {
		if _, err := policy.Sanitize(value, nil); !errors.Is(err, ErrStructuredContentBlocked) {
			t.Fatalf("structured input %q error = %v", value, err)
		}
	}
	if _, err := policy.Sanitize("TKT-20260806 192.168.1.2 ops@example.com", nil); !errors.Is(err, ErrInsufficientPublicQuery) {
		t.Fatalf("over-redacted error = %v", err)
	}
}

func TestSensitiveTermsFromExternalCaseExcludesPublicProductContext(t *testing.T) {
	item := externalcase.ExternalCase{
		ExternalCaseKey: "TKT-1", Customer: externalcase.CustomerContext{Name: "客户甲"},
		Product:    externalcase.ProductContext{Name: "SQL Server", Version: "2022"},
		Production: externalcase.ProductionContext{WorkOrderNo: "WO-1"},
	}
	terms := SensitiveTermsFromExternalCase(item)
	joined := strings.Join(terms, "|")
	if !strings.Contains(joined, "客户甲") || strings.Contains(joined, "SQL Server") || strings.Contains(joined, "2022") {
		t.Fatalf("case terms = %v", terms)
	}
}

func TestQueryPolicyRejectsInvalidSensitiveTerms(t *testing.T) {
	if _, err := NewQueryPolicy(QueryPolicyConfig{SensitiveTerms: []string{"x"}}); !errors.Is(err, ErrInvalidQueryPolicy) {
		t.Fatalf("NewQueryPolicy error = %v", err)
	}
	policy, err := NewQueryPolicy(QueryPolicyConfig{})
	if err != nil {
		t.Fatalf("NewQueryPolicy: %v", err)
	}
	if _, err := policy.Sanitize("SQL Server timeout error", []string{"x"}); !errors.Is(err, ErrInvalidQueryPolicy) {
		t.Fatalf("Sanitize error = %v", err)
	}
}
