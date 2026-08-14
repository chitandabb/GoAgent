package main

import (
	"context"
	"math"
	"testing"

	"github.com/chitandabb/GoAgent/internal/semanticcache"
)

func TestEvaluateFullIndexQualityCountsReturnedCandidateIdentity(t *testing.T) {
	dataset := semanticcache.EvaluationDataset{
		Version: "fixture-v1",
		Pairs: []semanticcache.EvaluationPair{
			{ID: "reuse", CandidateQuestion: "设备点检周期如何规定？", Reusable: true, Split: semanticcache.EvaluationSplitCalibration},
			{ID: "negative", CandidateQuestion: "设备点检周期是 60 天吗？", Reusable: false, Split: semanticcache.EvaluationSplitHoldout},
		},
	}
	lookups := map[string]fullIndexLookupResult{
		"设备点检周期如何规定？":    {PairID: "reuse", Similarity: 0.95, Hit: true},
		"设备点检周期是 60 天吗？": {PairID: "reuse", Similarity: 0.94, Hit: true},
	}
	report, err := evaluateFullIndexQuality(context.Background(), dataset, func(
		_ context.Context,
		pair semanticcache.EvaluationPair,
	) (fullIndexLookupResult, error) {
		return lookups[pair.CandidateQuestion], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.StrictPairIdentityCalibration.TruePositives != 1 || report.StrictPairIdentityCalibration.Precision != 1 {
		t.Fatalf("calibration = %+v", report.StrictPairIdentityCalibration)
	}
	if report.StrictPairIdentityHoldout.FalsePositives != 1 || report.StrictPairIdentityHoldout.Precision != 0 || report.CrossCandidateHits != 1 {
		t.Fatalf("holdout=%+v cross=%d", report.StrictPairIdentityHoldout, report.CrossCandidateHits)
	}
}

func TestBuildDraftDatasetIsStratifiedAndPendingHumanReview(t *testing.T) {
	dataset := buildDraftDataset()
	if err := dataset.Validate(false); err != nil {
		t.Fatalf("draft dataset: %v", err)
	}
	compatibleReusable := 0
	for _, pair := range dataset.Pairs {
		if pair.Reviewed {
			t.Fatalf("pair %s must remain pending human review", pair.ID)
		}
		if pair.ProposedReusable {
			comparison := semanticcache.CompareQuestions(pair.AnchorQuestion, pair.CandidateQuestion)
			if comparison.Compatible && semanticcache.EligibleForLookup(semanticcache.Question{Text: pair.CandidateQuestion}) {
				compatibleReusable++
			}
		}
	}
	if compatibleReusable < 30 {
		t.Fatalf("deterministic gate leaves only %d proposed reusable pairs", compatibleReusable)
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
