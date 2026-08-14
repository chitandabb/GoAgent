package semanticcache

import (
	"errors"
	"math"
	"regexp"
	"slices"
	"strings"
)

type EvaluationCategory string

const (
	EvaluationCategoryReusable          EvaluationCategory = "reusable_paraphrase"
	EvaluationCategoryDifficultNegative EvaluationCategory = "difficult_negative"
	EvaluationCategoryTemporal          EvaluationCategory = "temporal_version_negative"
	EvaluationCategoryContext           EvaluationCategory = "context_dependent_negative"
)

type EvaluationSplit string

const (
	EvaluationSplitCalibration EvaluationSplit = "calibration"
	EvaluationSplitHoldout     EvaluationSplit = "holdout"
)

type EvaluationPair struct {
	ID                string             `json:"id"`
	Category          EvaluationCategory `json:"category"`
	Split             EvaluationSplit    `json:"split"`
	AnchorQuestion    string             `json:"anchorQuestion"`
	CandidateQuestion string             `json:"candidateQuestion"`
	ProposedReusable  bool               `json:"proposedReusable"`
	Reviewed          bool               `json:"reviewed"`
	Reusable          bool               `json:"reusable"`
	Rationale         string             `json:"rationale,omitempty"`
}

type EvaluationDataset struct {
	Version string           `json:"version"`
	Seed    int64            `json:"seed"`
	Pairs   []EvaluationPair `json:"pairs"`
}

var evaluationPairIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{2,63}$`)

func (d EvaluationDataset) Validate(requireReviewed bool) error {
	if strings.TrimSpace(d.Version) == "" || len(d.Pairs) != 120 || d.Seed == 0 {
		return errors.New("semantic cache evaluation dataset identity is invalid")
	}
	type categorySplit struct {
		category EvaluationCategory
		split    EvaluationSplit
	}
	want := map[categorySplit]int{
		{EvaluationCategoryReusable, EvaluationSplitCalibration}:          27,
		{EvaluationCategoryReusable, EvaluationSplitHoldout}:              13,
		{EvaluationCategoryDifficultNegative, EvaluationSplitCalibration}: 27,
		{EvaluationCategoryDifficultNegative, EvaluationSplitHoldout}:     13,
		{EvaluationCategoryTemporal, EvaluationSplitCalibration}:          13,
		{EvaluationCategoryTemporal, EvaluationSplitHoldout}:              7,
		{EvaluationCategoryContext, EvaluationSplitCalibration}:           13,
		{EvaluationCategoryContext, EvaluationSplitHoldout}:               7,
	}
	counts := make(map[categorySplit]int, len(want))
	seen := make(map[string]struct{}, len(d.Pairs))
	for _, pair := range d.Pairs {
		if !evaluationPairIDPattern.MatchString(pair.ID) || strings.TrimSpace(pair.AnchorQuestion) == "" ||
			strings.TrimSpace(pair.CandidateQuestion) == "" || len([]rune(pair.AnchorQuestion)) > MaxQuestionRunes ||
			len([]rune(pair.CandidateQuestion)) > MaxQuestionRunes ||
			(pair.Split != EvaluationSplitCalibration && pair.Split != EvaluationSplitHoldout) {
			return errors.New("semantic cache evaluation pair is invalid")
		}
		if _, exists := seen[pair.ID]; exists {
			return errors.New("semantic cache evaluation pair id is duplicated")
		}
		seen[pair.ID] = struct{}{}
		key := categorySplit{pair.Category, pair.Split}
		if _, exists := want[key]; !exists {
			return errors.New("semantic cache evaluation category is invalid")
		}
		counts[key]++
		if requireReviewed && !pair.Reviewed {
			return errors.New("semantic cache evaluation labels require human review")
		}
	}
	for key, expected := range want {
		if counts[key] != expected {
			return errors.New("semantic cache evaluation split is not stratified")
		}
	}
	return nil
}

type SimilarityObservation struct {
	PairID              string  `json:"pairId"`
	Similarity          float64 `json:"similarity"`
	Compatible          bool    `json:"compatible"`
	LookupDurationNanos int64   `json:"lookupDurationNanos,omitempty"`
}

type CacheMetrics struct {
	TruePositives  int     `json:"truePositives"`
	FalsePositives int     `json:"falsePositives"`
	FalseNegatives int     `json:"falseNegatives"`
	TrueNegatives  int     `json:"trueNegatives"`
	Hits           int     `json:"hits"`
	Total          int     `json:"total"`
	Precision      float64 `json:"precision"`
	Recall         float64 `json:"recall"`
	HitRate        float64 `json:"hitRate"`
}

type ThresholdSelection struct {
	Enabled       bool         `json:"enabled"`
	Threshold     float64      `json:"threshold,omitempty"`
	PrecisionGate float64      `json:"precisionGate"`
	Calibration   CacheMetrics `json:"calibration"`
}

func SelectThreshold(
	pairs []EvaluationPair,
	observations []SimilarityObservation,
	precisionGate float64,
) (ThresholdSelection, error) {
	if math.IsNaN(precisionGate) || math.IsInf(precisionGate, 0) || precisionGate <= 0 || precisionGate > 1 {
		return ThresholdSelection{}, errors.New("semantic cache precision gate is invalid")
	}
	pairByID := make(map[string]EvaluationPair, len(pairs))
	for _, pair := range pairs {
		if pair.ID == "" || !pair.Reviewed {
			return ThresholdSelection{}, errors.New("semantic cache threshold selection requires reviewed labels")
		}
		if _, exists := pairByID[pair.ID]; exists {
			return ThresholdSelection{}, errors.New("semantic cache threshold pair is duplicated")
		}
		pairByID[pair.ID] = pair
	}
	observationByID := make(map[string]SimilarityObservation, len(observations))
	thresholds := make([]float64, 0, len(observations))
	for _, observation := range observations {
		pair, exists := pairByID[observation.PairID]
		if !exists || math.IsNaN(observation.Similarity) || math.IsInf(observation.Similarity, 0) ||
			observation.Similarity < -1 || observation.Similarity > 1 || observation.LookupDurationNanos < 0 {
			return ThresholdSelection{}, errors.New("semantic cache similarity observation is invalid")
		}
		if _, duplicate := observationByID[observation.PairID]; duplicate {
			return ThresholdSelection{}, errors.New("semantic cache similarity observation is duplicated")
		}
		observationByID[observation.PairID] = observation
		if pair.Split == EvaluationSplitCalibration && observation.Compatible {
			thresholds = append(thresholds, observation.Similarity)
		}
	}
	for id := range pairByID {
		if _, exists := observationByID[id]; !exists {
			return ThresholdSelection{}, errors.New("semantic cache similarity observation is missing")
		}
	}
	slices.Sort(thresholds)
	thresholds = slices.Compact(thresholds)
	selection := ThresholdSelection{PrecisionGate: precisionGate}
	bestRecall := -1.0
	for _, threshold := range thresholds {
		metrics := evaluateThreshold(pairs, observationByID, EvaluationSplitCalibration, threshold)
		if metrics.Hits == 0 || metrics.Precision < precisionGate {
			continue
		}
		if metrics.Recall > bestRecall || metrics.Recall == bestRecall && (!selection.Enabled || threshold < selection.Threshold) {
			selection.Enabled = true
			selection.Threshold = threshold
			selection.Calibration = metrics
			bestRecall = metrics.Recall
		}
	}
	return selection, nil
}

func EvaluateThreshold(
	pairs []EvaluationPair,
	observations []SimilarityObservation,
	split EvaluationSplit,
	threshold float64,
) (CacheMetrics, error) {
	observationByID := make(map[string]SimilarityObservation, len(observations))
	for _, observation := range observations {
		if _, exists := observationByID[observation.PairID]; exists {
			return CacheMetrics{}, errors.New("semantic cache similarity observation is duplicated")
		}
		observationByID[observation.PairID] = observation
	}
	for _, pair := range pairs {
		if !pair.Reviewed {
			return CacheMetrics{}, errors.New("semantic cache evaluation labels require human review")
		}
		if _, exists := observationByID[pair.ID]; !exists {
			return CacheMetrics{}, errors.New("semantic cache similarity observation is missing")
		}
	}
	return evaluateThreshold(pairs, observationByID, split, threshold), nil
}

func evaluateThreshold(
	pairs []EvaluationPair,
	observations map[string]SimilarityObservation,
	split EvaluationSplit,
	threshold float64,
) CacheMetrics {
	var metrics CacheMetrics
	for _, pair := range pairs {
		if pair.Split != split {
			continue
		}
		observation := observations[pair.ID]
		hit := observation.Compatible && observation.Similarity >= threshold
		metrics.Total++
		if hit {
			metrics.Hits++
		}
		switch {
		case hit && pair.Reusable:
			metrics.TruePositives++
		case hit && !pair.Reusable:
			metrics.FalsePositives++
		case !hit && pair.Reusable:
			metrics.FalseNegatives++
		default:
			metrics.TrueNegatives++
		}
	}
	if metrics.Hits > 0 {
		metrics.Precision = float64(metrics.TruePositives) / float64(metrics.Hits)
	}
	positives := metrics.TruePositives + metrics.FalseNegatives
	if positives > 0 {
		metrics.Recall = float64(metrics.TruePositives) / float64(positives)
	}
	if metrics.Total > 0 {
		metrics.HitRate = float64(metrics.Hits) / float64(metrics.Total)
	}
	return metrics
}
