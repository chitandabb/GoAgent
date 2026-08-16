// Command mesguard-rag-retrieval-eval evaluates the PostgreSQL FTS retrieval baseline.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformembedding "github.com/chitandabb/GoAgent/internal/platform/dashscopeembedding"
	platformrerank "github.com/chitandabb/GoAgent/internal/platform/dashscopererank"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const (
	ragRetrieverFTS       = "postgres-fts-v1"
	ragRetrieverVector    = "postgres-vector-v1"
	ragRetrieverRRF       = "postgres-rrf-v1"
	ragRetrieverRRFRerank = "postgres-rrf-rerank-v1"
)

var errRollbackRAGEvaluation = errors.New("rollback RAG evaluation fixtures")

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("mesguard-rag-retrieval-eval", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	corpusPath := flags.String("corpus", "testdata/rag-retrieval-v1.corpus.jsonl", "versioned retrieval corpus")
	datasetPath := flags.String("dataset", "testdata/rag-retrieval-v1.jsonl", "versioned retrieval cases")
	outputPath := flags.String("output", "testdata/rag-retrieval-v1.observations.jsonl", "observation JSONL output")
	summaryPath := flags.String("summary", "testdata/rag-retrieval-v1.summary.json", "summary JSON output")
	retrieverName := flags.String("retriever", "fts", "retriever variant: fts, vector, rrf, or rrf-rerank")
	caseID := flags.String("case-id", "", "optional single evaluation case id")
	embeddingPrice := flags.Float64("embedding-price-cny-per-million", 0, "optional embedding input price in CNY per million tokens")
	rerankPrice := flags.Float64("rerank-price-cny-per-million", 0, "optional rerank input price in CNY per million tokens")
	timeout := flags.Duration("timeout", 2*time.Minute, "total evaluation timeout")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("usage: mesguard-rag-retrieval-eval [-retriever fts|vector|rrf|rrf-rerank] [-case-id id] [-corpus path] [-dataset path] [-output path] [-summary path] [-embedding-price-cny-per-million price] [-rerank-price-cny-per-million price] [-timeout duration]")
	}
	if *timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	if *embeddingPrice < 0 {
		return errors.New("embedding-price-cny-per-million must not be negative")
	}
	if *rerankPrice < 0 {
		return errors.New("rerank-price-cny-per-million must not be negative")
	}
	if *retrieverName != "fts" && *retrieverName != "vector" && *retrieverName != "rrf" && *retrieverName != "rrf-rerank" {
		return errors.New("retriever must be fts, vector, rrf, or rrf-rerank")
	}
	documents, err := readJSONL[knowledge.RetrievalEvaluationDocument](*corpusPath, func(value knowledge.RetrievalEvaluationDocument) error {
		return value.Validate()
	})
	if err != nil {
		return err
	}
	cases, err := readJSONL[knowledge.RetrievalEvaluationCase](*datasetPath, func(value knowledge.RetrievalEvaluationCase) error {
		return value.Validate()
	})
	if err != nil {
		return err
	}
	if strings.TrimSpace(*caseID) != "" {
		filtered := cases[:0]
		for _, definition := range cases {
			if definition.CaseID == strings.TrimSpace(*caseID) {
				filtered = append(filtered, definition)
			}
		}
		if len(filtered) != 1 {
			return fmt.Errorf("case-id %q was not found exactly once", *caseID)
		}
		cases = filtered
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	db, closeDB, err := platformpostgres.Open(ctx, cfg.Postgres, zap.NewNop())
	if err != nil {
		return fmt.Errorf("open postgres: %w", err)
	}
	defer closeDB()
	var embedder knowledge.Embedder
	var embeddingMeasurement *measuredEmbedder
	var embeddingProfile knowledge.EmbeddingProfile
	if *retrieverName != "fts" {
		if !cfg.Models.Embedding.Enabled {
			return errors.New("embedding model is disabled")
		}
		embeddingClientConfig := cfg.Models.Embedding
		embeddingClientConfig.MaxAttempts = 1
		client, err := platformembedding.NewClient(embeddingClientConfig, nil)
		if err != nil {
			return fmt.Errorf("create embedding client: %w", err)
		}
		embeddingMeasurement = &measuredEmbedder{delegate: client}
		embedder = embeddingMeasurement
		embeddingProfile, err = cfg.Models.Embedding.Profile()
		if err != nil {
			return err
		}
		if err := platformpostgres.NewKnowledgeWorkerRepository(db).EnsureEmbeddingProfile(ctx, embeddingProfile); err != nil {
			return err
		}
	}
	var reranker knowledge.Reranker
	var rerankMeasurement *measuredReranker
	if *retrieverName == "rrf-rerank" {
		cfg.Models.Rerank.Enabled = true
		client, err := platformrerank.NewClient(cfg.Models.Rerank, nil)
		if err != nil {
			return fmt.Errorf("create rerank client: %w", err)
		}
		rerankMeasurement = &measuredReranker{delegate: client}
		reranker = rerankMeasurement
	}

	var observations []knowledge.RetrievalEvaluationObservation
	var summary knowledge.RetrievalEvaluationSummary
	evaluationErr := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		actorID := uuid.New()
		if err := tx.Exec(`
INSERT INTO users (id, username, display_name, password_hash, role)
VALUES (?, ?, 'RAG Evaluation', 'evaluation-only', 'analyst')`,
			actorID, "rag_eval_"+strings.ReplaceAll(uuid.NewString(), "-", "")[:12],
		).Error; err != nil {
			return err
		}
		repository := platformpostgres.NewKnowledgeRepository(tx)
		documentKeyByID := make(map[uuid.UUID]string, len(documents))
		ingestionStartedAt := time.Now()
		chunkCount := 0
		for _, document := range documents {
			documentID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(document.DatasetVersion+":"+document.DocumentKey))
			if _, err := repository.CreateDocument(ctx, knowledge.CreateDocumentInput{
				ID: documentID, Scope: knowledge.ScopeGlobal, Title: document.Title, CreatedBy: actorID,
			}); err != nil {
				return err
			}
			chunks, err := knowledge.ChunkMarkdown(document.Content, knowledge.TextChunkOptions{MaxRunes: 700, OverlapRunes: 80})
			if err != nil {
				return fmt.Errorf("chunk document %q: %w", document.DocumentKey, err)
			}
			chunkCount += len(chunks)
			if _, err := repository.PublishVersion(ctx, knowledge.PublishVersionInput{
				ID: uuid.New(), DocumentID: documentID, SourceMediaType: document.MediaType,
				SourceSizeBytes: int64(len([]byte(document.Content))), SourceSHA256: knowledge.SHA256Hex(document.Content),
				ParserVersion: "markdown-v1", CreatedBy: actorID, Chunks: chunks,
			}); err != nil {
				return err
			}
			documentKeyByID[documentID] = document.DocumentKey
		}
		if embedder != nil {
			if err := indexEvaluationEmbeddings(ctx, tx, embeddingProfile, embedder, cfg.Models.Embedding.BatchSize, documentKeyByID); err != nil {
				return err
			}
		}
		ingestionDuration := float64(time.Since(ingestionStartedAt).Microseconds()) / 1000

		observations = make([]knowledge.RetrievalEvaluationObservation, 0, len(cases))
		for _, definition := range cases {
			startedAt := time.Now()
			var results []knowledge.SearchResult
			if *retrieverName == "fts" {
				results, err = repository.SearchFTS(ctx, actorID, definition.Query, definition.K)
			} else if *retrieverName == "vector" {
				var queryEmbedding knowledge.EmbeddingResult
				queryEmbedding, err = embedder.Embed(ctx, knowledge.EmbeddingRequest{
					Texts: []string{definition.Query}, InputType: embeddingProfile.QueryInputType,
				})
				if err == nil {
					err = queryEmbedding.Validate(1, embeddingProfile.Dimensions, embeddingProfile.Normalize)
				}
				if err == nil {
					results, err = repository.SearchVector(ctx, actorID, embeddingProfile.ID, queryEmbedding.Vectors[0], definition.K)
				}
			} else if *retrieverName == "rrf" {
				retriever, constructErr := knowledge.NewHybridRetriever(repository, embedder, embeddingProfile, definition.K)
				if constructErr == nil {
					var hybrid knowledge.HybridSearch
					hybrid, err = retriever.Search(ctx, actorID, definition.Query, definition.K)
					results = hybrid.Results
				} else {
					err = constructErr
				}
			} else {
				candidateN := cfg.Models.Rerank.MaxCandidates
				service, constructErr := knowledge.NewSearchServiceWithReranker(
					repository, embedder, embeddingProfile, candidateN, reranker, candidateN,
				)
				if constructErr == nil {
					var hybrid knowledge.HybridSearch
					hybrid, err = service.Search(ctx, actorID, definition.Query, definition.K)
					results = hybrid.Results
				} else {
					err = constructErr
				}
			}
			if err != nil {
				return fmt.Errorf("search case %q: %w", definition.CaseID, err)
			}
			returned := make([]string, 0, len(results))
			seen := make(map[string]struct{}, len(results))
			for _, result := range results {
				key := documentKeyByID[result.DocumentID]
				if key == "" {
					return fmt.Errorf("search case %q returned unknown document %s", definition.CaseID, result.DocumentID)
				}
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
				returned = append(returned, key)
			}
			rank := knowledge.FirstRelevantRank(definition.RelevantDocumentKeys, returned)
			observation := knowledge.RetrievalEvaluationObservation{
				DatasetVersion: definition.DatasetVersion, CaseID: definition.CaseID,
				RunID: definition.CaseID + "-" + uuid.NewString(), Retriever: retrieverVersion(*retrieverName),
				Query: definition.Query, K: definition.K,
				RelevantDocumentKeys: append([]string(nil), definition.RelevantDocumentKeys...),
				ReturnedDocumentKeys: returned, FirstRelevantRank: rank, HitAtK: rank > 0,
				DurationMillis: float64(time.Since(startedAt).Microseconds()) / 1000,
			}
			if err := observation.Validate(); err != nil {
				return err
			}
			observations = append(observations, observation)
			fmt.Fprintf(os.Stdout, "%s hit=%t rank=%d returned=%v\n", definition.CaseID, observation.HitAtK, rank, returned)
		}
		summary, err = knowledge.EvaluateRetrieval(
			documents, cases, observations, retrieverVersion(*retrieverName), chunkCount, ingestionDuration,
		)
		if err != nil {
			return err
		}
		if embeddingMeasurement != nil {
			metrics := embeddingMeasurement.Snapshot()
			summary.EmbeddingDocumentRequests = metrics.DocumentRequests
			summary.EmbeddingQueryRequests = metrics.QueryRequests
			summary.EmbeddingDocumentTokens = metrics.DocumentTokens
			summary.EmbeddingQueryTokens = metrics.QueryTokens
			summary.EmbeddingTotalTokens = metrics.DocumentTokens + metrics.QueryTokens
			summary.EmbeddingDocumentDuration = metrics.DocumentDurationMillis
			summary.EmbeddingQueryDuration = metrics.QueryDurationMillis
			summary.EmbeddingPricePerMillion = *embeddingPrice
			if *embeddingPrice > 0 {
				summary.EmbeddingEstimatedCostCNY = float64(summary.EmbeddingTotalTokens) / 1_000_000 * *embeddingPrice
			}
		}
		if rerankMeasurement != nil {
			metrics := rerankMeasurement.Snapshot()
			summary.RerankRequests = metrics.Requests
			summary.RerankTotalTokens = metrics.TotalTokens
			summary.RerankDurationMillis = metrics.DurationMillis
			summary.RerankPricePerMillion = *rerankPrice
			if *rerankPrice > 0 {
				summary.RerankEstimatedCostCNY = float64(summary.RerankTotalTokens) / 1_000_000 * *rerankPrice
			}
		}
		return errRollbackRAGEvaluation
	})
	if !errors.Is(evaluationErr, errRollbackRAGEvaluation) {
		return evaluationErr
	}
	return writeEvaluationFiles(*outputPath, *summaryPath, observations, summary)
}

