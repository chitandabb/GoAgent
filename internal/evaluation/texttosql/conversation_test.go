package texttosql

import (
	"strings"
	"testing"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
)

func conversationObservationForTest(t *testing.T) TextToSQLConversationEvaluationObservation {
	t.Helper()
	return TextToSQLConversationEvaluationObservation{
		ObservationSchemaVersion: TextToSQLConversationObservationSchemaVersion,
		EntryMode:                TextToSQLConversationEntryMode,
		DatasetVersion:           "text-to-sql-v1",
		CaseID:                   "case-1",
		RunID:                    "case-1-run",
		ModelProvider:            "stepfun",
		ModelID:                  "step-3.7-flash",
		ReasoningEffort:          "low",
		PromptVersion:            "conversation-v1",
		ModelProfileFingerprint:  strings.Repeat("a", 64),
		ImplementationRevision:   "git:test-revision",
		ImplementationDirty:      false,
		ToolProfileID:            "conversation-default",
		ToolSchemaFingerprint:    strings.Repeat("b", 64),
		ActualToolCallCount:      2,
		ToolTraceComplete:        true,
		ToolSequenceCorrect:      true,
		ActualToolCalls: []TextToSQLConversationToolCall{
			{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: true},
			{ToolName: mesagent.ToolExecuteReadonlyQuery, QueryHash: "sha256:" + strings.Repeat("c", 64), Succeeded: true},
		},
		Answer:           "工单 TKT-999 当前状态为 处理中。",
		QueryHash:        "sha256:" + strings.Repeat("c", 64),
		Columns:          []string{"Status"},
		Rows:             [][]any{{"处理中"}},
		ExecutionCorrect: true,
		AnswerCorrect:    true,
		Correct:          true,
		Usage:            mesagent.ModelUsage{ModelCalls: 2, PromptTokens: 20, CompletionTokens: 4, TotalTokens: 24},
		DurationMillis:   100,
	}
}

func TestTextToSQLConversationObservationRejectsDirectV1Contract(t *testing.T) {
	observation := conversationObservationForTest(t)
	observation.ObservationSchemaVersion = ""
	if err := observation.Validate(); err == nil ||
		!strings.Contains(err.Error(), "must not be mixed into conversation v2 summaries") {
		t.Fatalf("unversioned direct v1 observation must be rejected with the mixing message, got %v", err)
	}
	observation = conversationObservationForTest(t)
	observation.ObservationSchemaVersion = "text-to-sql-v1"
	if err := observation.Validate(); err == nil {
		t.Fatal("a foreign v1 schema version must be rejected")
	}
}

func TestTextToSQLConversationObservationRejectsInvalidIdentity(t *testing.T) {
	observation := conversationObservationForTest(t)
	observation.EntryMode = "direct"
	if err := observation.Validate(); err == nil {
		t.Fatal("entryMode must be conversation")
	}
	observation = conversationObservationForTest(t)
	observation.ToolProfileID = "evaluation-wide-v1"
	if err := observation.Validate(); err == nil ||
		!strings.Contains(err.Error(), "conversation-default") {
		t.Fatalf("toolProfileId must be conversation-default, got %v", err)
	}
	observation = conversationObservationForTest(t)
	observation.ModelProfileFingerprint = "not-a-sha"
	if err := observation.Validate(); err == nil {
		t.Fatal("modelProfileFingerprint must be a SHA-256 hex value")
	}
	observation = conversationObservationForTest(t)
	observation.ToolSchemaFingerprint = ""
	if err := observation.Validate(); err == nil {
		t.Fatal("toolSchemaFingerprint is required")
	}
	observation = conversationObservationForTest(t)
	observation.ImplementationRevision = ""
	if err := observation.Validate(); err == nil {
		t.Fatal("implementationRevision is required")
	}
	observation = conversationObservationForTest(t)
	observation.ActualToolCalls = []TextToSQLConversationToolCall{
		{ToolName: mesagent.ToolExecuteReadonlyQuery, Succeeded: true},
	}
	if err := observation.Validate(); err == nil {
		t.Fatal("execute_readonly_query call without queryHash must be rejected")
	}
	observation = conversationObservationForTest(t)
	observation.Correct = true
	observation.ErrorType = "result_mismatch"
	if err := observation.Validate(); err == nil {
		t.Fatal("correct observation cannot carry errorType")
	}
	observation = conversationObservationForTest(t)
	observation.Usage = mesagent.ModelUsage{}
	if err := observation.Validate(); err == nil {
		t.Fatal("zero model calls without errorType must be rejected")
	}
}

