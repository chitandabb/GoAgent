package agent

import "testing"

func TestTextToSQLResultMatchesSupportsUnorderedNumericRows(t *testing.T) {
	definition := TextToSQLEvaluationCase{
		DatasetVersion: "text-to-sql-v1", CaseID: "group-status", UserQuery: "group",
		ExpectedColumns: []string{"Status", "Total"},
		ExpectedRows:    [][]any{{"New", 2}, {"Resolved", 1}},
		ResultOrder:     SQLResultUnordered,
	}
	if !TextToSQLResultMatches(definition,
		[]string{"status", "total"},
		[][]any{{"Resolved", int64(1)}, {"New", int64(2)}},
		false,
	) {
		t.Fatal("equivalent unordered rows did not match")
	}
	if TextToSQLResultMatches(definition,
		[]string{"status", "total"},
		[][]any{{"Resolved", int64(1)}, {"New", int64(3)}},
		false,
	) {
		t.Fatal("different rows matched")
	}
}

func TestEvaluateTextToSQLCountsResultMismatch(t *testing.T) {
	definition := TextToSQLEvaluationCase{
		DatasetVersion: "text-to-sql-v1", CaseID: "count", UserQuery: "count",
		ExpectedColumns: []string{"Total"}, ExpectedRows: [][]any{{4}}, ResultOrder: SQLResultOrdered,
	}
	observation := TextToSQLEvaluationObservation{
		DatasetVersion: definition.DatasetVersion, CaseID: definition.CaseID, RunID: "run-1",
		ModelProvider: "stepfun", ModelID: "model", ReasoningEffort: "low", PromptVersion: "prompt-v1",
		SelectedTool: ToolExecuteReadonlyQuery, ToolCallCount: 1,
		GeneratedQuery: "SELECT COUNT(*) AS Total FROM dbo.v_MESGuardExternalCases", QueryHash: "sha256:test",
		Columns: []string{"Total"}, Rows: [][]any{{3}}, ErrorType: "result_mismatch",
		Usage: ModelUsage{ModelCalls: 1, PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110},
	}
	summary, err := EvaluateTextToSQL([]TextToSQLEvaluationCase{definition}, []TextToSQLEvaluationObservation{observation})
	if err != nil {
		t.Fatalf("EvaluateTextToSQL(): %v", err)
	}
	if summary.Correct != 0 || summary.ExecutionAccuracy != 0 || summary.FailureTypes["result_mismatch"] != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}
