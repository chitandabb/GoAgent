package knowledge

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
)

type AdvancedRetrievalVariant string

const (
	AdvancedRetrievalBaseline   AdvancedRetrievalVariant = "baseline"
	AdvancedRetrievalExperiment AdvancedRetrievalVariant = "experiment"
	queryRewriteNotObserved     QueryRewriteStatus       = "not_observed"
)

func (v AdvancedRetrievalVariant) Valid() bool {
	return v == AdvancedRetrievalBaseline || v == AdvancedRetrievalExperiment
}

type RetrievalQueryMode string

const (
	RetrievalQueryOriginal RetrievalQueryMode = "original"
	RetrievalQueryRewrite  RetrievalQueryMode = "rewrite"
)

func (m RetrievalQueryMode) Valid() bool {
	return m == RetrievalQueryOriginal || m == RetrievalQueryRewrite
}

type RetrievalContextMode string

const (
	RetrievalContextChild  RetrievalContextMode = "child"
	RetrievalContextParent RetrievalContextMode = "parent"
)

func (m RetrievalContextMode) Valid() bool {
	return m == RetrievalContextChild || m == RetrievalContextParent
}

type RetrievalEvaluationChunkRef struct {
	DocumentKey   string `json:"documentKey"`
	Ordinal       int    `json:"ordinal"`
	ContentSHA256 string `json:"contentSha256"`
}

func (r RetrievalEvaluationChunkRef) Validate() error {
	if !retrievalEvaluationIDPattern.MatchString(r.DocumentKey) || r.Ordinal < 0 || r.Ordinal > 999_999 ||
		!validSHA256Hex(r.ContentSHA256) {
		return errors.New("retrieval evaluation chunk reference is invalid")
	}
	return nil
}

func (r RetrievalEvaluationChunkRef) key() string {
	return fmt.Sprintf("%s:%d:%s", r.DocumentKey, r.Ordinal, r.ContentSHA256)
}

func (r RetrievalEvaluationChunkRef) locationKey() string {
	return fmt.Sprintf("%s:%d", r.DocumentKey, r.Ordinal)
}

type AdvancedRetrievalEvaluationCase struct {
	DatasetVersion       string                        `json:"datasetVersion"`
	CaseID               string                        `json:"caseId"`
	Query                string                        `json:"query"`
	RelevantDocumentKeys []string                      `json:"relevantDocumentKeys"`
	RelevantChunks       []RetrievalEvaluationChunkRef `json:"relevantChunks"`
	K                    int                           `json:"k"`
	Tags                 []string                      `json:"tags,omitempty"`
}

func (c AdvancedRetrievalEvaluationCase) Validate() error {
	if !retrievalEvaluationIDPattern.MatchString(c.DatasetVersion) ||
		!retrievalEvaluationIDPattern.MatchString(c.CaseID) {
		return errors.New("advanced retrieval datasetVersion and caseId must be stable identifiers")
	}
	if strings.TrimSpace(c.Query) == "" || c.Query != strings.TrimSpace(c.Query) ||
		len([]rune(c.Query)) > MaxKnowledgeSearchQueryRunes {
		return errors.New("advanced retrieval query is invalid")
	}
	if c.K < 1 || c.K > MaxKnowledgeSearchLimit || len(c.RelevantDocumentKeys) == 0 ||
		len(c.RelevantDocumentKeys) > 20 || len(c.RelevantChunks) == 0 || len(c.RelevantChunks) > 200 {
		return errors.New("advanced retrieval relevance dimensions are invalid")
	}
	documents := make(map[string]struct{}, len(c.RelevantDocumentKeys))
	for _, documentKey := range c.RelevantDocumentKeys {
		if !retrievalEvaluationIDPattern.MatchString(documentKey) {
			return errors.New("advanced retrieval relevant document key is invalid")
		}
		if _, exists := documents[documentKey]; exists {
			return errors.New("advanced retrieval relevant document keys must be unique")
		}
		documents[documentKey] = struct{}{}
	}
	chunks := make(map[string]struct{}, len(c.RelevantChunks))
	for _, chunk := range c.RelevantChunks {
		if err := chunk.Validate(); err != nil {
			return err
		}
		if _, exists := documents[chunk.DocumentKey]; !exists {
			return errors.New("advanced retrieval chunk references a non-relevant document")
		}
		if _, exists := chunks[chunk.locationKey()]; exists {
			return errors.New("advanced retrieval relevant chunk locations must be unique")
		}
		chunks[chunk.locationKey()] = struct{}{}
	}
	tags := make(map[string]struct{}, len(c.Tags))
	for _, tag := range c.Tags {
		if !retrievalEvaluationIDPattern.MatchString(tag) {
			return errors.New("advanced retrieval tag is invalid")
		}
		if _, exists := tags[tag]; exists {
			return errors.New("advanced retrieval tags must be unique")
		}
		tags[tag] = struct{}{}
	}
	return nil
}

var retrievalEvaluationLabelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)

type AdvancedRetrievalObservation struct {
	DatasetVersion string                   `json:"datasetVersion"`
	CaseID         string                   `json:"caseId"`
	Variant        AdvancedRetrievalVariant `json:"variant"`
	RunID          string                   `json:"runId"`
	Query          string                   `json:"query"`
	K              int                      `json:"k"`

	RetrieverVersion string `json:"retrieverVersion"`
	EmbeddingProfile string `json:"embeddingProfile,omitempty"`
	RerankProfile    string `json:"rerankProfile,omitempty"`

	QueryMode            RetrievalQueryMode `json:"queryMode"`
	FTSQueryCount        int                `json:"ftsQueryCount"`
	VectorQueryCount     int                `json:"vectorQueryCount"`
	QueryRewriteStatus   QueryRewriteStatus `json:"queryRewriteStatus"`
	RewriteApplied       bool               `json:"rewriteApplied"`
	RewriteProvider      string             `json:"rewriteProvider,omitempty"`
	RewriteModelID       string             `json:"rewriteModelId,omitempty"`
	RewritePromptVersion string             `json:"rewritePromptVersion,omitempty"`
	RewriteUsage         QueryRewriteUsage  `json:"rewriteUsage"`

	ContextMode             RetrievalContextMode          `json:"contextMode"`
	ContextExpansionEnabled bool                          `json:"contextExpansionEnabled"`
	ContextExpanded         bool                          `json:"contextExpanded"`
	ReturnedDocumentKeys    []string                      `json:"returnedDocumentKeys"`
	ReturnedHitChunks       []RetrievalEvaluationChunkRef `json:"returnedHitChunks"`
	ReturnedContextChunks   []RetrievalEvaluationChunkRef `json:"returnedContextChunks,omitempty"`
	HitContextRunes         int                           `json:"hitContextRunes"`
	ExpandedContextRunes    int                           `json:"expandedContextRunes"`
	DegradedChannels        []string                      `json:"degradedChannels,omitempty"`
	DurationMillis          float64                       `json:"durationMillis"`
	ErrorType               string                        `json:"errorType,omitempty"`
}