func TestEvaluateTextToSQLConversationSummary(t *testing.T) {
	definitions := []mesagent.TextToSQLEvaluationCase{
		{
			DatasetVersion: "text-to-sql-v1", CaseID: "case-1",
			UserQuery:       "查询工单 TKT-999 的实时状态",
			ExpectedColumns: []string{"Status"}, ExpectedRows: [][]any{{"处理中"}},
			ResultOrder: mesagent.SQLResultOrdered,
		},
		{
			DatasetVersion: "text-to-sql-v1", CaseID: "case-2",
			UserQuery:       "查询工单 TKT-998 的实时状态",
			ExpectedColumns: []string{"Status"}, ExpectedRows: [][]any{{"已解决"}},
			ResultOrder: mesagent.SQLResultOrdered,
		},
	}
	first := conversationObservationForTest(t)
	second := conversationObservationForTest(t)
	second.CaseID = "case-2"
	second.RunID = "case-2-run"
	second.Correct = false
	second.ExecutionCorrect = false
	second.AnswerCorrect = false
	second.ErrorType = "guard_rejected"
	second.ActualToolCalls = []TextToSQLConversationToolCall{
		{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: true},
		{ToolName: mesagent.ToolExecuteReadonlyQuery, QueryHash: "sha256:" + strings.Repeat("d", 64), Succeeded: false, ErrorType: "guard_rejected"},
	}
	second.Usage = mesagent.ModelUsage{ModelCalls: 3, PromptTokens: 30, CompletionTokens: 6, TotalTokens: 36}

	summary, err := EvaluateTextToSQLConversation(definitions, []TextToSQLConversationEvaluationObservation{first, second})
	if err != nil {
		t.Fatalf("EvaluateTextToSQLConversation: %v", err)
	}
	if summary.Cases != 2 || summary.ExecutionCorrect != 1 || summary.AnswerCorrect != 1 ||
		summary.EndToEndCorrect != 1 || summary.ToolSequenceCorrect != 2 || summary.ToolSequenceAccuracy != 1 ||
		summary.ExecutionAccuracy != 0.5 ||
		summary.AnswerAccuracy != 0.5 || summary.EndToEndAccuracy != 0.5 {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.ObservationSchemaVersion != TextToSQLConversationObservationSchemaVersion || summary.EntryMode != TextToSQLConversationEntryMode {
		t.Fatalf("summary contract = %+v", summary)
	}
	if !summary.Formal || summary.ImplementationDirty {
		t.Fatalf("clean summary identity = formal:%t dirty:%t", summary.Formal, summary.ImplementationDirty)
	}
	if summary.Usage.ModelCalls != 5 || summary.Usage.TotalTokens != 60 {
		t.Fatalf("summary usage = %+v", summary.Usage)
	}
	if summary.FailureTypes["guard_rejected"] != 1 {
		t.Fatalf("summary failure types = %+v", summary.FailureTypes)
	}
	if summary.AverageDurationMillis <= 0 {
		t.Fatalf("average duration = %v", summary.AverageDurationMillis)
	}
}

func TestEvaluateTextToSQLConversationAllowsUniformDirtySmokeButMarksNonFormal(t *testing.T) {
	definitions := []mesagent.TextToSQLEvaluationCase{{
		DatasetVersion: "text-to-sql-v1", CaseID: "case-1",
		UserQuery:       "查询工单 TKT-999 的实时状态",
		ExpectedColumns: []string{"Status"}, ExpectedRows: [][]any{{"处理中"}},
		ResultOrder: mesagent.SQLResultOrdered,
	}}
	observation := conversationObservationForTest(t)
	observation.ImplementationRevision = "unknown"
	observation.ImplementationDirty = true
	summary, err := EvaluateTextToSQLConversation(definitions, []TextToSQLConversationEvaluationObservation{observation})
	if err != nil {
		t.Fatalf("uniform dirty smoke summary: %v", err)
	}
	if summary.Formal || !summary.ImplementationDirty || summary.ImplementationRevision != "unknown" {
		t.Fatalf("dirty smoke summary identity = %+v", summary)
	}
}

func TestTextToSQLConversationToolTraceAllowsRepeatedSchemaSearchesBeforeOneQuery(t *testing.T) {
	calls := []TextToSQLConversationToolCall{
		{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: true},
		{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: true},
		{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: true},
		{ToolName: mesagent.ToolExecuteReadonlyQuery, QueryHash: "sha256:" + strings.Repeat("f", 64), Succeeded: true},
	}
	complete, sequenceCorrect := TextToSQLConversationToolTraceMatchesRequiredSequence(4, calls)
	if !complete || !sequenceCorrect {
		t.Fatalf("repeated schema searches should be accepted: complete=%t sequence=%t", complete, sequenceCorrect)
	}
	calls = append(calls, TextToSQLConversationToolCall{ToolName: mesagent.ToolExecuteReadonlyQuery, QueryHash: "sha256:" + strings.Repeat("e", 64), Succeeded: true})
	complete, sequenceCorrect = TextToSQLConversationToolTraceMatchesRequiredSequence(5, calls)
	if !complete || sequenceCorrect {
		t.Fatalf("repeated readonly queries must be rejected: complete=%t sequence=%t", complete, sequenceCorrect)
	}
}

func TestEvaluateTextToSQLConversationRejectsHistoricalDirectV1Mixing(t *testing.T) {
	definitions := []mesagent.TextToSQLEvaluationCase{{
		DatasetVersion: "text-to-sql-v1", CaseID: "case-1",
		UserQuery:       "查询工单 TKT-999 的实时状态",
		ExpectedColumns: []string{"Status"}, ExpectedRows: [][]any{{"处理中"}},
		ResultOrder: mesagent.SQLResultOrdered,
	}}
	observation := conversationObservationForTest(t)
	observation.ObservationSchemaVersion = ""
	_, err := EvaluateTextToSQLConversation(definitions, []TextToSQLConversationEvaluationObservation{observation})
	if err == nil || !strings.Contains(err.Error(), "must not be mixed into conversation v2 summaries") {
		t.Fatalf("historical direct v1 observation must be refused by the reducer, got %v", err)
	}
}

func TestEvaluateTextToSQLConversationRejectsInconsistentCorrectness(t *testing.T) {
	definitions := []mesagent.TextToSQLEvaluationCase{{
		DatasetVersion: "text-to-sql-v1", CaseID: "case-1",
		UserQuery:       "查询工单 TKT-999 的实时状态",
		ExpectedColumns: []string{"Status"}, ExpectedRows: [][]any{{"处理中"}},
		ResultOrder: mesagent.SQLResultOrdered,
	}}
	observation := conversationObservationForTest(t)
	observation.Columns = []string{"Status"}
	observation.Rows = [][]any{{"已解决"}}
	observation.Correct = true
	_, err := EvaluateTextToSQLConversation(definitions, []TextToSQLConversationEvaluationObservation{observation})
	if err == nil || !strings.Contains(err.Error(), "inconsistent correctness") {
		t.Fatalf("inconsistent correctness must be rejected, got %v", err)
	}
}

func TestEvaluateTextToSQLConversationRejectsMixedFormalIdentity(t *testing.T) {
	definitions := []mesagent.TextToSQLEvaluationCase{
		{
			DatasetVersion: "text-to-sql-v1", CaseID: "case-1", UserQuery: "查询一号工单状态",
			ExpectedColumns: []string{"Status"}, ExpectedRows: [][]any{{"处理中"}}, ResultOrder: mesagent.SQLResultOrdered,
		},
		{
			DatasetVersion: "text-to-sql-v1", CaseID: "case-2", UserQuery: "查询二号工单状态",
			ExpectedColumns: []string{"Status"}, ExpectedRows: [][]any{{"处理中"}}, ResultOrder: mesagent.SQLResultOrdered,
		},
	}
	mutations := map[string]func(*TextToSQLConversationEvaluationObservation){
		"dirty": func(current *TextToSQLConversationEvaluationObservation) {
			current.ImplementationDirty = true
		},
		"revision": func(current *TextToSQLConversationEvaluationObservation) {
			current.ImplementationRevision = "git:other-revision"
		},
		"model profile": func(current *TextToSQLConversationEvaluationObservation) {
			current.ModelProfileFingerprint = strings.Repeat("d", 64)
		},
		"tool schema": func(current *TextToSQLConversationEvaluationObservation) {
			current.ToolSchemaFingerprint = strings.Repeat("e", 64)
		},
		"prompt": func(current *TextToSQLConversationEvaluationObservation) {
			current.PromptVersion = "conversation-v2"
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			first := conversationObservationForTest(t)
			second := conversationObservationForTest(t)
			second.CaseID = "case-2"
			second.RunID = "case-2-run"
			mutate(&second)
			_, err := EvaluateTextToSQLConversation(
				definitions,
				[]TextToSQLConversationEvaluationObservation{first, second},
			)
			if err == nil {
				t.Fatalf("formal reducer accepted %s identity drift", name)
			}
		})
	}
}
