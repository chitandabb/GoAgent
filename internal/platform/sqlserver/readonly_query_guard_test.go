package sqlserver

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestReadonlyQueryGuardAcceptsNarrowReadOnlyQueries(t *testing.T) {
	t.Parallel()
	guard := newReadonlyQueryGuardForTest(t)
	tests := []struct {
		name      string
		query     string
		wantCTE   bool
		wantUnion bool
		wantRefs  []ReadonlyQueryObjectRef
	}{
		{
			name:     "basic select and trailing semicolon",
			query:    `SELECT t.TicketID, t.Title FROM dbo.Tickets AS t WHERE t.Status = N'Open';`,
			wantRefs: []ReadonlyQueryObjectRef{{Schema: "dbo", Name: "Tickets"}},
		},
		{
			name: "keywords and semicolon inside comments and strings",
			query: `
SELECT t.TicketID, N'EXEC dbo.bad; DROP TABLE x' AS sample
FROM /* UPDATE dbo.Hidden /* nested DELETE */ */ dbo.Tickets AS t
WHERE t.Title = 'SELECT INTO should stay text' -- MERGE dbo.Hidden
;`,
			wantRefs: []ReadonlyQueryObjectRef{{Schema: "dbo", Name: "Tickets"}},
		},
		{
			name: "cte resolves to physical object",
			query: `WITH recent AS (
    SELECT TicketID, Status FROM dbo.Tickets WHERE ReportedAt >= '2026-01-01'
)
SELECT TicketID FROM recent WHERE Status = 'Open'`,
			wantCTE:  true,
			wantRefs: []ReadonlyQueryObjectRef{{Schema: "dbo", Name: "Tickets"}},
		},
		{
			name: "multiple ctes and union",
			query: `WITH base (TicketID) AS (
    SELECT TicketID FROM dbo.Tickets
), copied AS (
    SELECT TicketID FROM base
)
SELECT TicketID FROM copied
UNION ALL
SELECT TicketID FROM reporting.ArchivedTickets`,
			wantCTE:   true,
			wantUnion: true,
			wantRefs: []ReadonlyQueryObjectRef{
				{Schema: "dbo", Name: "Tickets"},
				{Schema: "reporting", Name: "ArchivedTickets"},
			},
		},
		{
			name: "join derived query and duplicate reference",
			query: `SELECT t.TicketID
FROM dbo.Tickets AS t
JOIN (
    SELECT TicketID FROM dbo.TicketEvents WHERE EventType = 'failed'
) AS e ON e.TicketID = t.TicketID
JOIN dbo.Tickets AS duplicate_t ON duplicate_t.TicketID = t.TicketID`,
			wantRefs: []ReadonlyQueryObjectRef{
				{Schema: "dbo", Name: "Tickets"},
				{Schema: "dbo", Name: "TicketEvents"},
			},
		},
		{
			name:  "comma separated sources",
			query: `SELECT t.TicketID FROM dbo.Tickets AS t, dbo.Customers AS c WHERE c.ID = t.CustomerID`,
			wantRefs: []ReadonlyQueryObjectRef{
				{Schema: "dbo", Name: "Tickets"},
				{Schema: "dbo", Name: "Customers"},
			},
		},
		{
			name:  "quoted identifiers",
			query: `SELECT d.[Order ID] FROM [dbo].[Order Details] AS d JOIN "reporting"."Daily Report" AS r ON r.ID = d.[Order ID]`,
			wantRefs: []ReadonlyQueryObjectRef{
				{Schema: "dbo", Name: "Order Details"},
				{Schema: "reporting", Name: "Daily Report"},
			},
		},
		{
			name:     "authorized table valued function",
			query:    `SELECT f.TicketID FROM dbo.fn_RecentTickets(30) AS f`,
			wantRefs: []ReadonlyQueryObjectRef{{Schema: "dbo", Name: "fn_RecentTickets"}},
		},
		{
			name:     "safe nolock table hint",
			query:    `SELECT t.*, dbo.Tickets.* FROM dbo.Tickets AS t WITH (NOLOCK)`,
			wantRefs: []ReadonlyQueryObjectRef{{Schema: "dbo", Name: "Tickets"}},
		},
		{
			name:  "scalar function joins catalog object list",
			query: `SELECT dbo.fn_NormalizeTitle(t.Title), GETDATE() FROM dbo.Tickets AS t`,
			wantRefs: []ReadonlyQueryObjectRef{
				{Schema: "dbo", Name: "Tickets"},
				{Schema: "dbo", Name: "fn_NormalizeTitle"},
			},
		},
		{
			name:     "scalar function without table source",
			query:    `SELECT dbo.fn_ProductVersion()`,
			wantRefs: []ReadonlyQueryObjectRef{{Schema: "dbo", Name: "fn_ProductVersion"}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := guard.Analyze(test.query)
			if err != nil {
				t.Fatalf("Analyze() error = %v", err)
			}
			if got.PolicyVersion != ReadonlyQueryPolicyVersion || got.StatementType != "SELECT" {
				t.Fatalf("Analyze() policy/type = %q/%q", got.PolicyVersion, got.StatementType)
			}
			if got.HasCTE != test.wantCTE || got.HasUnion != test.wantUnion {
				t.Fatalf("Analyze() flags cte=%t union=%t, want %t/%t", got.HasCTE, got.HasUnion, test.wantCTE, test.wantUnion)
			}
			if !reflect.DeepEqual(got.Objects, test.wantRefs) {
				t.Fatalf("Analyze() objects = %#v, want %#v", got.Objects, test.wantRefs)
			}
		})
	}
}

