// Command mesguard-rag-paired-observe runs bounded Advanced RAG pairs against the production retrieval chain.
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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/chitandabb/GoAgent/internal/bootstrap"
	"github.com/chitandabb/GoAgent/internal/knowledge"
	platformchatmodel "github.com/chitandabb/GoAgent/internal/platform/chatmodel"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformembedding "github.com/chitandabb/GoAgent/internal/platform/dashscopeembedding"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	"github.com/cloudwego/eino/components/model"
	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const (
	axisContext     = "context"
	axisRewrite     = "rewrite"
	axisCompression = "compression"

	retrieverRRF       = "rrf"
	retrieverRRFRerank = "rrf-rerank"
)

var errRollbackAdvancedRAGFixture = errors.New("rollback Advanced RAG evaluation fixture")

type commandOptions struct {
	corpusPath                   string
	datasetPath                  string
	observationsPath             string
	summaryPath                  string
	axis                         string
	retriever                    string
	caseID                       string
	maxCases                     int
	timeout                      time.Duration
	validateOnly                 bool
	listChunks                   bool
	executeProvider              bool
	requireCompressionAcceptance bool
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	options, err := parseOptions(args)
	if err != nil {
		return err
	}
	corpus, err := readStrictJSON[knowledge.AdvancedRetrievalEvaluationCorpus](options.corpusPath)
	if err != nil {
		return err
	}
	if options.listChunks {
		return printCorpusChunks(corpus)
	}
	cases, err := readStrictJSONL(options.datasetPath, func(value knowledge.AdvancedRetrievalEvaluationCase) error {
		return value.Validate()
	})
	if err != nil {
		return err
	}
	chunksByDocument, err := knowledge.ValidateAdvancedRetrievalFixture(corpus, cases)
	if err != nil {
		return err
	}
	selected, err := selectCases(cases, options.caseID, options.maxCases)
	if err != nil {
		return err
	}
	chunkCount := 0
	for _, chunks := range chunksByDocument {
		chunkCount += len(chunks)
	}
	if options.validateOnly {
		fmt.Printf(
			"dataset=%s documents=%d chunks=%d cases=%d selected_cases=%d validation=passed\n",
			corpus.DatasetVersion, len(corpus.Documents), chunkCount, len(cases), len(selected),
		)
		return nil
	}
	if !options.executeProvider {
		return errors.New("provider execution is disabled; review the budget and add -execute-provider")
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !cfg.Models.Embedding.Enabled {
		return errors.New("Advanced RAG paired observation requires the configured embedding model")
	}
	if options.axis == axisCompression && !cfg.Knowledge.Retrieval.ContextCompression.Enabled {
		return errors.New("compression observation requires knowledge.retrieval.contextCompression.enabled")
	}
	if options.retriever == retrieverRRFRerank {
		cfg.Models.Rerank.Enabled = true
	} else {
		cfg.Models.Rerank.Enabled = false
	}
	profile, err := cfg.Models.Embedding.Profile()
	if err != nil {
		return err
	}
	printProviderBudget(cfg, options, chunkCount, len(selected))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, options.timeout)
	defer cancel()
	db, closeDB, err := platformpostgres.Open(ctx, cfg.Postgres, zap.NewNop())
	if err != nil {
		return fmt.Errorf("open postgres: %w", err)
	}
	defer closeDB()

	var observations []knowledge.AdvancedRetrievalObservation
	var summary knowledge.AdvancedRetrievalEvaluationSummary
	fixtureEmbeddingTokens := 0
	evaluationErr := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		actorID, documentKeyByID, err := seedFixture(ctx, tx, corpus, chunksByDocument)
		if err != nil {
			return err
		}
		embedder, err := platformembedding.NewClient(cfg.Models.Embedding, nil)
		if err != nil {
			return fmt.Errorf("create fixture embedding client: %w", err)
		}
		if err := platformpostgres.NewKnowledgeWorkerRepository(tx).EnsureEmbeddingProfile(ctx, profile); err != nil {
			return err
		}
		fixtureEmbeddingTokens, err = indexFixtureEmbeddings(
			ctx, tx, profile, embedder, cfg.Models.Embedding.BatchSize, documentKeyByID,
		)
		if err != nil {
			return err
		}
		baselineConfig, experimentConfig := pairedConfigs(cfg, options.axis)
		var chatModel model.ToolCallingChatModel
		if options.axis == axisRewrite {
			instance, modelErr := platformchatmodel.NewProfile(
				ctx, cfg.Models.Chat, cfg.Knowledge.Retrieval.QueryRewrite.ModelProfile,
			)
			err = modelErr
			if err != nil {
				return fmt.Errorf("create query rewrite chat model: %w", err)
			}
			chatModel = instance.Model
		}
		baselineService, err := bootstrap.BuildKnowledgeSearchService(ctx, tx, baselineConfig, nil, zap.NewNop())
		if err != nil {
			return fmt.Errorf("build baseline search service: %w", err)
		}
		experimentService, err := bootstrap.BuildKnowledgeSearchService(ctx, tx, experimentConfig, chatModel, zap.NewNop())
		if err != nil {
			return fmt.Errorf("build experiment search service: %w", err)
		}
		baselineArm, experimentArm := pairedRuntimeArms(
			cfg, profile, options.axis, options.retriever, baselineService, experimentService,
		)
		observer, err := knowledge.NewAdvancedRetrievalObserver(baselineArm, experimentArm, documentKeyByID)
		if err != nil {
			return err
		}
		observations, err = observer.Observe(ctx, actorID, selected)
		if err != nil {
			return err
		}
		summary, err = knowledge.EvaluateAdvancedRetrieval(selected, observations)
		if err != nil {
			return err
		}
		if options.requireCompressionAcceptance {
			if err := validateCompressionAcceptance(summary); err != nil {
				return err
			}
		}
		return errRollbackAdvancedRAGFixture
	})
	if !errors.Is(evaluationErr, errRollbackAdvancedRAGFixture) {
		return evaluationErr
	}
	if err := writeObservationFiles(options.observationsPath, options.summaryPath, observations, summary); err != nil {
		return err
	}
	fmt.Printf(
		"dataset=%s pairs=%d fixture_embedding_tokens=%d hit_rate_delta=%.4f document_recall_delta=%.4f mrr_delta=%.4f\n",
		summary.DatasetVersion, summary.PairedCases, fixtureEmbeddingTokens, summary.Delta.HitRateAtK,
		summary.Delta.RecallAtK, summary.Delta.MeanReciprocalRank,
	)
	return nil
}

