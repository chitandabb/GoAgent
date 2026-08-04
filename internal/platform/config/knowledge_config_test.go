package config

import "testing"

func TestKnowledgeConfigValidate(t *testing.T) {
	valid := KnowledgeConfig{
		PipelineVersion: "ingestion-v1", MaxAttempts: 3, MaxUploadBytes: 50 * 1024 * 1024,
		ChunkMaxRunes: 700, ChunkOverlapRunes: 80,
		ParserMaxDocumentUnits: 500, ParserMaxArchiveEntries: 4096,
		ParserMaxExpandedBytes: 256 * 1024 * 1024, ParserMaxXMLBytes: 32 * 1024 * 1024,
		ParserMaxExtractedRunes: 2_000_000, ParserMaxSpreadsheetRows: 10_000,
		ParserMaxSpreadsheetColumns: 512,
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