type embeddingMetrics struct {
	DocumentRequests       int
	QueryRequests          int
	DocumentTokens         int
	QueryTokens            int
	DocumentDurationMillis float64
	QueryDurationMillis    float64
}

type measuredEmbedder struct {
	delegate knowledge.Embedder
	mu       sync.Mutex
	metrics  embeddingMetrics
}

type rerankMetrics struct {
	Requests       int
	TotalTokens    int
	DurationMillis float64
}

type measuredReranker struct {
	delegate knowledge.Reranker
	mu       sync.Mutex
	metrics  rerankMetrics
}

func (m *measuredReranker) Rerank(ctx context.Context, request knowledge.RerankRequest) (knowledge.RerankResult, error) {
	startedAt := time.Now()
	result, err := m.delegate.Rerank(ctx, request)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.metrics.Requests++
	m.metrics.DurationMillis += float64(time.Since(startedAt).Microseconds()) / 1000
	if err == nil {
		m.metrics.TotalTokens += result.Usage.TotalTokens
	}
	return result, err
}

func (m *measuredReranker) Snapshot() rerankMetrics {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.metrics
}

func (m *measuredEmbedder) Embed(ctx context.Context, request knowledge.EmbeddingRequest) (knowledge.EmbeddingResult, error) {
	startedAt := time.Now()
	result, err := m.delegate.Embed(ctx, request)
	durationMillis := float64(time.Since(startedAt).Microseconds()) / 1000

	m.mu.Lock()
	defer m.mu.Unlock()
	if request.InputType == knowledge.EmbeddingInputDocument {
		m.metrics.DocumentRequests++
		m.metrics.DocumentDurationMillis += durationMillis
		if err == nil {
			m.metrics.DocumentTokens += result.Usage.TotalTokens
		}
	} else if request.InputType == knowledge.EmbeddingInputQuery {
		m.metrics.QueryRequests++
		m.metrics.QueryDurationMillis += durationMillis
		if err == nil {
			m.metrics.QueryTokens += result.Usage.TotalTokens
		}
	}
	return result, err
}

