package sqlserver

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const QueryGuardEvaluationSummarySchemaVersion = "sql_safety_summary_v1"

type QueryGuardRiskClass string

const (
	QueryGuardHighRiskWrite QueryGuardRiskClass = "high_risk_write"
	QueryGuardSafeRead      QueryGuardRiskClass = "safe_read"
)

func (c QueryGuardRiskClass) Valid() bool {
	return c == QueryGuardHighRiskWrite || c == QueryGuardSafeRead
}

var queryGuardEvaluationIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

type QueryGuardEvaluationCase struct {
	DatasetVersion string               `json:"datasetVersion"`
	CaseID         string               `json:"caseId"`
	Category       string               `json:"category"`
	RiskClass      QueryGuardRiskClass  `json:"riskClass"`
	Query          string               `json:"query"`
	ExpectedReason QueryRejectionReason `json:"expectedReason,omitempty"`
}

func (c QueryGuardEvaluationCase) Validate() error {
	if !queryGuardEvaluationIDPattern.MatchString(c.DatasetVersion) ||
		!queryGuardEvaluationIDPattern.MatchString(c.CaseID) ||
		!queryGuardEvaluationIDPattern.MatchString(c.Category) {
		return errors.New("datasetVersion, caseId, and category must be stable identifiers")
	}
	if !c.RiskClass.Valid() {
		return fmt.Errorf("invalid riskClass %q", c.RiskClass)
	}
	if strings.TrimSpace(c.Query) == "" || len(c.Query) > 64*1024 {
		return errors.New("query must be non-empty and at most 65536 bytes")
	}
	if c.RiskClass == QueryGuardSafeRead && c.ExpectedReason != "" {
		return errors.New("safe read case cannot declare expectedReason")
	}
	return nil
}

type QueryGuardEvaluationObservation struct {
	DatasetVersion  string                   `json:"datasetVersion"`
	CaseID          string                   `json:"caseId"`
	Category        string                   `json:"category"`
	RiskClass       QueryGuardRiskClass      `json:"riskClass"`
	PolicyVersion   string                   `json:"policyVersion"`
	QueryHash       string                   `json:"queryHash"`
	Allowed         bool                     `json:"allowed"`
	DecisionMatch   bool                     `json:"decisionMatch"`
	ReasonMatch     bool                     `json:"reasonMatch"`
	RejectionReason QueryRejectionReason     `json:"rejectionReason,omitempty"`
	Objects         []ReadonlyQueryObjectRef `json:"objects,omitempty"`
}

type QueryGuardEvaluationSummary struct {
	SchemaVersion          string         `json:"schemaVersion"`
	DatasetVersion         string         `json:"datasetVersion"`
	PolicyVersion          string         `json:"policyVersion"`
	Cases                  int            `json:"cases"`
	Correct                int            `json:"correct"`
	Accuracy               float64        `json:"accuracy"`
	HighRiskCases          int            `json:"highRiskCases"`
	BlockedHighRisk        int            `json:"blockedHighRisk"`
	HighRiskBlockingRate   float64        `json:"highRiskBlockingRate"`
	SafeReadCases          int            `json:"safeReadCases"`
	AcceptedSafeReads      int            `json:"acceptedSafeReads"`
	SafeReadAcceptanceRate float64        `json:"safeReadAcceptanceRate"`
	ExpectedReasonCases    int            `json:"expectedReasonCases"`
	MatchedReasons         int            `json:"matchedReasons"`
	ReasonMatchRate        float64        `json:"reasonMatchRate"`
	MismatchedCases        []string       `json:"mismatchedCases,omitempty"`
	RejectionReasons       map[string]int `json:"rejectionReasons,omitempty"`
}

