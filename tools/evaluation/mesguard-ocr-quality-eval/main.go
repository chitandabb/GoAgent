// Command mesguard-ocr-quality-eval runs an explicitly enabled, bounded OCR comparison.
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
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/chitandabb/GoAgent/internal/knowledgeenrichment"
	"github.com/chitandabb/GoAgent/internal/knowledgelayout"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/platform/pdfiumrenderer"
	"github.com/chitandabb/GoAgent/internal/platform/visualmodel"
	"github.com/joho/godotenv"
)

const evaluatorVersion = "ocr-quality-eval-v1"

const maxPairedCharacterComparisonCells int64 = 64_000_000

type options struct {
	configPath            string
	inputPath             string
	page                  int
	outputPath            string
	executeProvider       bool
	inputPricePerMillion  float64
	outputPricePerMillion float64
	timeout               time.Duration
}

type observation struct {
	Label             string                             `json:"label"`
	RequestedDPI      int                                `json:"requestedDpi"`
	EffectiveDPI      int                                `json:"effectiveDpi"`
	MaxRasterPixels   int64                              `json:"maxRasterPixels"`
	Width             int                                `json:"width"`
	Height            int                                `json:"height"`
	RasterBytes       int                                `json:"rasterBytes"`
	RasterSHA256      string                             `json:"rasterSha256"`
	RenderMillis      float64                            `json:"renderMillis"`
	ProviderMillis    float64                            `json:"providerMillis,omitempty"`
	StrictJSONSuccess bool                               `json:"strictJsonSuccess"`
	Usage             *knowledgeenrichment.ProviderUsage `json:"usage,omitempty"`
	EstimatedCostCNY  float64                            `json:"estimatedCostCny,omitempty"`
	OCRText           string                             `json:"ocrText,omitempty"`
	Error             string                             `json:"error,omitempty"`
}

type report struct {
	EvaluatorVersion            string        `json:"evaluatorVersion"`
	RecordedAt                  time.Time     `json:"recordedAt"`
	ProviderExecuted            bool          `json:"providerExecuted"`
	Provider                    string        `json:"provider"`
	Model                       string        `json:"model"`
	InputName                   string        `json:"inputName"`
	InputSHA256                 string        `json:"inputSha256"`
	Page                        int           `json:"page"`
	Observations                []observation `json:"observations"`
	InputPricePerMillionCNY     float64       `json:"inputPricePerMillionCny"`
	OutputPricePerMillionCNY    float64       `json:"outputPricePerMillionCny"`
	TotalEstimatedCostCNY       float64       `json:"totalEstimatedCostCny"`
	PairedCharacterEditDistance int           `json:"pairedCharacterEditDistance,omitempty"`
	PairedCharacterSimilarity   float64       `json:"pairedCharacterSimilarity,omitempty"`
	PairedComparisonSkipped     string        `json:"pairedComparisonSkipped,omitempty"`
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
	cfg, err := loadConfig(opts.configPath)
	if err != nil {
		return err
	}
	content, err := os.ReadFile(opts.inputPath)
	if err != nil {
		return err
	}
	if int64(len(content)) < 1 || int64(len(content)) > cfg.Knowledge.MaxUploadBytes {
		return errors.New("OCR evaluation input size is invalid")
	}
	digest := sha256.Sum256(content)
	sha := hex.EncodeToString(digest[:])
	result := report{
		EvaluatorVersion: evaluatorVersion, RecordedAt: time.Now().UTC(),
		ProviderExecuted: opts.executeProvider, Provider: cfg.Models.OCR.Provider,
		Model: cfg.Models.OCR.Model, InputName: filepath.Base(opts.inputPath),
		InputSHA256: sha, Page: opts.page,
		InputPricePerMillionCNY:  opts.inputPricePerMillion,
		OutputPricePerMillionCNY: opts.outputPricePerMillion,
	}
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	var processor *visualmodel.Processor
	if opts.executeProvider {
		if !cfg.Models.OCR.Enabled {
			return errors.New("models.ocr is disabled")
		}
		prompt, err := cfg.Models.OCR.LoadPrompt("models.ocr")
		if err != nil {
			return err
		}
		generator, err := visualmodel.NewDashScopeModel(ctx, cfg.Models.OCR, "models.ocr")
		if err != nil {
			return err
		}
		processor, err = visualmodel.NewProcessor(&visualmodel.Endpoint{
			Generator: generator, Provider: cfg.Models.OCR.Provider, Model: cfg.Models.OCR.Model,
			Prompt: prompt, PromptVersion: cfg.Models.OCR.PromptVersion,
		}, nil)
		if err != nil {
			return err
		}
	}
	for _, candidate := range []struct {
		label     string
		maxPixels int64
	}{{"20m", 20_000_000}, {"8m", 8_000_000}} {
		current, err := evaluateCandidate(ctx, cfg, processor, content, sha, filepath.Base(opts.inputPath), opts.page, candidate.label, candidate.maxPixels, opts.executeProvider, opts.inputPricePerMillion, opts.outputPricePerMillion)
		result.Observations = append(result.Observations, current)
		result.TotalEstimatedCostCNY += current.EstimatedCostCNY
		fmt.Fprintf(os.Stdout, "%s dpi=%d size=%dx%d bytes=%d render_ms=%.2f provider_ms=%.2f tokens=%d cost_cny=%.6f error=%q\n",
			current.Label, current.EffectiveDPI, current.Width, current.Height, current.RasterBytes,
			current.RenderMillis, current.ProviderMillis, usageTotal(current.Usage), current.EstimatedCostCNY, current.Error)
		if err != nil {
			if writeErr := writeReport(opts.outputPath, result); writeErr != nil {
				return errors.Join(err, writeErr)
			}
			return err
		}
	}
	if len(result.Observations) == 2 && result.Observations[0].OCRText != "" && result.Observations[1].OCRText != "" {
		distance, similarity, compared := pairedCharacterSimilarity(
			result.Observations[0].OCRText, result.Observations[1].OCRText,
		)
		if compared {
			result.PairedCharacterEditDistance = distance
			result.PairedCharacterSimilarity = similarity
		} else {
			result.PairedComparisonSkipped = "comparison_size_limit_exceeded"
		}
	}
	return writeReport(opts.outputPath, result)
}

