package knowledge

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var retrievalEvaluationIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

type RetrievalEvaluationDocument struct {
	DatasetVersion string `json:"datasetVersion"`
	DocumentKey    string `json:"documentKey"`
	Title          string `json:"title"`
	Content        string `json:"content"`
	MediaType      string `json:"mediaType"`
}

func (d RetrievalEvaluationDocument) Validate() error {
	if !retrievalEvaluationIDPattern.MatchString(d.DatasetVersion) ||
		!retrievalEvaluationIDPattern.MatchString(d.DocumentKey) {
		return errors.New("datasetVersion and documentKey must be stable identifiers")
	}
	if strings.TrimSpace(d.Title) == "" || d.Title != strings.TrimSpace(d.Title) || len([]rune(d.Title)) > 512 {
		return errors.New("retrieval document title is invalid")
	}
	if strings.TrimSpace(d.Content) == "" || len([]rune(d.Content)) > 100000 {
		return errors.New("retrieval document content is invalid")
	}
	if strings.TrimSpace(d.MediaType) == "" || d.MediaType != strings.TrimSpace(d.MediaType) {
		return errors.New("retrieval document media type is invalid")
	}
	return nil
}

type RetrievalEvaluationCase struct {
	DatasetVersion       string   `json:"datasetVersion"`
	CaseID               string   `json:"caseId"`
	Query                string   `json:"query"`
	RelevantDocumentKeys []string `json:"relevantDocumentKeys"`
	K                    int      `json:"k"`
}

func (c RetrievalEvaluationCase) Validate() error {
	if !retrievalEvaluationIDPattern.MatchString(c.DatasetVersion) ||
		!retrievalEvaluationIDPattern.MatchString(c.CaseID) {
		return errors.New("datasetVersion and caseId must be stable identifiers")
	}
	if strings.TrimSpace(c.Query) == "" || c.Query != strings.TrimSpace(c.Query) || len([]rune(c.Query)) > 512 {
		return errors.New("retrieval query is invalid")
	}
	if c.K < 1 || c.K > 50 || len(c.RelevantDocumentKeys) == 0 || len(c.RelevantDocumentKeys) > 10 {
		return errors.New("retrieval relevance dimensions are invalid")
	}
	seen := make(map[string]struct{}, len(c.RelevantDocumentKeys))
	for _, key := range c.RelevantDocumentKeys {
		if !retrievalEvaluationIDPattern.MatchString(key) {
			return errors.New("relevant document key is invalid")
		}
		if _, exists := seen[key]; exists {
			return errors.New("relevant document keys must be unique")
		}
		seen[key] = struct{}{}
	}
	return nil
}

type RetrievalEvaluationObservation struct {
	DatasetVersion       string   `json:"datasetVersion"`
	CaseID               string   `json:"caseId"`
	RunID                string   `json:"runId"`
	Retriever            string   `json:"retriever"`
	Query                string   `json:"query"`
	K                    int      `json:"k"`
	RelevantDocumentKeys []string `json:"relevantDocumentKeys"`
	ReturnedDocumentKeys []string `json:"returnedDocumentKeys"`
	FirstRelevantRank    int      `json:"firstRelevantRank,omitempty"`
	HitAtK               bool     `json:"hitAtK"`
	DurationMillis       float64  `json:"durationMillis"`
}

func (o RetrievalEvaluationObservation) Validate() error {
	if !retrievalEvaluationIDPattern.MatchString(o.DatasetVersion) ||
		!retrievalEvaluationIDPattern.MatchString(o.CaseID) || strings.TrimSpace(o.RunID) == "" ||
		strings.TrimSpace(o.Retriever) == "" || strings.TrimSpace(o.Query) == "" {
		return errors.New("retrieval observation identity is invalid")
	}
	if o.K < 1 || o.K > 50 || len(o.RelevantDocumentKeys) == 0 || len(o.ReturnedDocumentKeys) > o.K || o.DurationMillis < 0 {
		return errors.New("retrieval observation dimensions are invalid")
	}
	if o.HitAtK != (o.FirstRelevantRank > 0 && o.FirstRelevantRank <= o.K) {
		return errors.New("retrieval observation hit and rank are inconsistent")
	}
	return nil
}

