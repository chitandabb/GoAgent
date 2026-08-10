package agent

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
)

const (
	maxConversationQualitySources  = 200
	maxConversationQualitySignals  = 100
	maxConversationQualityDuration = 10 * 60 * 1000
)

type ConversationQualityOutcome string

const (
	ConversationQualityAnswered             ConversationQualityOutcome = "answered"
	ConversationQualityInsufficientEvidence ConversationQualityOutcome = "insufficient_evidence"
	ConversationQualityDegraded             ConversationQualityOutcome = "degraded"
	ConversationQualityFailed               ConversationQualityOutcome = "failed"
)

func (o ConversationQualityOutcome) Valid() bool {
	switch o {
	case ConversationQualityAnswered, ConversationQualityInsufficientEvidence,
		ConversationQualityDegraded, ConversationQualityFailed:
		return true
	default:
		return false
	}
}

type ConversationQualityObservationKind string

const (
	ConversationQualitySeededContract ConversationQualityObservationKind = "seeded_contract"
	ConversationQualityRecordedRun    ConversationQualityObservationKind = "recorded_run"
)

func (k ConversationQualityObservationKind) Valid() bool {
	return k == ConversationQualitySeededContract || k == ConversationQualityRecordedRun
}

type ConversationQualitySource struct {
	SourceType      EvidenceSourceType `json:"sourceType"`
	SourceRef       string             `json:"sourceRef"`
	ContentSHA256   string             `json:"contentSha256"`
	PreviewRequired bool               `json:"previewRequired"`
}

func (s ConversationQualitySource) Validate() error {
	if !conversationQualitySourceType(s.SourceType) || !validConversationQualitySourceRef(s.SourceType, s.SourceRef) ||
		!validConversationQualitySHA256(s.ContentSHA256) {
		return errors.New("conversation quality source is invalid")
	}
	return nil
}

type ConversationQualityCitation struct {
	SourceType           EvidenceSourceType `json:"sourceType"`
	SourceRef            string             `json:"sourceRef"`
	ContentSHA256        string             `json:"contentSha256"`
	PreviewContentSHA256 string             `json:"previewContentSha256,omitempty"`
}

func (c ConversationQualityCitation) Validate() error {
	if err := (ConversationQualitySource{
		SourceType: c.SourceType, SourceRef: c.SourceRef, ContentSHA256: c.ContentSHA256,
	}).Validate(); err != nil {
		return err
	}
	if c.PreviewContentSHA256 != "" && !validConversationQualitySHA256(c.PreviewContentSHA256) {
		return errors.New("conversation quality citation preview hash is invalid")
	}
	return nil
}

type ConversationQualityJudgeObservation struct {
	Method            string  `json:"method"`
	JudgeID           string  `json:"judgeId"`
	RubricVersion     string  `json:"rubricVersion"`
	Faithfulness      float64 `json:"faithfulness"`
	AnswerRelevance   float64 `json:"answerRelevance"`
	CitationAlignment float64 `json:"citationAlignment"`
}

func (j ConversationQualityJudgeObservation) Validate() error {
	if j.Method != "human" && j.Method != "llm" {
		return errors.New("conversation quality judge method is invalid")
	}
	if !conversationQualityLabelPattern.MatchString(j.JudgeID) ||
		!conversationQualityLabelPattern.MatchString(j.RubricVersion) {
		return errors.New("conversation quality judge identity is invalid")
	}
	for _, score := range []float64{j.Faithfulness, j.AnswerRelevance, j.CitationAlignment} {
		if math.IsNaN(score) || math.IsInf(score, 0) || score < 0 || score > 1 {
			return errors.New("conversation quality judge score must be between zero and one")
		}
	}
	return nil
}

type ConversationQualityCase struct {
	DatasetVersion           string                      `json:"datasetVersion"`
	CaseID                   string                      `json:"caseId"`
	UserQuery                string                      `json:"userQuery"`
	RetrievalMaxResults      int                         `json:"retrievalMaxResults,omitempty"`
	RelevantSources          []ConversationQualitySource `json:"relevantSources,omitempty"`
	RequiredCitationRefs     []string                    `json:"requiredCitationRefs,omitempty"`
	RequiredAnswerTerms      []string                    `json:"requiredAnswerTerms,omitempty"`
	ForbiddenAnswerTerms     []string                    `json:"forbiddenAnswerTerms,omitempty"`
	ExpectedOutcome          ConversationQualityOutcome  `json:"expectedOutcome"`
	ExpectedDegradedChannels []string                    `json:"expectedDegradedChannels,omitempty"`
	Tags                     []string                    `json:"tags,omitempty"`
}

