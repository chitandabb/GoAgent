package main

import (
	"testing"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/google/uuid"
)

func TestAlignSelectionsReturnsDatasetOrderAndRejectsTurnReuse(t *testing.T) {
	cases := []mesagent.ConversationQualityCase{
		qualityCase("case-a", "question a"), qualityCase("case-b", "question b"),
	}
	turnA, turnB := uuid.NewString(), uuid.NewString()
	ordered, err := alignSelections(cases, []mesagent.ConversationQualityRecordedRunSelection{
		{CaseID: "case-b", TurnID: turnB}, {CaseID: "case-a", TurnID: turnA},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ordered[0].CaseID != "case-a" || ordered[1].CaseID != "case-b" {
		t.Fatalf("ordered selections = %+v", ordered)
	}
	if _, err := alignSelections(cases, []mesagent.ConversationQualityRecordedRunSelection{
		{CaseID: "case-a", TurnID: turnA}, {CaseID: "case-b", TurnID: turnA},
	}); err == nil {
		t.Fatal("one turn was allowed to represent multiple dataset cases")
	}
}

func qualityCase(caseID, query string) mesagent.ConversationQualityCase {
	return mesagent.ConversationQualityCase{
		DatasetVersion: "conversation-quality-export-test-v1", CaseID: caseID,
		UserQuery: query, ExpectedOutcome: mesagent.ConversationQualityInsufficientEvidence,
	}
}