func (o AdvancedRetrievalObservation) Validate() error {
	if !retrievalEvaluationIDPattern.MatchString(o.DatasetVersion) ||
		!retrievalEvaluationIDPattern.MatchString(o.CaseID) || !retrievalEvaluationLabelPattern.MatchString(o.RunID) ||
		!o.Variant.Valid() || strings.TrimSpace(o.Query) == "" || o.Query != strings.TrimSpace(o.Query) ||
		o.K < 1 || o.K > MaxKnowledgeSearchLimit {
		return errors.New("advanced retrieval observation identity is invalid")
	}
	if !retrievalEvaluationIDPattern.MatchString(o.RetrieverVersion) ||
		!validOptionalEvaluationID(o.EmbeddingProfile) || !validOptionalEvaluationID(o.RerankProfile) ||
		!o.QueryMode.Valid() || !o.ContextMode.Valid() {
		return errors.New("advanced retrieval observation arm is invalid")
	}
	if o.FTSQueryCount < 0 || o.FTSQueryCount > MaxQuerySubqueries+2 ||
		o.VectorQueryCount < 0 || o.VectorQueryCount > MaxQuerySubqueries+2 ||
		(o.FTSQueryCount+o.VectorQueryCount == 0 && o.ErrorType == "") {
		return errors.New("advanced retrieval query counts are invalid")
	}
	if err := o.validateRewrite(); err != nil {
		return err
	}
	if err := o.validateContext(); err != nil {
		return err
	}
	if math.IsNaN(o.DurationMillis) || math.IsInf(o.DurationMillis, 0) || o.DurationMillis < 0 ||
		o.HitContextRunes < 0 || o.ExpandedContextRunes < 0 {
		return errors.New("advanced retrieval duration or context size is invalid")
	}
	if len(o.ReturnedHitChunks) > o.K || len(o.ReturnedContextChunks) > 6000 ||
		(len(o.ReturnedHitChunks) > 0 && o.HitContextRunes == 0) ||
		(len(o.ReturnedContextChunks) > 0 && o.ExpandedContextRunes == 0) {
		return errors.New("advanced retrieval returned context dimensions are invalid")
	}
	if len(o.ReturnedDocumentKeys) > o.K || hasDuplicateEvaluationStrings(o.ReturnedDocumentKeys) {
		return errors.New("advanced retrieval returned documents are invalid")
	}
	documents := make(map[string]struct{}, len(o.ReturnedDocumentKeys))
	for _, documentKey := range o.ReturnedDocumentKeys {
		if !retrievalEvaluationIDPattern.MatchString(documentKey) {
			return errors.New("advanced retrieval returned document key is invalid")
		}
		documents[documentKey] = struct{}{}
	}
	seenChunks := make(map[string]struct{}, len(o.ReturnedHitChunks)+len(o.ReturnedContextChunks))
	for _, group := range [][]RetrievalEvaluationChunkRef{o.ReturnedHitChunks, o.ReturnedContextChunks} {
		for _, chunk := range group {
			if err := chunk.Validate(); err != nil {
				return err
			}
			if _, exists := documents[chunk.DocumentKey]; !exists {
				return errors.New("advanced retrieval chunk references an unreturned document")
			}
			if _, exists := seenChunks[chunk.locationKey()]; exists {
				return errors.New("advanced retrieval returned chunk locations must be unique")
			}
			seenChunks[chunk.locationKey()] = struct{}{}
		}
	}
	if hasDuplicateEvaluationStrings(o.DegradedChannels) {
		return errors.New("advanced retrieval degraded channels must be unique")
	}
	for _, channel := range o.DegradedChannels {
		if !retrievalEvaluationIDPattern.MatchString(channel) {
			return errors.New("advanced retrieval degraded channel is invalid")
		}
	}
	if o.ErrorType != "" && !retrievalEvaluationIDPattern.MatchString(o.ErrorType) {
		return errors.New("advanced retrieval error type is invalid")
	}
	return nil
}

func (o AdvancedRetrievalObservation) validateRewrite() error {
	if err := o.RewriteUsage.Validate(); err != nil {
		return err
	}
	if o.QueryMode == RetrievalQueryOriginal {
		if o.FTSQueryCount > 1 || o.VectorQueryCount > 1 || o.QueryRewriteStatus != QueryRewriteDisabled ||
			o.RewriteApplied || o.RewriteProvider != "" || o.RewriteModelID != "" ||
			o.RewritePromptVersion != "" || o.RewriteUsage != (QueryRewriteUsage{}) {
			return errors.New("advanced retrieval original-query observation is invalid")
		}
		return nil
	}
	if !retrievalEvaluationLabelPattern.MatchString(o.RewriteProvider) ||
		!retrievalEvaluationLabelPattern.MatchString(o.RewriteModelID) ||
		!retrievalEvaluationLabelPattern.MatchString(o.RewritePromptVersion) {
		return errors.New("advanced retrieval rewrite metadata is invalid")
	}
	switch o.QueryRewriteStatus {
	case QueryRewriteAccepted:
		return nil
	case QueryRewriteProviderFailed:
		if o.RewriteApplied || o.RewriteUsage != (QueryRewriteUsage{}) {
			return errors.New("advanced retrieval provider-failed rewrite is invalid")
		}
		return nil
	case QueryRewritePolicyRejected:
		if o.RewriteApplied {
			return errors.New("advanced retrieval rejected rewrite cannot be applied")
		}
		return nil
	case queryRewriteNotObserved:
		if o.ErrorType == "" || o.RewriteApplied || o.RewriteUsage != (QueryRewriteUsage{}) {
			return errors.New("advanced retrieval unobserved rewrite is invalid")
		}
		return nil
	default:
		return errors.New("advanced retrieval rewrite status is invalid")
	}
}

