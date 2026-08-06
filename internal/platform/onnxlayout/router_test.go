package onnxlayout

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledgelayout"
)

type fakeEngine struct {
	run        func(context.Context, []float32, []float32) ([]float32, []int32, error)
	closeCalls atomic.Int32
}

func (e *fakeEngine) Run(ctx context.Context, image, scale []float32) ([]float32, []int32, error) {
	return e.run(ctx, image, scale)
}

func (e *fakeEngine) Close() error {
	e.closeCalls.Add(1)
	return nil
}

func TestLoadManifestMapsEveryUpstreamLabel(t *testing.T) {
	manifest := testManifest(t)
	if len(manifest.Labels) != 23 || len(manifest.regionTypes) != len(manifest.Labels) {
		t.Fatalf("labels = %d, region types = %d", len(manifest.Labels), len(manifest.regionTypes))
	}
	wants := map[int]knowledgelayout.RegionType{
		0: knowledgelayout.RegionText, 6: knowledgelayout.RegionCaption,
		7: knowledgelayout.RegionFormula, 8: knowledgelayout.RegionTable,
		18: knowledgelayout.RegionPicture, 20: knowledgelayout.RegionDecorative,
	}
	for classID, want := range wants {
		got, err := manifest.regionType(classID)
		if err != nil || got != want {
			t.Fatalf("regionType(%d) = %q, %v; want %q", classID, got, err, want)
		}
	}
}

func TestRouterDetectPreprocessesAndMapsModelOutput(t *testing.T) {
	manifest := testManifest(t)
	page := testRaster(t, 100, 200)
	engine := &fakeEngine{run: func(_ context.Context, image, scale []float32) ([]float32, []int32, error) {
		if len(image) != 3*640*640 {
			t.Fatalf("image tensor length = %d", len(image))
		}
		if len(scale) != 2 || math.Abs(float64(scale[0]-3.2)) > 0.0001 ||
			math.Abs(float64(scale[1]-6.4)) > 0.0001 {
			t.Fatalf("scale factor = %v", scale)
		}
		return []float32{
			8, 0.91, 10, 20, 80, 180,
			20, 0.72, 0, 0, 100, 20,
		}, []int32{2}, nil
	}}
	router := newRouter(testConfig(manifest), manifest, engine)

	result, err := router.Detect(context.Background(), page)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(result.Regions) != 2 || result.Regions[0].Type != knowledgelayout.RegionTable ||
		result.Regions[1].Type != knowledgelayout.RegionDecorative {
		t.Fatalf("regions = %+v", result.Regions)
	}
	if got := result.Regions[0].Box; math.Abs(got.Left-0.1) > 0.0001 ||
		math.Abs(got.Top-0.1) > 0.0001 || math.Abs(got.Right-0.8) > 0.0001 ||
		math.Abs(got.Bottom-0.9) > 0.0001 {
		t.Fatalf("normalized box = %+v", got)
	}
	if err := result.Validate(256); err != nil {
		t.Fatalf("result validation: %v", err)
	}
}

func TestRouterRejectsMalformedModelOutput(t *testing.T) {
	manifest := testManifest(t)
	engine := &fakeEngine{run: func(context.Context, []float32, []float32) ([]float32, []int32, error) {
		return []float32{8, 0.9, 0, 0, 10}, []int32{1}, nil
	}}
	router := newRouter(testConfig(manifest), manifest, engine)
	if _, err := router.Detect(context.Background(), testRaster(t, 10, 10)); err == nil {
		t.Fatal("Detect accepted malformed model output")
	}
}

func TestRouterSuppressesOnlySmallDecorativePictureDuplicates(t *testing.T) {
	manifest := testManifest(t)
	config := testConfig(manifest)
	router := newRouter(config, manifest, &fakeEngine{})
	regions := []knowledgelayout.DetectedRegion{
		{Type: knowledgelayout.RegionDecorative, Confidence: 0.61, Box: knowledgelayout.BoundingBox{Left: 0.05, Top: 0.05, Right: 0.12, Bottom: 0.12}},
		{Type: knowledgelayout.RegionPicture, Confidence: 0.35, Box: knowledgelayout.BoundingBox{Left: 0.051, Top: 0.051, Right: 0.121, Bottom: 0.121}},
		{Type: knowledgelayout.RegionPicture, Confidence: 0.42, Box: knowledgelayout.BoundingBox{Left: 0.2, Top: 0.2, Right: 0.8, Bottom: 0.8}},
	}
	filtered := router.suppressDecorativePictureDuplicates(regions)
	if len(filtered) != 2 || filtered[0].Type != knowledgelayout.RegionDecorative ||
		filtered[1].Type != knowledgelayout.RegionPicture || filtered[1].Confidence != 0.42 {
		t.Fatalf("filtered regions = %+v", filtered)
	}
	config.SuppressDecorativePictureDuplicates = false
	router = newRouter(config, manifest, &fakeEngine{})
	if got := router.suppressDecorativePictureDuplicates(regions); len(got) != len(regions) {
		t.Fatalf("disabled arbitration returned %d regions", len(got))
	}
}