func TestReadonlyQueryGuardRejectsUnsafeOrAmbiguousQueries(t *testing.T) {
	t.Parallel()
	guard := newReadonlyQueryGuardForTest(t)
	tests := []struct {
		name   string
		query  string
		reason QueryRejectionReason
	}{
		{name: "empty", query: " \n\t", reason: QueryRejectedEmpty},
		{name: "empty statement", query: ";", reason: QueryRejectedEmpty},
		{name: "too large", query: "SELECT * FROM dbo.Tickets /*" + strings.Repeat("x", 4096) + "*/", reason: QueryRejectedTooLarge},
		{name: "multiple selects", query: "SELECT * FROM dbo.Tickets; SELECT * FROM dbo.Users", reason: QueryRejectedMultipleStatements},
		{name: "multiple selects without semicolon", query: "SELECT * FROM dbo.Tickets SELECT * FROM dbo.Users", reason: QueryRejectedInvalidSyntax},
		{name: "leading batch separator", query: ";WITH cte AS (SELECT * FROM dbo.Tickets) SELECT * FROM cte", reason: QueryRejectedMultipleStatements},
		{name: "delete statement", query: "DELETE FROM dbo.Tickets", reason: QueryRejectedStatement},
		{name: "dml inside cte", query: "WITH changed AS (DELETE FROM dbo.Tickets OUTPUT deleted.TicketID) SELECT * FROM changed", reason: QueryRejectedDangerousKeyword},
		{name: "select into", query: "SELECT * INTO dbo.TicketCopy FROM dbo.Tickets", reason: QueryRejectedSelectInto},
		{name: "exec", query: "SELECT * FROM dbo.Tickets WHERE EXISTS (EXEC dbo.usp_Test)", reason: QueryRejectedDangerousKeyword},
		{name: "variable", query: "SELECT * FROM dbo.Tickets WHERE TicketID = @ticketID", reason: QueryRejectedVariable},
		{name: "temporary table", query: "SELECT * FROM #Tickets", reason: QueryRejectedTemporaryObject},
		{name: "unqualified table", query: "SELECT * FROM Tickets", reason: QueryRejectedUnqualifiedObject},
		{name: "disallowed schema", query: "SELECT * FROM sys.objects", reason: QueryRejectedSchema},
		{name: "three part database name", query: "SELECT * FROM Support.dbo.Tickets", reason: QueryRejectedCrossDatabase},
		{name: "four part linked server name", query: "SELECT * FROM Remote.Support.dbo.Tickets", reason: QueryRejectedCrossDatabase},
		{name: "cross database scalar function", query: "SELECT Support.dbo.fn_Secret() FROM dbo.Tickets", reason: QueryRejectedCrossDatabase},
		{name: "four part projected column", query: "SELECT Support.dbo.Tickets.Secret FROM dbo.Tickets", reason: QueryRejectedCrossDatabase},
		{name: "no catalog object", query: "SELECT 1", reason: QueryRejectedNoObject},
		{name: "open rowset", query: "SELECT * FROM OPENROWSET(BULK 'secret.txt', SINGLE_CLOB) AS x", reason: QueryRejectedDangerousKeyword},
		{name: "values table source", query: "SELECT v.ID FROM (VALUES (1)) AS v(ID)", reason: QueryRejectedUnsupportedSource},
		{name: "cross apply", query: "SELECT * FROM dbo.Tickets CROSS APPLY dbo.fn_Events(TicketID)", reason: QueryRejectedDangerousKeyword},
		{name: "pivot", query: "SELECT * FROM dbo.Tickets PIVOT (COUNT(TicketID) FOR Status IN ([Open])) AS p", reason: QueryRejectedDangerousKeyword},
		{name: "query option", query: "SELECT * FROM dbo.Tickets OPTION (MAXDOP 0)", reason: QueryRejectedDangerousKeyword},
		{name: "update lock hint", query: "SELECT * FROM dbo.Tickets WITH (UPDLOCK)", reason: QueryRejectedDangerousKeyword},
		{name: "index table hint", query: "SELECT * FROM dbo.Tickets WITH (INDEX(IX_Tickets_Status))", reason: QueryRejectedDangerousKeyword},
		{name: "combined table hints", query: "SELECT * FROM dbo.Tickets WITH (NOLOCK, INDEX(IX_Tickets_Status))", reason: QueryRejectedDangerousKeyword},
		{name: "legacy nolock table hint", query: "SELECT * FROM dbo.Tickets (NOLOCK)", reason: QueryRejectedDangerousKeyword},
		{name: "legacy index table hint", query: "SELECT * FROM dbo.Tickets (INDEX(IX_Tickets_Status))", reason: QueryRejectedDangerousKeyword},
		{name: "function from disallowed schema", query: "SELECT sys.fn_builtin_permissions() FROM dbo.Tickets", reason: QueryRejectedSchema},
		{name: "sequence state change", query: "SELECT NEXT VALUE FOR dbo.TicketSequence FROM dbo.Tickets", reason: QueryRejectedDangerousKeyword},
		{name: "go batch", query: "SELECT * FROM dbo.Tickets GO SELECT * FROM dbo.Users", reason: QueryRejectedDangerousKeyword},
		{name: "unbalanced parenthesis", query: "SELECT * FROM dbo.Tickets WHERE (TicketID = 1", reason: QueryRejectedInvalidSyntax},
		{name: "unterminated string", query: "SELECT * FROM dbo.Tickets WHERE Title = 'secret", reason: QueryRejectedInvalidSyntax},
		{name: "unterminated comment", query: "SELECT * FROM dbo.Tickets /* secret", reason: QueryRejectedInvalidSyntax},
		{name: "unterminated identifier", query: "SELECT * FROM [dbo].[Tickets", reason: QueryRejectedInvalidSyntax},
		{name: "malformed cte", query: "WITH cte SELECT * FROM dbo.Tickets", reason: QueryRejectedInvalidSyntax},
		{name: "cte without final select", query: "WITH cte AS (SELECT * FROM dbo.Tickets)", reason: QueryRejectedInvalidSyntax},
		{name: "empty cte column list", query: "WITH cte() AS (SELECT * FROM dbo.Tickets) SELECT * FROM cte", reason: QueryRejectedInvalidSyntax},
		{name: "unsupported derived cte source", query: "SELECT * FROM (WITH cte AS (SELECT * FROM dbo.Tickets) SELECT * FROM cte) AS x", reason: QueryRejectedUnsupportedSource},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := guard.Analyze(test.query)
			if !errors.Is(err, ErrReadonlyQueryRejected) {
				t.Fatalf("Analyze() error = %v, want ErrReadonlyQueryRejected", err)
			}
			var rejection *QueryGuardError
			if !errors.As(err, &rejection) || rejection.Reason != test.reason {
				t.Fatalf("Analyze() reason = %v, want %s", err, test.reason)
			}
		})
	}
}

