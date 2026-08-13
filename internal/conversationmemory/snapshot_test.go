package conversationmemory_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/conversationmemory"
)

func TestSnapshotPayloadAcceptsSourceBackedStructuredMemory(t *testing.T) {
	payload := validPayload()
	context := validValidationContext()

	if err := conversationmemory.ValidatePayload(payload, context); err != nil {
		t.Fatalf("ValidatePayload() error = %v", err)
	}
}

func TestSnapshotPayloadRejectsUnsafeMemory(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*conversationmemory.Payload, *conversationmemory.ValidationContext)
		want   error
	}{
		{
			name: "source sequence outside coverage",
			mutate: func(payload *conversationmemory.Payload, _ *conversationmemory.ValidationContext) {
				payload.Facts[0].SourceMessageSeqs = []int64{4}
			},
			want: conversationmemory.ErrSourceOutOfRange,
		},
		{
			name: "user fact sourced only from assistant",
			mutate: func(payload *conversationmemory.Payload, _ *conversationmemory.ValidationContext) {
				payload.Facts[0].SourceMessageSeqs = []int64{2}
			},
			want: conversationmemory.ErrUserSourceRequired,
		},
		{
			name: "entry lineage is not part of current summary",
			mutate: func(payload *conversationmemory.Payload, _ *conversationmemory.ValidationContext) {
				payload.Corrections[0].SupersedesEntryID = "fact_missing"
			},
			want: conversationmemory.ErrUnknownEntryReference,
		},
		{
			name: "superseded ordinary entry is not current state",
			mutate: func(payload *conversationmemory.Payload, _ *conversationmemory.ValidationContext) {
				payload.Facts[0].Status = conversationmemory.EntryStatusSuperseded
			},
			want: conversationmemory.ErrInvalidEntryStatus,
		},
		{
			name: "illegal todo state",
			mutate: func(payload *conversationmemory.Payload, _ *conversationmemory.ValidationContext) {
				payload.Todos[0].Status = "doing"
			},
			want: conversationmemory.ErrInvalidEntryStatus,
		},
		{
			name: "unknown task reference",
			mutate: func(payload *conversationmemory.Payload, _ *conversationmemory.ValidationContext) {
				payload.TaskReferences[0].ReferenceID = "d6a901c9-1472-4c63-a2d6-b7b3b4276299"
			},
			want: conversationmemory.ErrUnknownStableReference,
		},
		{
			name: "task reference is attributed to an unrelated message",
			mutate: func(payload *conversationmemory.Payload, _ *conversationmemory.ValidationContext) {
				payload.TaskReferences[0].SourceMessageSeqs = []int64{2, 3}
			},
			want: conversationmemory.ErrUnknownStableReference,
		},
		{
			name: "evidence type does not match trusted citation",
			mutate: func(payload *conversationmemory.Payload, _ *conversationmemory.ValidationContext) {
				payload.EvidenceReferences[0].ReferenceType = "web"
			},
			want: conversationmemory.ErrUnknownStableReference,
		},
		{
			name: "payload exceeds configured bytes",
			mutate: func(payload *conversationmemory.Payload, context *conversationmemory.ValidationContext) {
				payload.ConversationGoal.Content = strings.Repeat("x", 2048)
				context.MaxPayloadBytes = 512
			},
			want: conversationmemory.ErrPayloadTooLarge,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := validPayload()
			context := validValidationContext()
			tt.mutate(&payload, &context)
			if err := conversationmemory.ValidatePayload(payload, context); !errors.Is(err, tt.want) {
				t.Fatalf("ValidatePayload() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestDecodePayloadRequiresTheFixedSchema(t *testing.T) {
	encoded, err := json.Marshal(validPayload())
	if err != nil {
		t.Fatal(err)
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	delete(object, "decisions")
	object["modelSpeculation"] = json.RawMessage(`[]`)
	encoded, err = json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := conversationmemory.DecodePayload(encoded); !errors.Is(err, conversationmemory.ErrInvalidPayloadSchema) {
		t.Fatalf("DecodePayload() error = %v, want ErrInvalidPayloadSchema", err)
	}
}

func TestDecodePayloadReportsContentFreeSchemaLocation(t *testing.T) {
	_, err := conversationmemory.DecodePayload([]byte(`{
		"conversationGoal":null,"facts":"not-an-array","decisions":[],"corrections":[],
		"evidenceReferences":[],"openQuestions":[],"todos":[],"taskReferences":[],"reportReferences":[]
	}`))
	if !errors.Is(err, conversationmemory.ErrInvalidPayloadSchema) ||
		conversationmemory.PayloadSchemaFailureCode(err) != "field_facts_string" {
		t.Fatalf("DecodePayload() error/code = %v/%q", err, conversationmemory.PayloadSchemaFailureCode(err))
	}
}

func TestDecodePayloadRejectsDuplicateFields(t *testing.T) {
	encoded := []byte(`{"conversationGoal":null,"facts":[{"entryId":"fact_a","entryId":"fact_b","content":"x","sourceMessageSeqs":[1],"status":"active"}],"decisions":[],"corrections":[],"evidenceReferences":[],"openQuestions":[],"todos":[],"taskReferences":[],"reportReferences":[]}`)
	if _, err := conversationmemory.DecodePayload(encoded); !errors.Is(err, conversationmemory.ErrInvalidPayloadSchema) {
		t.Fatalf("DecodePayload() duplicate field error = %v, want ErrInvalidPayloadSchema", err)
	}
}

func TestValidatePayloadReportsContentFreeEntryFailureCode(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*conversationmemory.Payload)
		code   string
	}{
		{name: "entry id", mutate: func(payload *conversationmemory.Payload) {
			payload.ConversationGoal.EntryID = "Invalid ID"
		}, code: "entry_id"},
		{name: "content", mutate: func(payload *conversationmemory.Payload) {
			payload.ConversationGoal.Content = ""
		}, code: "content"},
		{name: "source count", mutate: func(payload *conversationmemory.Payload) {
			payload.ConversationGoal.SourceMessageSeqs = nil
		}, code: "source_count"},
		{name: "source order", mutate: func(payload *conversationmemory.Payload) {
			payload.ConversationGoal.SourceMessageSeqs = []int64{3, 1}
		}, code: "source_order"},
		{name: "source duplicate", mutate: func(payload *conversationmemory.Payload) {
			payload.ConversationGoal.SourceMessageSeqs = []int64{1, 1}
		}, code: "source_duplicate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := validPayload()
			tt.mutate(&payload)
			err := conversationmemory.ValidatePayload(payload, validValidationContext())
			if !errors.Is(err, conversationmemory.ErrInvalidEntry) ||
				conversationmemory.EntryValidationFailureCode(err) != tt.code {
				t.Fatalf("ValidatePayload() error/code = %v/%q, want ErrInvalidEntry/%s",
					err, conversationmemory.EntryValidationFailureCode(err), tt.code)
			}
		})
	}
}

func TestFailureCodeNormalizesDomainValidationFailures(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{err: conversationmemory.ErrUserSourceRequired, want: "user_source_required"},
		{err: &conversationmemory.EntryValidationError{Code: "entry_id"}, want: "entry_entry_id"},
		{err: &conversationmemory.PayloadSchemaError{Code: "top_level_json"}, want: "payload_schema_top_level_json"},
	}
	for _, tt := range tests {
		if got := conversationmemory.FailureCode(tt.err); got != tt.want {
			t.Fatalf("FailureCode(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}

func TestStableReferenceValidationFailureCodeDistinguishesSectionAndReason(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*conversationmemory.Payload)
		want   string
	}{
		{name: "unknown task id", mutate: func(payload *conversationmemory.Payload) {
			payload.TaskReferences[0].ReferenceID = "d6a901c9-1472-4c63-a2d6-b7b3b4276299"
		}, want: "task_reference_id_unknown"},
		{name: "report source mismatch", mutate: func(payload *conversationmemory.Payload) {
			payload.ReportReferences = []conversationmemory.ReferenceEntry{{
				Entry:         conversationmemory.Entry{EntryID: "report_one", Content: "report", SourceMessageSeqs: []int64{2}, Status: conversationmemory.EntryStatusActive},
				ReferenceType: conversationmemory.ReferenceTypeDiagnosisReport, ReferenceID: "report:one",
			}}
		}, want: "report_reference_source_mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := validPayload()
			context := validValidationContext()
			context.KnownReportReferences["report:one"] = conversationmemory.StableReferenceIdentity{SourceMessageSeqs: []int64{3}}
			tt.mutate(&payload)
			err := conversationmemory.ValidatePayload(payload, context)
			if !errors.Is(err, conversationmemory.ErrUnknownStableReference) || conversationmemory.FailureCode(err) != tt.want {
				t.Fatalf("ValidatePayload() error/code = %v/%q, want %q", err, conversationmemory.FailureCode(err), tt.want)
			}
		})
	}
}

func TestIncrementalCurrentSummaryCanDropStaleEntries(t *testing.T) {
	previous := validPayload()
	candidate := validPayload()
	candidate.Facts = []conversationmemory.Entry{}
	context := validValidationContext()
	context.ThroughSeq = 4
	context.MessageRoles = map[int64]conversation.MessageRole{4: conversation.MessageRoleUser}
	context.PreviousPayload = &previous

	if err := conversationmemory.ValidatePayload(candidate, context); err != nil {
		t.Fatalf("ValidatePayload() incremental current-state replacement error = %v", err)
	}
}

func TestSummaryUsageRequiresProviderAccounting(t *testing.T) {
	if err := (conversationmemory.SummaryUsage{}).Validate(); !errors.Is(err, conversationmemory.ErrInvalidSnapshot) {
		t.Fatalf("SummaryUsage.Validate() error = %v, want ErrInvalidSnapshot", err)
	}
}

func TestTodoCurrentSummaryRejectsLineageField(t *testing.T) {
	payload := validPayload()
	payload.Todos = append(payload.Todos,
		conversationmemory.Entry{
			EntryID: "todo_eval_completed", Content: "固定集评测已完成", SourceMessageSeqs: []int64{3},
			Status: conversationmemory.EntryStatusCompleted, SupersedesEntryID: "todo_eval",
		},
		conversationmemory.Entry{
			EntryID: "todo_eval_cancelled", Content: "固定集评测已取消", SourceMessageSeqs: []int64{3},
			Status: conversationmemory.EntryStatusCancelled, SupersedesEntryID: "todo_eval",
		},
	)
	if err := conversationmemory.ValidatePayload(payload, validValidationContext()); !errors.Is(err, conversationmemory.ErrUnknownEntryReference) {
		t.Fatalf("ValidatePayload() Todo lineage error = %v, want ErrUnknownEntryReference", err)
	}
}

func validPayload() conversationmemory.Payload {
	return conversationmemory.Payload{
		ConversationGoal: &conversationmemory.Entry{
			EntryID: "goal_context_governance", Content: "完成动态上下文治理",
			SourceMessageSeqs: []int64{1}, Status: conversationmemory.EntryStatusActive,
		},
		Facts: []conversationmemory.Entry{
			{
				EntryID: "fact_timezone", Content: "服务器时区是 UTC",
				SourceMessageSeqs: []int64{1}, Status: conversationmemory.EntryStatusActive,
			},
		},
		Decisions: []conversationmemory.Entry{},
		Corrections: []conversationmemory.Entry{
			{
				EntryID: "correction_timezone", Content: "服务器时区是 Asia/Shanghai",
				SourceMessageSeqs: []int64{3}, Status: conversationmemory.EntryStatusActive,
			},
		},
		EvidenceReferences: []conversationmemory.ReferenceEntry{
			{
				Entry: conversationmemory.Entry{
					EntryID: "evidence_policy", Content: "知识库版本策略",
					SourceMessageSeqs: []int64{2}, Status: conversationmemory.EntryStatusActive,
				},
				ReferenceType: "knowledge_chunk",
				ReferenceID:   "knowledge:8c4c15e7-1d72-453d-b1a0-c64f70a03dc8/2cd3198e-0bff-4ab2-bc4c-e6838043039f",
				ContentSHA256: strings.Repeat("a", 64),
			},
		},
		OpenQuestions: []conversationmemory.Entry{},
		Todos: []conversationmemory.Entry{
			{
				EntryID: "todo_eval", Content: "运行固定集评测",
				SourceMessageSeqs: []int64{3}, Status: conversationmemory.EntryStatusOpen,
			},
		},
		TaskReferences: []conversationmemory.ReferenceEntry{
			{
				Entry: conversationmemory.Entry{
					EntryID: "task_diagnosis", Content: "等待诊断任务完成",
					SourceMessageSeqs: []int64{3}, Status: conversationmemory.EntryStatusActive,
				},
				ReferenceType: "diagnosis_task",
				ReferenceID:   "f954b23d-28c3-4dd4-a94b-8e859d3c6dcc",
			},
		},
		ReportReferences: []conversationmemory.ReferenceEntry{},
	}
}

func validValidationContext() conversationmemory.ValidationContext {
	return conversationmemory.ValidationContext{
		FromSeq: 1, ThroughSeq: 3, MaxPayloadBytes: 64 * 1024,
		MessageRoles: map[int64]conversation.MessageRole{
			1: conversation.MessageRoleUser,
			2: conversation.MessageRoleAssistant,
			3: conversation.MessageRoleUser,
		},
		KnownEvidenceReferences: map[string]conversationmemory.EvidenceReferenceIdentity{
			"knowledge:8c4c15e7-1d72-453d-b1a0-c64f70a03dc8/2cd3198e-0bff-4ab2-bc4c-e6838043039f": {
				ReferenceType: "knowledge_chunk", ContentSHA256: strings.Repeat("a", 64), SourceMessageSeqs: []int64{2},
			},
		},
		KnownTaskReferences: map[string]conversationmemory.StableReferenceIdentity{
			"f954b23d-28c3-4dd4-a94b-8e859d3c6dcc": {SourceMessageSeqs: []int64{3}},
		},
		KnownReportReferences: map[string]conversationmemory.StableReferenceIdentity{},
	}
}
