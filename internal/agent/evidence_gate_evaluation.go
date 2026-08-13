package agent

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

const evidenceGateReviewedCaseTarget = 30

// EvidenceGateEvaluationCase is the reviewed Gold needed specifically for the
// Early Exit ablation. A value of zero means that the configured budget is not
// expected to obtain sufficient evidence.
type EvidenceGateEvaluationCase struct {
	DatasetVersion          string `json:"datasetVersion"`
	CaseID                  string `json:"caseId"`
	EvidenceSufficientAtRun int    `json:"evidenceSufficientAtRun"`
}

func (c EvidenceGateEvaluationCase) Validate() error {
	if strings.TrimSpace(c.DatasetVersion) == "" || strings.TrimSpace(c.CaseID) == "" {
		return errors.New("datasetVersion and caseId are required")
	}
	if c.EvidenceSufficientAtRun < 0 || c.EvidenceSufficientAtRun > 4 {
		return errors.New("evidenceSufficientAtRun must be between 0 and 4")
	}
	return nil
}

// EvidenceGateEvaluationObservation is a reviewed result from one arm. The
// pairing fingerprint binds Tool contracts, budgets and fixtures that do not
// otherwise belong in the domain-neutral Evaluation Ledger.
type EvidenceGateEvaluationObservation struct {
	DatasetVersion              string            `json:"datasetVersion"`
	CaseID                      string            `json:"caseId"`
	Variant                     EvaluationVariant `json:"variant"`
	RunID                       string            `json:"runId"`
	EarlyExitEnabled            bool              `json:"earlyExitEnabled"`
	PairingFingerprint          string            `json:"pairingFingerprint"`
	ModelProvider               string            `json:"modelProvider"`
	ModelID                     string            `json:"modelId"`
	ModelProfile                string            `json:"modelProfile"`
	PromptVersion               string            `json:"promptVersion"`
	ReasoningEffort             string            `json:"reasoningEffort"`
	AgentRuns                   int               `json:"agentRuns"`
	Completed                   bool              `json:"completed"`
	QualityReviewed             bool              `json:"qualityReviewed"`
	ConclusionCorrect           bool              `json:"conclusionCorrect"`
	CitationCorrect             bool              `json:"citationCorrect"`
	HighSeverityWrongConclusion bool              `json:"highSeverityWrongConclusion"`
	Usage                       ModelUsage        `json:"usage"`
	ToolCalls                   int               `json:"toolCalls"`
	DurationMillis              int64             `json:"durationMillis"`
	ErrorType                   string            `json:"errorType,omitempty"`
	DegradationReasons          []string          `json:"degradationReasons,omitempty"`
	SkippedReason               string            `json:"skippedReason,omitempty"`
}

func (o EvidenceGateEvaluationObservation) Validate() error {
	if strings.TrimSpace(o.DatasetVersion) == "" || strings.TrimSpace(o.CaseID) == "" ||
		strings.TrimSpace(o.RunID) == "" || strings.TrimSpace(o.PairingFingerprint) == "" {
		return errors.New("datasetVersion, caseId, runId, and pairingFingerprint are required")
	}
	if !o.Variant.Valid() {
		return fmt.Errorf("invalid evaluation variant %q", o.Variant)
	}
	if (o.Variant == EvaluationExperiment) != o.EarlyExitEnabled {
		return errors.New("baseline must disable Early Exit and experiment must enable it")
	}
	if strings.TrimSpace(o.ModelProvider) == "" || strings.TrimSpace(o.ModelID) == "" ||
		strings.TrimSpace(o.ModelProfile) == "" || strings.TrimSpace(o.PromptVersion) == "" ||
		strings.TrimSpace(o.ReasoningEffort) == "" {
		return errors.New("model, profile, Prompt, and reasoning identities are required")
	}
	if o.AgentRuns < 0 || o.ToolCalls < 0 || o.DurationMillis < 0 {
		return errors.New("run, Tool call, and duration values cannot be negative")
	}
	if o.Usage.ModelCalls < 0 || o.Usage.PromptTokens < 0 || o.Usage.CompletionTokens < 0 ||
		o.Usage.TotalTokens < 0 || o.Usage.CachedTokens < 0 || o.Usage.ReasoningTokens < 0 {
		return errors.New("usage values cannot be negative")
	}
	maxInt := int(^uint(0) >> 1)
	if o.Usage.CompletionTokens > maxInt-o.Usage.PromptTokens {
		return errors.New("promptTokens plus completionTokens overflow")
	}
	if o.Usage.TotalTokens < o.Usage.PromptTokens+o.Usage.CompletionTokens {
		return errors.New("totalTokens cannot be less than promptTokens plus completionTokens")
	}
	if o.Usage.ModelCalls == 0 && o.Usage.TotalTokens > 0 {
		return errors.New("Token usage requires a positive modelCalls value")
	}
	if o.Usage.ModelCalls > 0 && o.Usage.TotalTokens == 0 {
		return errors.New("positive modelCalls requires positive totalTokens")
	}
	if o.Usage.CachedTokens > o.Usage.PromptTokens || o.Usage.ReasoningTokens > o.Usage.CompletionTokens {
		return errors.New("cachedTokens and reasoningTokens exceed their parent usage")
	}
	if o.QualityReviewed && strings.TrimSpace(o.SkippedReason) != "" {
		return errors.New("skipped observations cannot be quality reviewed")
	}
	if !o.QualityReviewed && (o.ConclusionCorrect || o.CitationCorrect || o.HighSeverityWrongConclusion) {
		return errors.New("quality labels require qualityReviewed")
	}
	if o.ConclusionCorrect && o.HighSeverityWrongConclusion {
		return errors.New("a correct conclusion cannot also be a high-severity wrong conclusion")
	}
	return nil
}