func (o AdvancedRetrievalObservation) validateContext() error {
	if o.ContextMode == RetrievalContextChild {
		if o.ContextExpansionEnabled || o.ContextExpanded || len(o.ReturnedContextChunks) != 0 ||
			o.ExpandedContextRunes != 0 {
			return errors.New("advanced retrieval child-only context is invalid")
		}
		return nil
	}
	if !o.ContextExpansionEnabled || o.ContextExpanded != (len(o.ReturnedContextChunks) > 0) {
		return errors.New("advanced retrieval parent context state is invalid")
	}
	return nil
}

type AdvancedRetrievalArm struct {
	RetrieverVersion     string               `json:"retrieverVersion"`
	EmbeddingProfile     string               `json:"embeddingProfile,omitempty"`
	RerankProfile        string               `json:"rerankProfile,omitempty"`
	QueryMode            RetrievalQueryMode   `json:"queryMode"`
	ContextMode          RetrievalContextMode `json:"contextMode"`
	RewriteProvider      string               `json:"rewriteProvider,omitempty"`
	RewriteModelID       string               `json:"rewriteModelId,omitempty"`
	RewritePromptVersion string               `json:"rewritePromptVersion,omitempty"`
}

func (a AdvancedRetrievalArm) Validate() error {
	if !retrievalEvaluationIDPattern.MatchString(a.RetrieverVersion) ||
		!validOptionalEvaluationID(a.EmbeddingProfile) || !validOptionalEvaluationID(a.RerankProfile) ||
		!a.QueryMode.Valid() || !a.ContextMode.Valid() {
		return errors.New("advanced retrieval arm is invalid")
	}
	if a.QueryMode == RetrievalQueryOriginal {
		if a.RewriteProvider != "" || a.RewriteModelID != "" || a.RewritePromptVersion != "" {
			return errors.New("advanced retrieval original-query arm has rewrite metadata")
		}
		return nil
	}
	if !retrievalEvaluationLabelPattern.MatchString(a.RewriteProvider) ||
		!retrievalEvaluationLabelPattern.MatchString(a.RewriteModelID) ||
		!retrievalEvaluationLabelPattern.MatchString(a.RewritePromptVersion) {
		return errors.New("advanced retrieval rewrite arm metadata is invalid")
	}
	return nil
}

type AdvancedRetrievalVariantSummary struct {
	Arm                          AdvancedRetrievalArm `json:"arm"`
	Runs                         int                  `json:"runs"`
	FailedRuns                   int                  `json:"failedRuns"`
	DegradedRuns                 int                  `json:"degradedRuns"`
	HitsAtK                      int                  `json:"hitsAtK"`
	HitRateAtK                   float64              `json:"hitRateAtK"`
	RecallAtK                    float64              `json:"recallAtK"`
	MeanReciprocalRank           float64              `json:"meanReciprocalRank"`
	ContextPrecision             float64              `json:"contextPrecision"`
	ContextRecall                float64              `json:"contextRecall"`
	AverageFTSQueryCount         float64              `json:"averageFtsQueryCount"`
	AverageVectorQueryCount      float64              `json:"averageVectorQueryCount"`
	AverageHitContextRunes       float64              `json:"averageHitContextRunes"`
	AverageExpandedContextRunes  float64              `json:"averageExpandedContextRunes"`
	AverageContextExpansionRatio float64              `json:"averageContextExpansionRatio"`
	AverageDurationMillis        float64              `json:"averageDurationMillis"`
	RewriteAccepted              int                  `json:"rewriteAccepted"`
	RewriteProviderFailed        int                  `json:"rewriteProviderFailed"`
	RewritePolicyRejected        int                  `json:"rewritePolicyRejected"`
	RewriteNotObserved           int                  `json:"rewriteNotObserved"`
	RewritePromptTokens          int                  `json:"rewritePromptTokens"`
	RewriteCompletionTokens      int                  `json:"rewriteCompletionTokens"`
	RewriteTotalTokens           int                  `json:"rewriteTotalTokens"`
}

