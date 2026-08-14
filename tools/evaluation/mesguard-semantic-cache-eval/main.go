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
	"slices"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformembedding "github.com/chitandabb/GoAgent/internal/platform/dashscopeembedding"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	platformredis "github.com/chitandabb/GoAgent/internal/platform/redis"
	platformredisstack "github.com/chitandabb/GoAgent/internal/platform/redisstack"
	"github.com/chitandabb/GoAgent/internal/semanticcache"
	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"go.uber.org/zap"
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
	PostgresQuality      *fullIndexQualityReport               `json:"postgresQuality,omitempty"`
	RedisStackQuality    *fullIndexQualityReport               `json:"redisStackQuality,omitempty"`
}

type fullIndexQualityReport struct {
	Threshold                     float64                    `json:"threshold"`
	StrictPairIdentityCalibration semanticcache.CacheMetrics `json:"strictPairIdentityCalibration"`
	StrictPairIdentityHoldout     semanticcache.CacheMetrics `json:"strictPairIdentityHoldout"`
	CrossCandidateHits            int                        `json:"crossCandidateHits"`
	CrossCandidates               []wrongCandidate           `json:"crossCandidates,omitempty"`
	LookupP50Millis               float64                    `json:"lookupP50Millis"`
	LookupP95Millis               float64                    `json:"lookupP95Millis"`
}