func parseOptions(args []string) (commandOptions, error) {
	flags := flag.NewFlagSet("mesguard-rag-paired-observe", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := commandOptions{}
	flags.StringVar(&options.corpusPath, "corpus", "testdata/rag-advanced-v1.corpus.json", "versioned public-source corpus")
	flags.StringVar(&options.datasetPath, "dataset", "testdata/rag-advanced-v1.jsonl", "versioned Advanced RAG cases")
	flags.StringVar(&options.observationsPath, "output", "", "paired observation JSONL output")
	flags.StringVar(&options.summaryPath, "summary", "", "paired summary JSON output")
	flags.StringVar(&options.axis, "axis", axisContext, "single evaluation axis: context, rewrite, or compression")
	flags.StringVar(&options.retriever, "retriever", retrieverRRF, "retriever: rrf or rrf-rerank")
	flags.StringVar(&options.caseID, "case-id", "", "optional exact case id")
	flags.IntVar(&options.maxCases, "max-cases", 1, "maximum provider cases for this run")
	flags.DurationVar(&options.timeout, "timeout", 3*time.Minute, "total run timeout")
	flags.BoolVar(&options.validateOnly, "validate-only", false, "validate corpus and gold chunks without database or provider calls")
	flags.BoolVar(&options.listChunks, "list-chunks", false, "print stable corpus chunk references without database or provider calls")
	flags.BoolVar(&options.executeProvider, "execute-provider", false, "allow bounded embedding, rerank, and rewrite provider calls")
	flags.BoolVar(
		&options.requireCompressionAcceptance,
		"require-compression-acceptance",
		false,
		"fail unless compression omits context without reducing gold context recall",
	)
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return commandOptions{}, errors.New("usage: mesguard-rag-paired-observe [-validate-only] [-execute-provider] [-require-compression-acceptance] [-axis context|rewrite|compression] [-retriever rrf|rrf-rerank] [-case-id id] [-max-cases n] [-corpus path] [-dataset path] [-output path] [-summary path] [-timeout duration]")
	}
	options.axis = strings.ToLower(strings.TrimSpace(options.axis))
	options.retriever = strings.ToLower(strings.TrimSpace(options.retriever))
	options.caseID = strings.TrimSpace(options.caseID)
	if options.axis != axisContext && options.axis != axisRewrite && options.axis != axisCompression {
		return commandOptions{}, errors.New("axis must be context, rewrite, or compression")
	}
	if options.requireCompressionAcceptance && options.axis != axisCompression {
		return commandOptions{}, errors.New("compression acceptance requires the compression axis")
	}
	if options.retriever != retrieverRRF && options.retriever != retrieverRRFRerank {
		return commandOptions{}, errors.New("retriever must be rrf or rrf-rerank")
	}
	if options.maxCases < 1 || options.maxCases > 20 || options.timeout <= 0 || options.timeout > 30*time.Minute {
		return commandOptions{}, errors.New("max-cases or timeout is outside the evaluation safety limit")
	}
	if options.observationsPath == "" {
		options.observationsPath = filepath.Join("output", "evaluation", "rag-advanced-v1."+options.axis+".observations.jsonl")
	}
	if options.summaryPath == "" {
		options.summaryPath = filepath.Join("output", "evaluation", "rag-advanced-v1."+options.axis+".summary.json")
	}
	observationsAbsolute, observationsErr := filepath.Abs(options.observationsPath)
	summaryAbsolute, summaryErr := filepath.Abs(options.summaryPath)
	if observationsErr != nil || summaryErr != nil || strings.EqualFold(observationsAbsolute, summaryAbsolute) {
		return commandOptions{}, errors.New("output and summary paths must be different valid paths")
	}
	return options, nil
}