type AdvancedRetrievalDelta struct {
	HitRateAtK              float64 `json:"hitRateAtK"`
	RecallAtK               float64 `json:"recallAtK"`
	MeanReciprocalRank      float64 `json:"meanReciprocalRank"`
	ContextPrecision        float64 `json:"contextPrecision"`
	ContextRecall           float64 `json:"contextRecall"`
	QueryAmplificationRatio float64 `json:"queryAmplificationRatio"`
	ContextRuneChangeRate   float64 `json:"contextRuneChangeRate"`
	DurationChangeRate      float64 `json:"durationChangeRate"`
}

type AdvancedRetrievalEvaluationSummary struct {
	DatasetVersion string                          `json:"datasetVersion"`
	K              int                             `json:"k"`
	Cases          int                             `json:"cases"`
	Runs           int                             `json:"runs"`
	PairedCases    int                             `json:"pairedCases"`
	Baseline       AdvancedRetrievalVariantSummary `json:"baseline"`
	Experiment     AdvancedRetrievalVariantSummary `json:"experiment"`
	Delta          AdvancedRetrievalDelta          `json:"delta"`
}

type advancedRetrievalScore struct {
	observation      AdvancedRetrievalObservation
	hitAtK           bool
	documentRecall   float64
	firstRank        int
	contextPrecision float64
	contextRecall    float64
	expansionRatio   float64
}

