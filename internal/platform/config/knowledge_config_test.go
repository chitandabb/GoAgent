package config

import "testing"

func TestKnowledgeConfigValidate(t *testing.T) {
	valid := KnowledgeConfig{
		PipelineVersion: "ingestion-v1", MaxAttempts: 3, MaxUploadBytes: 50 * 1024 * 1024,
		ChunkMaxRunes: 700, ChunkOverlapRunes: 80,
		ParserMaxDocumentUnits: 500, ParserMaxArchiveEntries: 4096,
		ParserMaxExpandedBytes: 256 * 1024 * 1024, ParserMaxXMLBytes: 32 * 1024 * 1024,
		ParserMaxExtractedRunes: 2_000_000, ParserMaxSpreadsheetRows: 10_000,
		ParserMaxSpreadsheetColumns: 512, ParserMaxVisualAssets: 256,
		ParserMaxVisualAssetBytes: 16 * 1024 * 1024, ParserMaxTotalVisualBytes: 64 * 1024 * 1024,
		MaxVisualEnrichments: 30, MinVisualPixels: 4096,
		Retrieval: KnowledgeRetrievalConfig{
			ContextExpansionEnabled: true, ContextWindow: 1, ContextMaxRunes: 1800,
			QueryRewrite: KnowledgeQueryRewriteConfig{
				Enabled: true, PromptFile: "config/prompts/query-rewrite.md", PromptVersion: "query-rewrite-v1",
				TimeoutMillis: 10000, MaxSubqueries: 2, MaxOutputRunes: 2048,
			},
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate valid config: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*KnowledgeConfig)
	}{
		{name: "missing pipeline version", mutate: func(c *KnowledgeConfig) { c.PipelineVersion = "" }},
		{name: "invalid pipeline version", mutate: func(c *KnowledgeConfig) { c.PipelineVersion = "ingestion/v1" }},
		{name: "zero attempts", mutate: func(c *KnowledgeConfig) { c.MaxAttempts = 0 }},
		{name: "too many attempts", mutate: func(c *KnowledgeConfig) { c.MaxAttempts = 11 }},
		{name: "zero upload limit", mutate: func(c *KnowledgeConfig) { c.MaxUploadBytes = 0 }},
		{name: "upload limit too large", mutate: func(c *KnowledgeConfig) { c.MaxUploadBytes++ }},
		{name: "chunk too small", mutate: func(c *KnowledgeConfig) { c.ChunkMaxRunes = 127 }},
		{name: "negative overlap", mutate: func(c *KnowledgeConfig) { c.ChunkOverlapRunes = -1 }},
		{name: "overlap too large", mutate: func(c *KnowledgeConfig) { c.ChunkOverlapRunes = 350 }},
		{name: "zero document units", mutate: func(c *KnowledgeConfig) { c.ParserMaxDocumentUnits = 0 }},
		{name: "zero archive entries", mutate: func(c *KnowledgeConfig) { c.ParserMaxArchiveEntries = 0 }},
		{name: "expanded bytes too small", mutate: func(c *KnowledgeConfig) { c.ParserMaxExpandedBytes = 1024 }},
		{name: "XML exceeds expanded bytes", mutate: func(c *KnowledgeConfig) { c.ParserMaxXMLBytes = c.ParserMaxExpandedBytes + 1 }},
		{name: "extracted runes too small", mutate: func(c *KnowledgeConfig) { c.ParserMaxExtractedRunes = 999 }},
		{name: "zero spreadsheet rows", mutate: func(c *KnowledgeConfig) { c.ParserMaxSpreadsheetRows = 0 }},
		{name: "too many spreadsheet columns", mutate: func(c *KnowledgeConfig) { c.ParserMaxSpreadsheetColumns = 16385 }},
		{name: "zero visual assets", mutate: func(c *KnowledgeConfig) { c.ParserMaxVisualAssets = 0 }},
		{name: "visual asset exceeds expanded bytes", mutate: func(c *KnowledgeConfig) { c.ParserMaxVisualAssetBytes = c.ParserMaxExpandedBytes + 1 }},
		{name: "total visual bytes below one asset", mutate: func(c *KnowledgeConfig) { c.ParserMaxTotalVisualBytes = c.ParserMaxVisualAssetBytes - 1 }},
		{name: "zero visual enrichments", mutate: func(c *KnowledgeConfig) { c.MaxVisualEnrichments = 0 }},
		{name: "zero visual pixels", mutate: func(c *KnowledgeConfig) { c.MinVisualPixels = 0 }},
		{name: "zero context window", mutate: func(c *KnowledgeConfig) { c.Retrieval.ContextWindow = 0 }},
		{name: "context budget too small", mutate: func(c *KnowledgeConfig) { c.Retrieval.ContextMaxRunes = 127 }},
		{name: "query rewrite timeout too small", mutate: func(c *KnowledgeConfig) { c.Retrieval.QueryRewrite.TimeoutMillis = 999 }},
		{name: "query rewrite too many subqueries", mutate: func(c *KnowledgeConfig) { c.Retrieval.QueryRewrite.MaxSubqueries = 3 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate accepted invalid config")
			}
		})
	}
}

func TestKnowledgeQueryRewriteConfigLoadsPrompt(t *testing.T) {
	path := writePromptFileForTest(t, t.TempDir(), "query-rewrite.md", " strict query rewrite prompt \n")
	cfg := KnowledgeQueryRewriteConfig{
		Enabled: true, PromptFile: path, PromptVersion: "query-rewrite-v1",
		TimeoutMillis: 10000, MaxSubqueries: 2, MaxOutputRunes: 2048,
	}
	prompt, err := cfg.LoadPrompt()
	if err != nil {
		t.Fatal(err)
	}
	if prompt != "strict query rewrite prompt" {
		t.Fatalf("prompt = %q", prompt)
	}
}