func validateCompressionAcceptance(summary knowledge.AdvancedRetrievalEvaluationSummary) error {
	if summary.Experiment.CompressionTriggeredRuns < 1 || summary.Experiment.CompressionOmittedChunks < 1 {
		return errors.New("compression acceptance failed: no context chunks were omitted")
	}
	if summary.Delta.ContextRecall < -1e-9 {
		return errors.New("compression acceptance failed: gold context recall regressed")
	}
	return nil
}

func printCorpusChunks(corpus knowledge.AdvancedRetrievalEvaluationCorpus) error {
	chunksByDocument, err := knowledge.BuildAdvancedRetrievalCorpusChunks(corpus)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	for _, document := range corpus.Documents {
		for ordinal, chunk := range chunksByDocument[document.DocumentKey] {
			preview := []rune(chunk.ContentText)
			if len(preview) > 120 {
				preview = preview[:120]
			}
			if err := encoder.Encode(struct {
				DocumentKey   string `json:"documentKey"`
				Ordinal       int    `json:"ordinal"`
				ContentSHA256 string `json:"contentSha256"`
				Preview       string `json:"preview"`
			}{
				DocumentKey: document.DocumentKey, Ordinal: ordinal,
				ContentSHA256: chunk.ContentSHA256, Preview: string(preview),
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func selectCases(
	cases []knowledge.AdvancedRetrievalEvaluationCase,
	caseID string,
	maxCases int,
) ([]knowledge.AdvancedRetrievalEvaluationCase, error) {
	if caseID != "" {
		for _, definition := range cases {
			if definition.CaseID == caseID {
				return []knowledge.AdvancedRetrievalEvaluationCase{definition}, nil
			}
		}
		return nil, fmt.Errorf("case-id %q was not found", caseID)
	}
	if len(cases) > maxCases {
		return append([]knowledge.AdvancedRetrievalEvaluationCase(nil), cases[:maxCases]...), nil
	}
	return append([]knowledge.AdvancedRetrievalEvaluationCase(nil), cases...), nil
}

func pairedConfigs(cfg config.Config, axis string) (config.Config, config.Config) {
	baseline := cfg
	experiment := cfg
	baseline.Knowledge.Retrieval.QueryRewrite.Enabled = false
	experiment.Knowledge.Retrieval.QueryRewrite.Enabled = false
	baseline.Knowledge.Retrieval.ContextCompression.Enabled = false
	experiment.Knowledge.Retrieval.ContextCompression.Enabled = false
	if axis == axisContext {
		baseline.Knowledge.Retrieval.ContextExpansionEnabled = false
		experiment.Knowledge.Retrieval.ContextExpansionEnabled = true
		return baseline, experiment
	}
	if axis == axisCompression {
		baseline.Knowledge.Retrieval.ContextExpansionEnabled = true
		experiment.Knowledge.Retrieval.ContextExpansionEnabled = true
		experiment.Knowledge.Retrieval.ContextCompression = cfg.Knowledge.Retrieval.ContextCompression
		return baseline, experiment
	}
	baseline.Knowledge.Retrieval.ContextExpansionEnabled = true
	experiment.Knowledge.Retrieval.ContextExpansionEnabled = true
	experiment.Knowledge.Retrieval.QueryRewrite.Enabled = true
	return baseline, experiment
}

func pairedRuntimeArms(
	cfg config.Config,
	profile knowledge.EmbeddingProfile,
	axis string,
	retriever string,
	baseline, experiment knowledge.AdvancedRetrievalSearcher,
) (knowledge.AdvancedRetrievalRuntimeArm, knowledge.AdvancedRetrievalRuntimeArm) {
	retrieverVersion := "postgres-rrf-v1"
	rerankProfile := ""
	if retriever == retrieverRRFRerank {
		retrieverVersion = "postgres-rrf-rerank-v1"
		rerankProfile = strings.ToLower(strings.TrimSpace(cfg.Models.Rerank.Provider + "-" + cfg.Models.Rerank.Model))
	}
	baselineArm := knowledge.AdvancedRetrievalRuntimeArm{
		Arm: knowledge.AdvancedRetrievalArm{
			RetrieverVersion: retrieverVersion, EmbeddingProfile: profile.Key,
			RerankProfile: rerankProfile, QueryMode: knowledge.RetrievalQueryOriginal,
			ContextMode: knowledge.RetrievalContextChild,
		},
		Searcher: baseline, FTSEnabled: true, VectorEnabled: true,
	}
	experimentArm := baselineArm
	experimentArm.Searcher = experiment
	if axis == axisContext {
		experimentArm.Arm.ContextMode = knowledge.RetrievalContextParent
		return baselineArm, experimentArm
	}
	if axis == axisCompression {
		baselineArm.Arm.ContextMode = knowledge.RetrievalContextParent
		experimentArm.Arm.ContextMode = knowledge.RetrievalContextParent
		experimentArm.Arm.ContextCompressionEnabled = true
		experimentArm.Arm.ContextCompressionMaxChunks = cfg.Knowledge.Retrieval.ContextCompression.MaxChunks
		experimentArm.Arm.ContextCompressionMaxRunes = cfg.Knowledge.Retrieval.ContextCompression.MaxRunes
		experimentArm.Arm.ContextCompressionMinScore = cfg.Knowledge.Retrieval.ContextCompression.MinScore
		return baselineArm, experimentArm
	}
	baselineArm.Arm.ContextMode = knowledge.RetrievalContextParent
	experimentArm.Arm.ContextMode = knowledge.RetrievalContextParent
	experimentArm.Arm.QueryMode = knowledge.RetrievalQueryRewrite
	rewriteProfile, _ := cfg.Models.Chat.Profile(cfg.Knowledge.Retrieval.QueryRewrite.ModelProfile)
	experimentArm.Arm.RewriteProvider = strings.ToLower(strings.TrimSpace(rewriteProfile.Provider))
	experimentArm.Arm.RewriteModelID = strings.TrimSpace(rewriteProfile.Model)
	experimentArm.Arm.RewritePromptVersion = strings.TrimSpace(cfg.Knowledge.Retrieval.QueryRewrite.PromptVersion)
	return baselineArm, experimentArm
}

func printProviderBudget(cfg config.Config, options commandOptions, chunkCount, caseCount int) {
	batchSize := cfg.Models.Embedding.BatchSize
	if batchSize < 1 {
		batchSize = 1
	}
	documentEmbeddingRequests := (chunkCount + batchSize - 1) / batchSize
	rewriteRequests := 0
	if options.axis == axisRewrite {
		rewriteRequests = caseCount
	}
	rerankRequests := 0
	if options.retriever == retrieverRRFRerank {
		rerankRequests = caseCount * 2
	}
	fmt.Printf(
		"provider_budget document_embedding_requests<=%d query_embedding_requests<=%d rewrite_requests<=%d rerank_requests<=%d cases=%d\n",
		documentEmbeddingRequests, caseCount*2, rewriteRequests, rerankRequests, caseCount,
	)
}

func seedFixture(
	ctx context.Context,
	tx *gorm.DB,
	corpus knowledge.AdvancedRetrievalEvaluationCorpus,
	chunksByDocument map[string][]knowledge.ChunkDraft,
) (uuid.UUID, map[uuid.UUID]string, error) {
	actorID := uuid.New()
	if err := tx.Exec(`
INSERT INTO users (id, username, display_name, password_hash, role)
VALUES (?, ?, 'Advanced RAG Evaluation', 'evaluation-only', 'analyst')`,
		actorID, "rag_advanced_"+strings.ReplaceAll(uuid.NewString(), "-", "")[:12],
	).Error; err != nil {
		return uuid.Nil, nil, err
	}
	repository := platformpostgres.NewKnowledgeRepository(tx)
	documentKeyByID := make(map[uuid.UUID]string, len(corpus.Documents))
	for _, document := range corpus.Documents {
		documentID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(corpus.DatasetVersion+":"+document.DocumentKey))
		if _, err := repository.CreateDocument(ctx, knowledge.CreateDocumentInput{
			ID: documentID, Scope: knowledge.ScopeGlobal, Title: document.Title, CreatedBy: actorID,
		}); err != nil {
			return uuid.Nil, nil, err
		}
		if _, err := repository.PublishVersion(ctx, knowledge.PublishVersionInput{
			ID: uuid.New(), DocumentID: documentID, SourceMediaType: document.MediaType,
			SourceSizeBytes: int64(len([]byte(document.Content))), SourceSHA256: document.ContentSHA256,
			ParserVersion: corpus.ChunkerVersion, CreatedBy: actorID, Chunks: chunksByDocument[document.DocumentKey],
		}); err != nil {
			return uuid.Nil, nil, err
		}
		documentKeyByID[documentID] = document.DocumentKey
	}
	return actorID, documentKeyByID, nil
}

type fixtureChunkRow struct {
	ChunkID           uuid.UUID `gorm:"column:chunk_id"`
	DocumentVersionID uuid.UUID `gorm:"column:document_version_id"`
	ContentSHA256     string    `gorm:"column:content_sha256"`
	ContentText       string    `gorm:"column:content_text"`
}

func indexFixtureEmbeddings(
	ctx context.Context,
	tx *gorm.DB,
	profile knowledge.EmbeddingProfile,
	embedder knowledge.Embedder,
	batchSize int,
	documentKeyByID map[uuid.UUID]string,
) (int, error) {
	versionIDs := make([]uuid.UUID, 0, len(documentKeyByID))
	for documentID := range documentKeyByID {
		var versionIDText string
		if err := tx.WithContext(ctx).Raw(
			"SELECT id FROM knowledge_document_versions WHERE document_id = ? AND is_current = true", documentID,
		).Scan(&versionIDText).Error; err != nil {
			return 0, err
		}
		versionID, err := uuid.Parse(versionIDText)
		if err != nil {
			return 0, errors.New("fixture document has no valid current version")
		}
		versionIDs = append(versionIDs, versionID)
	}
	var rows []fixtureChunkRow
	if err := tx.WithContext(ctx).Raw(`
SELECT id AS chunk_id, document_version_id, content_sha256, content_text
FROM knowledge_chunks
WHERE document_version_id IN ?
ORDER BY document_version_id, ordinal`, versionIDs).Scan(&rows).Error; err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, errors.New("Advanced RAG fixture contains no chunks")
	}
	if batchSize < 1 || batchSize > 10 {
		batchSize = 10
	}
	totalTokens := 0
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
			return 0, fmt.Errorf("embed Advanced RAG fixture batch: %w", err)
		}
		if err := output.Validate(len(texts), profile.Dimensions, profile.Normalize); err != nil {
			return 0, err
		}
		totalTokens += output.Usage.TotalTokens
		for offset, vector := range output.Vectors {
			row := rows[start+offset]
			if err := tx.Exec(`
INSERT INTO knowledge_chunk_embeddings (chunk_id, profile_id, content_sha256, embedding)
VALUES (?, ?, ?, ?)`, row.ChunkID, profile.ID, row.ContentSHA256, pgvector.NewVector(vector)).Error; err != nil {
				return 0, err
			}
		}
	}
	return totalTokens, nil
}

func readStrictJSON[T any](path string) (T, error) {
	var result T
	encoded, err := os.ReadFile(path)
	if err != nil {
		return result, fmt.Errorf("read %s: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, fmt.Errorf("decode %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return result, fmt.Errorf("decode %s: %w", path, err)
	}
	return result, nil
}

func readStrictJSONL[T any](path string, validate func(T) error) ([]T, error) {
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
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
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

func writeObservationFiles(
	observationsPath string,
	summaryPath string,
	observations []knowledge.AdvancedRetrievalObservation,
	summary knowledge.AdvancedRetrievalEvaluationSummary,
) error {
	if err := os.MkdirAll(filepath.Dir(observationsPath), 0o750); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(summaryPath), 0o750); err != nil {
		return err
	}
	observationTemp := observationsPath + ".tmp-" + uuid.NewString()
	summaryTemp := summaryPath + ".tmp-" + uuid.NewString()
	defer os.Remove(observationTemp)
	defer os.Remove(summaryTemp)
	file, err := os.OpenFile(observationTemp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
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
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
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
	if err := replaceFile(observationTemp, observationsPath); err != nil {
		return err
	}
	return replaceFile(summaryTemp, summaryPath)
}

func replaceFile(source, target string) error {
	backup := target + ".backup-" + uuid.NewString()
	targetExists := false
	if _, err := os.Stat(target); err == nil {
		targetExists = true
		if err := os.Rename(target, backup); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(source, target); err != nil {
		if targetExists {
			_ = os.Rename(backup, target)
		}
		return err
	}
	if targetExists {
		if err := os.Remove(backup); err != nil {
			return err
		}
	}
	return nil
}
