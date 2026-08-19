package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/knowledgelayout"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
	"github.com/chitandabb/GoAgent/internal/knowledgetable"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformtablemodel "github.com/chitandabb/GoAgent/internal/platform/tablemodel"
	platformvisualmodel "github.com/chitandabb/GoAgent/internal/platform/visualmodel"
)

const (
	maxSourceImageBytes = 16 * 1024 * 1024
	maxRasterPixels     = 20_000_000
	maxCropBytes        = 8 * 1024 * 1024
)

type options struct {
	configPath            string
	inputPath             string
	box                   string
	page                  int
	outputPath            string
	cropOutputPath        string
	validateOnly          bool
	estimateOnly          bool
	executeProvider       bool
	paddingRatio          float64
	maxEstimatedInput     int
	inputPricePerMillion  float64
	outputPricePerMillion float64
	maxCostCNY            float64
}

type observation struct {
	DatasetVersion       string                      `json:"datasetVersion"`
	Mode                 string                      `json:"mode"`
	SourceName           string                      `json:"sourceName"`
	SourceSHA256         string                      `json:"sourceSha256"`
	Page                 int                         `json:"page"`
	RequestedBox         knowledgelayout.BoundingBox `json:"requestedBox"`
	AppliedBox           knowledgelayout.BoundingBox `json:"appliedBox"`
	CropSHA256           string                      `json:"cropSha256"`
	CropWidth            int                         `json:"cropWidth"`
	CropHeight           int                         `json:"cropHeight"`
	CropBytes            int                         `json:"cropBytes"`
	Provider             string                      `json:"provider"`
	Model                string                      `json:"model"`
	PromptVersion        string                      `json:"promptVersion"`
	MaxOutputTokens      int                         `json:"maxOutputTokens"`
	EstimatedInputTokens int                         `json:"estimatedInputTokens"`
	EstimatedMaxCostCNY  float64                     `json:"estimatedMaxCostCny"`
	DurationMillis       int64                       `json:"durationMillis,omitempty"`
	ActualCostCNY        *float64                    `json:"actualCostCny,omitempty"`
	Result               *knowledgetable.Result      `json:"result,omitempty"`
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	opts, err := parseOptions(args)
	if err != nil {
		return err
	}
	if opts.configPath != "" {
		if err := os.Setenv("MESGUARD_CONFIG_FILE", opts.configPath); err != nil {
			return err
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !cfg.Models.Table.Enabled {
		return errors.New("models.table must be enabled for table quality evaluation")
	}
	box, err := parseBox(opts.box)
	if err != nil {
		return err
	}
	content, mediaType, width, height, err := readSourceImage(opts.inputPath)
	if err != nil {
		return err
	}
	crop, err := knowledgelayout.CropRaster(knowledgelayout.RasterPage{
		MediaType: mediaType, Width: width, Height: height, Content: content,
	}, box, knowledgelayout.CropConfig{
		PaddingRatio: opts.paddingRatio, MaxPixels: maxRasterPixels, MaxBytes: maxCropBytes,
	})
	if err != nil {
		return fmt.Errorf("crop table region: %w", err)
	}
	if opts.cropOutputPath != "" {
		if err := writeBoundedOutput(opts.cropOutputPath, crop.Raster.Content); err != nil {
			return err
		}
	}
	estimatedMaxCost := estimatedCost(
		opts.maxEstimatedInput, cfg.Models.Table.MaxOutputTokens,
		opts.inputPricePerMillion, opts.outputPricePerMillion,
	)
	mode := "validate_only"
	if opts.estimateOnly {
		mode = "estimate_only"
	}
	if opts.executeProvider {
		mode = "execute_provider"
	}
	current := observation{
		DatasetVersion: "table-recovery-v1", Mode: mode,
		SourceName: filepath.Base(opts.inputPath), SourceSHA256: sha256Hex(content), Page: opts.page,
		RequestedBox: box, AppliedBox: crop.AppliedBox, CropSHA256: crop.RasterSHA256,
		CropWidth: crop.Raster.Width, CropHeight: crop.Raster.Height, CropBytes: len(crop.Raster.Content),
		Provider: cfg.Models.Table.Provider, Model: cfg.Models.Table.Model,
		PromptVersion: cfg.Models.Table.PromptVersion, MaxOutputTokens: cfg.Models.Table.MaxOutputTokens,
		EstimatedInputTokens: opts.maxEstimatedInput, EstimatedMaxCostCNY: estimatedMaxCost,
	}
	if opts.executeProvider {
		if estimatedMaxCost > opts.maxCostCNY {
			return fmt.Errorf("estimated maximum cost %.6f CNY exceeds cap %.6f CNY", estimatedMaxCost, opts.maxCostCNY)
		}
		prompt, err := cfg.Models.Table.LoadPrompt("models.table")
		if err != nil {
			return err
		}
		generator, err := platformvisualmodel.NewOpenAICompatibleModel(ctx, cfg.Models.Table, "models.table")
		if err != nil {
			return err
		}
		processor, err := platformtablemodel.NewProcessor(platformtablemodel.Endpoint{
			Generator: generator, Provider: cfg.Models.Table.Provider, Model: cfg.Models.Table.Model,
			Prompt: prompt, PromptVersion: cfg.Models.Table.PromptVersion,
		})
		if err != nil {
			return err
		}
		assetContent := crop.Raster.Content
		asset := knowledgeparser.VisualAsset{
			Index: 0, Kind: knowledgeparser.VisualAssetLayoutRegion, PageNumber: &opts.page,
			SourcePath: fmt.Sprintf("pages/%d/layout-regions/0", opts.page), MediaType: "image/png",
			SizeBytes: int64(len(assetContent)), SHA256: knowledge.SHA256Hex(string(assetContent)),
			Width: crop.Raster.Width, Height: crop.Raster.Height, Content: assetContent,
		}
		started := time.Now()
		result, err := processor.Recover(ctx, knowledgetable.Request{
			Asset: asset, Reason: "table_structure_required",
		})
		current.DurationMillis = time.Since(started).Milliseconds()
		if err != nil {
			return err
		}
		current.Result = &result
		if result.Usage != nil {
			cost := estimatedCost(
				result.Usage.PromptTokens, result.Usage.CompletionTokens,
				opts.inputPricePerMillion, opts.outputPricePerMillion,
			)
			current.ActualCostCNY = &cost
		}
	}
	encoded, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if opts.outputPath != "" {
		if err := writeBoundedOutput(opts.outputPath, encoded); err != nil {
			return err
		}
	}
	_, err = os.Stdout.Write(encoded)
	return err
}

func parseOptions(args []string) (options, error) {
	var result options
	flags := flag.NewFlagSet("mesguard-table-quality-eval", flag.ContinueOnError)
	flags.StringVar(&result.configPath, "config", "config/mesguard.toml", "MESGuard TOML configuration")
	flags.StringVar(&result.inputPath, "input", "", "source PNG/JPEG page image")
	flags.StringVar(&result.box, "box", "", "normalized table box: left,top,right,bottom")
	flags.IntVar(&result.page, "page", 1, "one-based source page number")
	flags.StringVar(&result.outputPath, "output", "", "observation JSON under output/evaluation")
	flags.StringVar(&result.cropOutputPath, "crop-output", "", "optional crop PNG under output/evaluation")
	flags.BoolVar(&result.validateOnly, "validate-only", false, "validate and crop without cost estimate execution")
	flags.BoolVar(&result.estimateOnly, "estimate-only", false, "validate, crop and print conservative cost estimate")
	flags.BoolVar(&result.executeProvider, "execute-provider", false, "perform exactly one table-provider call")
	flags.Float64Var(&result.paddingRatio, "padding-ratio", 0.01, "normalized crop padding")
	flags.IntVar(&result.maxEstimatedInput, "max-estimated-input-tokens", 5000, "conservative input-token planning bound")
	flags.Float64Var(&result.inputPricePerMillion, "input-price-per-million-cny", 1, "input price used only for this estimate")
	flags.Float64Var(&result.outputPricePerMillion, "output-price-per-million-cny", 10, "output price used only for this estimate")
	flags.Float64Var(&result.maxCostCNY, "max-cost-cny", 0.05, "hard preflight cost cap")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	modes := 0
	for _, enabled := range []bool{result.validateOnly, result.estimateOnly, result.executeProvider} {
		if enabled {
			modes++
		}
	}
	if modes != 1 || strings.TrimSpace(result.inputPath) == "" || strings.TrimSpace(result.box) == "" ||
		result.page < 1 || result.page > 100_000 || result.paddingRatio < 0 || result.paddingRatio > 0.2 ||
		result.maxEstimatedInput < 1 || result.maxEstimatedInput > 1_000_000 ||
		result.inputPricePerMillion < 0 || result.outputPricePerMillion < 0 || result.maxCostCNY <= 0 {
		return options{}, errors.New("usage: mesguard-table-quality-eval -input page.png -box left,top,right,bottom (-validate-only|-estimate-only|-execute-provider)")
	}
	return result, nil
}

func parseBox(value string) (knowledgelayout.BoundingBox, error) {
	parts := strings.Split(value, ",")
	if len(parts) != 4 {
		return knowledgelayout.BoundingBox{}, errors.New("table box must contain four normalized numbers")
	}
	values := make([]float64, 4)
	for index, part := range parts {
		parsed, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			return knowledgelayout.BoundingBox{}, errors.New("table box contains an invalid number")
		}
		values[index] = parsed
	}
	result := knowledgelayout.BoundingBox{Left: values[0], Top: values[1], Right: values[2], Bottom: values[3]}
	if err := result.Validate(); err != nil {
		return knowledgelayout.BoundingBox{}, err
	}
	return result, nil
}

func readSourceImage(path string) ([]byte, string, int, int, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, "", 0, 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() < 1 || info.Size() > maxSourceImageBytes {
		return nil, "", 0, 0, errors.New("source image size is invalid")
	}
	content := make([]byte, info.Size())
	read, err := io.ReadFull(file, content)
	if err != nil {
		return nil, "", 0, 0, err
	}
	if int64(read) != info.Size() {
		return nil, "", 0, 0, errors.New("source image read is incomplete")
	}
	decoded, format, err := image.DecodeConfig(bytes.NewReader(content))
	if err != nil || decoded.Width < 1 || decoded.Height < 1 || int64(decoded.Width)*int64(decoded.Height) > maxRasterPixels {
		return nil, "", 0, 0, errors.New("source image dimensions are invalid")
	}
	mediaType := ""
	switch format {
	case "png":
		mediaType = "image/png"
	case "jpeg":
		mediaType = "image/jpeg"
	default:
		return nil, "", 0, 0, errors.New("source image must be PNG or JPEG")
	}
	return content, mediaType, decoded.Width, decoded.Height, nil
}

func writeBoundedOutput(path string, content []byte) error {
	if len(content) == 0 || len(content) > maxCropBytes {
		return errors.New("evaluation output is empty or exceeds limit")
	}
	repositoryRoot, err := os.Getwd()
	if err != nil {
		return err
	}
	root := filepath.Join(repositoryRoot, "output", "evaluation")
	target, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("evaluation output must stay under output/evaluation")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, content, 0o600)
}

func estimatedCost(inputTokens, outputTokens int, inputPrice, outputPrice float64) float64 {
	return float64(inputTokens)*inputPrice/1_000_000 + float64(outputTokens)*outputPrice/1_000_000
}

func sha256Hex(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}
