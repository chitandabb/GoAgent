package onnxlayout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledgelayout"
)

const sha256HexLength = sha256.Size * 2

type Config struct {
	RuntimeLibraryPath                   string
	ModelPath                            string
	ModelSHA256                          string
	ManifestPath                         string
	Provider                             string
	ModelName                            string
	ModelVersion                         string
	PreprocessVersion                    string
	PostprocessVersion                   string
	InputWidth                           int
	InputHeight                          int
	IntraOpThreads                       int
	InterOpThreads                       int
	InferenceTimeout                     time.Duration
	MaxConcurrentPages                   int
	MaxRegions                           int
	MaxRasterPixels                      int64
	MaxRasterBytes                       int64
	SuppressDecorativePictureDuplicates  bool
	DecorativePictureMinIoU              float64
	DecorativePictureMaxAreaRatio        float64
	DecorativePictureMinConfidenceMargin float64
}

type inferenceEngine interface {
	Run(context.Context, []float32, []float32) ([]float32, []int32, error)
	Close() error
}

type Router struct {
	config   Config
	manifest Manifest
	trace    knowledgelayout.ModelTrace
	engine   inferenceEngine
	slots    chan struct{}
	mu       sync.RWMutex
	closed   bool
	closeErr error
}

func New(config Config) (*Router, error) {
	manifest, err := LoadManifest(config.ManifestPath)
	if err != nil {
		return nil, err
	}
	if err := validateConfig(config, manifest); err != nil {
		return nil, err
	}
	if err := verifyModelFile(config.ModelPath, manifest.Conversion.Bytes, config.ModelSHA256); err != nil {
		return nil, err
	}
	engine, err := newORTEngine(config)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", knowledgelayout.ErrRouterUnavailable, err)
	}
	return newRouter(config, manifest, engine), nil
}

func newRouter(config Config, manifest Manifest, engine inferenceEngine) *Router {
	return &Router{
		config: config, manifest: manifest, engine: engine,
		trace: knowledgelayout.ModelTrace{
			Provider: config.Provider, Name: config.ModelName, Version: config.ModelVersion,
			SHA256: config.ModelSHA256, PreprocessVersion: config.PreprocessVersion,
			PostprocessVersion: config.PostprocessVersion,
		},
		slots: make(chan struct{}, config.MaxConcurrentPages),
	}
}

func validateConfig(config Config, manifest Manifest) error {
	if strings.TrimSpace(config.RuntimeLibraryPath) == "" || strings.TrimSpace(config.ModelPath) == "" ||
		strings.TrimSpace(config.ManifestPath) == "" || config.Provider != "onnxruntime" ||
		config.ModelName != manifest.Name || config.ModelVersion != manifest.Source.Revision ||
		config.ModelSHA256 != manifest.Conversion.SHA256 ||
		config.PreprocessVersion != manifest.Preprocess.Version ||
		config.PostprocessVersion != manifest.Postprocess.Version ||
		config.InputWidth != manifest.Preprocess.InputWidth ||
		config.InputHeight != manifest.Preprocess.InputHeight ||
		config.IntraOpThreads < 1 || config.InterOpThreads < 1 ||
		config.InferenceTimeout <= 0 || config.MaxConcurrentPages < 1 ||
		config.MaxRegions < manifest.Postprocess.KeepTopK ||
		config.MaxRasterPixels < 1 || config.MaxRasterBytes < 1 ||
		config.DecorativePictureMinIoU <= 0 || config.DecorativePictureMinIoU > 1 ||
		config.DecorativePictureMaxAreaRatio <= 0 || config.DecorativePictureMaxAreaRatio > 1 ||
		config.DecorativePictureMinConfidenceMargin < 0 || config.DecorativePictureMinConfidenceMargin > 1 {
		return errors.New("ONNX layout router configuration does not match the model manifest")
	}
	trace := knowledgelayout.ModelTrace{
		Provider: config.Provider, Name: config.ModelName, Version: config.ModelVersion,
		SHA256: config.ModelSHA256, PreprocessVersion: config.PreprocessVersion,
		PostprocessVersion: config.PostprocessVersion,
	}
	return trace.Validate()
}

func verifyFileSHA256(path, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open ONNX layout model: %w", err)
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return fmt.Errorf("hash ONNX layout model: %w", err)
	}
	actual := hex.EncodeToString(hasher.Sum(nil))
	if actual != expected {
		return fmt.Errorf("ONNX layout model SHA-256 mismatch: got %s", actual)
	}
	return nil
}

func verifyModelFile(path string, expectedBytes int64, expectedSHA256 string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat ONNX layout model: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() != expectedBytes {
		return fmt.Errorf("ONNX layout model byte length mismatch: got %d", info.Size())
	}
	return verifyFileSHA256(path, expectedSHA256)
}