type wrongCandidate struct {
	QueryPairID    string  `json:"queryPairId"`
	ReturnedPairID string  `json:"returnedPairId"`
	Similarity     float64 `json:"similarity"`
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
	cacheProvider := flags.String("cache-provider", "pairwise", "observe backend: pairwise, postgres, redis-stack, or all")
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
		provider := strings.ToLower(strings.TrimSpace(*cacheProvider))
		if strings.TrimSpace(*outputPath) == "" || *observationsPath != "" || *generation < 1 || *maxProviderCalls < 1 ||
			provider != "pairwise" && provider != "postgres" && provider != "redis-stack" && provider != "all" {
			fmt.Fprintln(stderr, "observe mode requires -output, -generation, and a positive -max-provider-calls")
			return 2
		}
		observations, err := observeSimilarities(*datasetPath, *generation, *maxProviderCalls, provider)
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

func observeSimilarities(
	datasetPath string,
	generation int64,
	maxProviderCalls int,
	cacheProvider string,
) (similarityObservationSet, error) {
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
	if cacheProvider == "postgres" || cacheProvider == "all" {
		quality, err := observePostgresQuality(context.Background(), cfg, dataset, profile, vectors)
		if err != nil {
			return similarityObservationSet{}, err
		}
		observations.PostgresQuality = &quality
	}
	if cacheProvider == "redis-stack" || cacheProvider == "all" {
		quality, err := observeRedisStackQuality(context.Background(), cfg, dataset, profile, vectors, generation)
		if err != nil {
			return similarityObservationSet{}, err
		}
		observations.RedisStackQuality = &quality
	}
	return observations, nil
}

func observeRedisStackQuality(
	ctx context.Context,
	cfg config.Config,
	dataset semanticcache.EvaluationDataset,
	profile knowledge.EmbeddingProfile,
	vectors [][]float32,
	generation int64,
) (fullIndexQualityReport, error) {
	password, err := cfg.SemanticAnswerCache.RedisStack.Password()
	if err != nil {
		return fullIndexQualityReport{}, err
	}
	client, err := platformredis.Open(ctx, config.RedisConfig{
		Host: cfg.SemanticAnswerCache.RedisStack.Host, Port: cfg.SemanticAnswerCache.RedisStack.Port,
		Password: password, Database: cfg.SemanticAnswerCache.RedisStack.Database,
	})
	if err != nil {
		return fullIndexQualityReport{}, err
	}
	runID := strings.ReplaceAll(uuid.NewString(), "-", "")
	indexName := cfg.SemanticAnswerCache.RedisStack.IndexName + "_quality_" + runID
	keyPrefix := cfg.SemanticAnswerCache.RedisStack.KeyPrefix + "quality:" + runID + ":"
	defer func() {
		_ = client.Del(context.Background(), keyPrefix+"capacity").Err()
		_ = client.Do(context.Background(), "FT.DROPINDEX", indexName, "DD").Err()
		_ = client.Close()
	}()
	authority := fixedGenerationAuthority{generation: generation}
	cache, err := platformredisstack.NewSemanticAnswerCache(ctx, client, authority, platformredisstack.Config{
		IndexName: indexName, KeyPrefix: keyPrefix, MaxRecords: 1000, TTLJitterRatio: 0,
	})
	if err != nil {
		return fullIndexQualityReport{}, err
	}
	now := time.Now().UTC()
	for index, pair := range dataset.Pairs {
		hash, hashErr := semanticcache.ExactQuestionKey(pair.AnchorQuestion)
		if hashErr != nil {
			return fullIndexQualityReport{}, hashErr
		}
		sourceRunID := uuid.New()
		source := semanticcache.Source{
			Position: 0, SourceType: "knowledge_chunk",
			SourceRef:     "knowledge:" + uuid.NewString() + "/" + uuid.NewString(),
			ContentSHA256: strings.Repeat("a", 64),
		}
		put := semanticcache.PutInput{
			QuestionHash: hash, TTL: time.Hour,
			Answer: semanticcache.Answer{
				Content: pair.ID, Citations: []semanticcache.Source{source},
				RetrievedSources: []semanticcache.Source{source}, SourceRunID: sourceRunID,
				ModelProvider: "fixture", ModelID: "fixture", PromptVersion: "quality-v1", CreatedAt: now,
			},
		}
		if err := cache.Put(ctx, put); err != nil {
			return fullIndexQualityReport{}, fmt.Errorf("seed pair %s: %w", pair.ID, err)
		}
		if err := cache.IndexSemantic(ctx, semanticcache.SemanticIndexInput{
			QuestionHash: hash, Question: pair.AnchorQuestion, Vector: vectors[index*2],
			ProfileID: profile.ID, ProfileFingerprint: profile.Fingerprint,
			NormalizationVersion: semanticcache.SemanticNormalizationVersion, SourceRunID: sourceRunID,
		}); err != nil {
			return fullIndexQualityReport{}, fmt.Errorf("index pair %s: %w", pair.ID, err)
		}
	}
	report, err := evaluateFullIndexQuality(ctx, dataset, func(
		lookupCtx context.Context,
		pair semanticcache.EvaluationPair,
	) (fullIndexLookupResult, error) {
		index := slices.IndexFunc(dataset.Pairs, func(candidate semanticcache.EvaluationPair) bool {
			return candidate.ID == pair.ID
		})
		if index < 0 {
			return fullIndexLookupResult{}, semanticcache.ErrInvalidRecord
		}
		startedAt := time.Now()
		answer, hit, lookupErr := cache.LookupSemantic(lookupCtx, semanticcache.SemanticLookupInput{
			Question: pair.CandidateQuestion, Vector: vectors[index*2+1], ProfileID: profile.ID,
			ProfileFingerprint: profile.Fingerprint, NormalizationVersion: semanticcache.SemanticNormalizationVersion,
			MinimumSimilarity: cfg.SemanticAnswerCache.SemanticMinimumSimilarity,
			CandidateLimit:    cfg.SemanticAnswerCache.SemanticCandidateLimit, Now: now,
		})
		if lookupErr != nil {
			return fullIndexLookupResult{}, fmt.Errorf("lookup pair %s: %w", pair.ID, lookupErr)
		}
		return fullIndexLookupResult{
			PairID: answer.Content, Similarity: answer.Similarity, Hit: hit,
			Duration: time.Since(startedAt),
		}, nil
	})
	report.Threshold = cfg.SemanticAnswerCache.SemanticMinimumSimilarity
	return report, err
}

func observePostgresQuality(
	ctx context.Context,
	cfg config.Config,
	dataset semanticcache.EvaluationDataset,
	_ knowledge.EmbeddingProfile,
	vectors [][]float32,
) (fullIndexQualityReport, error) {
	db, closeDB, err := platformpostgres.Open(ctx, cfg.Postgres, zap.NewNop())
	if err != nil {
		return fullIndexQualityReport{}, err
	}
	defer func() { _ = closeDB() }()
	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return fullIndexQualityReport{}, tx.Error
	}
	defer func() { _ = tx.Rollback().Error }()
	if err := tx.Exec(`
CREATE TEMP TABLE semantic_cache_quality_eval (
    pair_id text PRIMARY KEY,
    question_text text NOT NULL,
    question_embedding vector(1024) NOT NULL
) ON COMMIT DROP`).Error; err != nil {
		return fullIndexQualityReport{}, err
	}
	for index, pair := range dataset.Pairs {
		if err := tx.Exec(`
INSERT INTO semantic_cache_quality_eval (pair_id, question_text, question_embedding)
VALUES (?, ?, ?)`, pair.ID, pair.AnchorQuestion, pgvector.NewVector(vectors[index*2])).Error; err != nil {
			return fullIndexQualityReport{}, fmt.Errorf("seed PostgreSQL pair %s: %w", pair.ID, err)
		}
	}
	report, err := evaluateFullIndexQuality(ctx, dataset, func(
		lookupCtx context.Context,
		pair semanticcache.EvaluationPair,
	) (fullIndexLookupResult, error) {
		index := slices.IndexFunc(dataset.Pairs, func(candidate semanticcache.EvaluationPair) bool {
			return candidate.ID == pair.ID
		})
		if index < 0 {
			return fullIndexLookupResult{}, semanticcache.ErrInvalidRecord
		}
		vector := pgvector.NewVector(vectors[index*2+1])
		var candidates []postgresQualityCandidate
		startedAt := time.Now()
		result := tx.WithContext(lookupCtx).Raw(`
SELECT pair_id, question_text, 1 - (question_embedding <=> ?) AS similarity
FROM semantic_cache_quality_eval
WHERE 1 - (question_embedding <=> ?) >= ?
ORDER BY question_embedding <=> ?, pair_id
LIMIT ?`, vector, vector, cfg.SemanticAnswerCache.SemanticMinimumSimilarity, vector,
			cfg.SemanticAnswerCache.SemanticCandidateLimit).Scan(&candidates)
		duration := time.Since(startedAt)
		if result.Error != nil {
			return fullIndexLookupResult{}, result.Error
		}
		for _, candidate := range candidates {
			if semanticcache.CompareQuestions(pair.CandidateQuestion, candidate.QuestionText).Compatible {
				return fullIndexLookupResult{
					PairID: candidate.PairID, Similarity: candidate.Similarity, Hit: true, Duration: duration,
				}, nil
			}
		}
		return fullIndexLookupResult{Duration: duration}, nil
	})
	report.Threshold = cfg.SemanticAnswerCache.SemanticMinimumSimilarity
	return report, err
}