type RetrievalEvaluationSummary struct {
	DatasetVersion             string  `json:"datasetVersion"`
	Retriever                  string  `json:"retriever"`
	K                          int     `json:"k"`
	Documents                  int     `json:"documents"`
	Chunks                     int     `json:"chunks"`
	Cases                      int     `json:"cases"`
	HitsAtK                    int     `json:"hitsAtK"`
	RecallAtK                  float64 `json:"recallAtK"`
	MeanReciprocalRank         float64 `json:"meanReciprocalRank"`
	AverageQueryDurationMillis float64 `json:"averageQueryDurationMillis"`
	IngestionDurationMillis    float64 `json:"ingestionDurationMillis"`
	DocumentsPerSecond         float64 `json:"documentsPerSecond"`
	ChunksPerSecond            float64 `json:"chunksPerSecond"`
	EmbeddingDocumentRequests  int     `json:"embeddingDocumentRequests,omitempty"`
	EmbeddingQueryRequests     int     `json:"embeddingQueryRequests,omitempty"`
	EmbeddingDocumentTokens    int     `json:"embeddingDocumentTokens,omitempty"`
	EmbeddingQueryTokens       int     `json:"embeddingQueryTokens,omitempty"`
	EmbeddingTotalTokens       int     `json:"embeddingTotalTokens,omitempty"`
	EmbeddingDocumentDuration  float64 `json:"embeddingDocumentDurationMillis,omitempty"`
	EmbeddingQueryDuration     float64 `json:"embeddingQueryDurationMillis,omitempty"`
	EmbeddingPricePerMillion   float64 `json:"embeddingPricePerMillionTokensCNY,omitempty"`
	EmbeddingEstimatedCostCNY  float64 `json:"embeddingEstimatedCostCNY,omitempty"`
	RerankRequests             int     `json:"rerankRequests,omitempty"`
	RerankTotalTokens          int     `json:"rerankTotalTokens,omitempty"`
	RerankDurationMillis       float64 `json:"rerankDurationMillis,omitempty"`
	RerankPricePerMillion      float64 `json:"rerankPricePerMillionTokensCNY,omitempty"`
	RerankEstimatedCostCNY     float64 `json:"rerankEstimatedCostCNY,omitempty"`
}

