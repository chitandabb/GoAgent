package sqlserver

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/externalcase"
)

func TestCaseWhereAddsNamedCaseTypeFilter(t *testing.T) {
	reader := &ExternalCaseReader{caseFields: map[string]string{"caseType": "CaseType"}}

	where, args := reader.caseWhere(externalcase.ListQuery{CaseType: " performance "})

	if !strings.Contains(where, "[CaseType] = @caseType") {
		t.Fatalf("where = %q, want named caseType condition", where)
	}
	if len(args) != 1 {
		t.Fatalf("args = %#v, want one argument", args)
	}
	named, ok := args[0].(sql.NamedArg)
	if !ok || named.Name != "caseType" || named.Value != "performance" {
		t.Fatalf("caseType argument = %#v, want named trimmed value", args[0])
	}
}

func TestTruncateUTF8KeepsCharacterBoundary(t *testing.T) {
	got, truncated := truncateUTF8("故障ABC", 5)
	if !truncated {
		t.Fatal("expected text to be truncated")
	}
	if got != "故" {
		t.Fatalf("got %q, want one complete Chinese character", got)
	}
}

func TestTruncateUTF8LeavesShortTextUntouched(t *testing.T) {
	got, truncated := truncateUTF8("正常", 6)
	if truncated || got != "正常" {
		t.Fatalf("got %q truncated=%v", got, truncated)
	}
}