type postgresQualityCandidate struct {
	PairID       string  `gorm:"column:pair_id"`
	QuestionText string  `gorm:"column:question_text"`
	Similarity   float64 `gorm:"column:similarity"`
}

type fullIndexLookupResult struct {
	PairID     string
	Similarity float64
	Hit        bool
	Duration   time.Duration
}

func evaluateFullIndexQuality(
	ctx context.Context,
	dataset semanticcache.EvaluationDataset,
	lookup func(context.Context, semanticcache.EvaluationPair) (fullIndexLookupResult, error),
) (fullIndexQualityReport, error) {
	metrics := map[semanticcache.EvaluationSplit]*semanticcache.CacheMetrics{
		semanticcache.EvaluationSplitCalibration: {}, semanticcache.EvaluationSplitHoldout: {},
	}
	durations := make([]time.Duration, 0, len(dataset.Pairs))
	crossCandidates := make([]wrongCandidate, 0)
	for _, pair := range dataset.Pairs {
		metric := metrics[pair.Split]
		metric.Total++
		if !semanticcache.EligibleForLookup(semanticcache.Question{Text: pair.CandidateQuestion}) {
			if pair.Reusable {
				metric.FalseNegatives++
			} else {
				metric.TrueNegatives++
			}
			continue
		}
		result, err := lookup(ctx, pair)
		if err != nil {
			return fullIndexQualityReport{}, err
		}
		durations = append(durations, result.Duration)
		correctHit := result.Hit && result.PairID == pair.ID
		if result.Hit {
			metric.Hits++
		}
		if result.Hit && !correctHit {
			crossCandidates = append(crossCandidates, wrongCandidate{
				QueryPairID: pair.ID, ReturnedPairID: result.PairID, Similarity: result.Similarity,
			})
		}
		switch {
		case pair.Reusable && correctHit:
			metric.TruePositives++
		case pair.Reusable:
			metric.FalseNegatives++
		case result.Hit:
			metric.FalsePositives++
		default:
			metric.TrueNegatives++
		}
	}
	for _, metric := range metrics {
		if metric.Hits > 0 {
			metric.Precision = float64(metric.TruePositives) / float64(metric.Hits)
		}
		positives := metric.TruePositives + metric.FalseNegatives
		if positives > 0 {
			metric.Recall = float64(metric.TruePositives) / float64(positives)
		}
		if metric.Total > 0 {
			metric.HitRate = float64(metric.Hits) / float64(metric.Total)
		}
	}
	report := fullIndexQualityReport{
		StrictPairIdentityCalibration: *metrics[semanticcache.EvaluationSplitCalibration],
		StrictPairIdentityHoldout:     *metrics[semanticcache.EvaluationSplitHoldout],
		CrossCandidateHits:            len(crossCandidates), CrossCandidates: crossCandidates,
	}
	if len(durations) > 0 {
		p50, p95 := durationPercentiles(durations)
		report.LookupP50Millis = float64(p50) / float64(time.Millisecond)
		report.LookupP95Millis = float64(p95) / float64(time.Millisecond)
	}
	return report, nil
}

type fixedGenerationAuthority struct{ generation int64 }

func (a fixedGenerationAuthority) CurrentGeneration(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return a.generation, nil
}

func (a fixedGenerationAuthority) AuthorizePut(ctx context.Context, _ semanticcache.PutInput) (int64, error) {
	return a.CurrentGeneration(ctx)
}

func (a fixedGenerationAuthority) AuthorizeSemanticIndex(ctx context.Context, _ semanticcache.SemanticIndexInput) (int64, error) {
	return a.CurrentGeneration(ctx)
}

func durationPercentiles(values []time.Duration) (time.Duration, time.Duration) {
	values = append([]time.Duration(nil), values...)
	slices.Sort(values)
	index := func(percentile float64) int {
		return max(0, min(int(math.Ceil(float64(len(values))*percentile))-1, len(values)-1))
	}
	return values[index(0.5)], values[index(0.95)]
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