func EvaluateAdvancedRetrieval(
	cases []AdvancedRetrievalEvaluationCase,
	observations []AdvancedRetrievalObservation,
) (AdvancedRetrievalEvaluationSummary, error) {
	caseByID, version, evaluationK, err := indexAdvancedRetrievalCases(cases)
	if err != nil {
		return AdvancedRetrievalEvaluationSummary{}, err
	}
	if len(observations) != len(cases)*2 {
		return AdvancedRetrievalEvaluationSummary{}, errors.New("advanced retrieval evaluation requires one complete pair per case")
	}
	pairs := make(map[string]map[AdvancedRetrievalVariant]AdvancedRetrievalObservation, len(cases))
	seenRuns := make(map[string]struct{}, len(observations))
	scores := make([]advancedRetrievalScore, 0, len(observations))
	for index, observation := range observations {
		if err := observation.Validate(); err != nil {
			return AdvancedRetrievalEvaluationSummary{}, fmt.Errorf("observation %d: %w", index, err)
		}
		definition, exists := caseByID[observation.CaseID]
		if !exists || observation.DatasetVersion != version || observation.Query != definition.Query ||
			observation.K != definition.K {
			return AdvancedRetrievalEvaluationSummary{}, fmt.Errorf("observation %q does not match its case", observation.RunID)
		}
		if _, exists := seenRuns[observation.RunID]; exists {
			return AdvancedRetrievalEvaluationSummary{}, fmt.Errorf("duplicate runId %q", observation.RunID)
		}
		seenRuns[observation.RunID] = struct{}{}
		if pairs[observation.CaseID] == nil {
			pairs[observation.CaseID] = make(map[AdvancedRetrievalVariant]AdvancedRetrievalObservation, 2)
		}
		if _, exists := pairs[observation.CaseID][observation.Variant]; exists {
			return AdvancedRetrievalEvaluationSummary{}, fmt.Errorf("case %q has duplicate %s observations", observation.CaseID, observation.Variant)
		}
		pairs[observation.CaseID][observation.Variant] = observation
		scores = append(scores, scoreAdvancedRetrieval(definition, observation))
	}
	for caseID, pair := range pairs {
		baseline, baselineOK := pair[AdvancedRetrievalBaseline]
		experiment, experimentOK := pair[AdvancedRetrievalExperiment]
		if !baselineOK || !experimentOK {
			return AdvancedRetrievalEvaluationSummary{}, fmt.Errorf("case %q does not contain a complete pair", caseID)
		}
		if baseline.RetrieverVersion != experiment.RetrieverVersion ||
			baseline.EmbeddingProfile != experiment.EmbeddingProfile || baseline.RerankProfile != experiment.RerankProfile {
			return AdvancedRetrievalEvaluationSummary{}, fmt.Errorf("case %q changes the underlying retrieval profile", caseID)
		}
		if err := validateAdvancedRetrievalPair(baseline, experiment); err != nil {
			return AdvancedRetrievalEvaluationSummary{}, fmt.Errorf("case %q: %w", caseID, err)
		}
	}
	baseline, err := summarizeAdvancedRetrieval(scores, AdvancedRetrievalBaseline)
	if err != nil {
		return AdvancedRetrievalEvaluationSummary{}, err
	}
	experiment, err := summarizeAdvancedRetrieval(scores, AdvancedRetrievalExperiment)
	if err != nil {
		return AdvancedRetrievalEvaluationSummary{}, err
	}
	if baseline.Arm == experiment.Arm {
		return AdvancedRetrievalEvaluationSummary{}, errors.New("advanced retrieval baseline and experiment arms are identical")
	}
	summary := AdvancedRetrievalEvaluationSummary{
		DatasetVersion: version, K: evaluationK, Cases: len(cases), Runs: len(observations), PairedCases: len(cases),
		Baseline: baseline, Experiment: experiment,
	}
	summary.Delta = AdvancedRetrievalDelta{
		HitRateAtK:         experiment.HitRateAtK - baseline.HitRateAtK,
		RecallAtK:          experiment.RecallAtK - baseline.RecallAtK,
		MeanReciprocalRank: experiment.MeanReciprocalRank - baseline.MeanReciprocalRank,
		ContextPrecision:   experiment.ContextPrecision - baseline.ContextPrecision,
		ContextRecall:      experiment.ContextRecall - baseline.ContextRecall,
		QueryAmplificationRatio: ratioOrZero(
			experiment.AverageFTSQueryCount+experiment.AverageVectorQueryCount,
			baseline.AverageFTSQueryCount+baseline.AverageVectorQueryCount,
		),
		ContextRuneChangeRate: changeRateOrZero(
			experiment.AverageHitContextRunes+experiment.AverageExpandedContextRunes,
			baseline.AverageHitContextRunes+baseline.AverageExpandedContextRunes,
		),
		DurationChangeRate: changeRateOrZero(experiment.AverageDurationMillis, baseline.AverageDurationMillis),
	}
	return summary, nil
}

func validateAdvancedRetrievalPair(baseline, experiment AdvancedRetrievalObservation) error {
	return validateAdvancedRetrievalArms(advancedRetrievalArm(baseline), advancedRetrievalArm(experiment))
}

