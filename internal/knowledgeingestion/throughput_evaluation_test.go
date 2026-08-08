package knowledgeingestion

import (
	"fmt"
	"strings"
	"testing"
)

func TestEvaluateThroughputRequiresFiveIntegrityPreservingPairsForAcceptance(t *testing.T) {
	observations := make([]ThroughputObservation, 0, 10)
	for repetition := 1; repetition <= 5; repetition++ {
		observations = append(observations,
			throughputObservationForTest(repetition, ThroughputBaseline, 1000),
			throughputObservationForTest(repetition, ThroughputExperiment, 500),
		)
	}
	summary, err := EvaluateThroughput(observations, 40)
	if err != nil {
		t.Fatal(err)
	}
	if !summary.AcceptanceEligible || !summary.IntegrityPreserved || !summary.MeetsTarget ||
		summary.MedianThroughputIncreasePercent != 100 || summary.MedianDurationReductionPercent != 50 ||
		summary.Baseline.MedianChunksPerSecond != 400 || summary.Experiment.MedianChunksPerSecond != 800 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestEvaluateThroughputKeepsSinglePairAsPilot(t *testing.T) {
	summary, err := EvaluateThroughput([]ThroughputObservation{
		throughputObservationForTest(1, ThroughputBaseline, 1000),
		throughputObservationForTest(1, ThroughputExperiment, 500),
	}, 40)
	if err != nil {
		t.Fatal(err)
	}
	if summary.AcceptanceEligible || summary.MeetsTarget || !summary.IntegrityPreserved {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestEvaluateThroughputRejectsQualityRegression(t *testing.T) {
	baseline := throughputObservationForTest(1, ThroughputBaseline, 1000)
	experiment := throughputObservationForTest(1, ThroughputExperiment, 500)
	experiment.Chunks--
	summary, err := EvaluateThroughput([]ThroughputObservation{baseline, experiment}, 40)
	if err != nil {
		t.Fatal(err)
	}
	if summary.IntegrityPreserved || summary.MeetsTarget {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestEvaluateThroughputRejectsChangedEnvironment(t *testing.T) {
	baseline := throughputObservationForTest(1, ThroughputBaseline, 1000)
	experiment := throughputObservationForTest(1, ThroughputExperiment, 500)
	experiment.EnvironmentFingerprint = fingerprintForTest('b')
	if _, err := EvaluateThroughput([]ThroughputObservation{baseline, experiment}, 40); err == nil {
		t.Fatal("EvaluateThroughput accepted a changed environment")
	}
}

func TestThroughputObservationValidatesDocumentDiagnostics(t *testing.T) {
	observation := throughputObservationForTest(1, ThroughputBaseline, 1000)
	observation.Documents = 1
	observation.FormatCount = 1
	observation.SucceededDocuments = 1
	observation.Elements = 2
	observation.Chunks = 3
	observation.EmbeddingTokens = 4
	observation.DocumentResults = []ThroughputDocumentObservation{{
		DocumentID: "fixture", FormatClass: "text", TaskStatus: "succeeded",
		OutcomeAction: "ack", OutcomeReason: "knowledge ingestion result committed",
		Elements: 2, Chunks: 3, EmbeddingTokens: 4,
	}}
	if err := observation.Validate(); err != nil {
		t.Fatal(err)
	}
	observation.DocumentResults[0].Chunks = 2
	if err := observation.Validate(); err == nil {
		t.Fatal("Validate accepted mismatched document diagnostics")
	}
}

func throughputObservationForTest(
	repetition int,
	variant ThroughputVariant,
	durationMillis int64,
) ThroughputObservation {
	return ThroughputObservation{
		DatasetVersion: "rag-ingestion-v1", RunID: fmt.Sprintf("run-%d-%s", repetition, variant),
		Repetition: repetition, Variant: variant, CorpusFingerprint: fingerprintForTest('a'),
		EnvironmentFingerprint: fingerprintForTest('e'), Documents: 40, FormatCount: 8, SucceededDocuments: 40,
		SourceBytes: 40 * 1024 * 1024, Pages: 200, Elements: 600, Chunks: 400,
		DurationMillis: durationMillis, QueueDurationMillis: durationMillis / 4,
		ProcessDurationMillis: durationMillis * 3 / 4, DocumentConcurrency: 1,
		EmbeddingBatchSize: 1, EmbeddingMaxConcurrent: 1, ChunkWriteBatchSize: 1,
		EmbeddingRequests: 400, EmbeddingTokens: 1000, ChunkInsertBatches: 400,
		EmbeddingInsertBatches: 400,
	}
}

func fingerprintForTest(char byte) string {
	return strings.Repeat(string(char), 64)
}
