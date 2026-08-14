package main

import (
	"math"
	"testing"

	"github.com/chitandabb/GoAgent/internal/semanticcache"
)

func TestBuildDraftDatasetIsStratifiedAndPendingHumanReview(t *testing.T) {
	dataset := buildDraftDataset()
	if err := dataset.Validate(false); err != nil {
		t.Fatalf("draft dataset: %v", err)
	}
	for _, pair := range dataset.Pairs {
		if pair.Reviewed {
			t.Fatalf("pair %s must remain pending human review", pair.ID)
		}
		if pair.ProposedReusable {
			comparison := semanticcache.CompareQuestions(pair.AnchorQuestion, pair.CandidateQuestion)
			if !comparison.Compatible || !semanticcache.EligibleForLookup(semanticcache.Question{Text: pair.CandidateQuestion}) {
				t.Errorf("proposed reusable pair %s is blocked by deterministic gate: %+v", pair.ID, comparison)
			}
		}
	}
}

func TestHoldoutPrecisionGateDisablesRelease(t *testing.T) {
	report := calibrationReport{Selection: semanticcache.ThresholdSelection{Enabled: true, PrecisionGate: 0.98}}
	applyHoldoutAcceptance(&report, semanticcache.CacheMetrics{Hits: 10, Precision: 0.9})
	if report.ReleaseEnabled || report.Selection.Enabled || report.RejectionReason != "holdout_precision_gate_failed" {
		t.Fatalf("report = %+v", report)
	}
}

func TestCosineSimilarityUsesRecordedVectors(t *testing.T) {
	if got := cosineSimilarity([]float32{1, 0}, []float32{0.6, 0.8}); math.Abs(got-0.6) > 0.000001 {
		t.Fatalf("cosineSimilarity() = %f, want 0.6", got)
	}
}
