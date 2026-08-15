package texttosql

import (
	"strings"
	"testing"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
)

func conversationObservationForTest(t *testing.T) TextToSQLConversationEvaluationObservation {
	t.Helper()
	schemaResultCount := 2
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
			{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: true,
				SchemaKeywordHash: "sha256:" + strings.Repeat("1", 64), SchemaResultCount: &schemaResultCount},
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

func schemaResultCountPointer(count int) *int {
	return &count
}

func TestTextToSQLConversationObservationRejectsDirectV1Contract(t *testing.T) {
	observation := conversationObservationForTest(t)
	observation.ObservationSchemaVersion = ""
	if err := observation.Validate(); err == nil ||
		!strings.Contains(err.Error(), "must not be mixed into conversation v3 summaries") {
		t.Fatalf("unversioned direct v1 observation must be rejected with the mixing message, got %v", err)
	}
	observation = conversationObservationForTest(t)
	observation.ObservationSchemaVersion = "text-to-sql-v1"
	if err := observation.Validate(); err == nil {
		t.Fatal("a foreign v1 schema version must be rejected")
	}
	observation = conversationObservationForTest(t)
	observation.ObservationSchemaVersion = "text-to-sql-conversation-observation-v2"
	if err := observation.Validate(); err == nil ||
		!strings.Contains(err.Error(), TextToSQLConversationObservationSchemaVersion) {
		t.Fatalf("historical v2 observation must be rejected by the v3 validator, got %v", err)
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
		{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: true,
			SchemaKeywordHash: "sha256:" + strings.Repeat("1", 64), SchemaResultCount: schemaResultCountPointer(2)},
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
	if err == nil || !strings.Contains(err.Error(), "must not be mixed into conversation v3 summaries") {
		t.Fatalf("historical direct v1 observation must be refused by the reducer, got %v", err)
	}
	observation = conversationObservationForTest(t)
	observation.ObservationSchemaVersion = "text-to-sql-conversation-observation-v2"
	_, err = EvaluateTextToSQLConversation(definitions, []TextToSQLConversationEvaluationObservation{observation})
	if err == nil || !strings.Contains(err.Error(), TextToSQLConversationObservationSchemaVersion) {
		t.Fatalf("historical v2 observation must be refused by the v3 reducer, got %v", err)
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

// ---------------------------------------------------------------------------
// 红测试：Observation v3 Schema Search 诊断字段

const testSchemaKeywordHash = "sha256:" + "1111111111111111111111111111111111111111111111111111111111111111"

func schemaObservationWithCalls(t *testing.T, calls []TextToSQLConversationToolCall) TextToSQLConversationEvaluationObservation {
	t.Helper()
	observation := conversationObservationForTest(t)
	observation.ActualToolCalls = calls
	observation.ActualToolCallCount = len(calls)
	return observation
}

func TestTextToSQLConversationV3ValidatorAcceptsSingleSchemaSearch(t *testing.T) {
	observation := schemaObservationWithCalls(t, []TextToSQLConversationToolCall{
		{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: true,
			SchemaKeywordHash: testSchemaKeywordHash, SchemaResultCount: schemaResultCountPointer(3)},
		{ToolName: mesagent.ToolExecuteReadonlyQuery, QueryHash: "sha256:" + strings.Repeat("c", 64), Succeeded: true},
	})
	if err := observation.Validate(); err != nil {
		t.Fatalf("v3 observation with one schema search must validate: %v", err)
	}
	call := observation.ActualToolCalls[0]
	if call.SchemaKeywordHash != testSchemaKeywordHash || call.SchemaResultCount == nil ||
		*call.SchemaResultCount != 3 || call.SchemaKeywordRepeated {
		t.Fatalf("schema diagnostic fields = %+v", call)
	}
}

func TestTextToSQLConversationV3ValidatorRejectsV2Contract(t *testing.T) {
	observation := conversationObservationForTest(t)
	observation.ObservationSchemaVersion = "text-to-sql-conversation-observation-v2"
	if err := observation.Validate(); err == nil ||
		!strings.Contains(err.Error(), TextToSQLConversationObservationSchemaVersion) {
		t.Fatalf("v2 observation must fail closed under the v3 validator, got %v", err)
	}
}

func TestTextToSQLConversationV3ValidatorRepeatedNormalizedKeyword(t *testing.T) {
	count := 1
	observation := schemaObservationWithCalls(t, []TextToSQLConversationToolCall{
		{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: true,
			SchemaKeywordHash: testSchemaKeywordHash, SchemaResultCount: &count},
		{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: true,
			SchemaKeywordHash: testSchemaKeywordHash, SchemaResultCount: &count, SchemaKeywordRepeated: true},
		{ToolName: mesagent.ToolExecuteReadonlyQuery, QueryHash: "sha256:" + strings.Repeat("c", 64), Succeeded: true},
	})
	if err := observation.Validate(); err != nil {
		t.Fatalf("repeated normalized keyword must validate with repeated=true on the second call: %v", err)
	}
}

func TestTextToSQLConversationV3ValidatorDistinctKeywordsNotRepeated(t *testing.T) {
	otherHash := "sha256:" + strings.Repeat("2", 64)
	count := 1
	observation := schemaObservationWithCalls(t, []TextToSQLConversationToolCall{
		{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: true,
			SchemaKeywordHash: testSchemaKeywordHash, SchemaResultCount: &count},
		{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: true,
			SchemaKeywordHash: otherHash, SchemaResultCount: &count},
		{ToolName: mesagent.ToolExecuteReadonlyQuery, QueryHash: "sha256:" + strings.Repeat("c", 64), Succeeded: true},
	})
	if err := observation.Validate(); err != nil {
		t.Fatalf("distinct keywords must validate with repeated=false: %v", err)
	}
	if observation.ActualToolCalls[0].SchemaKeywordHash == observation.ActualToolCalls[1].SchemaKeywordHash {
		t.Fatal("distinct keyword hashes must differ")
	}
	for index, call := range observation.ActualToolCalls[:2] {
		if call.SchemaKeywordRepeated {
			t.Fatalf("call %d with a distinct keyword must not be repeated", index)
		}
	}
}

func TestTextToSQLConversationV3ValidatorZeroResultsDistinctFromMissing(t *testing.T) {
	zero := 0
	observation := schemaObservationWithCalls(t, []TextToSQLConversationToolCall{
		{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: true,
			SchemaKeywordHash: testSchemaKeywordHash, SchemaResultCount: &zero},
		{ToolName: mesagent.ToolExecuteReadonlyQuery, QueryHash: "sha256:" + strings.Repeat("c", 64), Succeeded: true},
	})
	if err := observation.Validate(); err != nil {
		t.Fatalf("zero-result schema search must validate with a non-nil count of 0: %v", err)
	}
	if observation.ActualToolCalls[0].SchemaResultCount == nil || *observation.ActualToolCalls[0].SchemaResultCount != 0 {
		t.Fatalf("zero results must be recorded as non-nil 0, got %v", observation.ActualToolCalls[0].SchemaResultCount)
	}
}

func TestTextToSQLConversationV3ValidatorRepeatedFieldMustMatchPriorUsage(t *testing.T) {
	count := 1
	otherHash := "sha256:" + strings.Repeat("2", 64)
	mutations := []struct {
		name   string
		first  TextToSQLConversationToolCall
		second TextToSQLConversationToolCall
	}{
		{
			name: "first call marked repeated",
			first: TextToSQLConversationToolCall{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: true,
				SchemaKeywordHash: testSchemaKeywordHash, SchemaResultCount: &count, SchemaKeywordRepeated: true},
			second: TextToSQLConversationToolCall{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: true,
				SchemaKeywordHash: testSchemaKeywordHash, SchemaResultCount: &count, SchemaKeywordRepeated: true},
		},
		{
			name: "second repeated call not marked",
			first: TextToSQLConversationToolCall{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: true,
				SchemaKeywordHash: testSchemaKeywordHash, SchemaResultCount: &count},
			second: TextToSQLConversationToolCall{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: true,
				SchemaKeywordHash: testSchemaKeywordHash, SchemaResultCount: &count},
		},
		{
			name: "repeated with a fresh hash",
			first: TextToSQLConversationToolCall{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: true,
				SchemaKeywordHash: otherHash, SchemaResultCount: &count, SchemaKeywordRepeated: true},
			second: TextToSQLConversationToolCall{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: true,
				SchemaKeywordHash: testSchemaKeywordHash, SchemaResultCount: &count, SchemaKeywordRepeated: true},
		},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			observation := schemaObservationWithCalls(t, []TextToSQLConversationToolCall{
				mutation.first,
				mutation.second,
				{ToolName: mesagent.ToolExecuteReadonlyQuery, QueryHash: "sha256:" + strings.Repeat("c", 64), Succeeded: true},
			})
			if err := observation.Validate(); err == nil {
				t.Fatalf("schemaKeywordRepeated inconsistency must be rejected: %s", mutation.name)
			}
		})
	}
}

func TestTextToSQLConversationV3ValidatorRejectsSchemaFieldsOnOtherTools(t *testing.T) {
	count := 1
	observations := map[string]TextToSQLConversationEvaluationObservation{
		"hash on readonly query": schemaObservationWithCalls(t, []TextToSQLConversationToolCall{
			{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: true,
				SchemaKeywordHash: testSchemaKeywordHash, SchemaResultCount: &count},
			{ToolName: mesagent.ToolExecuteReadonlyQuery, QueryHash: "sha256:" + strings.Repeat("c", 64),
				Succeeded: true, SchemaKeywordHash: testSchemaKeywordHash},
		}),
		"result count on readonly query": schemaObservationWithCalls(t, []TextToSQLConversationToolCall{
			{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: true,
				SchemaKeywordHash: testSchemaKeywordHash, SchemaResultCount: &count},
			{ToolName: mesagent.ToolExecuteReadonlyQuery, QueryHash: "sha256:" + strings.Repeat("c", 64),
				Succeeded: true, SchemaResultCount: &count},
		}),
		"repeated on readonly query": schemaObservationWithCalls(t, []TextToSQLConversationToolCall{
			{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: true,
				SchemaKeywordHash: testSchemaKeywordHash, SchemaResultCount: &count},
			{ToolName: mesagent.ToolExecuteReadonlyQuery, QueryHash: "sha256:" + strings.Repeat("c", 64),
				Succeeded: true, SchemaKeywordRepeated: true},
		}),
	}
	for name, observation := range observations {
		t.Run(name, func(t *testing.T) {
			if err := observation.Validate(); err == nil {
				t.Fatalf("schema diagnostic fields on a non-schema Tool must be rejected: %s", name)
			}
		})
	}
}

func TestTextToSQLConversationV3ValidatorRejectsMissingOrInvalidSchemaDiagnostics(t *testing.T) {
	count := 1
	tooMany := 21
	negative := -1
	observations := map[string]TextToSQLConversationEvaluationObservation{
		"missing hash": schemaObservationWithCalls(t, []TextToSQLConversationToolCall{
			{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: true, SchemaResultCount: &count},
			{ToolName: mesagent.ToolExecuteReadonlyQuery, QueryHash: "sha256:" + strings.Repeat("c", 64), Succeeded: true},
		}),
		"missing result count": schemaObservationWithCalls(t, []TextToSQLConversationToolCall{
			{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: true, SchemaKeywordHash: testSchemaKeywordHash},
			{ToolName: mesagent.ToolExecuteReadonlyQuery, QueryHash: "sha256:" + strings.Repeat("c", 64), Succeeded: true},
		}),
		"malformed hash": schemaObservationWithCalls(t, []TextToSQLConversationToolCall{
			{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: true,
				SchemaKeywordHash: "not-a-hash", SchemaResultCount: &count},
			{ToolName: mesagent.ToolExecuteReadonlyQuery, QueryHash: "sha256:" + strings.Repeat("c", 64), Succeeded: true},
		}),
		"result count above contract ceiling": schemaObservationWithCalls(t, []TextToSQLConversationToolCall{
			{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: true,
				SchemaKeywordHash: testSchemaKeywordHash, SchemaResultCount: &tooMany},
			{ToolName: mesagent.ToolExecuteReadonlyQuery, QueryHash: "sha256:" + strings.Repeat("c", 64), Succeeded: true},
		}),
		"negative result count": schemaObservationWithCalls(t, []TextToSQLConversationToolCall{
			{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: true,
				SchemaKeywordHash: testSchemaKeywordHash, SchemaResultCount: &negative},
			{ToolName: mesagent.ToolExecuteReadonlyQuery, QueryHash: "sha256:" + strings.Repeat("c", 64), Succeeded: true},
		}),
		"failed call fabricates result count": schemaObservationWithCalls(t, []TextToSQLConversationToolCall{
			{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: false, ErrorType: "execution_error",
				SchemaKeywordHash: testSchemaKeywordHash, SchemaResultCount: &count},
			{ToolName: mesagent.ToolExecuteReadonlyQuery, QueryHash: "sha256:" + strings.Repeat("c", 64), Succeeded: true},
		}),
		"failed call without hash marked repeated": schemaObservationWithCalls(t, []TextToSQLConversationToolCall{
			{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: false, ErrorType: "execution_error",
				SchemaKeywordRepeated: true},
			{ToolName: mesagent.ToolExecuteReadonlyQuery, QueryHash: "sha256:" + strings.Repeat("c", 64), Succeeded: true},
		}),
	}
	for name, observation := range observations {
		t.Run(name, func(t *testing.T) {
			if err := observation.Validate(); err == nil {
				t.Fatalf("invalid schema diagnostics must be rejected: %s", name)
			}
		})
	}
}

func TestTextToSQLConversationV3ValidatorFailedSearchKeepsHashButNoCount(t *testing.T) {
	observation := schemaObservationWithCalls(t, []TextToSQLConversationToolCall{
		{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: false, ErrorType: "execution_error",
			SchemaKeywordHash: testSchemaKeywordHash},
		{ToolName: mesagent.ToolExecuteReadonlyQuery, QueryHash: "sha256:" + strings.Repeat("c", 64), Succeeded: true},
	})
	if err := observation.Validate(); err != nil {
		t.Fatalf("failed search with a decoded keyword hash and nil count must validate: %v", err)
	}
	call := observation.ActualToolCalls[0]
	if call.SchemaResultCount != nil || call.SchemaKeywordRepeated {
		t.Fatalf("failed search must not fabricate a result count or repetition: %+v", call)
	}
}

func TestEvaluateTextToSQLConversationV3RepeatedResetsPerCase(t *testing.T) {
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
			ExpectedColumns: []string{"Status"}, ExpectedRows: [][]any{{"处理中"}},
			ResultOrder: mesagent.SQLResultOrdered,
		},
	}
	count := 1
	first := conversationObservationForTest(t)
	second := conversationObservationForTest(t)
	second.CaseID = "case-2"
	second.RunID = "case-2-run"
	// 两个 Case 各自第一次使用同一个归一化 keyword：第二个 Case 的第一次
	// 使用必须仍是 repeated=false（重复状态不跨 Case）。
	second.ActualToolCalls = []TextToSQLConversationToolCall{
		{ToolName: mesagent.ToolSearchSchemaCatalog, Succeeded: true,
			SchemaKeywordHash: testSchemaKeywordHash, SchemaResultCount: &count},
		{ToolName: mesagent.ToolExecuteReadonlyQuery, QueryHash: "sha256:" + strings.Repeat("d", 64), Succeeded: true},
	}
	summary, err := EvaluateTextToSQLConversation(definitions, []TextToSQLConversationEvaluationObservation{first, second})
	if err != nil {
		t.Fatalf("per-case repeated state must reset across cases: %v", err)
	}
	if summary.Cases != 2 {
		t.Fatalf("summary cases = %d", summary.Cases)
	}
	if second.ActualToolCalls[0].SchemaKeywordRepeated {
		t.Fatal("the first use in the second case must not be marked repeated")
	}
}
