package sqlserver

import "testing"

func TestEvaluateQueryGuardReportsBlockingAndSafeAcceptanceSeparately(t *testing.T) {
	guard, err := NewReadonlyQueryGuard([]string{"dbo"}, 4096)
	if err != nil {
		t.Fatalf("NewReadonlyQueryGuard(): %v", err)
	}
	cases := []QueryGuardEvaluationCase{
		{
			DatasetVersion: "sql-safety-v1", CaseID: "write-update", Category: "dml",
			RiskClass: QueryGuardHighRiskWrite, Query: "UPDATE dbo.Tickets SET Status = 'Closed'",
			ExpectedReason: QueryRejectedStatement,
		},
		{
			DatasetVersion: "sql-safety-v1", CaseID: "read-status", Category: "select",
			RiskClass: QueryGuardSafeRead, Query: "SELECT TicketID FROM dbo.v_MESGuardExternalCases",
		},
	}
	observations, summary, err := EvaluateQueryGuard(guard, cases)
	if err != nil {
		t.Fatalf("EvaluateQueryGuard(): %v", err)
	}
	if len(observations) != 2 || summary.Correct != 2 || summary.Accuracy != 1 ||
		summary.HighRiskBlockingRate != 1 || summary.SafeReadAcceptanceRate != 1 ||
		summary.ReasonMatchRate != 1 {
		t.Fatalf("summary = %+v observations = %+v", summary, observations)
	}
}

func TestEvaluateQueryGuardRejectsDuplicateCases(t *testing.T) {
	guard, err := NewReadonlyQueryGuard([]string{"dbo"}, 4096)
	if err != nil {
		t.Fatalf("NewReadonlyQueryGuard(): %v", err)
	}
	current := QueryGuardEvaluationCase{
		DatasetVersion: "sql-safety-v1", CaseID: "duplicate", Category: "dml",
		RiskClass: QueryGuardHighRiskWrite, Query: "DELETE FROM dbo.Tickets",
	}
	if _, _, err = EvaluateQueryGuard(guard, []QueryGuardEvaluationCase{current, current}); err == nil {
		t.Fatal("EvaluateQueryGuard() accepted duplicate case IDs")
	}
}
