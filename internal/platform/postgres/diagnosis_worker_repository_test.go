package postgres

import (
	"encoding/json"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/diagnosis"
)

func TestMarshalReportSummariesPersistsDiagnosisContextObservation(t *testing.T) {
	result := agent.OrchestrationResult{
		Report: agent.StructuredReport{
			Conclusion: "conclusion", BusinessSummary: "business", TechnicalSummary: "technical",
			Confidence: agent.ConfidenceMedium,
		},
		ContextObservation: agent.DiagnosisContextObservation{
			PreflightCalls: 3, HighWaterTokens: 1400, AvailableInputTokens: 124928,
			HighWaterRatio: 0.0112, ToolResultTruncatedCount: 2,
			LastEstimatedUpperBoundTokens: 1300, ReportOutputReserveTokens: 4096,
			ToolGrowthReserveTokens: 8192, EstimationMethod: "local_calibrated",
		},
	}
	_, technical, err := marshalReportSummaries(result)
	if err != nil {
		t.Fatalf("marshalReportSummaries: %v", err)
	}
	var payload struct {
		ContextObservation diagnosis.ReportContextObservation `json:"contextObservation"`
	}
	if err := json.Unmarshal(technical, &payload); err != nil {
		t.Fatalf("decode technical summary: %v", err)
	}
	if payload.ContextObservation.PreflightCalls != 3 ||
		payload.ContextObservation.HighWaterTokens != 1400 ||
		payload.ContextObservation.ToolResultTruncatedCount != 2 ||
		payload.ContextObservation.ReportOutputReserveTokens != 4096 {
		t.Fatalf("context observation = %+v", payload.ContextObservation)
	}
}