func TestRouterHonorsInferenceTimeoutAndClose(t *testing.T) {
	manifest := testManifest(t)
	config := testConfig(manifest)
	config.InferenceTimeout = 10 * time.Millisecond
	engine := &fakeEngine{run: func(ctx context.Context, _ []float32, _ []float32) ([]float32, []int32, error) {
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}}
	router := newRouter(config, manifest, engine)
	_, err := router.Detect(context.Background(), testRaster(t, 10, 10))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Detect error = %v", err)
	}
	if err := router.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := router.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if engine.closeCalls.Load() != 1 {
		t.Fatalf("engine close calls = %d", engine.closeCalls.Load())
	}
	if _, err := router.Detect(context.Background(), testRaster(t, 10, 10)); !errors.Is(err, knowledgelayout.ErrRouterUnavailable) {
		t.Fatalf("Detect after close error = %v", err)
	}
}

func TestVerifyFileSHA256(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model.onnx")
	content := []byte("fixed-model")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	if err := verifyFileSHA256(path, hex.EncodeToString(digest[:])); err != nil {
		t.Fatalf("verifyFileSHA256: %v", err)
	}
	if err := verifyFileSHA256(path, string(make([]byte, 64))); err == nil {
		t.Fatal("verifyFileSHA256 accepted the wrong digest")
	}
}

func TestVerifyModelFileRejectsWrongByteLength(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model.onnx")
	content := []byte("fixed-model")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	if err := verifyModelFile(path, int64(len(content)), hex.EncodeToString(digest[:])); err != nil {
		t.Fatalf("verifyModelFile: %v", err)
	}
	if err := verifyModelFile(path, int64(len(content)+1), hex.EncodeToString(digest[:])); err == nil {
		t.Fatal("verifyModelFile accepted the wrong byte length")
	}
}

func TestORTIntegration(t *testing.T) {
	runtimePath := os.Getenv("MESGUARD_TEST_ONNX_RUNTIME_LIBRARY")
	modelPath := os.Getenv("MESGUARD_TEST_LAYOUT_MODEL")
	if runtimePath == "" || modelPath == "" {
		t.Skip("set MESGUARD_TEST_ONNX_RUNTIME_LIBRARY and MESGUARD_TEST_LAYOUT_MODEL")
	}
	manifest := testManifest(t)
	config := testConfig(manifest)
	config.RuntimeLibraryPath = runtimePath
	config.ModelPath = modelPath
	router, err := New(config)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer router.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	page := testRaster(t, 640, 640)
	expectTables := false
	if samplePath := os.Getenv("MESGUARD_TEST_LAYOUT_IMAGE"); samplePath != "" {
		content, readErr := os.ReadFile(samplePath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		imageConfig, format, decodeErr := image.DecodeConfig(bytes.NewReader(content))
		if decodeErr != nil || format != "jpeg" {
			t.Fatalf("decode sample: format=%q err=%v", format, decodeErr)
		}
		page = knowledgelayout.RasterPage{
			MediaType: "image/jpeg", Width: imageConfig.Width, Height: imageConfig.Height,
			Content: content,
		}
		expectTables = true
	}
	result, err := router.Detect(ctx, page)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if err := result.Validate(config.MaxRegions); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	counts := make(map[knowledgelayout.RegionType]int)
	for _, region := range result.Regions {
		counts[region.Type]++
	}
	if expectTables && (counts[knowledgelayout.RegionTable] < 2 || counts[knowledgelayout.RegionCaption] < 2) {
		t.Fatalf("semantic fixture region counts = %v", counts)
	}
	t.Logf("real ONNX inference returned %d regions: %v", len(result.Regions), counts)
}

func testManifest(t *testing.T) Manifest {
	t.Helper()
	path := filepath.Join("..", "..", "..", "config", "models", "pp-doclayout-m.json")
	manifest, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	return manifest
}

func testConfig(manifest Manifest) Config {
	return Config{
		RuntimeLibraryPath: "runtime/onnxruntime.dll", ModelPath: "models/pp-doclayout-m.onnx",
		ModelSHA256:  manifest.Conversion.SHA256,
		ManifestPath: filepath.Join("..", "..", "..", "config", "models", "pp-doclayout-m.json"),
		Provider:     "onnxruntime", ModelName: manifest.Name, ModelVersion: manifest.Source.Revision,
		PreprocessVersion: manifest.Preprocess.Version, PostprocessVersion: manifest.Postprocess.Version,
		InputWidth: manifest.Preprocess.InputWidth, InputHeight: manifest.Preprocess.InputHeight,
		IntraOpThreads: 2, InterOpThreads: 1, InferenceTimeout: 5 * time.Second,
		MaxConcurrentPages: 1, MaxRegions: 256, MaxRasterPixels: 20_000_000,
		MaxRasterBytes:                      16 * 1024 * 1024,
		SuppressDecorativePictureDuplicates: true, DecorativePictureMinIoU: 0.85,
		DecorativePictureMaxAreaRatio: 0.02, DecorativePictureMinConfidenceMargin: 0.15,
	}
}

func testRaster(t *testing.T, width, height int) knowledgelayout.RasterPage {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	return knowledgelayout.RasterPage{
		MediaType: "image/png", Width: width, Height: height, Content: buffer.Bytes(),
	}
}