type EvidenceGateVariantSummary struct {
	Runs                         int     `json:"runs"`
	CompletedRuns                int     `json:"completedRuns"`
	CompletionRate               float64 `json:"completionRate"`
	QualityReviewedRuns          int     `json:"qualityReviewedRuns"`
	ConclusionCorrectness        float64 `json:"conclusionCorrectness"`
	CitationCorrectness          float64 `json:"citationCorrectness"`
	HighSeverityWrongConclusions int     `json:"highSeverityWrongConclusions"`
	AgentRuns                    int     `json:"agentRuns"`
	ModelCalls                   int     `json:"modelCalls"`
	ToolCalls                    int     `json:"toolCalls"`
	PromptTokens                 int     `json:"promptTokens"`
	CompletionTokens             int     `json:"completionTokens"`
	TotalTokens                  int     `json:"totalTokens"`
	P50DurationMillis            int64   `json:"p50DurationMillis"`
	P95DurationMillis            int64   `json:"p95DurationMillis"`
}

type EvidenceGateCaseIssue struct {
	CaseID  string   `json:"caseId"`
	Reasons []string `json:"reasons"`
}

type EvidenceGateEarlyExitSummary struct {
	DatasetVersion           string                     `json:"datasetVersion"`
	Cases                    int                        `json:"cases"`
	PairedCases              int                        `json:"pairedCases"`
	UnpairedRuns             int                        `json:"unpairedRuns"`
	ReviewedCaseTarget       int                        `json:"reviewedCaseTarget"`
	ReviewedCaseGap          int                        `json:"reviewedCaseGap"`
	Baseline                 EvidenceGateVariantSummary `json:"baseline"`
	Experiment               EvidenceGateVariantSummary `json:"experiment"`
	QualityGatePassed        bool                       `json:"qualityGatePassed"`
	PerformanceClaimsAllowed bool                       `json:"performanceClaimsAllowed"`
	PairedModelCallReduction float64                    `json:"pairedModelCallReduction"`
	PairedToolCallReduction  float64                    `json:"pairedToolCallReduction"`
	PairedTokenReduction     float64                    `json:"pairedTokenReduction"`
	RegressedCases           []string                   `json:"regressedCases,omitempty"`
	CaseIssues               []EvidenceGateCaseIssue    `json:"caseIssues,omitempty"`
}

