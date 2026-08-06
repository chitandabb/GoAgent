// Command mesguard-pptx-element-eval evaluates PPTX structure extraction on a reviewed corpus.
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"
	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
	"github.com/chitandabb/GoAgent/internal/platform/config"
)

const evaluatorVersion = "pptx-element-quality-eval-v1"

type options struct {
	configPath  string
	corpusPath  string
	casesPath   string
	rootPath    string
	outputPath  string
	summaryPath string
	timeout     time.Duration
}

type corpus struct {
	Version   string           `json:"version"`
	Documents []corpusDocument `json:"documents"`
}

type corpusDocument struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
}

type evaluationCase struct {
	ID                              string     `json:"id"`
	DocumentID                      string     `json:"documentId"`
	PageNumber                      int        `json:"pageNumber"`
	TextAnchors                     []string   `json:"textAnchors"`
	ExpectedTableCount              *int       `json:"expectedTableCount,omitempty"`
	TableAnchors                    [][]string `json:"tableAnchors,omitempty"`
	SourceVisualUseCount            *int       `json:"sourceVisualUseCount,omitempty"`
	ExpectedVisualRelationshipCount *int       `json:"expectedVisualRelationshipCount,omitempty"`
}

type observation struct {
	CaseID                          string   `json:"caseId"`
	DocumentID                      string   `json:"documentId"`
	PageNumber                      int      `json:"pageNumber"`
	TextAnchorsMatched              int      `json:"textAnchorsMatched"`
	TextAnchorsExpected             int      `json:"textAnchorsExpected"`
	ActualTableCount                int      `json:"actualTableCount"`
	MatchedTableAnchors             int      `json:"matchedTableAnchors"`
	ExpectedTableAnchors            int      `json:"expectedTableAnchors"`
	ActualVisualRelationshipCount   int      `json:"actualVisualRelationshipCount"`
	CompleteVisualRelationshipCount int      `json:"completeVisualRelationshipCount"`
	SourceVisualUseCount            *int     `json:"sourceVisualUseCount,omitempty"`
	ExpectedVisualRelationshipCount *int     `json:"expectedVisualRelationshipCount,omitempty"`
	Passed                          bool     `json:"passed"`
	Failures                        []string `json:"failures"`
}

type summary struct {
	EvaluatorVersion                 string        `json:"evaluatorVersion"`
	CorpusVersion                    string        `json:"corpusVersion"`
	RecordedAt                       time.Time     `json:"recordedAt"`
	DocumentCount                    int           `json:"documentCount"`
	CaseCount                        int           `json:"caseCount"`
	PassedCases                      int           `json:"passedCases"`
	CasePassRate                     float64       `json:"casePassRate"`
	TextAnchorRecall                 float64       `json:"textAnchorRecall"`
	TableAnchorRecall                float64       `json:"tableAnchorRecall"`
	ExactTableCountRate              float64       `json:"exactTableCountRate"`
	ExactVisualRelationshipCountRate float64       `json:"exactVisualRelationshipCountRate"`
	VisualRelationshipCompleteness   float64       `json:"visualRelationshipCompleteness"`
	ReviewedVisualUses               int           `json:"reviewedVisualUses"`
	ExpectedDistinctRelationships    int           `json:"expectedDistinctVisualRelationships"`
	ExpectedDuplicateVisualUses      int           `json:"expectedDuplicateVisualUses"`
	Observations                     []observation `json:"observations"`
}

type parsedDocument struct {
	elementsByPage map[int][]knowledge.DocumentElement
	assetsByPage   map[int][]knowledgeparser.VisualAsset
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	opts, err := parseFlags(args)
	if err != nil {
		return err
	}
	fixture, err := readJSON[corpus](opts.corpusPath)
	if err != nil {
		return err
	}
	cases, err := readJSONL[evaluationCase](opts.casesPath)
	if err != nil {
		return err
	}
	if err := validateFixture(fixture, cases); err != nil {
		return err
	}
	knowledgeConfig, err := loadKnowledgeConfig(opts.configPath)
	if err != nil {
		return err
	}
	parser, err := knowledgeparser.NewOOXMLParser(parserLimits(knowledgeConfig))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	documents, err := parseDocuments(ctx, parser, knowledgeConfig.MaxUploadBytes, opts.rootPath, fixture.Documents)
	if err != nil {
		return err
	}
	result := evaluateCases(fixture.Version, cases, documents)
	if err := writeJSONL(opts.outputPath, result.Observations); err != nil {
		return err
	}
	if err := writeJSON(opts.summaryPath, result); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout,
		"summary cases=%d passed=%d text_recall=%.4f table_recall=%.4f visual_relationship_completeness=%.4f\n",
		result.CaseCount, result.PassedCases, result.TextAnchorRecall, result.TableAnchorRecall,
		result.VisualRelationshipCompleteness,
	)
	if result.PassedCases != result.CaseCount {
		return fmt.Errorf("PPTX element quality evaluation failed for %d case(s)", result.CaseCount-result.PassedCases)
	}
	return nil
}

