package agent

import (
	"fmt"
	"testing"

	"github.com/chitandabb/GoAgent/internal/conversationmemory"
)

func TestConversationSummaryPromptPayloadBoundsAndPrioritizesActiveMemory(t *testing.T) {
	payload := conversationmemory.Payload{
		ConversationGoal: &conversationmemory.Entry{EntryID: "goal", Content: "current goal", SourceMessageSeqs: []int64{1}, Status: conversationmemory.EntryStatusActive},
		Facts:            []conversationmemory.Entry{{EntryID: "old_fact", Content: "old", SourceMessageSeqs: []int64{2}, Status: conversationmemory.EntryStatusSuperseded}},
		Decisions:        []conversationmemory.Entry{}, Corrections: []conversationmemory.Entry{},
		EvidenceReferences: []conversationmemory.ReferenceEntry{}, OpenQuestions: []conversationmemory.Entry{},
		Todos:          []conversationmemory.Entry{{EntryID: "done", Content: "done", SourceMessageSeqs: []int64{3}, Status: conversationmemory.EntryStatusCompleted}},
		TaskReferences: []conversationmemory.ReferenceEntry{}, ReportReferences: []conversationmemory.ReferenceEntry{},
	}
	for index := 0; index < 30; index++ {
		payload.Facts = append(payload.Facts, conversationmemory.Entry{
			EntryID: fmt.Sprintf("fact_%02d", index), Content: fmt.Sprintf("fact %d", index),
			SourceMessageSeqs: []int64{int64(index + 4)}, Status: conversationmemory.EntryStatusActive,
		})
	}

	projected := conversationSummaryPromptPayload(payload, 24)
	if projected.ConversationGoal == nil || len(projected.Facts) != 23 || len(projected.Todos) != 0 {
		t.Fatalf("projected payload = %+v", projected)
	}
	if projected.Facts[0].EntryID != "fact_29" || projected.Facts[len(projected.Facts)-1].EntryID != "fact_07" {
		t.Fatalf("projected facts are not newest-first: first=%q last=%q", projected.Facts[0].EntryID, projected.Facts[len(projected.Facts)-1].EntryID)
	}
	if len(payload.Facts) != 31 || payload.Facts[0].EntryID != "old_fact" || len(payload.Todos) != 1 {
		t.Fatalf("source payload was mutated: %+v", payload)
	}
}
