// Command mesguard-document-parse-eval measures bounded local document parsing.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
	"github.com/chitandabb/GoAgent/internal/platform/config"
)

const evaluatorVersion = "document-parse-eval-v1"

type inputPaths []string

func (p *inputPaths) String() string { return strings.Join(*p, ",") }
func (p *inputPaths) Set(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return errors.New("input path is empty")
	}
	*p = append(*p, trimmed)
	return nil
}

type options struct {
	configPath string
	outputPath string
	inputs     inputPaths
	timeout    time.Duration
}

type fileObservation struct {
	Name                     string  `json:"name"`
	SizeBytes                int64   `json:"sizeBytes"`
	SHA256                   string  `json:"sha256"`
	ParserVersion            string  `json:"parserVersion,omitempty"`
	DurationMillis           float64 `json:"durationMillis"`
	TotalAllocatedBytes      uint64  `json:"totalAllocatedBytes"`
	SlideCount               int     `json:"slideCount"`
	ElementCount             int     `json:"elementCount"`
	TextElementCount         int     `json:"textElementCount"`
	TableElementCount        int     `json:"tableElementCount"`
	ExtractedRunes           int     `json:"extractedRunes"`
	VisualAssetCount         int     `json:"visualAssetCount"`
	ReferencedVisualAssets   int     `json:"referencedVisualAssets"`
	UnreferencedVisualAssets int     `json:"unreferencedVisualAssets"`
	Error                    string  `json:"error,omitempty"`
}

type summary struct {
	EvaluatorVersion       string            `json:"evaluatorVersion"`
	RecordedAt             time.Time         `json:"recordedAt"`
	FileCount              int               `json:"fileCount"`
	SucceededFiles         int               `json:"succeededFiles"`
	FailedFiles            int               `json:"failedFiles"`
	TotalBytes             int64             `json:"totalBytes"`
	TotalSlides            int               `json:"totalSlides"`
	TotalElements          int               `json:"totalElements"`
	TotalTextElements      int               `json:"totalTextElements"`
	TotalTableElements     int               `json:"totalTableElements"`
	TotalExtractedRunes    int               `json:"totalExtractedRunes"`
	TotalVisualAssets      int               `json:"totalVisualAssets"`
	TotalDurationMillis    float64           `json:"totalDurationMillis"`
	ThroughputMiBPerSecond float64           `json:"throughputMiBPerSecond"`
	SlidesPerSecond        float64           `json:"slidesPerSecond"`
	P50FileMillis          float64           `json:"p50FileMillis"`
	P95FileMillis          float64           `json:"p95FileMillis"`
	Files                  []fileObservation `json:"files"`
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

	result := summary{EvaluatorVersion: evaluatorVersion, RecordedAt: time.Now().UTC(), FileCount: len(opts.inputs)}
	result.Files = make([]fileObservation, 0, len(opts.inputs))
	for _, inputPath := range opts.inputs {
		if err := ctx.Err(); err != nil {
			return err
		}
		observation := evaluateFile(ctx, parser, knowledgeConfig.MaxUploadBytes, inputPath)
		result.Files = append(result.Files, observation)
		accumulate(&result, observation)
		fmt.Fprintf(os.Stdout, "%s slides=%d elements=%d tables=%d visuals=%d duration_ms=%.2f error=%q\n",
			observation.Name, observation.SlideCount, observation.ElementCount,
			observation.TableElementCount, observation.VisualAssetCount,
			observation.DurationMillis, observation.Error)
	}
	finishSummary(&result)
	if err := writeSummary(opts.outputPath, result); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "summary files=%d succeeded=%d slides=%d throughput_mib_s=%.2f slides_s=%.2f p95_ms=%.2f\n",
		result.FileCount, result.SucceededFiles, result.TotalSlides, result.ThroughputMiBPerSecond,
		result.SlidesPerSecond, result.P95FileMillis)
	if result.FailedFiles > 0 {
		return fmt.Errorf("document parse evaluation failed for %d file(s)", result.FailedFiles)
	}
	return nil
}

