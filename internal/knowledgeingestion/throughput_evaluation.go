package knowledgeingestion

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
)

type ThroughputVariant string

const (
	ThroughputBaseline   ThroughputVariant = "baseline"
	ThroughputExperiment ThroughputVariant = "experiment"
)

func (v ThroughputVariant) Valid() bool {
	return v == ThroughputBaseline || v == ThroughputExperiment
}

type ThroughputObservation struct {
	DatasetVersion         string            `json:"datasetVersion"`
	RunID                  string            `json:"runId"`
	Repetition             int               `json:"repetition"`
	Variant                ThroughputVariant `json:"variant"`
	CorpusFingerprint      string            `json:"corpusFingerprint"`
	EnvironmentFingerprint string            `json:"environmentFingerprint"`
	Documents              int               `json:"documents"`
	FormatCount            int               `json:"formatCount"`
	SucceededDocuments     int               `json:"succeededDocuments"`
	PartialDocuments       int               `json:"partialDocuments"`
	FailedDocuments        int               `json:"failedDocuments"`
	PartialDocumentIDs     []string          `json:"partialDocumentIds,omitempty"`
	FailedDocumentIDs      []string          `json:"failedDocumentIds,omitempty"`
	SourceBytes            int64             `json:"sourceBytes"`
	Pages                  int               `json:"pages"`
	Elements               int               `json:"elements"`
	Chunks                 int               `json:"chunks"`
	DurationMillis         int64             `json:"durationMillis"`
	QueueDurationMillis    int64             `json:"queueDurationMillis"`
	ProcessDurationMillis  int64             `json:"processDurationMillis"`
	DocumentConcurrency    int               `json:"documentConcurrency"`
	EmbeddingBatchSize     int               `json:"embeddingBatchSize"`
	EmbeddingMaxConcurrent int               `json:"embeddingMaxConcurrent"`
	ChunkWriteBatchSize    int               `json:"chunkWriteBatchSize"`
	EmbeddingRequests      int               `json:"embeddingRequests"`
	EmbeddingTokens        int               `json:"embeddingTokens"`
	ChunkInsertBatches     int               `json:"chunkInsertBatches"`
	EmbeddingInsertBatches int               `json:"embeddingInsertBatches"`
}

func (o ThroughputObservation) Validate() error {
	if strings.TrimSpace(o.DatasetVersion) == "" || o.DatasetVersion != strings.TrimSpace(o.DatasetVersion) ||
		strings.TrimSpace(o.RunID) == "" || o.RunID != strings.TrimSpace(o.RunID) || o.Repetition < 1 ||
		!o.Variant.Valid() || !validEvaluationFingerprint(o.CorpusFingerprint) ||
		!validEvaluationFingerprint(o.EnvironmentFingerprint) {
		return errors.New("knowledge ingestion throughput observation identity is invalid")
	}
	if o.Documents < 1 || o.Documents > 1000 || o.FormatCount < 1 || o.FormatCount > o.Documents ||
		o.SucceededDocuments < 0 || o.PartialDocuments < 0 ||
		o.FailedDocuments < 0 || o.SucceededDocuments+o.PartialDocuments+o.FailedDocuments != o.Documents ||
		len(o.PartialDocumentIDs) != o.PartialDocuments || len(o.FailedDocumentIDs) != o.FailedDocuments {
		return errors.New("knowledge ingestion throughput document counts are invalid")
	}
	if duplicateOrBlank(o.PartialDocumentIDs) || duplicateOrBlank(o.FailedDocumentIDs) ||
		hasOverlap(o.PartialDocumentIDs, o.FailedDocumentIDs) {
		return errors.New("knowledge ingestion throughput document outcomes are invalid")
	}
	if o.SourceBytes < 1 || o.Pages < 0 || o.Elements < 0 || o.Chunks < 0 || o.DurationMillis < 1 ||
		o.QueueDurationMillis < 0 || o.ProcessDurationMillis < 0 ||
		o.QueueDurationMillis > o.DurationMillis || o.ProcessDurationMillis > o.DurationMillis {
		return errors.New("knowledge ingestion throughput workload measurements are invalid")
	}
	if o.DocumentConcurrency < 1 || o.DocumentConcurrency > 32 || o.EmbeddingBatchSize < 1 ||
		o.EmbeddingBatchSize > 10 || o.EmbeddingMaxConcurrent < 1 || o.EmbeddingMaxConcurrent > 8 ||
		o.ChunkWriteBatchSize < 1 || o.ChunkWriteBatchSize > 500 {
		return errors.New("knowledge ingestion throughput variant configuration is invalid")
	}
	if o.EmbeddingRequests < 0 || o.EmbeddingTokens < 0 || o.ChunkInsertBatches < 0 ||
		o.EmbeddingInsertBatches < 0 {
		return errors.New("knowledge ingestion throughput usage is invalid")
	}
	return nil
}