func EvaluateQueryGuard(
	guard *ReadonlyQueryGuard,
	cases []QueryGuardEvaluationCase,
) ([]QueryGuardEvaluationObservation, QueryGuardEvaluationSummary, error) {
	if guard == nil {
		return nil, QueryGuardEvaluationSummary{}, errors.New("query guard is required")
	}
	if len(cases) == 0 {
		return nil, QueryGuardEvaluationSummary{}, errors.New("query guard evaluation contains no cases")
	}
	version := ""
	seen := make(map[string]struct{}, len(cases))
	observations := make([]QueryGuardEvaluationObservation, 0, len(cases))
	summary := QueryGuardEvaluationSummary{
		SchemaVersion:    QueryGuardEvaluationSummarySchemaVersion,
		PolicyVersion:    ReadonlyQueryPolicyVersion,
		RejectionReasons: make(map[string]int),
	}
	for index, current := range cases {
		if err := current.Validate(); err != nil {
			return nil, QueryGuardEvaluationSummary{}, fmt.Errorf("case %d: %w", index, err)
		}
		if version == "" {
			version = current.DatasetVersion
		} else if current.DatasetVersion != version {
			return nil, QueryGuardEvaluationSummary{}, errors.New("query guard evaluation mixes dataset versions")
		}
		if _, exists := seen[current.CaseID]; exists {
			return nil, QueryGuardEvaluationSummary{}, fmt.Errorf("duplicate caseId %q", current.CaseID)
		}
		seen[current.CaseID] = struct{}{}

		analysis, analyzeErr := guard.Analyze(current.Query)
		observation := QueryGuardEvaluationObservation{
			DatasetVersion: current.DatasetVersion,
			CaseID:         current.CaseID,
			Category:       current.Category,
			RiskClass:      current.RiskClass,
			PolicyVersion:  ReadonlyQueryPolicyVersion,
			QueryHash:      hashEvaluationQuery(current.Query),
			Allowed:        analyzeErr == nil,
			Objects:        analysis.Objects,
		}
		if analyzeErr != nil {
			var rejection *QueryGuardError
			if !errors.As(analyzeErr, &rejection) {
				return nil, QueryGuardEvaluationSummary{}, fmt.Errorf("case %q: analyze query: %w", current.CaseID, analyzeErr)
			}
			observation.RejectionReason = rejection.Reason
			summary.RejectionReasons[string(rejection.Reason)]++
		}
		observation.DecisionMatch =
			(current.RiskClass == QueryGuardHighRiskWrite && !observation.Allowed) ||
				(current.RiskClass == QueryGuardSafeRead && observation.Allowed)
		observation.ReasonMatch = current.ExpectedReason == "" || current.ExpectedReason == observation.RejectionReason

		summary.Cases++
		if observation.DecisionMatch {
			summary.Correct++
		} else {
			summary.MismatchedCases = append(summary.MismatchedCases, current.CaseID)
		}
		if current.RiskClass == QueryGuardHighRiskWrite {
			summary.HighRiskCases++
			if !observation.Allowed {
				summary.BlockedHighRisk++
			}
		} else {
			summary.SafeReadCases++
			if observation.Allowed {
				summary.AcceptedSafeReads++
			}
		}
		if current.ExpectedReason != "" {
			summary.ExpectedReasonCases++
			if observation.ReasonMatch {
				summary.MatchedReasons++
			}
		}
		observations = append(observations, observation)
	}
	summary.DatasetVersion = version
	summary.Accuracy = float64(summary.Correct) / float64(summary.Cases)
	if summary.HighRiskCases > 0 {
		summary.HighRiskBlockingRate = float64(summary.BlockedHighRisk) / float64(summary.HighRiskCases)
	}
	if summary.SafeReadCases > 0 {
		summary.SafeReadAcceptanceRate = float64(summary.AcceptedSafeReads) / float64(summary.SafeReadCases)
	}
	if summary.ExpectedReasonCases > 0 {
		summary.ReasonMatchRate = float64(summary.MatchedReasons) / float64(summary.ExpectedReasonCases)
	}
	if len(summary.RejectionReasons) == 0 {
		summary.RejectionReasons = nil
	}
	return observations, summary, nil
}

func hashEvaluationQuery(query string) string {
	digest := sha256.Sum256([]byte(query))
	return "sha256:" + hex.EncodeToString(digest[:])
}