func (r *Router) Detect(ctx context.Context, page knowledgelayout.RasterPage) (knowledgelayout.DetectionResult, error) {
	if r == nil {
		return knowledgelayout.DetectionResult{}, knowledgelayout.ErrRouterUnavailable
	}
	if err := page.Validate(r.config.MaxRasterPixels, r.config.MaxRasterBytes); err != nil {
		return knowledgelayout.DetectionResult{}, err
	}
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	case <-ctx.Done():
		return knowledgelayout.DetectionResult{}, ctx.Err()
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return knowledgelayout.DetectionResult{}, knowledgelayout.ErrRouterUnavailable
	}
	executionCtx, cancel := context.WithTimeout(ctx, r.config.InferenceTimeout)
	defer cancel()
	imageTensor, scaleFactor, err := preprocess(page, r.manifest)
	if err != nil {
		return knowledgelayout.DetectionResult{}, err
	}
	rows, counts, err := r.engine.Run(executionCtx, imageTensor, scaleFactor)
	if err != nil {
		if executionCtx.Err() != nil {
			return knowledgelayout.DetectionResult{}, executionCtx.Err()
		}
		return knowledgelayout.DetectionResult{}, fmt.Errorf("%w: %v", knowledgelayout.ErrRouterUnavailable, err)
	}
	regions, err := r.decodeRegions(page, rows, counts)
	if err != nil {
		return knowledgelayout.DetectionResult{}, err
	}
	return knowledgelayout.DetectionResult{Regions: regions, Model: r.trace}, nil
}

func (r *Router) decodeRegions(page knowledgelayout.RasterPage, rows []float32, counts []int32) ([]knowledgelayout.DetectedRegion, error) {
	if len(counts) != 1 || counts[0] < 0 || len(rows)%6 != 0 || int(counts[0]) > len(rows)/6 || int(counts[0]) > r.config.MaxRegions {
		return nil, errors.New("ONNX layout model returned an invalid output shape")
	}
	regions := make([]knowledgelayout.DetectedRegion, 0, counts[0])
	for index := 0; index < int(counts[0]); index++ {
		row := rows[index*6 : index*6+6]
		for _, value := range row {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return nil, errors.New("ONNX layout model returned a non-finite value")
			}
		}
		classID := int(math.Round(float64(row[0])))
		if math.Abs(float64(row[0])-float64(classID)) > 0.0001 {
			return nil, errors.New("ONNX layout model returned a non-integral class id")
		}
		regionType, err := r.manifest.regionType(classID)
		if err != nil {
			return nil, err
		}
		confidence := float64(row[1])
		if confidence < r.manifest.Postprocess.ScoreThreshold {
			continue
		}
		box := knowledgelayout.BoundingBox{
			Left:   clamp(float64(row[2])/float64(page.Width), 0, 1),
			Top:    clamp(float64(row[3])/float64(page.Height), 0, 1),
			Right:  clamp(float64(row[4])/float64(page.Width), 0, 1),
			Bottom: clamp(float64(row[5])/float64(page.Height), 0, 1),
		}
		region := knowledgelayout.DetectedRegion{Type: regionType, Box: box, Confidence: confidence}
		if err := region.Validate(); err != nil {
			return nil, errors.New("ONNX layout model returned an invalid region")
		}
		regions = append(regions, region)
	}
	return r.suppressDecorativePictureDuplicates(regions), nil
}

// suppressDecorativePictureDuplicates resolves one narrow cross-label conflict:
// a small decorative mark and a lower-confidence picture occupying effectively
// the same box. Larger or unmatched pictures remain available to the VLM.
func (r *Router) suppressDecorativePictureDuplicates(regions []knowledgelayout.DetectedRegion) []knowledgelayout.DetectedRegion {
	if !r.config.SuppressDecorativePictureDuplicates || len(regions) < 2 {
		return regions
	}
	drop := make([]bool, len(regions))
	for pictureIndex, picture := range regions {
		if picture.Type != knowledgelayout.RegionPicture || boxArea(picture.Box) > r.config.DecorativePictureMaxAreaRatio {
			continue
		}
		for _, decorative := range regions {
			if decorative.Type != knowledgelayout.RegionDecorative ||
				decorative.Confidence-picture.Confidence < r.config.DecorativePictureMinConfidenceMargin ||
				boxIoU(picture.Box, decorative.Box) < r.config.DecorativePictureMinIoU {
				continue
			}
			drop[pictureIndex] = true
			break
		}
	}
	filtered := make([]knowledgelayout.DetectedRegion, 0, len(regions))
	for index, region := range regions {
		if !drop[index] {
			filtered = append(filtered, region)
		}
	}
	return filtered
}

func boxArea(box knowledgelayout.BoundingBox) float64 {
	return (box.Right - box.Left) * (box.Bottom - box.Top)
}

func boxIoU(left, right knowledgelayout.BoundingBox) float64 {
	intersectionWidth := math.Max(0, math.Min(left.Right, right.Right)-math.Max(left.Left, right.Left))
	intersectionHeight := math.Max(0, math.Min(left.Bottom, right.Bottom)-math.Max(left.Top, right.Top))
	intersection := intersectionWidth * intersectionHeight
	union := boxArea(left) + boxArea(right) - intersection
	if union <= 0 {
		return 0
	}
	return intersection / union
}

func clamp(value, minimum, maximum float64) float64 {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func (r *Router) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return r.closeErr
	}
	r.closed = true
	r.closeErr = r.engine.Close()
	return r.closeErr
}