func parseFlags(args []string) (options, error) {
	flags := flag.NewFlagSet("mesguard-pptx-element-eval", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var result options
	flags.StringVar(&result.configPath, "config", "config/mesguard.toml", "MESGuard TOML configuration")
	flags.StringVar(&result.corpusPath, "corpus", "testdata/pptx-element-quality-local-v1.corpus.json", "reviewed corpus manifest")
	flags.StringVar(&result.casesPath, "cases", "testdata/pptx-element-quality-local-v1.jsonl", "reviewed element cases")
	flags.StringVar(&result.rootPath, "root", "", "directory containing corpus PPTX files")
	flags.StringVar(&result.outputPath, "output", "output/evaluation/pptx-element-quality-local-v1.observations.jsonl", "observation JSONL output")
	flags.StringVar(&result.summaryPath, "summary", "output/evaluation/pptx-element-quality-local-v1.summary.json", "summary JSON output")
	flags.DurationVar(&result.timeout, "timeout", 5*time.Minute, "total evaluation timeout")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || strings.TrimSpace(result.rootPath) == "" || result.timeout <= 0 {
		return options{}, errors.New("usage: mesguard-pptx-element-eval -root directory [-config path] [-corpus path] [-cases path] [-output path] [-summary path] [-timeout duration]")
	}
	return result, nil
}

func validateFixture(fixture corpus, cases []evaluationCase) error {
	if strings.TrimSpace(fixture.Version) == "" || len(fixture.Documents) == 0 || len(cases) == 0 {
		return errors.New("PPTX element evaluation fixture is empty")
	}
	documentIDs := make(map[string]struct{}, len(fixture.Documents))
	for _, document := range fixture.Documents {
		if strings.TrimSpace(document.ID) == "" || filepath.Base(document.Name) != document.Name ||
			document.SizeBytes < 1 || !validSHA256(document.SHA256) {
			return fmt.Errorf("PPTX element corpus document %q is invalid", document.ID)
		}
		if _, exists := documentIDs[document.ID]; exists {
			return fmt.Errorf("PPTX element corpus document id %q is duplicated", document.ID)
		}
		documentIDs[document.ID] = struct{}{}
	}
	caseIDs := make(map[string]struct{}, len(cases))
	for _, item := range cases {
		if strings.TrimSpace(item.ID) == "" || item.PageNumber < 1 || len(item.TextAnchors) == 0 {
			return fmt.Errorf("PPTX element case %q is invalid", item.ID)
		}
		if _, exists := caseIDs[item.ID]; exists {
			return fmt.Errorf("PPTX element case id %q is duplicated", item.ID)
		}
		caseIDs[item.ID] = struct{}{}
		if _, exists := documentIDs[item.DocumentID]; !exists {
			return fmt.Errorf("PPTX element case %q references an unknown document", item.ID)
		}
		if item.ExpectedTableCount != nil && (*item.ExpectedTableCount < 0 || len(item.TableAnchors) > *item.ExpectedTableCount) {
			return fmt.Errorf("PPTX element case %q table expectation is invalid", item.ID)
		}
		if item.SourceVisualUseCount != nil && *item.SourceVisualUseCount < 0 {
			return fmt.Errorf("PPTX element case %q visual-use expectation is invalid", item.ID)
		}
		if item.ExpectedVisualRelationshipCount != nil && *item.ExpectedVisualRelationshipCount < 0 {
			return fmt.Errorf("PPTX element case %q visual expectation is invalid", item.ID)
		}
		if item.SourceVisualUseCount != nil && item.ExpectedVisualRelationshipCount != nil &&
			*item.SourceVisualUseCount < *item.ExpectedVisualRelationshipCount {
			return fmt.Errorf("PPTX element case %q has fewer visual uses than relationships", item.ID)
		}
		for _, anchor := range append(append([]string{}, item.TextAnchors...), flatten(item.TableAnchors)...) {
			if normalize(anchor) == "" {
				return fmt.Errorf("PPTX element case %q contains an empty anchor", item.ID)
			}
		}
	}
	return nil
}

func parseDocuments(
	ctx context.Context,
	parser knowledgeparser.OOXMLParser,
	maxBytes int64,
	root string,
	fixture []corpusDocument,
) (map[string]parsedDocument, error) {
	result := make(map[string]parsedDocument, len(fixture))
	for _, document := range fixture {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		documentPath := filepath.Join(root, document.Name)
		info, err := os.Stat(documentPath)
		if err != nil {
			return nil, fmt.Errorf("stat PPTX element corpus document %q: %w", document.ID, err)
		}
		if !info.Mode().IsRegular() || info.Size() != document.SizeBytes || info.Size() > maxBytes {
			return nil, fmt.Errorf("PPTX element corpus document %q size mismatch", document.ID)
		}
		content, err := os.ReadFile(documentPath)
		if err != nil {
			return nil, fmt.Errorf("read PPTX element corpus document %q: %w", document.ID, err)
		}
		if int64(len(content)) != info.Size() {
			return nil, fmt.Errorf("PPTX element corpus document %q size mismatch", document.ID)
		}
		digest := sha256.Sum256(content)
		if hex.EncodeToString(digest[:]) != document.SHA256 {
			return nil, fmt.Errorf("PPTX element corpus document %q SHA-256 mismatch", document.ID)
		}
		parsed, err := parser.Parse(ctx, knowledgeparser.Input{
			MediaType: knowledgeparser.PPTXMediaType, OriginalName: document.Name, Content: content,
		})
		if err != nil {
			return nil, fmt.Errorf("parse PPTX element corpus document %q: %w", document.ID, err)
		}
		if err := parsed.Validate(); err != nil {
			return nil, fmt.Errorf("validate PPTX element corpus document %q: %w", document.ID, err)
		}
		indexed := parsedDocument{elementsByPage: map[int][]knowledge.DocumentElement{}, assetsByPage: map[int][]knowledgeparser.VisualAsset{}}
		for _, element := range parsed.Elements {
			if element.PageNumber != nil {
				indexed.elementsByPage[*element.PageNumber] = append(indexed.elementsByPage[*element.PageNumber], element)
			}
		}
		for _, asset := range parsed.VisualAssets {
			if asset.PageNumber != nil {
				indexed.assetsByPage[*asset.PageNumber] = append(indexed.assetsByPage[*asset.PageNumber], asset)
			}
		}
		result[document.ID] = indexed
	}
	return result, nil
}

func evaluateCases(corpusVersion string, cases []evaluationCase, documents map[string]parsedDocument) summary {
	result := summary{
		EvaluatorVersion: evaluatorVersion, CorpusVersion: corpusVersion, RecordedAt: time.Now().UTC(),
		DocumentCount: len(documents), CaseCount: len(cases), Observations: make([]observation, 0, len(cases)),
	}
	var textExpected, textMatched, tableExpected, tableMatched int
	var exactTableCases, exactTableMatched, exactVisualCases, exactVisualMatched int
	var visualExpected, visualLinked int
	for _, item := range cases {
		document := documents[item.DocumentID]
		current := observation{
			CaseID: item.ID, DocumentID: item.DocumentID, PageNumber: item.PageNumber,
			TextAnchorsExpected: len(item.TextAnchors), ExpectedTableAnchors: len(item.TableAnchors),
			SourceVisualUseCount:            item.SourceVisualUseCount,
			ExpectedVisualRelationshipCount: item.ExpectedVisualRelationshipCount,
		}
		pageElements := document.elementsByPage[item.PageNumber]
		pageText := normalizedElementText(pageElements)
		for _, anchor := range item.TextAnchors {
			if strings.Contains(pageText, normalize(anchor)) {
				current.TextAnchorsMatched++
			}
		}
		textExpected += current.TextAnchorsExpected
		textMatched += current.TextAnchorsMatched
		if current.TextAnchorsMatched != current.TextAnchorsExpected {
			current.Failures = append(current.Failures, "text_anchor_mismatch")
		}
		tables := elementsOfType(pageElements, knowledge.ElementTable)
		current.ActualTableCount = len(tables)
		current.MatchedTableAnchors = matchTableAnchors(tables, item.TableAnchors)
		tableExpected += current.ExpectedTableAnchors
		tableMatched += current.MatchedTableAnchors
		if current.MatchedTableAnchors != current.ExpectedTableAnchors {
			current.Failures = append(current.Failures, "table_anchor_mismatch")
		}
		if item.ExpectedTableCount != nil {
			exactTableCases++
			if current.ActualTableCount == *item.ExpectedTableCount {
				exactTableMatched++
			} else {
				current.Failures = append(current.Failures, "table_count_mismatch")
			}
		}
		assets := document.assetsByPage[item.PageNumber]
		current.ActualVisualRelationshipCount = len(assets)
		for _, asset := range assets {
			if asset.RelationshipID != "" && asset.SourcePart != "" {
				current.CompleteVisualRelationshipCount++
			}
		}
		if item.ExpectedVisualRelationshipCount != nil {
			exactVisualCases++
			visualExpected += *item.ExpectedVisualRelationshipCount
			visualLinked += min(current.CompleteVisualRelationshipCount, *item.ExpectedVisualRelationshipCount)
			result.ExpectedDistinctRelationships += *item.ExpectedVisualRelationshipCount
			if current.ActualVisualRelationshipCount == *item.ExpectedVisualRelationshipCount {
				exactVisualMatched++
			} else {
				current.Failures = append(current.Failures, "visual_relationship_count_mismatch")
			}
			if current.CompleteVisualRelationshipCount != current.ActualVisualRelationshipCount {
				current.Failures = append(current.Failures, "visual_relationship_incomplete")
			}
		}
		if item.SourceVisualUseCount != nil {
			result.ReviewedVisualUses += *item.SourceVisualUseCount
			if item.ExpectedVisualRelationshipCount != nil {
				result.ExpectedDuplicateVisualUses += *item.SourceVisualUseCount - *item.ExpectedVisualRelationshipCount
			}
		}
		current.Passed = len(current.Failures) == 0
		if current.Passed {
			result.PassedCases++
		}
		result.Observations = append(result.Observations, current)
	}
	result.CasePassRate = ratio(result.PassedCases, result.CaseCount)
	result.TextAnchorRecall = ratio(textMatched, textExpected)
	result.TableAnchorRecall = ratio(tableMatched, tableExpected)
	result.ExactTableCountRate = ratio(exactTableMatched, exactTableCases)
	result.ExactVisualRelationshipCountRate = ratio(exactVisualMatched, exactVisualCases)
	result.VisualRelationshipCompleteness = ratio(visualLinked, visualExpected)
	return result
}

func normalizedElementText(elements []knowledge.DocumentElement) string {
	var builder strings.Builder
	for _, element := range elements {
		builder.WriteString(normalize(element.ContentText))
	}
	return builder.String()
}

func elementsOfType(elements []knowledge.DocumentElement, elementType knowledge.ElementType) []knowledge.DocumentElement {
	result := make([]knowledge.DocumentElement, 0)
	for _, element := range elements {
		if element.ElementType == elementType {
			result = append(result, element)
		}
	}
	return result
}

func matchTableAnchors(tables []knowledge.DocumentElement, expectations [][]string) int {
	used := make([]bool, len(tables))
	matched := 0
	for _, anchors := range expectations {
		for index, table := range tables {
			if used[index] || !containsAll(normalize(table.ContentText), anchors) {
				continue
			}
			used[index] = true
			matched++
			break
		}
	}
	return matched
}

func containsAll(value string, anchors []string) bool {
	for _, anchor := range anchors {
		if !strings.Contains(value, normalize(anchor)) {
			return false
		}
	}
	return true
}

func normalize(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, strings.TrimSpace(value))
}

