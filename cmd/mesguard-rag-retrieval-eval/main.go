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
	"syscall"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const ragRetrieverVersion = "postgres-fts-v1"

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
	timeout := flags.Duration("timeout", 2*time.Minute, "total evaluation timeout")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("usage: mesguard-rag-retrieval-eval [-corpus path] [-dataset path] [-output path] [-summary path] [-timeout duration]")
	}
	if *timeout <= 0 {
		return errors.New("timeout must be positive")
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
		ingestionDuration := float64(time.Since(ingestionStartedAt).Microseconds()) / 1000

		observations = make([]knowledge.RetrievalEvaluationObservation, 0, len(cases))
		for _, definition := range cases {
			startedAt := time.Now()
			results, err := repository.SearchFTS(ctx, actorID, definition.Query, definition.K)
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
				RunID: definition.CaseID + "-" + uuid.NewString(), Retriever: ragRetrieverVersion,
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
			documents, cases, observations, ragRetrieverVersion, chunkCount, ingestionDuration,
		)
		if err != nil {
			return err
		}
		return errRollbackRAGEvaluation
	})
	if !errors.Is(evaluationErr, errRollbackRAGEvaluation) {
		return evaluationErr
	}
	return writeEvaluationFiles(*outputPath, *summaryPath, observations, summary)
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