func (m *measuredEmbedder) Snapshot() embeddingMetrics {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.metrics
}

func retrieverVersion(name string) string {
	switch name {
	case "vector":
		return ragRetrieverVector
	case "rrf":
		return ragRetrieverRRF
	case "rrf-rerank":
		return ragRetrieverRRFRerank
	default:
		return ragRetrieverFTS
	}
}

type evaluationChunkRow struct {
	ChunkID           uuid.UUID `gorm:"column:chunk_id"`
	DocumentVersionID uuid.UUID `gorm:"column:document_version_id"`
	ContentSHA256     string    `gorm:"column:content_sha256"`
	ContentText       string    `gorm:"column:content_text"`
}

func indexEvaluationEmbeddings(
	ctx context.Context,
	tx *gorm.DB,
	profile knowledge.EmbeddingProfile,
	embedder knowledge.Embedder,
	batchSize int,
	documentKeyByID map[uuid.UUID]string,
) error {
	if len(documentKeyByID) == 0 {
		return errors.New("evaluation embedding corpus is empty")
	}
	versionIDs := make([]uuid.UUID, 0, len(documentKeyByID))
	for documentID := range documentKeyByID {
		var versionIDText string
		if err := tx.WithContext(ctx).Raw(
			"SELECT id FROM knowledge_document_versions WHERE document_id = ? AND is_current = true", documentID,
		).Scan(&versionIDText).Error; err != nil {
			return err
		}
		versionID, err := uuid.Parse(versionIDText)
		if err != nil {
			return fmt.Errorf("parse evaluation version id: %w", err)
		}
		versionIDs = append(versionIDs, versionID)
	}
	var rows []evaluationChunkRow
	if err := tx.WithContext(ctx).Raw(`
SELECT id AS chunk_id, document_version_id, content_sha256, content_text
FROM knowledge_chunks
WHERE document_version_id IN ?
ORDER BY document_version_id, ordinal`, versionIDs).Scan(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return errors.New("evaluation embedding corpus contains no chunks")
	}
	if batchSize < 1 || batchSize > 10 {
		batchSize = 10
	}
	for start := 0; start < len(rows); start += batchSize {
		end := start + batchSize
		if end > len(rows) {
			end = len(rows)
		}
		texts := make([]string, end-start)
		for index := start; index < end; index++ {
			texts[index-start] = rows[index].ContentText
		}
		output, err := embedder.Embed(ctx, knowledge.EmbeddingRequest{
			Texts: texts, InputType: profile.DocumentInputType,
		})
		if err != nil {
			return fmt.Errorf("embed evaluation batch: %w", err)
		}
		if err := output.Validate(len(texts), profile.Dimensions, profile.Normalize); err != nil {
			return err
		}
		for offset, vector := range output.Vectors {
			row := rows[start+offset]
			if err := tx.Exec(`
INSERT INTO knowledge_chunk_embeddings (chunk_id, profile_id, content_sha256, embedding)
VALUES (?, ?, ?, ?)`, row.ChunkID, profile.ID, row.ContentSHA256, pgvector.NewVector(vector)).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func readJSONL[T any](path string, validate func(T) error) ([]T, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	var result []T
	for line := 1; scanner.Scan(); line++ {
		contents := bytes.TrimSpace(scanner.Bytes())
		if len(contents) == 0 {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(contents))
		decoder.DisallowUnknownFields()
		var value T
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode %s line %d: %w", path, line, err)
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			if err == nil {
				err = errors.New("multiple JSON values")
			}
			return nil, fmt.Errorf("decode %s line %d: %w", path, line, err)
		}
		if err := validate(value); err != nil {
			return nil, fmt.Errorf("validate %s line %d: %w", path, line, err)
		}
		result = append(result, value)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%s contains no records", path)
	}
	return result, nil
}

func writeEvaluationFiles(
	outputPath, summaryPath string,
	observations []knowledge.RetrievalEvaluationObservation,
	summary knowledge.RetrievalEvaluationSummary,
) error {
	outputTemp := outputPath + ".tmp-" + uuid.NewString()
	summaryTemp := summaryPath + ".tmp-" + uuid.NewString()
	defer os.Remove(outputTemp)
	defer os.Remove(summaryTemp)
	file, err := os.OpenFile(outputTemp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	for _, observation := range observations {
		if err := encoder.Encode(observation); err != nil {
			_ = file.Close()
			return err
		}
	}
	if err := file.Close(); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(summaryTemp, append(encoded, '\n'), 0o600); err != nil {
		return err
	}
	if err := replaceEvaluationFile(outputTemp, outputPath); err != nil {
		return err
	}
	return replaceEvaluationFile(summaryTemp, summaryPath)
}

func replaceEvaluationFile(source, target string) error {
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(source, target)
}
