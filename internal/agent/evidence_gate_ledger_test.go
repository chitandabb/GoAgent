package agent

import (
	"testing"

	"github.com/chitandabb/GoAgent/internal/evaluationledger"
)

func TestBuildEvidenceGateEarlyExitLedgerPreservesQualityGateAndIssues(t *testing.T) {
	asset := evaluationledger.Asset{
		ID: "evidence-gate-early-exit-v1", Domain: "evidence_gate_early_exit",
		ObservationKind: "evidence_gate_early_exit", Status: evaluationledger.AssetRecomputed,
		Reason:     "Deterministic fixture replay for the Early Exit control.",
		EntryPoint: "mesguard-evaluation-ledger", DatasetArtifact: "dataset.jsonl",
		ObservationArtifact: "observations.jsonl", ReportArtifact: "ledger.json",
	}
	metadata := evaluationledger.SourceMetadata{
		ModelProfile: "fixture", ConfigFingerprint: "sha256:config",
		ImplementationRevision: "git:revision", DatasetSHA256: "sha256:dataset",
		ObservationSHA256: "sha256:observations",
	}

	report, err := BuildEvidenceGateEarlyExitLedger(
		asset, metadata, evidenceGateCasesForTest(), evidenceGateObservationsForTest(),
	)
	if err != nil {
		t.Fatalf("BuildEvidenceGateEarlyExitLedger: %v", err)
	}
	if report.Summary.Runs != 6 || report.Summary.UsageAvailableRuns != 6 ||
		report.Records[0].Operation != "evidence_gate_early_exit" {
		t.Fatalf("ledger = %+v", report)
	}
	if len(report.Records[4].DegradationReasons) != 1 {
		t.Fatalf("degradation was not preserved: %+v", report.Records[4])
	}
	var summary EvidenceGateEarlyExitSummary
	if err := report.DecodeDomainSummary(&summary); err != nil {
		t.Fatal(err)
	}
	if !summary.PerformanceClaimsAllowed || summary.PairedCases != 3 {
		t.Fatalf("domain summary = %+v", summary)
	}
}