func validateAdvancedRetrievalArms(baseline, experiment AdvancedRetrievalArm) error {
	if err := baseline.Validate(); err != nil {
		return err
	}
	if err := experiment.Validate(); err != nil {
		return err
	}
	if baseline.RetrieverVersion != experiment.RetrieverVersion ||
		baseline.EmbeddingProfile != experiment.EmbeddingProfile || baseline.RerankProfile != experiment.RerankProfile {
		return errors.New("paired observations change the underlying retrieval profile")
	}
	queryChanged := baseline.QueryMode != experiment.QueryMode
	contextChanged := baseline.ContextMode != experiment.ContextMode
	if queryChanged == contextChanged {
		return errors.New("paired observations must change exactly one evaluation axis")
	}
	if queryChanged {
		if baseline.QueryMode != RetrievalQueryOriginal || experiment.QueryMode != RetrievalQueryRewrite {
			return errors.New("query evaluation must compare original baseline with rewrite experiment")
		}
		return nil
	}
	if baseline.ContextMode != RetrievalContextChild || experiment.ContextMode != RetrievalContextParent {
		return errors.New("context evaluation must compare child baseline with parent experiment")
	}
	if baseline.RewriteProvider != experiment.RewriteProvider || baseline.RewriteModelID != experiment.RewriteModelID ||
		baseline.RewritePromptVersion != experiment.RewritePromptVersion {
		return errors.New("context evaluation changes query rewrite metadata")
	}
	return nil
}

func indexAdvancedRetrievalCases(
	cases []AdvancedRetrievalEvaluationCase,
) (map[string]AdvancedRetrievalEvaluationCase, string, int, error) {
	if len(cases) == 0 {
		return nil, "", 0, errors.New("advanced retrieval dataset contains no cases")
	}
	indexed := make(map[string]AdvancedRetrievalEvaluationCase, len(cases))
	version := ""
	evaluationK := 0
	for index, definition := range cases {
		if err := definition.Validate(); err != nil {
			return nil, "", 0, fmt.Errorf("case %d: %w", index, err)
		}
		if version == "" {
			version = definition.DatasetVersion
		} else if definition.DatasetVersion != version {
			return nil, "", 0, errors.New("advanced retrieval dataset mixes versions")
		}
		if evaluationK == 0 {
			evaluationK = definition.K
		} else if definition.K != evaluationK {
			return nil, "", 0, errors.New("advanced retrieval dataset mixes K values")
		}
		if _, exists := indexed[definition.CaseID]; exists {
			return nil, "", 0, fmt.Errorf("duplicate caseId %q", definition.CaseID)
		}
		indexed[definition.CaseID] = definition
	}
	return indexed, version, evaluationK, nil
}

func scoreAdvancedRetrieval(
	definition AdvancedRetrievalEvaluationCase,
	observation AdvancedRetrievalObservation,
) advancedRetrievalScore {
	rank := firstRelevantRank(definition.RelevantDocumentKeys, observation.ReturnedDocumentKeys)
	relevantDocuments := make(map[string]struct{}, len(definition.RelevantDocumentKeys))
	for _, documentKey := range definition.RelevantDocumentKeys {
		relevantDocuments[documentKey] = struct{}{}
	}
	returnedRelevantDocuments := 0
	for _, documentKey := range observation.ReturnedDocumentKeys {
		if _, exists := relevantDocuments[documentKey]; exists {
			returnedRelevantDocuments++
		}
	}
	relevant := make(map[string]struct{}, len(definition.RelevantChunks))
	for _, chunk := range definition.RelevantChunks {
		relevant[chunk.key()] = struct{}{}
	}
	returned := append(append([]RetrievalEvaluationChunkRef(nil), observation.ReturnedHitChunks...), observation.ReturnedContextChunks...)
	relevantHits := 0
	for _, chunk := range returned {
		if _, exists := relevant[chunk.key()]; exists {
			relevantHits++
		}
	}
	precision := 0.0
	if len(returned) > 0 {
		precision = float64(relevantHits) / float64(len(returned))
	}
	recall := float64(relevantHits) / float64(len(relevant))
	expansionRatio := 0.0
	if observation.HitContextRunes > 0 {
		expansionRatio = float64(observation.HitContextRunes+observation.ExpandedContextRunes) /
			float64(observation.HitContextRunes)
	}
	return advancedRetrievalScore{
		observation: observation, hitAtK: rank > 0, firstRank: rank,
		documentRecall:   float64(returnedRelevantDocuments) / float64(len(relevantDocuments)),
		contextPrecision: precision, contextRecall: recall, expansionRatio: expansionRatio,
	}
}