func EvaluateEvidenceGateEarlyExit(
	cases []EvidenceGateEvaluationCase,
	observations []EvidenceGateEvaluationObservation,
) (EvidenceGateEarlyExitSummary, error) {
	caseByID, version, err := indexEvidenceGateCases(cases)
	if err != nil {
		return EvidenceGateEarlyExitSummary{}, err
	}
	summary := EvidenceGateEarlyExitSummary{
		DatasetVersion: version, Cases: len(cases), ReviewedCaseTarget: evidenceGateReviewedCaseTarget,
	}
	summary.ReviewedCaseGap = max(0, evidenceGateReviewedCaseTarget-len(cases))
	pairs := make(map[string]map[EvaluationVariant]EvidenceGateEvaluationObservation, len(cases))
	seenRuns := make(map[string]struct{}, len(observations))
	issues := make(map[string][]string)
	for index, observation := range observations {
		if err := observation.Validate(); err != nil {
			return EvidenceGateEarlyExitSummary{}, fmt.Errorf("observation %d: %w", index, err)
		}
		if observation.DatasetVersion != version {
			return EvidenceGateEarlyExitSummary{}, fmt.Errorf("observation %q uses dataset version %q, expected %q", observation.RunID, observation.DatasetVersion, version)
		}
		if _, exists := caseByID[observation.CaseID]; !exists {
			return EvidenceGateEarlyExitSummary{}, fmt.Errorf("observation %q references unknown case %q", observation.RunID, observation.CaseID)
		}
		if _, exists := seenRuns[observation.RunID]; exists {
			return EvidenceGateEarlyExitSummary{}, fmt.Errorf("duplicate runId %q", observation.RunID)
		}
		seenRuns[observation.RunID] = struct{}{}
		if pairs[observation.CaseID] == nil {
			pairs[observation.CaseID] = make(map[EvaluationVariant]EvidenceGateEvaluationObservation, 2)
		}
		if _, exists := pairs[observation.CaseID][observation.Variant]; exists {
			return EvidenceGateEarlyExitSummary{}, fmt.Errorf("duplicate %s observation for case %q", observation.Variant, observation.CaseID)
		}
		pairs[observation.CaseID][observation.Variant] = observation
		for _, reason := range observationIssueReasons(observation) {
			issues[observation.CaseID] = appendUnique(issues[observation.CaseID], string(observation.Variant)+": "+reason)
		}
	}

	summary.Baseline = summarizeEvidenceGateVariant(observations, EvaluationBaseline)
	summary.Experiment = summarizeEvidenceGateVariant(observations, EvaluationExperiment)
	for _, definition := range cases {
		pair := pairs[definition.CaseID]
		baseline, baselineOK := pair[EvaluationBaseline]
		experiment, experimentOK := pair[EvaluationExperiment]
		if !baselineOK || !experimentOK {
			if baselineOK {
				summary.UnpairedRuns++
			}
			if experimentOK {
				summary.UnpairedRuns++
			}
			issues[definition.CaseID] = appendUnique(issues[definition.CaseID], "missing paired arm")
			continue
		}
		if !sameEvidenceGatePairingControls(baseline, experiment) {
			return EvidenceGateEarlyExitSummary{}, fmt.Errorf("case %q pairing controls differ between baseline and experiment", definition.CaseID)
		}
		summary.PairedCases++
		if evidenceGateRegressed(baseline, experiment) {
			summary.RegressedCases = append(summary.RegressedCases, definition.CaseID)
		}
		if definition.EvidenceSufficientAtRun > 0 && experiment.AgentRuns < definition.EvidenceSufficientAtRun {
			summary.RegressedCases = appendUnique(summary.RegressedCases, definition.CaseID)
			issues[definition.CaseID] = appendUnique(issues[definition.CaseID], "experiment stopped before the reviewed evidence-sufficient run")
		}
	}
	summary.QualityGatePassed = summary.PairedCases == len(cases) && summary.UnpairedRuns == 0 &&
		len(summary.RegressedCases) == 0 &&
		summary.Baseline.QualityReviewedRuns == summary.Baseline.Runs &&
		summary.Experiment.QualityReviewedRuns == summary.Experiment.Runs &&
		summary.Experiment.CompletionRate >= summary.Baseline.CompletionRate &&
		summary.Experiment.ConclusionCorrectness >= summary.Baseline.ConclusionCorrectness &&
		summary.Experiment.CitationCorrectness >= summary.Baseline.CitationCorrectness &&
		summary.Experiment.HighSeverityWrongConclusions <= summary.Baseline.HighSeverityWrongConclusions
	summary.PerformanceClaimsAllowed = summary.QualityGatePassed && summary.PairedCases > 0
	if summary.PerformanceClaimsAllowed {
		if summary.Baseline.ModelCalls > 0 {
			summary.PairedModelCallReduction = reductionRate(int64(summary.Baseline.ModelCalls), int64(summary.Experiment.ModelCalls))
		}
		if summary.Baseline.ToolCalls > 0 {
			summary.PairedToolCallReduction = reductionRate(int64(summary.Baseline.ToolCalls), int64(summary.Experiment.ToolCalls))
		}
		if summary.Baseline.TotalTokens > 0 {
			summary.PairedTokenReduction = reductionRate(int64(summary.Baseline.TotalTokens), int64(summary.Experiment.TotalTokens))
		}
	}
	caseIDs := make([]string, 0, len(issues))
	for caseID := range issues {
		caseIDs = append(caseIDs, caseID)
	}
	sort.Strings(caseIDs)
	for _, caseID := range caseIDs {
		summary.CaseIssues = append(summary.CaseIssues, EvidenceGateCaseIssue{CaseID: caseID, Reasons: issues[caseID]})
	}
	return summary, nil
}