func (c ConversationQualityCase) Validate() error {
	if !conversationQualityLabelPattern.MatchString(c.DatasetVersion) ||
		!conversationQualityLabelPattern.MatchString(c.CaseID) {
		return errors.New("conversation quality datasetVersion and caseId must be stable identifiers")
	}
	if strings.TrimSpace(c.UserQuery) == "" || c.UserQuery != strings.TrimSpace(c.UserQuery) ||
		len([]rune(c.UserQuery)) > 2_000 || c.RetrievalMaxResults < 0 ||
		c.RetrievalMaxResults > 20 || !c.ExpectedOutcome.Valid() {
		return errors.New("conversation quality query or expected outcome is invalid")
	}
	if len(c.RelevantSources) > maxConversationQualitySources ||
		len(c.RequiredCitationRefs) > maxConversationQualitySources ||
		len(c.RequiredAnswerTerms) > maxConversationQualitySignals ||
		len(c.ForbiddenAnswerTerms) > maxConversationQualitySignals {
		return errors.New("conversation quality case exceeds a bounded dimension")
	}
	sources := make(map[string]ConversationQualitySource, len(c.RelevantSources))
	for _, source := range c.RelevantSources {
		if err := source.Validate(); err != nil {
			return err
		}
		if _, exists := sources[source.SourceRef]; exists {
			return errors.New("conversation quality relevant source refs must be unique")
		}
		sources[source.SourceRef] = source
	}
	if hasDuplicateStrings(c.RequiredCitationRefs) {
		return errors.New("conversation quality required citation refs must be unique")
	}
	for _, sourceRef := range c.RequiredCitationRefs {
		if _, exists := sources[sourceRef]; !exists {
			return errors.New("conversation quality required citation is not a relevant source")
		}
	}
	if c.ExpectedOutcome == ConversationQualityAnswered &&
		(len(c.RelevantSources) == 0 || len(c.RequiredCitationRefs) == 0) {
		return errors.New("answered conversation quality cases require relevant and cited sources")
	}
	if err := validateConversationQualitySignals(c.RequiredAnswerTerms, "required answer terms"); err != nil {
		return err
	}
	if err := validateConversationQualitySignals(c.ForbiddenAnswerTerms, "forbidden answer terms"); err != nil {
		return err
	}
	for _, required := range c.RequiredAnswerTerms {
		if containsFold(c.ForbiddenAnswerTerms, required) {
			return errors.New("conversation quality answer term cannot be both required and forbidden")
		}
	}
	if err := validateConversationQualityLabels(c.ExpectedDegradedChannels, "expected degraded channels"); err != nil {
		return err
	}
	return validateConversationQualityLabels(c.Tags, "tags")
}

type ConversationQualityObservation struct {
	DatasetVersion   string                               `json:"datasetVersion"`
	CaseID           string                               `json:"caseId"`
	RunID            string                               `json:"runId"`
	ObservationKind  ConversationQualityObservationKind   `json:"observationKind"`
	Model            string                               `json:"model"`
	ModelVersion     string                               `json:"modelVersion"`
	PromptVersion    string                               `json:"promptVersion"`
	Answer           string                               `json:"answer,omitempty"`
	Outcome          ConversationQualityOutcome           `json:"outcome"`
	RetrievedSources []ConversationQualitySource          `json:"retrievedSources,omitempty"`
	Citations        []ConversationQualityCitation        `json:"citations,omitempty"`
	DegradedChannels []string                             `json:"degradedChannels,omitempty"`
	Usage            ModelUsage                           `json:"usage"`
	DurationMillis   int64                                `json:"durationMillis"`
	EstimatedCostCNY float64                              `json:"estimatedCostCny"`
	Judge            *ConversationQualityJudgeObservation `json:"judge,omitempty"`
	ErrorType        string                               `json:"errorType,omitempty"`
}