func TestReadonlyQueryGuardDoesNotLeakQueryInError(t *testing.T) {
	t.Parallel()
	guard := newReadonlyQueryGuardForTest(t)
	secret := "customer-secret-4711"
	_, err := guard.Analyze("SELECT * FROM dbo.Tickets; DROP TABLE [" + secret + "]")
	if err == nil {
		t.Fatal("Analyze() error = nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Analyze() leaked query content in error: %v", err)
	}
}

func TestNewReadonlyQueryGuardValidatesPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		schemas []string
		max     int
	}{
		{name: "empty schemas", max: 1024},
		{name: "unsafe schema", schemas: []string{"dbo;DROP"}, max: 1024},
		{name: "zero max", schemas: []string{"dbo"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewReadonlyQueryGuard(test.schemas, test.max); err == nil {
				t.Fatal("NewReadonlyQueryGuard() error = nil")
			}
		})
	}
}

func FuzzReadonlyQueryGuardNeverPanics(f *testing.F) {
	guard, err := NewReadonlyQueryGuard([]string{"dbo", "reporting"}, 4096)
	if err != nil {
		f.Fatalf("NewReadonlyQueryGuard(): %v", err)
	}
	seeds := []string{
		"SELECT * FROM dbo.Tickets",
		"WITH x AS (SELECT * FROM dbo.Tickets) SELECT * FROM x",
		"SELECT N'; DROP TABLE x' FROM dbo.Tickets -- harmless",
		"SELECT * FROM [dbo].[Order]]Details]",
		"/* nested /* comment */ still comment */ SELECT * FROM dbo.Tickets",
		"SELECT Support.dbo.fn_secret() FROM dbo.Tickets",
		"\x00\xff;EXEC(@x)",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, query string) {
		analysis, err := guard.Analyze(query)
		if err != nil {
			if !errors.Is(err, ErrReadonlyQueryRejected) {
				t.Fatalf("Analyze() returned non-policy error: %v", err)
			}
			return
		}
		if analysis.PolicyVersion != ReadonlyQueryPolicyVersion || analysis.StatementType != "SELECT" || len(analysis.Objects) == 0 {
			t.Fatalf("Analyze() returned incomplete accepted result: %#v", analysis)
		}
	})
}

func newReadonlyQueryGuardForTest(t *testing.T) *ReadonlyQueryGuard {
	t.Helper()
	guard, err := NewReadonlyQueryGuard([]string{"dbo", "reporting"}, 4096)
	if err != nil {
		t.Fatalf("NewReadonlyQueryGuard(): %v", err)
	}
	return guard
}