type ThroughputVariantSummary struct {
	Variant                     ThroughputVariant `json:"variant"`
	Runs                        int               `json:"runs"`
	MedianDurationMillis        float64           `json:"medianDurationMillis"`
	P95DurationMillis           float64           `json:"p95DurationMillis"`
	MedianDocumentsPerMinute    float64           `json:"medianDocumentsPerMinute"`
	MedianPagesPerMinute        float64           `json:"medianPagesPerMinute"`
	MedianMiBPerMinute          float64           `json:"medianMiBPerMinute"`
	MedianElementsPerSecond     float64           `json:"medianElementsPerSecond"`
	MedianChunksPerSecond       float64           `json:"medianChunksPerSecond"`
	TotalEmbeddingRequests      int               `json:"totalEmbeddingRequests"`
	TotalEmbeddingTokens        int               `json:"totalEmbeddingTokens"`
	TotalChunkInsertBatches     int               `json:"totalChunkInsertBatches"`
	TotalEmbeddingInsertBatches int               `json:"totalEmbeddingInsertBatches"`
}

type ThroughputEvaluationSummary struct {
	DatasetVersion                  string                   `json:"datasetVersion"`
	Pairs                           int                      `json:"pairs"`
	CorpusDocuments                 int                      `json:"corpusDocuments"`
	CorpusFormats                   int                      `json:"corpusFormats"`
	TargetThroughputIncreasePercent float64                  `json:"targetThroughputIncreasePercent"`
	MedianThroughputIncreasePercent float64                  `json:"medianThroughputIncreasePercent"`
	MedianDurationReductionPercent  float64                  `json:"medianDurationReductionPercent"`
	IntegrityPreserved              bool                     `json:"integrityPreserved"`
	AcceptanceEligible              bool                     `json:"acceptanceEligible"`
	MeetsTarget                     bool                     `json:"meetsTarget"`
	Baseline                        ThroughputVariantSummary `json:"baseline"`
	Experiment                      ThroughputVariantSummary `json:"experiment"`
}

func EvaluateThroughput(
	observations []ThroughputObservation,
	targetIncreasePercent float64,
) (ThroughputEvaluationSummary, error) {
	if targetIncreasePercent < 0 || targetIncreasePercent > 1000 {
		return ThroughputEvaluationSummary{}, errors.New("knowledge ingestion throughput target is invalid")
	}
	if len(observations) == 0 {
		return ThroughputEvaluationSummary{}, errors.New("knowledge ingestion throughput observations are empty")
	}
	byRepetition := make(map[int]map[ThroughputVariant]ThroughputObservation)
	datasetVersion := ""
	for _, observation := range observations {
		if err := observation.Validate(); err != nil {
			return ThroughputEvaluationSummary{}, fmt.Errorf("validate throughput observation: %w", err)
		}
		if datasetVersion == "" {
			datasetVersion = observation.DatasetVersion
		} else if observation.DatasetVersion != datasetVersion {
			return ThroughputEvaluationSummary{}, errors.New("knowledge ingestion throughput dataset versions differ")
		}
		variants := byRepetition[observation.Repetition]
		if variants == nil {
			variants = make(map[ThroughputVariant]ThroughputObservation, 2)
			byRepetition[observation.Repetition] = variants
		}
		if _, exists := variants[observation.Variant]; exists {
			return ThroughputEvaluationSummary{}, errors.New("knowledge ingestion throughput pair contains a duplicate variant")
		}
		variants[observation.Variant] = observation
	}
	baseline := make([]ThroughputObservation, 0, len(byRepetition))
	experiment := make([]ThroughputObservation, 0, len(byRepetition))
	throughputChanges := make([]float64, 0, len(byRepetition))
	durationChanges := make([]float64, 0, len(byRepetition))
	integrityPreserved := true
	for repetition := 1; repetition <= len(byRepetition); repetition++ {
		variants, exists := byRepetition[repetition]
		if !exists || len(variants) != 2 {
			return ThroughputEvaluationSummary{}, errors.New("knowledge ingestion throughput repetitions must be contiguous pairs")
		}
		base, baseOK := variants[ThroughputBaseline]
		experimentRun, experimentOK := variants[ThroughputExperiment]
		if !baseOK || !experimentOK {
			return ThroughputEvaluationSummary{}, errors.New("knowledge ingestion throughput pair is incomplete")
		}
		if base.CorpusFingerprint != experimentRun.CorpusFingerprint ||
			base.EnvironmentFingerprint != experimentRun.EnvironmentFingerprint ||
			base.Documents != experimentRun.Documents || base.FormatCount != experimentRun.FormatCount ||
			base.SourceBytes != experimentRun.SourceBytes ||
			base.Pages != experimentRun.Pages {
			return ThroughputEvaluationSummary{}, errors.New("knowledge ingestion throughput pair changed the workload or environment")
		}
		if base.SucceededDocuments != experimentRun.SucceededDocuments ||
			base.PartialDocuments != experimentRun.PartialDocuments ||
			base.FailedDocuments != experimentRun.FailedDocuments || base.Elements != experimentRun.Elements ||
			base.Chunks != experimentRun.Chunks || !slices.Equal(base.PartialDocumentIDs, experimentRun.PartialDocumentIDs) ||
			!slices.Equal(base.FailedDocumentIDs, experimentRun.FailedDocumentIDs) {
			integrityPreserved = false
		}
		throughputChanges = append(throughputChanges,
			(float64(base.DurationMillis)/float64(experimentRun.DurationMillis)-1)*100,
		)
		durationChanges = append(durationChanges,
			(1-float64(experimentRun.DurationMillis)/float64(base.DurationMillis))*100,
		)
		baseline = append(baseline, base)
		experiment = append(experiment, experimentRun)
	}
	pairs := len(byRepetition)
	corpusDocuments, corpusFormats := baseline[0].Documents, baseline[0].FormatCount
	acceptanceEligible := pairs >= 5 && corpusDocuments >= 40 && corpusFormats >= 8
	medianIncrease := percentile(throughputChanges, 0.5)
	return ThroughputEvaluationSummary{
		DatasetVersion: datasetVersion, Pairs: pairs,
		CorpusDocuments: corpusDocuments, CorpusFormats: corpusFormats,
		TargetThroughputIncreasePercent: targetIncreasePercent,
		MedianThroughputIncreasePercent: medianIncrease,
		MedianDurationReductionPercent:  percentile(durationChanges, 0.5),
		IntegrityPreserved:              integrityPreserved, AcceptanceEligible: acceptanceEligible,
		MeetsTarget: acceptanceEligible && integrityPreserved && medianIncrease >= targetIncreasePercent,
		Baseline:    summarizeThroughputVariant(ThroughputBaseline, baseline),
		Experiment:  summarizeThroughputVariant(ThroughputExperiment, experiment),
	}, nil
}