func (o ConversationQualityObservation) Validate() error {
	if !conversationQualityLabelPattern.MatchString(o.DatasetVersion) ||
		!conversationQualityLabelPattern.MatchString(o.CaseID) ||
		!conversationQualityLabelPattern.MatchString(o.RunID) || !o.ObservationKind.Valid() || !o.Outcome.Valid() {
		return errors.New("conversation quality observation identity is invalid")
	}
	if !conversationQualityDisplayLabel(o.Model) || !conversationQualityDisplayLabel(o.ModelVersion) ||
		!conversationQualityLabelPattern.MatchString(o.PromptVersion) {
		return errors.New("conversation quality model or prompt identity is invalid")
	}
	if o.Outcome != ConversationQualityFailed &&
		(strings.TrimSpace(o.Answer) == "" || o.Answer != strings.TrimSpace(o.Answer)) {
		return errors.New("non-failed conversation quality observation requires a trimmed answer")
	}
	if len([]rune(o.Answer)) > 20_000 || len(o.RetrievedSources) > maxConversationQualitySources ||
		len(o.Citations) > maxConversationQualitySources {
		return errors.New("conversation quality observation exceeds a bounded dimension")
	}
	seenRetrieved := make(map[string]struct{}, len(o.RetrievedSources))
	for _, source := range o.RetrievedSources {
		if err := source.Validate(); err != nil {
			return err
		}
		if _, exists := seenRetrieved[source.SourceRef]; exists {
			return errors.New("conversation quality retrieved source refs must be unique")
		}
		seenRetrieved[source.SourceRef] = struct{}{}
	}
	seenCitations := make(map[string]struct{}, len(o.Citations))
	for _, citation := range o.Citations {
		if err := citation.Validate(); err != nil {
			return err
		}
		if _, exists := seenCitations[citation.SourceRef]; exists {
			return errors.New("conversation quality citation refs must be unique")
		}
		seenCitations[citation.SourceRef] = struct{}{}
	}
	if err := validateConversationQualityLabels(o.DegradedChannels, "degraded channels"); err != nil {
		return err
	}
	if (o.Outcome == ConversationQualityFailed) != (o.ErrorType != "") ||
		(o.ErrorType != "" && !conversationQualityLabelPattern.MatchString(o.ErrorType)) {
		return errors.New("conversation quality error type is invalid")
	}
	if o.Usage.ModelCalls < 0 || o.Usage.PromptTokens < 0 || o.Usage.CompletionTokens < 0 ||
		o.Usage.TotalTokens < 0 || o.Usage.CachedTokens < 0 || o.Usage.ReasoningTokens < 0 ||
		o.Usage.TotalTokens < o.Usage.PromptTokens+o.Usage.CompletionTokens ||
		o.DurationMillis < 0 || o.DurationMillis > maxConversationQualityDuration ||
		math.IsNaN(o.EstimatedCostCNY) || math.IsInf(o.EstimatedCostCNY, 0) ||
		o.EstimatedCostCNY < 0 || o.EstimatedCostCNY > 1_000 {
		return errors.New("conversation quality usage, duration, or cost is invalid")
	}
	if o.ObservationKind == ConversationQualityRecordedRun && o.Outcome != ConversationQualityFailed &&
		o.Usage.ModelCalls < 1 {
		return errors.New("recorded conversation quality observation requires provider usage")
	}
	if o.Judge != nil {
		if err := o.Judge.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type ConversationQualitySourceSummary struct {
	RelevantSources        int     `json:"relevantSources"`
	RetrievedSources       int     `json:"retrievedSources"`
	CorrectRetrieved       int     `json:"correctRetrieved"`
	ContextPrecision       float64 `json:"contextPrecision"`
	ContextRecall          float64 `json:"contextRecall"`
	RequiredCitations      int     `json:"requiredCitations"`
	Citations              int     `json:"citations"`
	CorrectCitations       int     `json:"correctCitations"`
	CorrectRequired        int     `json:"correctRequiredCitations"`
	CitationPrecision      float64 `json:"citationPrecision"`
	CitationRecall         float64 `json:"citationRecall"`
	PreviewChecks          int     `json:"previewChecks"`
	PreviewMatches         int     `json:"previewMatches"`
	PreviewConsistencyRate float64 `json:"previewConsistencyRate"`
}

type ConversationQualitySummary struct {
	DatasetVersion                string                                                  `json:"datasetVersion"`
	ObservationKind               ConversationQualityObservationKind                      `json:"observationKind"`
	Cases                         int                                                     `json:"cases"`
	Runs                          int                                                     `json:"runs"`
	PassedRuns                    int                                                     `json:"passedRuns"`
	PassRate                      float64                                                 `json:"passRate"`
	OutcomeAccuracy               float64                                                 `json:"outcomeAccuracy"`
	ContextPrecision              float64                                                 `json:"contextPrecision"`
	ContextRecall                 float64                                                 `json:"contextRecall"`
	CitationPrecision             float64                                                 `json:"citationPrecision"`
	CitationRecall                float64                                                 `json:"citationRecall"`
	PreviewConsistencyRate        float64                                                 `json:"previewConsistencyRate"`
	RequiredAnswerTermRecall      float64                                                 `json:"requiredAnswerTermRecall"`
	ForbiddenAnswerTermHitRate    float64                                                 `json:"forbiddenAnswerTermHitRate"`
	ExpectedDegradedChannelRecall float64                                                 `json:"expectedDegradedChannelRecall"`
	JudgedRuns                    int                                                     `json:"judgedRuns"`
	AverageFaithfulness           float64                                                 `json:"averageFaithfulness"`
	AverageAnswerRelevance        float64                                                 `json:"averageAnswerRelevance"`
	AverageCitationAlignment      float64                                                 `json:"averageCitationAlignment"`
	FailedRuns                    int                                                     `json:"failedRuns"`
	DegradedRuns                  int                                                     `json:"degradedRuns"`
	PromptTokens                  int                                                     `json:"promptTokens"`
	CompletionTokens              int                                                     `json:"completionTokens"`
	TotalTokens                   int                                                     `json:"totalTokens"`
	AverageTokensPerRun           float64                                                 `json:"averageTokensPerRun"`
	P50DurationMillis             int64                                                   `json:"p50DurationMillis"`
	P95DurationMillis             int64                                                   `json:"p95DurationMillis"`
	TotalEstimatedCostCNY         float64                                                 `json:"totalEstimatedCostCny"`
	AverageEstimatedCostCNY       float64                                                 `json:"averageEstimatedCostCny"`
	EstimatedCostPerThousandCNY   float64                                                 `json:"estimatedCostPerThousandCny"`
	BySourceType                  map[EvidenceSourceType]ConversationQualitySourceSummary `json:"bySourceType"`
	FailureTypes                  map[string]int                                          `json:"failureTypes,omitempty"`
}

type conversationQualityScore struct {
	passed               bool
	outcomeCorrect       int
	relevant             int
	retrieved            int
	correctRetrieved     int
	requiredCitations    int
	citations            int
	correctCitations     int
	correctRequired      int
	previewChecks        int
	previewMatches       int
	requiredTerms        int
	requiredTermHits     int
	forbiddenTerms       int
	forbiddenTermHits    int
	expectedDegraded     int
	expectedDegradedHits int
	bySourceType         map[EvidenceSourceType]*ConversationQualitySourceSummary
}

func EvaluateConversationQuality(
	cases []ConversationQualityCase,
	observations []ConversationQualityObservation,
) (ConversationQualitySummary, error) {
	caseByID, version, err := indexConversationQualityCases(cases)
	if err != nil {
		return ConversationQualitySummary{}, err
	}
	if len(observations) != len(cases) {
		return ConversationQualitySummary{}, errors.New("conversation quality evaluation requires exactly one observation per case")
	}
	summary := ConversationQualitySummary{
		DatasetVersion: version, Cases: len(cases), Runs: len(observations),
		BySourceType: map[EvidenceSourceType]ConversationQualitySourceSummary{
			EvidenceSourceKnowledgeChunk: {}, EvidenceSourceAttachment: {}, EvidenceSourceWebPage: {},
		},
		FailureTypes: make(map[string]int),
	}
	seenRuns := make(map[string]struct{}, len(observations))
	seenCases := make(map[string]struct{}, len(observations))
	durations := make([]int64, 0, len(observations))
	var faithfulness, relevance, citationAlignment float64
	var totals conversationQualityScore
	for index, observation := range observations {
		if err := observation.Validate(); err != nil {
			return ConversationQualitySummary{}, fmt.Errorf("observation %d: %w", index, err)
		}
		definition, exists := caseByID[observation.CaseID]
		if !exists || observation.DatasetVersion != version {
			return ConversationQualitySummary{}, fmt.Errorf("observation %q does not match the dataset", observation.RunID)
		}
		if _, exists := seenRuns[observation.RunID]; exists {
			return ConversationQualitySummary{}, fmt.Errorf("duplicate runId %q", observation.RunID)
		}
		if _, exists := seenCases[observation.CaseID]; exists {
			return ConversationQualitySummary{}, fmt.Errorf("duplicate observation for case %q", observation.CaseID)
		}
		seenRuns[observation.RunID] = struct{}{}
		seenCases[observation.CaseID] = struct{}{}
		if summary.ObservationKind == "" {
			summary.ObservationKind = observation.ObservationKind
		} else if summary.ObservationKind != observation.ObservationKind {
			return ConversationQualitySummary{}, errors.New("conversation quality evaluation cannot mix seeded and recorded observations")
		}
		score := scoreConversationQuality(definition, observation)
		mergeConversationQualityScore(&totals, score)
		mergeConversationQualitySourceSummaries(summary.BySourceType, score.bySourceType)
		if score.passed {
			summary.PassedRuns++
		}
		if observation.Outcome == ConversationQualityFailed || observation.ErrorType != "" {
			summary.FailedRuns++
		}
		if observation.Outcome == ConversationQualityDegraded || len(observation.DegradedChannels) > 0 {
			summary.DegradedRuns++
		}
		if observation.ErrorType != "" {
			summary.FailureTypes[observation.ErrorType]++
		}
		summary.PromptTokens += observation.Usage.PromptTokens
		summary.CompletionTokens += observation.Usage.CompletionTokens
		summary.TotalTokens += observation.Usage.TotalTokens
		summary.TotalEstimatedCostCNY += observation.EstimatedCostCNY
		durations = append(durations, observation.DurationMillis)
		if observation.Judge != nil {
			summary.JudgedRuns++
			faithfulness += observation.Judge.Faithfulness
			relevance += observation.Judge.AnswerRelevance
			citationAlignment += observation.Judge.CitationAlignment
		}
	}
	runs := float64(summary.Runs)
	summary.PassRate = float64(summary.PassedRuns) / runs
	summary.OutcomeAccuracy = ratio(totals.outcomeCorrect, summary.Runs)
	summary.ContextPrecision = ratio(totals.correctRetrieved, totals.retrieved)
	summary.ContextRecall = ratio(totals.correctRetrieved, totals.relevant)
	summary.CitationPrecision = ratio(totals.correctCitations, totals.citations)
	summary.CitationRecall = ratio(totals.correctRequired, totals.requiredCitations)
	summary.PreviewConsistencyRate = ratio(totals.previewMatches, totals.previewChecks)
	summary.RequiredAnswerTermRecall = ratio(totals.requiredTermHits, totals.requiredTerms)
	summary.ForbiddenAnswerTermHitRate = ratio(totals.forbiddenTermHits, totals.forbiddenTerms)
	summary.ExpectedDegradedChannelRecall = ratio(totals.expectedDegradedHits, totals.expectedDegraded)
	summary.AverageTokensPerRun = float64(summary.TotalTokens) / runs
	summary.P50DurationMillis = nearestRankPercentile(durations, 0.50)
	summary.P95DurationMillis = nearestRankPercentile(durations, 0.95)
	summary.AverageEstimatedCostCNY = summary.TotalEstimatedCostCNY / runs
	summary.EstimatedCostPerThousandCNY = summary.AverageEstimatedCostCNY * 1_000
	if summary.JudgedRuns > 0 {
		judgedRuns := float64(summary.JudgedRuns)
		summary.AverageFaithfulness = faithfulness / judgedRuns
		summary.AverageAnswerRelevance = relevance / judgedRuns
		summary.AverageCitationAlignment = citationAlignment / judgedRuns
	}
	for sourceType, sourceSummary := range summary.BySourceType {
		sourceSummary.ContextPrecision = ratio(sourceSummary.CorrectRetrieved, sourceSummary.RetrievedSources)
		sourceSummary.ContextRecall = ratio(sourceSummary.CorrectRetrieved, sourceSummary.RelevantSources)
		sourceSummary.CitationPrecision = ratio(sourceSummary.CorrectCitations, sourceSummary.Citations)
		sourceSummary.CitationRecall = ratio(sourceSummary.CorrectRequired, sourceSummary.RequiredCitations)
		sourceSummary.PreviewConsistencyRate = ratio(sourceSummary.PreviewMatches, sourceSummary.PreviewChecks)
		summary.BySourceType[sourceType] = sourceSummary
	}
	if len(summary.FailureTypes) == 0 {
		summary.FailureTypes = nil
	}
	return summary, nil
}

func indexConversationQualityCases(
	cases []ConversationQualityCase,
) (map[string]ConversationQualityCase, string, error) {
	if len(cases) == 0 {
		return nil, "", errors.New("conversation quality dataset contains no cases")
	}
	indexed := make(map[string]ConversationQualityCase, len(cases))
	version := ""
	for index, definition := range cases {
		if err := definition.Validate(); err != nil {
			return nil, "", fmt.Errorf("case %d: %w", index, err)
		}
		if version == "" {
			version = definition.DatasetVersion
		} else if definition.DatasetVersion != version {
			return nil, "", errors.New("conversation quality dataset mixes versions")
		}
		if _, exists := indexed[definition.CaseID]; exists {
			return nil, "", fmt.Errorf("duplicate caseId %q", definition.CaseID)
		}
		indexed[definition.CaseID] = definition
	}
	return indexed, version, nil
}

func scoreConversationQuality(
	definition ConversationQualityCase,
	observation ConversationQualityObservation,
) conversationQualityScore {
	score := conversationQualityScore{
		outcomeCorrect: boolInt(observation.Outcome == definition.ExpectedOutcome),
		bySourceType: map[EvidenceSourceType]*ConversationQualitySourceSummary{
			EvidenceSourceKnowledgeChunk: {}, EvidenceSourceAttachment: {}, EvidenceSourceWebPage: {},
		},
	}
	relevant := make(map[string]ConversationQualitySource, len(definition.RelevantSources))
	for _, source := range definition.RelevantSources {
		relevant[source.SourceRef] = source
		score.relevant++
		score.bySourceType[source.SourceType].RelevantSources++
	}
	for _, source := range observation.RetrievedSources {
		score.retrieved++
		score.bySourceType[source.SourceType].RetrievedSources++
		if expected, exists := relevant[source.SourceRef]; exists && conversationQualitySourceEqual(expected, source) {
			score.correctRetrieved++
			score.bySourceType[source.SourceType].CorrectRetrieved++
		}
	}
	required := make(map[string]struct{}, len(definition.RequiredCitationRefs))
	for _, sourceRef := range definition.RequiredCitationRefs {
		required[sourceRef] = struct{}{}
		score.requiredCitations++
		score.bySourceType[relevant[sourceRef].SourceType].RequiredCitations++
	}
	for _, citation := range observation.Citations {
		score.citations++
		score.bySourceType[citation.SourceType].Citations++
		expected, relevantCitation := relevant[citation.SourceRef]
		correct := relevantCitation && citation.SourceType == expected.SourceType && citation.ContentSHA256 == expected.ContentSHA256
		if correct {
			score.correctCitations++
			score.bySourceType[citation.SourceType].CorrectCitations++
			if _, requiredCitation := required[citation.SourceRef]; requiredCitation {
				score.correctRequired++
				score.bySourceType[citation.SourceType].CorrectRequired++
			}
		}
		if relevantCitation && expected.PreviewRequired {
			score.previewChecks++
			score.bySourceType[citation.SourceType].PreviewChecks++
			if correct && citation.PreviewContentSHA256 == expected.ContentSHA256 {
				score.previewMatches++
				score.bySourceType[citation.SourceType].PreviewMatches++
			}
		}
	}
	answer := strings.ToLower(observation.Answer)
	for _, term := range definition.RequiredAnswerTerms {
		score.requiredTerms++
		if strings.Contains(answer, strings.ToLower(term)) {
			score.requiredTermHits++
		}
	}
	for _, term := range definition.ForbiddenAnswerTerms {
		score.forbiddenTerms++
		if strings.Contains(answer, strings.ToLower(term)) {
			score.forbiddenTermHits++
		}
	}
	for _, channel := range definition.ExpectedDegradedChannels {
		score.expectedDegraded++
		if slices.Contains(observation.DegradedChannels, channel) {
			score.expectedDegradedHits++
		}
	}
	noUnexpectedFailure := observation.ErrorType == "" || definition.ExpectedOutcome == ConversationQualityFailed ||
		definition.ExpectedOutcome == ConversationQualityDegraded
	score.passed = score.outcomeCorrect == 1 && noUnexpectedFailure &&
		score.correctRequired == score.requiredCitations && score.correctCitations == score.citations &&
		score.previewMatches == score.previewChecks && score.requiredTermHits == score.requiredTerms &&
		score.forbiddenTermHits == 0 && score.expectedDegradedHits == score.expectedDegraded
	return score
}

func mergeConversationQualityScore(target *conversationQualityScore, source conversationQualityScore) {
	target.outcomeCorrect += source.outcomeCorrect
	target.relevant += source.relevant
	target.retrieved += source.retrieved
	target.correctRetrieved += source.correctRetrieved
	target.requiredCitations += source.requiredCitations
	target.citations += source.citations
	target.correctCitations += source.correctCitations
	target.correctRequired += source.correctRequired
	target.previewChecks += source.previewChecks
	target.previewMatches += source.previewMatches
	target.requiredTerms += source.requiredTerms
	target.requiredTermHits += source.requiredTermHits
	target.forbiddenTerms += source.forbiddenTerms
	target.forbiddenTermHits += source.forbiddenTermHits
	target.expectedDegraded += source.expectedDegraded
	target.expectedDegradedHits += source.expectedDegradedHits
}

func mergeConversationQualitySourceSummaries(
	target map[EvidenceSourceType]ConversationQualitySourceSummary,
	source map[EvidenceSourceType]*ConversationQualitySourceSummary,
) {
	for sourceType, current := range source {
		merged := target[sourceType]
		merged.RelevantSources += current.RelevantSources
		merged.RetrievedSources += current.RetrievedSources
		merged.CorrectRetrieved += current.CorrectRetrieved
		merged.RequiredCitations += current.RequiredCitations
		merged.Citations += current.Citations
		merged.CorrectCitations += current.CorrectCitations
		merged.CorrectRequired += current.CorrectRequired
		merged.PreviewChecks += current.PreviewChecks
		merged.PreviewMatches += current.PreviewMatches
		target[sourceType] = merged
	}
}

var conversationQualityLabelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)

func conversationQualitySourceType(sourceType EvidenceSourceType) bool {
	return sourceType == EvidenceSourceKnowledgeChunk || sourceType == EvidenceSourceAttachment ||
		sourceType == EvidenceSourceWebPage
}

func validConversationQualitySourceRef(sourceType EvidenceSourceType, sourceRef string) bool {
	if strings.TrimSpace(sourceRef) == "" || sourceRef != strings.TrimSpace(sourceRef) || len(sourceRef) > 2_048 {
		return false
	}
	switch sourceType {
	case EvidenceSourceKnowledgeChunk:
		parts := strings.Split(strings.TrimPrefix(sourceRef, "knowledge:"), "/")
		return strings.HasPrefix(sourceRef, "knowledge:") && len(parts) == 2 && validUUID(parts[0]) && validUUID(parts[1])
	case EvidenceSourceAttachment:
		return strings.HasPrefix(sourceRef, "attachment:") && validUUID(strings.TrimPrefix(sourceRef, "attachment:"))
	case EvidenceSourceWebPage:
		parsed, err := url.ParseRequestURI(sourceRef)
		return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
	default:
		return false
	}
}

func validUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, current := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if current != '-' {
				return false
			}
			continue
		}
		if !((current >= '0' && current <= '9') || (current >= 'a' && current <= 'f')) {
			return false
		}
	}
	return true
}