func parseFlags(args []string) (options, error) {
	flags := flag.NewFlagSet("mesguard-ocr-quality-eval", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var result options
	flags.StringVar(&result.configPath, "config", "config/mesguard.toml", "MESGuard TOML configuration")
	flags.StringVar(&result.inputPath, "input", "", "PDF input path")
	flags.IntVar(&result.page, "page", 0, "one-based page number")
	flags.StringVar(&result.outputPath, "output", "output/evaluation/ocr-quality.summary.json", "summary JSON output")
	flags.BoolVar(&result.executeProvider, "execute-provider", false, "perform exactly two configured OCR provider calls")
	flags.Float64Var(&result.inputPricePerMillion, "input-price-per-million-cny", 0.3, "OCR input price per million tokens")
	flags.Float64Var(&result.outputPricePerMillion, "output-price-per-million-cny", 0.5, "OCR output price per million tokens")
	flags.DurationVar(&result.timeout, "timeout", 5*time.Minute, "total evaluation timeout")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || strings.TrimSpace(result.inputPath) == "" ||
		result.page < 1 || result.timeout <= 0 || result.inputPricePerMillion < 0 || result.outputPricePerMillion < 0 {
		return options{}, errors.New("usage: mesguard-ocr-quality-eval -input file -page number [-execute-provider] [-output path] [-timeout duration]")
	}
	return result, nil
}

func loadConfig(path string) (config.Config, error) {
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		return config.Config{}, err
	}
	var cfg config.Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return config.Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return config.Config{}, err
	}
	return cfg, nil
}

