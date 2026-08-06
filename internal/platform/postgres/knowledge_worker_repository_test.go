package postgres

import (
	"testing"

	"github.com/chitandabb/GoAgent/internal/knowledge"
)

func TestDocumentVersionStatusForStage(t *testing.T) {
	tests := []struct {
		name  string
		stage knowledge.IngestionStage
		want  string
	}{
		{name: "scanning", stage: knowledge.IngestionStageScanning, want: "scanning"},
		{name: "parsing", stage: knowledge.IngestionStageParsing, want: "parsing"},
		{name: "chunking", stage: knowledge.IngestionStageChunking, want: "chunking"},
		{name: "indexing", stage: knowledge.IngestionStageIndexing, want: "indexing"},
		{name: "publishing remains visible", stage: knowledge.IngestionStagePublishing, want: "publishing"},
		{name: "uploaded defaults to processing", stage: knowledge.IngestionStageUploaded, want: "processing"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := documentVersionStatusForStage(test.stage); got != test.want {
				t.Fatalf("documentVersionStatusForStage(%q) = %q, want %q", test.stage, got, test.want)
			}
		})
	}
}