func summarizeThroughputVariant(variant ThroughputVariant, observations []ThroughputObservation) ThroughputVariantSummary {
	durations := make([]float64, 0, len(observations))
	documentsPerMinute := make([]float64, 0, len(observations))
	pagesPerMinute := make([]float64, 0, len(observations))
	mibPerMinute := make([]float64, 0, len(observations))
	elementsPerSecond := make([]float64, 0, len(observations))
	chunksPerSecond := make([]float64, 0, len(observations))
	summary := ThroughputVariantSummary{Variant: variant, Runs: len(observations)}
	for _, observation := range observations {
		seconds := float64(observation.DurationMillis) / 1000
		minutes := seconds / 60
		durations = append(durations, float64(observation.DurationMillis))
		documentsPerMinute = append(documentsPerMinute, float64(observation.Documents)/minutes)
		pagesPerMinute = append(pagesPerMinute, float64(observation.Pages)/minutes)
		mibPerMinute = append(mibPerMinute, (float64(observation.SourceBytes)/(1024*1024))/minutes)
		elementsPerSecond = append(elementsPerSecond, float64(observation.Elements)/seconds)
		chunksPerSecond = append(chunksPerSecond, float64(observation.Chunks)/seconds)
		summary.TotalEmbeddingRequests += observation.EmbeddingRequests
		summary.TotalEmbeddingTokens += observation.EmbeddingTokens
		summary.TotalChunkInsertBatches += observation.ChunkInsertBatches
		summary.TotalEmbeddingInsertBatches += observation.EmbeddingInsertBatches
	}
	summary.MedianDurationMillis = percentile(durations, 0.5)
	summary.P95DurationMillis = percentile(durations, 0.95)
	summary.MedianDocumentsPerMinute = percentile(documentsPerMinute, 0.5)
	summary.MedianPagesPerMinute = percentile(pagesPerMinute, 0.5)
	summary.MedianMiBPerMinute = percentile(mibPerMinute, 0.5)
	summary.MedianElementsPerSecond = percentile(elementsPerSecond, 0.5)
	summary.MedianChunksPerSecond = percentile(chunksPerSecond, 0.5)
	return summary
}

func percentile(values []float64, quantile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]float64(nil), values...)
	slices.Sort(ordered)
	if len(ordered) == 1 {
		return ordered[0]
	}
	position := quantile * float64(len(ordered)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return ordered[lower]
	}
	weight := position - float64(lower)
	return ordered[lower]*(1-weight) + ordered[upper]*weight
}

func validEvaluationFingerprint(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func duplicateOrBlank(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return true
		}
		if _, exists := seen[value]; exists {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}

func hasOverlap(left, right []string) bool {
	seen := make(map[string]struct{}, len(left))
	for _, value := range left {
		seen[value] = struct{}{}
	}
	for _, value := range right {
		if _, exists := seen[value]; exists {
			return true
		}
	}
	return false
}