func evaluateCandidate(
	ctx context.Context,
	cfg config.Config,
	processor *visualmodel.Processor,
	content []byte,
	sha string,
	sourceName string,
	page int,
	label string,
	maxPixels int64,
	executeProvider bool,
	inputPricePerMillion float64,
	outputPricePerMillion float64,
) (observation, error) {
	layout := cfg.Knowledge.Layout
	renderer, err := pdfiumrenderer.OpenWASM(ctx, pdfiumrenderer.Config{
		RendererVersion: layout.RendererVersion, MaxSourceBytes: cfg.Knowledge.MaxUploadBytes,
		MaxRasterPixels: maxPixels, MaxRasterBytes: layout.MaxRasterBytes,
		MaxExtractedRunes: cfg.Knowledge.ParserMaxExtractedRunes, MaxConcurrent: 1,
		AcquireTimeout: time.Duration(layout.RendererAcquireMillis) * time.Millisecond,
		RenderTimeout:  time.Duration(layout.RendererTimeoutMillis) * time.Millisecond,
	})
	if err != nil {
		return observation{Label: label, MaxRasterPixels: maxPixels, Error: err.Error()}, err
	}
	defer renderer.Close()
	started := time.Now()
	rendered, err := renderer.RenderPage(ctx, knowledgelayout.RenderRequest{
		Source:     knowledgelayout.DocumentSource{MediaType: "application/pdf", Content: content, SHA256: sha},
		PageNumber: page, DPI: layout.RenderDPI,
	})
	current := observation{
		Label: label, RequestedDPI: layout.RenderDPI, MaxRasterPixels: maxPixels,
		RenderMillis: float64(time.Since(started).Microseconds()) / 1000,
	}
	if err != nil {
		current.Error = err.Error()
		return current, err
	}
	current.EffectiveDPI, current.Width, current.Height = rendered.DPI, rendered.Raster.Width, rendered.Raster.Height
	current.RasterBytes, current.RasterSHA256 = len(rendered.Raster.Content), rendered.RasterSHA256
	if !executeProvider {
		return current, nil
	}
	started = time.Now()
	ocr, err := processor.ExtractOCR(ctx, fmt.Sprintf("%s/page-%d-%s.png", filepath.Base(sourceName), page, label), rendered.Raster.MediaType, rendered.Raster.Content)
	current.ProviderMillis = float64(time.Since(started).Microseconds()) / 1000
	if err != nil {
		current.Error = err.Error()
		return current, err
	}
	current.StrictJSONSuccess, current.OCRText, current.Usage = true, ocr.Text, ocr.Usage
	current.EstimatedCostCNY = estimatedOCRCost(ocr.Usage, inputPricePerMillion, outputPricePerMillion)
	return current, nil
}

// qwen-vl-ocr-latest China pricing observed on 2026-08-05:
// CNY 0.3 / 1M input tokens and CNY 0.5 / 1M output tokens.
func estimatedOCRCost(usage *knowledgeenrichment.ProviderUsage, inputPricePerMillion, outputPricePerMillion float64) float64 {
	if usage == nil {
		return 0
	}
	return float64(usage.PromptTokens)*inputPricePerMillion/1_000_000 +
		float64(usage.CompletionTokens)*outputPricePerMillion/1_000_000
}

func pairedCharacterSimilarity(leftText, rightText string) (int, float64, bool) {
	left, right := []rune(leftText), []rune(rightText)
	if len(right) > len(left) {
		left, right = right, left
	}
	if int64(len(left))*int64(len(right)) > maxPairedCharacterComparisonCells {
		return 0, 0, false
	}
	previous := make([]int, len(right)+1)
	current := make([]int, len(right)+1)
	for index := range previous {
		previous[index] = index
	}
	for leftIndex := 1; leftIndex <= len(left); leftIndex++ {
		current[0] = leftIndex
		for rightIndex := 1; rightIndex <= len(right); rightIndex++ {
			cost := 1
			if left[leftIndex-1] == right[rightIndex-1] {
				cost = 0
			}
			current[rightIndex] = minInt(
				current[rightIndex-1]+1,
				previous[rightIndex]+1,
				previous[rightIndex-1]+cost,
			)
		}
		previous, current = current, previous
	}
	distance := previous[len(right)]
	maximum := max(len(left), len(right))
	if maximum == 0 {
		return distance, 1, true
	}
	return distance, 1 - float64(distance)/float64(maximum), true
}

func minInt(values ...int) int {
	result := values[0]
	for _, value := range values[1:] {
		if value < result {
			result = value
		}
	}
	return result
}

func usageTotal(usage *knowledgeenrichment.ProviderUsage) int {
	if usage == nil {
		return 0
	}
	return usage.TotalTokens
}

func writeReport(path string, result report) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0o600)
}
