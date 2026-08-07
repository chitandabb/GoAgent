package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
	"github.com/chitandabb/GoAgent/internal/platform/config"
)

type corpusAuditStatus string

const (
	corpusAuditTextReady          corpusAuditStatus = "text_ready"
	corpusAuditTextVisualPending  corpusAuditStatus = "text_ready_visual_pending"
	corpusAuditVisualRequired     corpusAuditStatus = "visual_enrichment_required"
	corpusAuditParserFailed       corpusAuditStatus = "parser_failed"
	corpusAuditNoSearchableOutput corpusAuditStatus = "no_searchable_output"
)

type corpusAuditDocument struct {
	DocumentID                string            `json:"documentId"`
	Title                     string            `json:"title"`
	FileName                  string            `json:"fileName"`
	MediaType                 string            `json:"mediaType"`
	FormatClass               string            `json:"formatClass"`
	SizeBytes                 int64             `json:"sizeBytes"`
	ManifestPages             int               `json:"manifestPages"`
	ObservedPages             int               `json:"observedPages"`
	ParserVersion             string            `json:"parserVersion,omitempty"`
	Status                    corpusAuditStatus `json:"status"`
	Elements                  int               `json:"elements"`
	ElementRunes              int               `json:"elementRunes"`
	VisualAssets              int               `json:"visualAssets"`
	MaterializedVisualBytes   int64             `json:"materializedVisualBytes"`
	Chunks                    int               `json:"chunks"`
	SearchableWithoutProvider bool              `json:"searchableWithoutProvider"`
	RequiresVisualProvider    bool              `json:"requiresVisualProvider"`
	DurationMillis            int64             `json:"durationMillis"`
	Error                     string            `json:"error,omitempty"`
}

type corpusAuditSummary struct {
	DatasetVersion               string                `json:"datasetVersion"`
	CorpusFingerprint            string                `json:"corpusFingerprint"`
	Documents                    int                   `json:"documents"`
	FormatCount                  int                   `json:"formatCount"`
	FormatClasses                []string              `json:"formatClasses"`
	TextReadyDocuments           int                   `json:"textReadyDocuments"`
	TextVisualPendingDocuments   int                   `json:"textVisualPendingDocuments"`
	VisualRequiredDocuments      int                   `json:"visualRequiredDocuments"`
	ParserFailedDocuments        int                   `json:"parserFailedDocuments"`
	NoSearchableOutputDocuments  int                   `json:"noSearchableOutputDocuments"`
	TotalSourceBytes             int64                 `json:"totalSourceBytes"`
	TotalElements                int                   `json:"totalElements"`
	TotalElementRunes            int                   `json:"totalElementRunes"`
	TotalVisualAssets            int                   `json:"totalVisualAssets"`
	TotalMaterializedVisualBytes int64                 `json:"totalMaterializedVisualBytes"`
	TotalChunks                  int                   `json:"totalChunks"`
	GeneratedAt                  time.Time             `json:"generatedAt"`
	Results                      []corpusAuditDocument `json:"results"`
}

func runCorpusAudit(
	ctx context.Context,
	cfg config.KnowledgeConfig,
	datasetVersion string,
	documents []loadedDocument,
	outputPath string,
) error {
	parser, err := buildParser(cfg)
	if err != nil {
		return err
	}
	summary := corpusAuditSummary{
		DatasetVersion: datasetVersion, CorpusFingerprint: corpusFingerprint(datasetVersion, documents),
		Documents: len(documents), GeneratedAt: time.Now().UTC(),
		Results: make([]corpusAuditDocument, 0, len(documents)),
	}
	formats := make(map[string]struct{}, len(documents))
	for _, document := range documents {
		if err := ctx.Err(); err != nil {
			return err
		}
		result := auditCorpusDocument(ctx, parser, cfg, document)
		summary.Results = append(summary.Results, result)
		formats[result.FormatClass] = struct{}{}
		summary.TotalSourceBytes += result.SizeBytes
		summary.TotalElements += result.Elements
		summary.TotalElementRunes += result.ElementRunes
		summary.TotalVisualAssets += result.VisualAssets
		summary.TotalMaterializedVisualBytes += result.MaterializedVisualBytes
		summary.TotalChunks += result.Chunks
		switch result.Status {
		case corpusAuditTextReady:
			summary.TextReadyDocuments++
		case corpusAuditTextVisualPending:
			summary.TextVisualPendingDocuments++
		case corpusAuditVisualRequired:
			summary.VisualRequiredDocuments++
		case corpusAuditParserFailed:
			summary.ParserFailedDocuments++
		case corpusAuditNoSearchableOutput:
			summary.NoSearchableOutputDocuments++
		}
	}
	for formatClass := range formats {
		summary.FormatClasses = append(summary.FormatClasses, formatClass)
	}
	slices.Sort(summary.FormatClasses)
	summary.FormatCount = len(summary.FormatClasses)
	if err := writeCorpusAudit(outputPath, summary); err != nil {
		return err
	}
	fmt.Printf(
		"dataset=%s documents=%d formats=%d text_ready=%d text_visual_pending=%d visual_required=%d parser_failed=%d chunks=%d output=%s\n",
		summary.DatasetVersion, summary.Documents, summary.FormatCount, summary.TextReadyDocuments,
		summary.TextVisualPendingDocuments, summary.VisualRequiredDocuments, summary.ParserFailedDocuments,
		summary.TotalChunks, outputPath,
	)
	if summary.ParserFailedDocuments > 0 || summary.NoSearchableOutputDocuments > 0 {
		return errors.New("corpus audit found documents that cannot enter the ingestion pipeline")
	}
	return nil
}