func summarizeAdvancedRetrieval(
	scores []advancedRetrievalScore,
	variant AdvancedRetrievalVariant,
) (AdvancedRetrievalVariantSummary, error) {
	var summary AdvancedRetrievalVariantSummary
	armSet := false
	var reciprocalRank, documentRecall, precision, recall, ftsQueries, vectorQueries float64
	var hitRunes, expandedRunes, expansionRatio, duration float64
	for _, score := range scores {
		if score.observation.Variant != variant {
			continue
		}
		arm := advancedRetrievalArm(score.observation)
		if !armSet {
			summary.Arm = arm
			armSet = true
		} else if summary.Arm != arm {
			return AdvancedRetrievalVariantSummary{}, fmt.Errorf("%s observations mix evaluation arms", variant)
		}
		summary.Runs++
		if score.observation.ErrorType != "" {
			summary.FailedRuns++
		}
		if len(score.observation.DegradedChannels) > 0 {
			summary.DegradedRuns++
		}
		if score.hitAtK {
			summary.HitsAtK++
			reciprocalRank += 1 / float64(score.firstRank)
		}
		documentRecall += score.documentRecall
		precision += score.contextPrecision
		recall += score.contextRecall
		ftsQueries += float64(score.observation.FTSQueryCount)
		vectorQueries += float64(score.observation.VectorQueryCount)
		hitRunes += float64(score.observation.HitContextRunes)
		expandedRunes += float64(score.observation.ExpandedContextRunes)
		expansionRatio += score.expansionRatio
		duration += score.observation.DurationMillis
		summary.RewritePromptTokens += score.observation.RewriteUsage.PromptTokens
		summary.RewriteCompletionTokens += score.observation.RewriteUsage.CompletionTokens
		summary.RewriteTotalTokens += score.observation.RewriteUsage.TotalTokens
		switch score.observation.QueryRewriteStatus {
		case QueryRewriteAccepted:
			summary.RewriteAccepted++
		case QueryRewriteProviderFailed:
			summary.RewriteProviderFailed++
		case QueryRewritePolicyRejected:
			summary.RewritePolicyRejected++
		case queryRewriteNotObserved:
			summary.RewriteNotObserved++
		}
	}
	if summary.Runs == 0 {
		return AdvancedRetrievalVariantSummary{}, fmt.Errorf("evaluation contains no %s observations", variant)
	}
	runs := float64(summary.Runs)
	summary.HitRateAtK = float64(summary.HitsAtK) / runs
	summary.RecallAtK = documentRecall / runs
	summary.MeanReciprocalRank = reciprocalRank / runs
	summary.ContextPrecision = precision / runs
	summary.ContextRecall = recall / runs
	summary.AverageFTSQueryCount = ftsQueries / runs
	summary.AverageVectorQueryCount = vectorQueries / runs
	summary.AverageHitContextRunes = hitRunes / runs
	summary.AverageExpandedContextRunes = expandedRunes / runs
	summary.AverageContextExpansionRatio = expansionRatio / runs
	summary.AverageDurationMillis = duration / runs
	return summary, nil
}

func advancedRetrievalArm(o AdvancedRetrievalObservation) AdvancedRetrievalArm {
	return AdvancedRetrievalArm{
		RetrieverVersion: o.RetrieverVersion, EmbeddingProfile: o.EmbeddingProfile,
		RerankProfile: o.RerankProfile, QueryMode: o.QueryMode, ContextMode: o.ContextMode,
		RewriteProvider: o.RewriteProvider, RewriteModelID: o.RewriteModelID,
		RewritePromptVersion: o.RewritePromptVersion,
	}
}

func validOptionalEvaluationID(value string) bool {
	return value == "" || retrievalEvaluationIDPattern.MatchString(value)
}

func hasDuplicateEvaluationStrings(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}

func ratioOrZero(numerator, denominator float64) float64 {
	if denominator <= 0 {
		return 0
	}
	return numerator / denominator
}

func changeRateOrZero(experiment, baseline float64) float64 {
	if baseline <= 0 {
		return 0
	}
	return (experiment - baseline) / baseline
}