func parseFlags(args []string) (options, error) {
	flags := flag.NewFlagSet("mesguard-document-parse-eval", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var result options
	flags.StringVar(&result.configPath, "config", "config/mesguard.toml", "MESGuard TOML configuration")
	flags.StringVar(&result.outputPath, "output", "output/evaluation/document-parse.summary.json", "summary JSON output")
	flags.Var(&result.inputs, "input", "document path; repeat for multiple files")
	flags.DurationVar(&result.timeout, "timeout", 10*time.Minute, "total evaluation timeout")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || len(result.inputs) == 0 || result.timeout <= 0 {
		return options{}, errors.New("usage: mesguard-document-parse-eval [-config path] [-output path] -input file [-input file ...] [-timeout duration]")
	}
	return result, nil
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

func evaluateFile(ctx context.Context, parser knowledgeparser.OOXMLParser, maxBytes int64, path string) fileObservation {
	observation := fileObservation{Name: filepath.Base(path)}
	content, info, err := readBounded(path, maxBytes)
	if err != nil {
		observation.Error = err.Error()
		return observation
	}
	observation.SizeBytes = info.Size()
	digest := sha256.Sum256(content)
	observation.SHA256 = hex.EncodeToString(digest[:])
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	startedAt := time.Now()
	parsed, err := parser.Parse(ctx, knowledgeparser.Input{
		MediaType: knowledgeparser.PPTXMediaType, OriginalName: observation.Name, Content: content,
	})
	observation.DurationMillis = float64(time.Since(startedAt).Microseconds()) / 1000
	runtime.ReadMemStats(&after)
	observation.TotalAllocatedBytes = after.TotalAlloc - before.TotalAlloc
	if err != nil {
		observation.Error = err.Error()
		return observation
	}
	if err := parsed.Validate(); err != nil {
		observation.Error = "validate parser result: " + err.Error()
		return observation
	}
	observation.ParserVersion = parsed.ParserVersion
	for _, element := range parsed.Elements {
		observation.ElementCount++
		observation.ExtractedRunes += utf8.RuneCountInString(element.ContentText)
		switch element.ElementType {
		case knowledge.ElementText:
			observation.TextElementCount++
		case knowledge.ElementTable:
			observation.TableElementCount++
		}
	}
	observation.VisualAssetCount = len(parsed.VisualAssets)
	for _, asset := range parsed.VisualAssets {
		if asset.RelationshipID == "" {
			observation.UnreferencedVisualAssets++
		} else {
			observation.ReferencedVisualAssets++
		}
	}
	var metadata struct {
		SlideCount int `json:"slideCount"`
	}
	if err := json.Unmarshal(parsed.Metadata, &metadata); err != nil {
		observation.Error = "decode parser metadata: " + err.Error()
		return observation
	}
	observation.SlideCount = metadata.SlideCount
	return observation
}

func readBounded(path string, maxBytes int64) ([]byte, os.FileInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxBytes {
		return nil, nil, errors.New("input file size is invalid")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(content)) != info.Size() {
		return nil, nil, errors.New("input file changed while being read")
	}
	return content, info, nil
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

func accumulate(result *summary, observation fileObservation) {
	if observation.Error != "" {
		result.FailedFiles++
		return
	}
	result.SucceededFiles++
	result.TotalBytes += observation.SizeBytes
	result.TotalSlides += observation.SlideCount
	result.TotalElements += observation.ElementCount
	result.TotalTextElements += observation.TextElementCount
	result.TotalTableElements += observation.TableElementCount
	result.TotalExtractedRunes += observation.ExtractedRunes
	result.TotalVisualAssets += observation.VisualAssetCount
	result.TotalDurationMillis += observation.DurationMillis
}

func finishSummary(result *summary) {
	if result.TotalDurationMillis > 0 {
		seconds := result.TotalDurationMillis / 1000
		result.ThroughputMiBPerSecond = float64(result.TotalBytes) / (1024 * 1024) / seconds
		result.SlidesPerSecond = float64(result.TotalSlides) / seconds
	}
	durations := make([]float64, 0, result.SucceededFiles)
	for _, observation := range result.Files {
		if observation.Error == "" {
			durations = append(durations, observation.DurationMillis)
		}
	}
	sort.Float64s(durations)
	result.P50FileMillis = percentile(durations, 0.50)
	result.P95FileMillis = percentile(durations, 0.95)
}

func percentile(sorted []float64, ratio float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	index := int(math.Ceil(ratio*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	return sorted[index]
}

func writeSummary(path string, result summary) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return err
	}
	return nil
}