func auditCorpusDocument(
	ctx context.Context,
	parser interface {
		Parse(context.Context, knowledgeparser.Input) (knowledgeparser.Result, error)
	},
	cfg config.KnowledgeConfig,
	document loadedDocument,
) corpusAuditDocument {
	startedAt := time.Now()
	result := corpusAuditDocument{
		DocumentID: document.definition.DocumentID, Title: document.definition.Title,
		FileName: document.definition.FileName, MediaType: document.definition.MediaType,
		FormatClass: document.definition.FormatClass, SizeBytes: document.definition.SizeBytes,
		ManifestPages: document.definition.PageCount,
	}
	parsed, err := parser.Parse(ctx, knowledgeparser.Input{
		MediaType: document.definition.MediaType, OriginalName: document.definition.FileName,
		Content: document.content,
	})
	if err != nil {
		result.Status = corpusAuditParserFailed
		result.Error = boundedAuditError(err)
		result.DurationMillis = max(1, time.Since(startedAt).Milliseconds())
		return result
	}
	result.ParserVersion = parsed.ParserVersion
	result.ObservedPages = len(parsed.Pages)
	result.Elements = len(parsed.Elements)
	result.VisualAssets = len(parsed.VisualAssets)
	for _, element := range parsed.Elements {
		result.ElementRunes += utf8.RuneCountInString(element.ContentText)
	}
	result.MaterializedVisualBytes = materializedVisualBytes(parsed.VisualAssets)
	if len(parsed.Elements) > 0 {
		chunks, chunkErr := knowledge.ChunkElements(parsed.Elements, knowledge.TextChunkOptions{
			MaxRunes: cfg.ChunkMaxRunes, OverlapRunes: cfg.ChunkOverlapRunes,
		})
		if chunkErr != nil {
			result.Status = corpusAuditParserFailed
			result.Error = boundedAuditError(chunkErr)
			result.DurationMillis = max(1, time.Since(startedAt).Milliseconds())
			return result
		}
		result.Chunks = len(chunks)
	}
	result.Status, result.SearchableWithoutProvider, result.RequiresVisualProvider = classifyCorpusAuditResult(
		result.Chunks, result.VisualAssets,
	)
	result.DurationMillis = max(1, time.Since(startedAt).Milliseconds())
	return result
}

func materializedVisualBytes(assets []knowledgeparser.VisualAsset) int64 {
	var total int64
	for _, asset := range assets {
		total += int64(len(asset.Content))
	}
	return total
}

func classifyCorpusAuditResult(chunks, visualAssets int) (corpusAuditStatus, bool, bool) {
	switch {
	case chunks > 0 && visualAssets == 0:
		return corpusAuditTextReady, true, false
	case chunks > 0 && visualAssets > 0:
		return corpusAuditTextVisualPending, true, true
	case chunks == 0 && visualAssets > 0:
		return corpusAuditVisualRequired, false, true
	default:
		return corpusAuditNoSearchableOutput, false, false
	}
}

func writeCorpusAudit(path string, summary corpusAuditSummary) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return err
	}
	temp, err := os.CreateTemp(directory, ".corpus-audit-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	encoder := json.NewEncoder(temp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(summary); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(tempPath, path)
}

func boundedAuditError(err error) string {
	value := strings.TrimSpace(err.Error())
	if len(value) <= 500 {
		return value
	}
	return strings.ToValidUTF8(value[:500], "?")
}