func flatten(values [][]string) []string {
	var result []string
	for _, value := range values {
		result = append(result, value...)
	}
	return result
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 1
	}
	return float64(numerator) / float64(denominator)
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func readJSON[T any](path string) (T, error) {
	var result T
	content, err := os.ReadFile(path)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(content, &result); err != nil {
		return result, fmt.Errorf("decode %q: %w", path, err)
	}
	return result, nil
}

func readJSONL[T any](path string) ([]T, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var result []T
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item T
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			return nil, fmt.Errorf("decode %q line %d: %w", path, lineNumber, err)
		}
		result = append(result, item)
	}
	return result, scanner.Err()
}

func writeJSONL[T any](path string, values []T) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	for _, value := range values {
		if err := encoder.Encode(value); err != nil {
			return errors.Join(err, file.Close())
		}
	}
	return file.Close()
}

func writeJSON[T any](path string, value T) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0o600)
}

func loadKnowledgeConfig(path string) (config.KnowledgeConfig, error) {
	var decoded struct {
		Knowledge config.KnowledgeConfig `toml:"knowledge"`
	}
	if _, err := toml.DecodeFile(path, &decoded); err != nil {
		return config.KnowledgeConfig{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if err := decoded.Knowledge.Validate(); err != nil {
		return config.KnowledgeConfig{}, err
	}
	return decoded.Knowledge, nil
}

func parserLimits(cfg config.KnowledgeConfig) knowledgeparser.Limits {
	return knowledgeparser.Limits{
		MaxDocumentUnits: cfg.ParserMaxDocumentUnits, MaxArchiveEntries: cfg.ParserMaxArchiveEntries,
		MaxExpandedBytes: cfg.ParserMaxExpandedBytes, MaxXMLBytes: cfg.ParserMaxXMLBytes,
		MaxExtractedRunes: cfg.ParserMaxExtractedRunes, MaxSpreadsheetRows: cfg.ParserMaxSpreadsheetRows,
		MaxSpreadsheetColumns: cfg.ParserMaxSpreadsheetColumns, MaxVisualAssets: cfg.ParserMaxVisualAssets,
		MaxVisualAssetBytes: cfg.ParserMaxVisualAssetBytes, MaxTotalVisualBytes: cfg.ParserMaxTotalVisualBytes,
	}
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}
