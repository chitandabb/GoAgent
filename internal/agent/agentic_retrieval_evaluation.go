package agent

import (
	"errors"
	"fmt"
	"strings"
)

// AgenticRetrievalScenario describes the first-run state injected by the
// bounded Agentic retrieval evaluation. The real model is only used for the
// follow-up decision, so Gate behavior can be compared without a drifting
// first-pass report.
type AgenticRetrievalScenario string

const (
	AgenticScenarioEvidenceGap AgenticRetrievalScenario = "evidence_gap"
	AgenticScenarioFormatOnly  AgenticRetrievalScenario = "format_only"
	AgenticScenarioValid       AgenticRetrievalScenario = "valid_first_pass"
)

func (s AgenticRetrievalScenario) Valid() bool {
	return s == AgenticScenarioEvidenceGap || s == AgenticScenarioFormatOnly || s == AgenticScenarioValid
}

type AgenticRetrievalEvaluationCase struct {
	DatasetVersion    string                   `json:"datasetVersion"`
	CaseID            string                   `json:"caseId"`
	Scenario          AgenticRetrievalScenario `json:"scenario"`
	UserQuery         string                   `json:"userQuery"`
	ExpectedAttempted bool                     `json:"expectedAttempted"`
	ExpectedAdded     bool                     `json:"expectedAddedEvidence"`
	ExpectedStop      string                   `json:"expectedStopReason"`
}

func (c AgenticRetrievalEvaluationCase) Validate() error {
	if strings.TrimSpace(c.DatasetVersion) == "" || strings.TrimSpace(c.CaseID) == "" ||
		!c.Scenario.Valid() || strings.TrimSpace(c.UserQuery) == "" || c.UserQuery != strings.TrimSpace(c.UserQuery) {
		return errors.New("Agentic retrieval evaluation identity or query is invalid")
	}
	if strings.TrimSpace(c.ExpectedStop) == "" || c.ExpectedStop != strings.TrimSpace(c.ExpectedStop) {
		return errors.New("Agentic retrieval expected stop reason is required")
	}
	switch c.Scenario {
	case AgenticScenarioEvidenceGap:
		if !c.ExpectedAttempted || !c.ExpectedAdded || c.ExpectedStop != "new_evidence_added" {
			return errors.New("evidence-gap evaluation case has inconsistent expectations")
		}
	case AgenticScenarioFormatOnly:
		if c.ExpectedAttempted || c.ExpectedAdded || c.ExpectedStop != "not_eligible" {
			return errors.New("format-only evaluation case has inconsistent expectations")
		}
	case AgenticScenarioValid:
		if c.ExpectedAttempted || c.ExpectedAdded || c.ExpectedStop != "not_needed" {
			return errors.New("valid-first-pass evaluation case has inconsistent expectations")
		}
	}
	return nil
}

type AgenticRetrievalEvaluationObservation struct {
	DatasetVersion                string                   `json:"datasetVersion"`
	CaseID                        string                   `json:"caseId"`
	Scenario                      AgenticRetrievalScenario `json:"scenario"`
	RunID                         string                   `json:"runId"`
	Model                         string                   `json:"model"`
	ModelVersion                  string                   `json:"modelVersion"`
	ReasoningEffort               string                   `json:"reasoningEffort"`
	PromptVersion                 string                   `json:"promptVersion"`
	AgentRuns                     int                      `json:"agentRuns"`
	ActualToolCalls               []string                 `json:"actualToolCalls"`
	AgenticRetrievalAttempted     bool                     `json:"agenticRetrievalAttempted"`
	AgenticRetrievalAddedEvidence bool                     `json:"agenticRetrievalAddedEvidence"`
	AgenticRetrievalStopReason    string                   `json:"agenticRetrievalStopReason"`
	Partial                       bool                     `json:"partial"`
	Usage                         ModelUsage               `json:"usage"`
	DurationMillis                int64                    `json:"durationMillis"`
	ErrorType                     string                   `json:"errorType,omitempty"`
}

func (o AgenticRetrievalEvaluationObservation) Validate() error {
	if strings.TrimSpace(o.DatasetVersion) == "" || strings.TrimSpace(o.CaseID) == "" ||
		strings.TrimSpace(o.RunID) == "" || !o.Scenario.Valid() ||
		strings.TrimSpace(o.Model) == "" || strings.TrimSpace(o.ModelVersion) == "" ||
		strings.TrimSpace(o.ReasoningEffort) == "" || strings.TrimSpace(o.PromptVersion) == "" ||
		o.AgentRuns < 1 || o.AgentRuns > 4 || strings.TrimSpace(o.AgenticRetrievalStopReason) == "" ||
		o.DurationMillis < 0 || o.ErrorType != "" && strings.TrimSpace(o.ErrorType) != o.ErrorType {
		return errors.New("Agentic retrieval evaluation observation is invalid")
	}
	if o.AgenticRetrievalAddedEvidence && !o.AgenticRetrievalAttempted {
		return errors.New("Agentic retrieval added evidence without an attempt")
	}
	if o.Usage.ModelCalls < 0 || o.Usage.PromptTokens < 0 || o.Usage.CompletionTokens < 0 ||
		o.Usage.TotalTokens < 0 || o.Usage.CachedTokens < 0 || o.Usage.ReasoningTokens < 0 {
		return errors.New("Agentic retrieval evaluation usage is invalid")
	}
	for _, toolName := range o.ActualToolCalls {
		if strings.TrimSpace(toolName) == "" || toolName != strings.TrimSpace(toolName) {
			return errors.New("Agentic retrieval evaluation tool name is invalid")
		}
	}
	return nil
}

