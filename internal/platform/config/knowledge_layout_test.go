package config

import "testing"

func TestKnowledgeLayoutConfigValidate(t *testing.T) {
	valid := KnowledgeLayoutConfig{
		Enabled: true, Provider: "onnxruntime", ModelName: "PP-DocLayout-M",
		ModelVersion:       "7dbfcce3154a55776dc71ca026a4a2a8388dad8d",
		ModelPath:          "models/pp-doclayout-m.onnx",
		ModelSHA256:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ManifestPath:       "config/models/pp-doclayout-m.json",
		RuntimeLibraryPath: "runtime/onnxruntime.dll", PreprocessVersion: "pp-layout-pre-v1",
		PostprocessVersion: "pp-layout-post-v1", InputWidth: 800, InputHeight: 800,
		IntraOpThreads: 2, InterOpThreads: 1, InferenceTimeoutMillis: 5_000,
		MaxConcurrentPages: 1, RenderDPI: 144, MinNativeTextRunes: 64,
		RendererProvider: "pdfium-wasm", RendererVersion: "go-pdfium-v1.19.6",
		RendererAcquireMillis: 5_000, RendererTimeoutMillis: 30_000,
		MinNativePrintableRatio: 0.85, MinRegionConfidence: 0.65,
		SuppressDecorativePictureDuplicates: true, DecorativePictureMinIoU: 0.85,
		DecorativePictureMaxAreaRatio: 0.02, DecorativePictureMinConfidenceMargin: 0.15,
		RegionPaddingRatio: 0.01,
		MaxRegions:         256, MaxRasterPixels: 20_000_000, MaxRasterBytes: 16 * 1024 * 1024,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate valid config: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*KnowledgeLayoutConfig)
	}{
		{name: "provider", mutate: func(c *KnowledgeLayoutConfig) { c.Provider = "paddle" }},
		{name: "model name", mutate: func(c *KnowledgeLayoutConfig) { c.ModelName = "" }},
		{name: "model path", mutate: func(c *KnowledgeLayoutConfig) { c.ModelPath = "" }},
		{name: "model sha", mutate: func(c *KnowledgeLayoutConfig) { c.ModelSHA256 = "ABC" }},
		{name: "manifest path", mutate: func(c *KnowledgeLayoutConfig) { c.ManifestPath = "" }},
		{name: "runtime path", mutate: func(c *KnowledgeLayoutConfig) { c.RuntimeLibraryPath = "" }},
		{name: "input width", mutate: func(c *KnowledgeLayoutConfig) { c.InputWidth = 0 }},
		{name: "threads", mutate: func(c *KnowledgeLayoutConfig) { c.IntraOpThreads = 0 }},
		{name: "timeout", mutate: func(c *KnowledgeLayoutConfig) { c.InferenceTimeoutMillis = 0 }},
		{name: "concurrency", mutate: func(c *KnowledgeLayoutConfig) { c.MaxConcurrentPages = 0 }},
		{name: "DPI", mutate: func(c *KnowledgeLayoutConfig) { c.RenderDPI = 0 }},
		{name: "renderer provider", mutate: func(c *KnowledgeLayoutConfig) { c.RendererProvider = "poppler" }},
		{name: "renderer version", mutate: func(c *KnowledgeLayoutConfig) { c.RendererVersion = "" }},
		{name: "renderer acquire", mutate: func(c *KnowledgeLayoutConfig) { c.RendererAcquireMillis = 0 }},
		{name: "renderer timeout", mutate: func(c *KnowledgeLayoutConfig) { c.RendererTimeoutMillis = 0 }},
		{name: "native threshold", mutate: func(c *KnowledgeLayoutConfig) { c.MinNativeTextRunes = 0 }},
		{name: "printable ratio", mutate: func(c *KnowledgeLayoutConfig) { c.MinNativePrintableRatio = 0 }},
		{name: "confidence", mutate: func(c *KnowledgeLayoutConfig) { c.MinRegionConfidence = 2 }},
		{name: "decorative picture IoU", mutate: func(c *KnowledgeLayoutConfig) { c.DecorativePictureMinIoU = 0 }},
		{name: "decorative picture area", mutate: func(c *KnowledgeLayoutConfig) { c.DecorativePictureMaxAreaRatio = 2 }},
		{name: "decorative picture margin", mutate: func(c *KnowledgeLayoutConfig) { c.DecorativePictureMinConfidenceMargin = -1 }},
		{name: "padding", mutate: func(c *KnowledgeLayoutConfig) { c.RegionPaddingRatio = 0.3 }},
		{name: "regions", mutate: func(c *KnowledgeLayoutConfig) { c.MaxRegions = 0 }},
		{name: "pixels", mutate: func(c *KnowledgeLayoutConfig) { c.MaxRasterPixels = 0 }},
		{name: "bytes", mutate: func(c *KnowledgeLayoutConfig) { c.MaxRasterBytes = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := valid
			test.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate accepted invalid config")
			}
		})
	}
	if err := (KnowledgeLayoutConfig{}).Validate(); err != nil {
		t.Fatalf("disabled layout config must degrade: %v", err)
	}
}