func validConversationQualitySHA256(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func conversationQualitySourceEqual(left, right ConversationQualitySource) bool {
	return left.SourceType == right.SourceType && left.SourceRef == right.SourceRef &&
		left.ContentSHA256 == right.ContentSHA256
}

func conversationQualityDisplayLabel(value string) bool {
	return strings.TrimSpace(value) != "" && value == strings.TrimSpace(value) && len(value) <= 255
}

func validateConversationQualitySignals(values []string, field string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) || len([]rune(value)) > 128 {
			return fmt.Errorf("conversation quality %s contains an invalid value", field)
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("conversation quality %s must be unique", field)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateConversationQualityLabels(values []string, field string) error {
	if hasDuplicateStrings(values) {
		return fmt.Errorf("conversation quality %s must be unique", field)
	}
	for _, value := range values {
		if !conversationQualityLabelPattern.MatchString(value) {
			return fmt.Errorf("conversation quality %s contains an invalid value", field)
		}
	}
	return nil
}

func hasDuplicateStrings(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nearestRankPercentile(values []int64, percentile float64) int64 {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]int64(nil), values...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left] < ordered[right] })
	rank := int(math.Ceil(percentile*float64(len(ordered)))) - 1
	if rank < 0 {
		rank = 0
	}
	return ordered[rank]
}
