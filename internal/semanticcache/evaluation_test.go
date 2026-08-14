package semanticcache_test

import (
	"testing"

	"github.com/chitandabb/GoAgent/internal/semanticcache"
)

func TestSelectSemanticThresholdUsesCalibrationOnlyAndPrioritizesPrecision(t *testing.T) {
	t.Parallel()

	pairs := []semanticcache.EvaluationPair{
		{ID: "positive-high", Split: semanticcache.EvaluationSplitCalibration, Reviewed: true, Reusable: true, AnchorQuestion: "文档如何发布？", CandidateQuestion: "怎样发布文档？"},
		{ID: "positive-low", Split: semanticcache.EvaluationSplitCalibration, Reviewed: true, Reusable: true, AnchorQuestion: "缓存如何失效？", CandidateQuestion: "怎样让缓存失效？"},
		{ID: "negative-close", Split: semanticcache.EvaluationSplitCalibration, Reviewed: true, Reusable: false, AnchorQuestion: "文档发布流程是什么？", CandidateQuestion: "文档上传流程是什么？"},
		{ID: "holdout-negative", Split: semanticcache.EvaluationSplitHoldout, Reviewed: true, Reusable: false, AnchorQuestion: "制度是什么？", CandidateQuestion: "当前制度是什么？"},
	}
	observations := []semanticcache.SimilarityObservation{
		{PairID: "positive-high", Similarity: 0.97, Compatible: true},
		{PairID: "positive-low", Similarity: 0.93, Compatible: true},
		{PairID: "negative-close", Similarity: 0.95, Compatible: true},
		{PairID: "holdout-negative", Similarity: 0.99, Compatible: true},
	}
	selection, err := semanticcache.SelectThreshold(pairs, observations, 0.98)
	if err != nil {
		t.Fatalf("SelectThreshold() error = %v", err)
	}
	if !selection.Enabled || selection.Threshold != 0.96 || selection.Calibration.TruePositives != 1 ||
		selection.Calibration.FalsePositives != 0 || selection.Calibration.FalseNegatives != 1 ||
		selection.Calibration.Precision != 1 || selection.Calibration.Recall != 0.5 {
		t.Fatalf("selection = %+v", selection)
	}
}

func TestSelectSemanticThresholdStaysDisabledWithoutPrecisionGate(t *testing.T) {
	t.Parallel()

	pairs := []semanticcache.EvaluationPair{
		{ID: "positive", Split: semanticcache.EvaluationSplitCalibration, Reviewed: true, Reusable: true, AnchorQuestion: "发布流程是什么？", CandidateQuestion: "发布步骤是什么？"},
		{ID: "negative", Split: semanticcache.EvaluationSplitCalibration, Reviewed: true, Reusable: false, AnchorQuestion: "发布流程是什么？", CandidateQuestion: "上传流程是什么？"},
	}
	observations := []semanticcache.SimilarityObservation{
		{PairID: "positive", Similarity: 0.9, Compatible: true},
		{PairID: "negative", Similarity: 0.95, Compatible: true},
	}
	selection, err := semanticcache.SelectThreshold(pairs, observations, 0.98)
	if err != nil {
		t.Fatalf("SelectThreshold() error = %v", err)
	}
	if selection.Enabled {
		t.Fatalf("selection unexpectedly enabled: %+v", selection)
	}
}

func TestSelectSemanticThresholdRecomputesCurrentDeterministicGate(t *testing.T) {
	t.Parallel()

	pairs := []semanticcache.EvaluationPair{
		{ID: "positive", Split: semanticcache.EvaluationSplitCalibration, Reviewed: true, Reusable: true,
			AnchorQuestion: "知识制度包含什么？", CandidateQuestion: "知识制度有哪些内容？"},
		{ID: "temporal", Split: semanticcache.EvaluationSplitCalibration, Reviewed: true, Reusable: false,
			AnchorQuestion: "知识制度包含什么？", CandidateQuestion: "当前线上知识制度包含什么？"},
	}
	observations := []semanticcache.SimilarityObservation{
		{PairID: "positive", Similarity: 0.9, Compatible: false},
		{PairID: "temporal", Similarity: 0.99, Compatible: true},
	}
	selection, err := semanticcache.SelectThreshold(pairs, observations, 0.98)
	if err != nil {
		t.Fatal(err)
	}
	if !selection.Enabled || selection.Threshold != 0.9 || selection.Calibration.Precision != 1 {
		t.Fatalf("selection = %+v", selection)
	}
}

func TestSemanticEvaluationDatasetRequiresReviewedLabelsAndFixedCounts(t *testing.T) {
	t.Parallel()

	dataset := semanticcache.EvaluationDataset{Version: "semantic-cache-v1", Seed: 20260814}
	for index := 0; index < 120; index++ {
		category := semanticcache.EvaluationCategoryReusable
		split := semanticcache.EvaluationSplitCalibration
		switch {
		case index >= 100:
			category = semanticcache.EvaluationCategoryContext
		case index >= 80:
			category = semanticcache.EvaluationCategoryTemporal
		case index >= 40:
			category = semanticcache.EvaluationCategoryDifficultNegative
		}
		if index%3 == 0 {
			split = semanticcache.EvaluationSplitHoldout
		}
		dataset.Pairs = append(dataset.Pairs, semanticcache.EvaluationPair{
			ID: "pair-" + string(rune(index+1)), Category: category, Split: split,
			AnchorQuestion: "原始问题", CandidateQuestion: "候选问题", Reviewed: true,
		})
	}
	if err := dataset.Validate(false); err == nil {
		t.Fatal("invalid non-stratified dataset was accepted")
	}
	dataset.Pairs[0].Reviewed = false
	if err := dataset.Validate(true); err == nil {
		t.Fatal("unreviewed dataset was accepted for calibration")
	}
}