type AgenticRetrievalEvaluationSummary struct {
	DatasetVersion                   string  `json:"datasetVersion"`
	Cases                            int     `json:"cases"`
	Runs                             int     `json:"runs"`
	AttemptExpectationAccuracy       float64 `json:"attemptExpectationAccuracy"`
	AttemptPrecision                 float64 `json:"attemptPrecision"`
	AttemptRecall                    float64 `json:"attemptRecall"`
	AddedEvidenceExpectationAccuracy float64 `json:"addedEvidenceExpectationAccuracy"`
	AddedEvidenceRate                float64 `json:"addedEvidenceRate"`
	StopReasonAccuracy               float64 `json:"stopReasonAccuracy"`
	CompletedRuns                    int     `json:"completedRuns"`
	PartialRuns                      int     `json:"partialRuns"`
	PromptTokens                     int     `json:"promptTokens"`
	CompletionTokens                 int     `json:"completionTokens"`
	TotalTokens                      int     `json:"totalTokens"`
	AverageDurationMillis            float64 `json:"averageDurationMillis"`
	FailedRuns                       int     `json:"failedRuns"`
}

func EvaluateAgenticRetrieval(
	cases []AgenticRetrievalEvaluationCase,
	observations []AgenticRetrievalEvaluationObservation,
) (AgenticRetrievalEvaluationSummary, error) {
	if len(cases) == 0 {
		return AgenticRetrievalEvaluationSummary{}, errors.New("Agentic retrieval evaluation has no cases")
	}
	caseByID := make(map[string]AgenticRetrievalEvaluationCase, len(cases))
	version := ""
	for index, definition := range cases {
		if err := definition.Validate(); err != nil {
			return AgenticRetrievalEvaluationSummary{}, fmt.Errorf("case %d: %w", index, err)
		}
		if version == "" {
			version = definition.DatasetVersion
		} else if definition.DatasetVersion != version {
			return AgenticRetrievalEvaluationSummary{}, errors.New("Agentic retrieval evaluation mixes dataset versions")
		}
		if _, exists := caseByID[definition.CaseID]; exists {
			return AgenticRetrievalEvaluationSummary{}, fmt.Errorf("duplicate Agentic retrieval case %q", definition.CaseID)
		}
		caseByID[definition.CaseID] = definition
	}
	if len(observations) != len(cases) {
		return AgenticRetrievalEvaluationSummary{}, errors.New("Agentic retrieval evaluation requires one observation per case")
	}
	seen := make(map[string]struct{}, len(observations))
	summary := AgenticRetrievalEvaluationSummary{DatasetVersion: version, Cases: len(cases), Runs: len(observations)}
	var expectedAttempts, expectedAttemptHits, actualAttempts, truePositiveAttempts int
	var addedExpectationHits, actualAdded, expectedStops, matchingStops int
	var totalDuration float64
	for index, observation := range observations {
		if err := observation.Validate(); err != nil {
			return AgenticRetrievalEvaluationSummary{}, fmt.Errorf("observation %d: %w", index, err)
		}
		definition, exists := caseByID[observation.CaseID]
		if !exists || observation.DatasetVersion != version || observation.Scenario != definition.Scenario {
			return AgenticRetrievalEvaluationSummary{}, fmt.Errorf("observation %q does not match its case", observation.RunID)
		}
		if _, exists := seen[observation.CaseID]; exists {
			return AgenticRetrievalEvaluationSummary{}, fmt.Errorf("duplicate observation for case %q", observation.CaseID)
		}
		seen[observation.CaseID] = struct{}{}
		if observation.AgenticRetrievalAttempted == definition.ExpectedAttempted {
			expectedAttemptHits++
		}
		if definition.ExpectedAttempted {
			expectedAttempts++
		}
		if observation.AgenticRetrievalAttempted {
			actualAttempts++
			if definition.ExpectedAttempted {
				truePositiveAttempts++
			}
		}
		if observation.AgenticRetrievalAddedEvidence == definition.ExpectedAdded {
			addedExpectationHits++
		}
		if observation.AgenticRetrievalAddedEvidence {
			actualAdded++
		}
		if observation.AgenticRetrievalStopReason == definition.ExpectedStop {
			matchingStops++
		}
		expectedStops++
		if observation.Partial {
			summary.PartialRuns++
		} else if observation.ErrorType == "" {
			summary.CompletedRuns++
		}
		if observation.ErrorType != "" {
			summary.FailedRuns++
		}
		summary.PromptTokens += observation.Usage.PromptTokens
		summary.CompletionTokens += observation.Usage.CompletionTokens
		summary.TotalTokens += observation.Usage.TotalTokens
		totalDuration += float64(observation.DurationMillis)
	}
	summary.AttemptExpectationAccuracy = float64(expectedAttemptHits) / float64(len(observations))
	if actualAttempts > 0 {
		summary.AttemptPrecision = float64(truePositiveAttempts) / float64(actualAttempts)
	}
	if expectedAttempts > 0 {
		summary.AttemptRecall = float64(truePositiveAttempts) / float64(expectedAttempts)
	}
	summary.AddedEvidenceExpectationAccuracy = float64(addedExpectationHits) / float64(len(observations))
	if actualAttempts > 0 {
		summary.AddedEvidenceRate = float64(actualAdded) / float64(actualAttempts)
	}
	summary.StopReasonAccuracy = float64(matchingStops) / float64(expectedStops)
	summary.AverageDurationMillis = totalDuration / float64(len(observations))
	return summary, nil
}
