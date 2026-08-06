package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"strings"
)

// KnowledgeLayoutConfig configures the optional local page-layout router.
// Disabled profiles may keep deployment-specific artifact fields empty; enabling
// the router requires a complete, reproducible model and runtime contract.
type KnowledgeLayoutConfig struct {
	Enabled                              bool    `toml:"enabled"`
	Provider                             string  `toml:"provider"`
	ModelName                            string  `toml:"modelName"`
	ModelVersion                         string  `toml:"modelVersion"`
	ModelPath                            string  `toml:"modelPath"`
	ModelSHA256                          string  `toml:"modelSHA256"`
	ManifestPath                         string  `toml:"manifestPath"`
	RuntimeLibraryPath                   string  `toml:"runtimeLibraryPath"`
	PreprocessVersion                    string  `toml:"preprocessVersion"`
	PostprocessVersion                   string  `toml:"postprocessVersion"`
	InputWidth                           int     `toml:"inputWidth"`
	InputHeight                          int     `toml:"inputHeight"`
	IntraOpThreads                       int     `toml:"intraOpThreads"`
	InterOpThreads                       int     `toml:"interOpThreads"`
	InferenceTimeoutMillis               int     `toml:"inferenceTimeoutMillis"`
	MaxConcurrentPages                   int     `toml:"maxConcurrentPages"`
	RenderDPI                            int     `toml:"renderDPI"`
	RendererProvider                     string  `toml:"rendererProvider"`
	RendererVersion                      string  `toml:"rendererVersion"`
	RendererAcquireMillis                int     `toml:"rendererAcquireMillis"`
	RendererTimeoutMillis                int     `toml:"rendererTimeoutMillis"`
	MinNativeTextRunes                   int     `toml:"minNativeTextRunes"`
	MinNativePrintableRatio              float64 `toml:"minNativePrintableRatio"`
	MinRegionConfidence                  float64 `toml:"minRegionConfidence"`
	SuppressDecorativePictureDuplicates  bool    `toml:"suppressDecorativePictureDuplicates"`
	DecorativePictureMinIoU              float64 `toml:"decorativePictureMinIoU"`
	DecorativePictureMaxAreaRatio        float64 `toml:"decorativePictureMaxAreaRatio"`
	DecorativePictureMinConfidenceMargin float64 `toml:"decorativePictureMinConfidenceMargin"`
	RegionPaddingRatio                   float64 `toml:"regionPaddingRatio"`
	MaxRegions                           int     `toml:"maxRegions"`
	MaxRasterPixels                      int64   `toml:"maxRasterPixels"`
	MaxRasterBytes                       int64   `toml:"maxRasterBytes"`
}

func (c KnowledgeLayoutConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if strings.ToLower(strings.TrimSpace(c.Provider)) != "onnxruntime" {
		return errors.New("knowledge.layout provider must be onnxruntime")
	}
	for name, value := range map[string]string{
		"modelName": c.ModelName, "modelVersion": c.ModelVersion,
		"preprocessVersion": c.PreprocessVersion, "postprocessVersion": c.PostprocessVersion,
	} {
		if !modelName.MatchString(strings.TrimSpace(value)) {
			return errors.New("knowledge.layout " + name + " is invalid")
		}
	}
	if !validLayoutArtifactPath(c.ModelPath) {
		return errors.New("knowledge.layout modelPath is invalid")
	}
	if !validLayoutArtifactPath(c.ManifestPath) {
		return errors.New("knowledge.layout manifestPath is invalid")
	}
	if !validLayoutArtifactPath(c.RuntimeLibraryPath) {
		return errors.New("knowledge.layout runtimeLibraryPath is invalid")
	}
	if !validLowerSHA256(c.ModelSHA256) {
		return errors.New("knowledge.layout modelSHA256 is invalid")
	}
	if c.InputWidth < 32 || c.InputWidth > 8192 || c.InputHeight < 32 || c.InputHeight > 8192 {
		return errors.New("knowledge.layout inputWidth and inputHeight must be between 32 and 8192")
	}
	if c.IntraOpThreads < 1 || c.IntraOpThreads > 64 || c.InterOpThreads < 1 || c.InterOpThreads > 64 {
		return errors.New("knowledge.layout thread counts must be between 1 and 64")
	}
	if c.InferenceTimeoutMillis < 100 || c.InferenceTimeoutMillis > 120_000 {
		return errors.New("knowledge.layout inferenceTimeoutMillis must be between 100 and 120000")
	}
	if c.MaxConcurrentPages < 1 || c.MaxConcurrentPages > 64 {
		return errors.New("knowledge.layout maxConcurrentPages must be between 1 and 64")
	}
	if c.RenderDPI < 72 || c.RenderDPI > 600 {
		return errors.New("knowledge.layout renderDPI must be between 72 and 600")
	}
	if strings.ToLower(strings.TrimSpace(c.RendererProvider)) != "pdfium-wasm" {
		return errors.New("knowledge.layout rendererProvider must be pdfium-wasm")
	}
	if !modelName.MatchString(strings.TrimSpace(c.RendererVersion)) {
		return errors.New("knowledge.layout rendererVersion is invalid")
	}
	if c.RendererAcquireMillis < 100 || c.RendererAcquireMillis > 120_000 {
		return errors.New("knowledge.layout rendererAcquireMillis must be between 100 and 120000")
	}
	if c.RendererTimeoutMillis < 100 || c.RendererTimeoutMillis > 300_000 {
		return errors.New("knowledge.layout rendererTimeoutMillis must be between 100 and 300000")
	}
	if c.MinNativeTextRunes < 1 || c.MinNativeTextRunes > 1_000_000 {
		return errors.New("knowledge.layout minNativeTextRunes must be between 1 and 1000000")
	}
	if invalidUnitInterval(c.MinNativePrintableRatio) || invalidUnitInterval(c.MinRegionConfidence) {
		return errors.New("knowledge.layout printable ratio and region confidence must be in (0,1]")
	}
	if invalidUnitInterval(c.DecorativePictureMinIoU) ||
		invalidUnitInterval(c.DecorativePictureMaxAreaRatio) ||
		math.IsNaN(c.DecorativePictureMinConfidenceMargin) ||
		math.IsInf(c.DecorativePictureMinConfidenceMargin, 0) ||
		c.DecorativePictureMinConfidenceMargin < 0 || c.DecorativePictureMinConfidenceMargin > 1 {
		return errors.New("knowledge.layout decorative-picture arbitration thresholds are invalid")
	}
	if math.IsNaN(c.RegionPaddingRatio) || math.IsInf(c.RegionPaddingRatio, 0) ||
		c.RegionPaddingRatio < 0 || c.RegionPaddingRatio > 0.2 {
		return errors.New("knowledge.layout regionPaddingRatio must be between 0 and 0.2")
	}
	if c.MaxRegions < 1 || c.MaxRegions > 10_000 {
		return errors.New("knowledge.layout maxRegions must be between 1 and 10000")
	}
	if c.MaxRasterPixels < 1 || c.MaxRasterPixels > 1_000_000_000 {
		return errors.New("knowledge.layout maxRasterPixels must be between 1 and 1000000000")
	}
	if c.MaxRasterBytes < 1 || c.MaxRasterBytes > 256*1024*1024 {
		return errors.New("knowledge.layout maxRasterBytes must be between 1 and 268435456")
	}
	return nil
}

func validLayoutArtifactPath(value string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed != "" && trimmed == value && len(value) <= 1024 && !strings.ContainsRune(value, '\x00')
}

func validLowerSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func invalidUnitInterval(value float64) bool {
	return math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value > 1
}