func indexEvidenceGateCases(cases []EvidenceGateEvaluationCase) (map[string]EvidenceGateEvaluationCase, string, error) {
	if len(cases) == 0 {
		return nil, "", errors.New("Evidence Gate evaluation contains no cases")
	}
	result := make(map[string]EvidenceGateEvaluationCase, len(cases))
	version := ""
	for index, definition := range cases {
		if err := definition.Validate(); err != nil {
			return nil, "", fmt.Errorf("case %d: %w", index, err)
		}
		if version == "" {
			version = definition.DatasetVersion
		} else if definition.DatasetVersion != version {
			return nil, "", fmt.Errorf("dataset mixes versions %q and %q", version, definition.DatasetVersion)
		}
		if _, exists := result[definition.CaseID]; exists {
			return nil, "", fmt.Errorf("duplicate caseId %q", definition.CaseID)
		}
		result[definition.CaseID] = definition
	}
	return result, version, nil
}

func summarizeEvidenceGateVariant(observations []EvidenceGateEvaluationObservation, variant EvaluationVariant) EvidenceGateVariantSummary {
	var result EvidenceGateVariantSummary
	var conclusionCorrect, citationCorrect int
	var durations []int64
	for _, observation := range observations {
		if observation.Variant != variant {
			continue
		}
		result.Runs++
		if observation.Completed {
			result.CompletedRuns++
		}
		if observation.QualityReviewed {
			result.QualityReviewedRuns++
			if observation.ConclusionCorrect {
				conclusionCorrect++
			}
			if observation.CitationCorrect {
				citationCorrect++
			}
			if observation.HighSeverityWrongConclusion {
				result.HighSeverityWrongConclusions++
			}
		}
		result.AgentRuns += observation.AgentRuns
		result.ModelCalls += observation.Usage.ModelCalls
		result.ToolCalls += observation.ToolCalls
		result.PromptTokens += observation.Usage.PromptTokens
		result.CompletionTokens += observation.Usage.CompletionTokens
		result.TotalTokens += observation.Usage.TotalTokens
		durations = append(durations, observation.DurationMillis)
	}
	if result.Runs > 0 {
		result.CompletionRate = float64(result.CompletedRuns) / float64(result.Runs)
	}
	if result.QualityReviewedRuns > 0 {
		result.ConclusionCorrectness = float64(conclusionCorrect) / float64(result.QualityReviewedRuns)
		result.CitationCorrectness = float64(citationCorrect) / float64(result.QualityReviewedRuns)
	}
	result.P50DurationMillis = nearestRank(durations, 0.50)
	result.P95DurationMillis = nearestRank(durations, 0.95)
	return result
}

func nearestRank(values []int64, percentile float64) int64 {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]int64(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	rank := int(math.Ceil(percentile*float64(len(ordered)))) - 1
	if rank < 0 {
		rank = 0
	}
	return ordered[rank]
}

func sameEvidenceGatePairingControls(left, right EvidenceGateEvaluationObservation) bool {
	return left.PairingFingerprint == right.PairingFingerprint &&
		left.ModelProvider == right.ModelProvider && left.ModelID == right.ModelID &&
		left.ModelProfile == right.ModelProfile && left.PromptVersion == right.PromptVersion &&
		left.ReasoningEffort == right.ReasoningEffort
}

func evidenceGateRegressed(baseline, experiment EvidenceGateEvaluationObservation) bool {
	return (baseline.Completed && !experiment.Completed) ||
		(baseline.QualityReviewed && baseline.ConclusionCorrect && !experiment.ConclusionCorrect) ||
		(baseline.QualityReviewed && baseline.CitationCorrect && !experiment.CitationCorrect) ||
		(!baseline.HighSeverityWrongConclusion && experiment.HighSeverityWrongConclusion)
}

func observationIssueReasons(observation EvidenceGateEvaluationObservation) []string {
	var result []string
	if observation.ErrorType != "" {
		result = append(result, "failed: "+observation.ErrorType)
	}
	if observation.SkippedReason != "" {
		result = append(result, "skipped: "+observation.SkippedReason)
	}
	for _, reason := range observation.DegradationReasons {
		if strings.TrimSpace(reason) != "" {
			result = append(result, "degraded: "+strings.TrimSpace(reason))
		}
	}
	return result
}

func appendUnique(values []string, value string) []string {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}