func EvaluateRetrieval(
	documents []RetrievalEvaluationDocument,
	cases []RetrievalEvaluationCase,
	observations []RetrievalEvaluationObservation,
	retriever string,
	chunks int,
	ingestionDurationMillis float64,
) (RetrievalEvaluationSummary, error) {
	if len(documents) == 0 || len(cases) == 0 || len(observations) != len(cases) {
		return RetrievalEvaluationSummary{}, errors.New("retrieval evaluation inputs are incomplete")
	}
	if strings.TrimSpace(retriever) == "" || chunks < 1 || ingestionDurationMillis < 0 {
		return RetrievalEvaluationSummary{}, errors.New("retrieval evaluation metadata is invalid")
	}
	documentKeys := make(map[string]struct{}, len(documents))
	version := ""
	for index, document := range documents {
		if err := document.Validate(); err != nil {
			return RetrievalEvaluationSummary{}, fmt.Errorf("document %d: %w", index, err)
		}
		if version == "" {
			version = document.DatasetVersion
		} else if document.DatasetVersion != version {
			return RetrievalEvaluationSummary{}, errors.New("retrieval corpus mixes dataset versions")
		}
		if _, exists := documentKeys[document.DocumentKey]; exists {
			return RetrievalEvaluationSummary{}, fmt.Errorf("duplicate documentKey %q", document.DocumentKey)
		}
		documentKeys[document.DocumentKey] = struct{}{}
	}
	definitions := make(map[string]RetrievalEvaluationCase, len(cases))
	evaluationK := 0
	for index, definition := range cases {
		if err := definition.Validate(); err != nil {
			return RetrievalEvaluationSummary{}, fmt.Errorf("case %d: %w", index, err)
		}
		if definition.DatasetVersion != version {
			return RetrievalEvaluationSummary{}, errors.New("retrieval cases do not match corpus version")
		}
		if evaluationK == 0 {
			evaluationK = definition.K
		} else if definition.K != evaluationK {
			return RetrievalEvaluationSummary{}, errors.New("retrieval cases mix K values")
		}
		for _, key := range definition.RelevantDocumentKeys {
			if _, exists := documentKeys[key]; !exists {
				return RetrievalEvaluationSummary{}, fmt.Errorf("case %q references unknown document %q", definition.CaseID, key)
			}
		}
		if _, exists := definitions[definition.CaseID]; exists {
			return RetrievalEvaluationSummary{}, fmt.Errorf("duplicate caseId %q", definition.CaseID)
		}
		definitions[definition.CaseID] = definition
	}

	summary := RetrievalEvaluationSummary{
		DatasetVersion: version, Retriever: retriever, Documents: len(documents),
		K: evaluationK, Chunks: chunks, Cases: len(cases), IngestionDurationMillis: ingestionDurationMillis,
	}
	seen := make(map[string]struct{}, len(observations))
	var reciprocalRank float64
	var totalDuration float64
	for index, observation := range observations {
		if err := observation.Validate(); err != nil {
			return RetrievalEvaluationSummary{}, fmt.Errorf("observation %d: %w", index, err)
		}
		definition, exists := definitions[observation.CaseID]
		if !exists || observation.DatasetVersion != version || observation.Retriever != retriever ||
			observation.Query != definition.Query || observation.K != definition.K ||
			!equalStrings(observation.RelevantDocumentKeys, definition.RelevantDocumentKeys) {
			return RetrievalEvaluationSummary{}, fmt.Errorf("observation %q does not match its case", observation.RunID)
		}
		if _, exists := seen[observation.CaseID]; exists {
			return RetrievalEvaluationSummary{}, fmt.Errorf("duplicate observation for case %q", observation.CaseID)
		}
		seen[observation.CaseID] = struct{}{}
		expectedRank := firstRelevantRank(definition.RelevantDocumentKeys, observation.ReturnedDocumentKeys)
		if expectedRank != observation.FirstRelevantRank || observation.HitAtK != (expectedRank > 0) {
			return RetrievalEvaluationSummary{}, fmt.Errorf("observation %q has inconsistent relevance", observation.RunID)
		}
		if observation.HitAtK {
			summary.HitsAtK++
			reciprocalRank += 1 / float64(observation.FirstRelevantRank)
		}
		totalDuration += observation.DurationMillis
	}
	summary.RecallAtK = float64(summary.HitsAtK) / float64(summary.Cases)
	summary.MeanReciprocalRank = reciprocalRank / float64(summary.Cases)
	summary.AverageQueryDurationMillis = totalDuration / float64(summary.Cases)
	if ingestionDurationMillis > 0 {
		seconds := ingestionDurationMillis / 1000
		summary.DocumentsPerSecond = float64(summary.Documents) / seconds
		summary.ChunksPerSecond = float64(summary.Chunks) / seconds
	}
	return summary, nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func FirstRelevantRank(relevant, returned []string) int {
	return firstRelevantRank(relevant, returned)
}

func firstRelevantRank(relevant, returned []string) int {
	relevantSet := make(map[string]struct{}, len(relevant))
	for _, key := range relevant {
		relevantSet[key] = struct{}{}
	}
	seen := make(map[string]struct{}, len(returned))
	for index, key := range returned {
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		if _, exists := relevantSet[key]; exists {
			return index + 1
		}
	}
	return 0
}
