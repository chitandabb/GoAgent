// Command mesguard-semantic-cache-eval creates the human-review dataset and
// calibrates an Embedding-Profile-specific threshold from recorded scores.
// Draft, validation and calibration modes never call an online Provider.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformembedding "github.com/chitandabb/GoAgent/internal/platform/dashscopeembedding"
	"github.com/chitandabb/GoAgent/internal/semanticcache"
)

const maxSemanticCacheEvaluationBytes = 8 << 20

type similarityObservationSet struct {
	DatasetVersion       string                                `json:"datasetVersion"`
	EmbeddingProfile     string                                `json:"embeddingProfile"`
	NormalizationVersion string                                `json:"normalizationVersion"`
	Generation           int64                                 `json:"generation"`
	RecordedAt           time.Time                             `json:"recordedAt"`
	ProviderCalls        int                                   `json:"providerCalls"`
	EmbeddingTokens      int                                   `json:"embeddingTokens"`
	Pairs                []semanticcache.SimilarityObservation `json:"pairs"`
}

type calibrationReport struct {
	SchemaVersion        string                           `json:"schemaVersion"`
	DatasetVersion       string                           `json:"datasetVersion"`
	EmbeddingProfile     string                           `json:"embeddingProfile"`
	NormalizationVersion string                           `json:"normalizationVersion"`
	Generation           int64                            `json:"generation"`
	CreatedAt            time.Time                        `json:"createdAt"`
	Selection            semanticcache.ThresholdSelection `json:"selection"`
	Holdout              *semanticcache.CacheMetrics      `json:"holdout,omitempty"`
	ProviderCalls        int                              `json:"providerCalls"`
	EmbeddingTokens      int                              `json:"embeddingTokens"`
	ReleaseEnabled       bool                             `json:"releaseEnabled"`
	RejectionReason      string                           `json:"rejectionReason,omitempty"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("mesguard-semantic-cache-eval", flag.ContinueOnError)
	flags.SetOutput(stderr)
	mode := flags.String("mode", "validate", "draft, validate, observe, or calibrate")
	datasetPath := flags.String("dataset", "", "semantic cache dataset JSON")
	observationsPath := flags.String("observations", "", "recorded similarity observations JSON")
	outputPath := flags.String("output", "", "new output JSON; existing files are rejected")
	generation := flags.Int64("generation", 0, "Global Knowledge Generation recorded with observations")
	maxProviderCalls := flags.Int("max-provider-calls", 24, "hard cost guard for observe mode")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(*datasetPath) == "" {
		fmt.Fprintln(stderr, "-dataset is required and positional arguments are not supported")
		return 2
	}
	switch strings.ToLower(strings.TrimSpace(*mode)) {
	case "draft":
		if *observationsPath != "" || *outputPath != "" {
			fmt.Fprintln(stderr, "draft mode only accepts -dataset as its new output path")
			return 2
		}
		dataset := buildDraftDataset()
		if err := dataset.Validate(false); err != nil {
			fmt.Fprintf(stderr, "validate generated draft: %v\n", err)
			return 1
		}
		if err := writeNewJSON(*datasetPath, dataset); err != nil {
			fmt.Fprintf(stderr, "write draft dataset: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "semantic_cache_dataset pairs=%d reviewed=0 provider_calls=0\n", len(dataset.Pairs))
		return 0
	case "validate":
		dataset, err := readDataset(*datasetPath)
		if err != nil {
			fmt.Fprintf(stderr, "validate dataset: %v\n", err)
			return 1
		}
		reviewed := 0
		for _, pair := range dataset.Pairs {
			if pair.Reviewed {
				reviewed++
			}
		}
		fmt.Fprintf(stdout, "semantic_cache_dataset pairs=%d reviewed=%d pending=%d provider_calls=0\n",
			len(dataset.Pairs), reviewed, len(dataset.Pairs)-reviewed)
		return 0
	case "observe":
		if strings.TrimSpace(*outputPath) == "" || *observationsPath != "" || *generation < 1 || *maxProviderCalls < 1 {
			fmt.Fprintln(stderr, "observe mode requires -output, -generation, and a positive -max-provider-calls")
			return 2
		}
		observations, err := observeSimilarities(*datasetPath, *generation, *maxProviderCalls)
		if err != nil {
			fmt.Fprintf(stderr, "observe semantic cache similarities: %v\n", err)
			return 1
		}
		if err := writeNewJSON(*outputPath, observations); err != nil {
			fmt.Fprintf(stderr, "write similarity observations: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "semantic_cache_observations pairs=%d provider_calls=%d embedding_tokens=%d\n",
			len(observations.Pairs), observations.ProviderCalls, observations.EmbeddingTokens)
		return 0
	case "calibrate":
		if strings.TrimSpace(*observationsPath) == "" || strings.TrimSpace(*outputPath) == "" {
			fmt.Fprintln(stderr, "calibrate mode requires -observations and -output")
			return 2
		}
		report, err := calibrate(*datasetPath, *observationsPath)
		if err != nil {
			fmt.Fprintf(stderr, "calibrate semantic cache: %v\n", err)
			return 1
		}
		if err := writeNewJSON(*outputPath, report); err != nil {
			fmt.Fprintf(stderr, "write calibration report: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "semantic_cache_calibration enabled=%t threshold=%.6f precision=%.4f recall=%.4f provider_calls=0\n",
			report.ReleaseEnabled, report.Selection.Threshold,
			report.Selection.Calibration.Precision, report.Selection.Calibration.Recall)
		return 0
	default:
		fmt.Fprintln(stderr, "-mode must be draft, validate, observe, or calibrate")
		return 2
	}
}

func observeSimilarities(datasetPath string, generation int64, maxProviderCalls int) (similarityObservationSet, error) {
	dataset, err := readDataset(datasetPath)
	if err != nil {
		return similarityObservationSet{}, err
	}
	cfg, err := config.Load()
	if err != nil {
		return similarityObservationSet{}, err
	}
	if !cfg.Models.Embedding.Enabled {
		return similarityObservationSet{}, errors.New("models.embedding is disabled")
	}
	profile, err := cfg.Models.Embedding.Profile()
	if err != nil {
		return similarityObservationSet{}, err
	}
	client, err := platformembedding.NewClient(cfg.Models.Embedding, nil)
	if err != nil {
		return similarityObservationSet{}, err
	}
	texts := make([]string, 0, len(dataset.Pairs)*2)
	for _, pair := range dataset.Pairs {
		texts = append(texts, pair.AnchorQuestion, pair.CandidateQuestion)
	}
	batchSize := cfg.Models.Embedding.BatchSize
	expectedCalls := (len(texts) + batchSize - 1) / batchSize
	if expectedCalls > maxProviderCalls {
		return similarityObservationSet{}, fmt.Errorf(
			"cost guard: observe requires %d embedding calls, limit is %d", expectedCalls, maxProviderCalls,
		)
	}
	vectors := make([][]float32, 0, len(texts))
	providerCalls, tokens := 0, 0
	for offset := 0; offset < len(texts); offset += batchSize {
		end := min(offset+batchSize, len(texts))
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Models.Embedding.TimeoutMillis)*time.Millisecond)
		result, embedErr := client.Embed(ctx, knowledge.EmbeddingRequest{
			Texts: texts[offset:end], InputType: profile.QueryInputType,
		})
		cancel()
		providerCalls++
		if embedErr != nil {
			return similarityObservationSet{}, fmt.Errorf("embedding batch %d: %w", providerCalls, embedErr)
		}
		if err := result.Validate(end-offset, profile.Dimensions, profile.Normalize); err != nil {
			return similarityObservationSet{}, fmt.Errorf("validate embedding batch %d: %w", providerCalls, err)
		}
		vectors = append(vectors, result.Vectors...)
		tokens += result.Usage.TotalTokens
	}
	observations := similarityObservationSet{
		DatasetVersion: dataset.Version, EmbeddingProfile: profile.Fingerprint,
		NormalizationVersion: semanticcache.SemanticNormalizationVersion,
		Generation:           generation, RecordedAt: time.Now().UTC(), ProviderCalls: providerCalls, EmbeddingTokens: tokens,
		Pairs: make([]semanticcache.SimilarityObservation, 0, len(dataset.Pairs)),
	}
	for index, pair := range dataset.Pairs {
		similarity := cosineSimilarity(vectors[index*2], vectors[index*2+1])
		comparison := semanticcache.CompareQuestions(pair.AnchorQuestion, pair.CandidateQuestion)
		eligible := semanticcache.EligibleForLookup(semanticcache.Question{Text: pair.CandidateQuestion})
		observations.Pairs = append(observations.Pairs, semanticcache.SimilarityObservation{
			PairID: pair.ID, Similarity: similarity, Compatible: eligible && comparison.Compatible,
		})
	}
	return observations, nil
}

func cosineSimilarity(left, right []float32) float64 {
	var dot, leftNorm, rightNorm float64
	for index := range left {
		dot += float64(left[index]) * float64(right[index])
		leftNorm += float64(left[index]) * float64(left[index])
		rightNorm += float64(right[index]) * float64(right[index])
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	return dot / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm))
}

func readDataset(path string) (semanticcache.EvaluationDataset, error) {
	var dataset semanticcache.EvaluationDataset
	if err := readStrictJSON(path, &dataset); err != nil {
		return semanticcache.EvaluationDataset{}, err
	}
	if err := dataset.Validate(false); err != nil {
		return semanticcache.EvaluationDataset{}, err
	}
	return dataset, nil
}

func calibrate(datasetPath, observationsPath string) (calibrationReport, error) {
	dataset, err := readDataset(datasetPath)
	if err != nil {
		return calibrationReport{}, err
	}
	if err := dataset.Validate(true); err != nil {
		return calibrationReport{}, err
	}
	var observations similarityObservationSet
	if err := readStrictJSON(observationsPath, &observations); err != nil {
		return calibrationReport{}, err
	}
	if observations.DatasetVersion != dataset.Version || strings.TrimSpace(observations.EmbeddingProfile) == "" ||
		observations.NormalizationVersion != semanticcache.SemanticNormalizationVersion || observations.Generation < 1 ||
		observations.RecordedAt.IsZero() || observations.ProviderCalls < 0 || observations.EmbeddingTokens < 0 {
		return calibrationReport{}, errors.New("similarity observation identity is invalid")
	}
	selection, err := semanticcache.SelectThreshold(dataset.Pairs, observations.Pairs, 0.98)
	if err != nil {
		return calibrationReport{}, err
	}
	report := calibrationReport{
		SchemaVersion: "semantic_cache_calibration_v1", DatasetVersion: dataset.Version,
		EmbeddingProfile:     observations.EmbeddingProfile,
		NormalizationVersion: observations.NormalizationVersion, Generation: observations.Generation,
		CreatedAt: time.Now().UTC(), Selection: selection,
		ProviderCalls: observations.ProviderCalls, EmbeddingTokens: observations.EmbeddingTokens,
	}
	if selection.Enabled {
		holdout, err := semanticcache.EvaluateThreshold(
			dataset.Pairs, observations.Pairs, semanticcache.EvaluationSplitHoldout, selection.Threshold,
		)
		if err != nil {
			return calibrationReport{}, err
		}
		report.Holdout = &holdout
		applyHoldoutAcceptance(&report, holdout)
	} else {
		report.RejectionReason = "calibration_precision_gate_failed"
	}
	return report, nil
}

func applyHoldoutAcceptance(report *calibrationReport, holdout semanticcache.CacheMetrics) {
	if report == nil {
		return
	}
	report.ReleaseEnabled = holdout.Hits > 0 && holdout.Precision >= report.Selection.PrecisionGate
	if !report.ReleaseEnabled {
		report.Selection.Enabled = false
		report.RejectionReason = "holdout_precision_gate_failed"
	}
}

func readStrictJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) == 0 || len(data) > maxSemanticCacheEvaluationBytes {
		return errors.New("evaluation JSON size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("evaluation JSON contains trailing data")
	}
	return nil
}

func writeNewJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
