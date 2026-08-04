package knowledgeingestion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
	"github.com/chitandabb/GoAgent/internal/knowledgeworker"
	"github.com/chitandabb/GoAgent/internal/objectstore"
	"github.com/google/uuid"
)

func TestExecutorProducesArtifactAndChunksFromVerifiedMarkdown(t *testing.T) {
	content := []byte("# 连接池故障\n\n检查 max connections。\n\n| 参数 | 值 |\n| --- | --- |\n| timeout | 30s |")
	store := &memoryStore{source: content}
	router, _ := knowledgeparser.NewRouter(knowledgeparser.TextParser{})
	executor, err := NewExecutor(store, router, Config{
		MaxSourceBytes: 1024, MaxArtifactBytes: 4096,
		ChunkOptions: knowledge.TextChunkOptions{MaxRunes: 128, OverlapRunes: 16},
		Clock:        func() time.Time { return time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC) },
		NewID:        func() uuid.UUID { return uuid.MustParse("018f6bb7-6e72-7d44-9b0e-f6f8a4e5e9c0") },
	})
	if err != nil {
		t.Fatal(err)
	}
	task := executorTask(content, "text/markdown")
	var stages []knowledge.IngestionStage
	result, err := executor.Execute(context.Background(), task, func(_ context.Context, update knowledgeworker.CheckpointUpdate) error {
		stages = append(stages, update.Stage)
		return nil
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(result.Chunks) != 2 || result.Artifact.Bucket != objectstore.BucketKnowledgeArtifacts {
		t.Fatalf("result = %+v", result)
	}
	if got := stages; len(got) != 4 || got[0] != knowledge.IngestionStageScanning || got[3] != knowledge.IngestionStageIndexing {
		t.Fatalf("stages = %v", got)
	}
	var artifact elementArtifact
	if err := json.Unmarshal(store.artifact, &artifact); err != nil || len(artifact.Elements) != 2 {
		t.Fatalf("artifact=%+v err=%v", artifact, err)
	}
}

func TestExecutorRejectsIntegrityMismatchAndUnsupportedParserPermanently(t *testing.T) {
	router, _ := knowledgeparser.NewRouter(knowledgeparser.TextParser{})
	for _, test := range []struct {
		name      string
		mediaType string
		mutate    func(*knowledgeworker.Task)
	}{
		{name: "sha mismatch", mediaType: "text/plain", mutate: func(task *knowledgeworker.Task) { task.Source.SHA256 = knowledge.SHA256Hex("other") }},
		{name: "unsupported PDF", mediaType: "application/pdf", mutate: func(*knowledgeworker.Task) {}},
	} {
		t.Run(test.name, func(t *testing.T) {
			content := []byte("content")
			executor, _ := NewExecutor(&memoryStore{source: content}, router, Config{
				MaxSourceBytes: 1024, MaxArtifactBytes: 4096,
				ChunkOptions: knowledge.TextChunkOptions{MaxRunes: 128, OverlapRunes: 16},
			})
			task := executorTask(content, test.mediaType)
			test.mutate(&task)
			_, err := executor.Execute(context.Background(), task, func(context.Context, knowledgeworker.CheckpointUpdate) error { return nil })
			if !errors.Is(err, knowledgeworker.ErrPermanentInput) {
				t.Fatalf("Execute error = %v", err)
			}
		})
	}
}

func executorTask(content []byte, mediaType string) knowledgeworker.Task {
	return knowledgeworker.Task{
		ID: uuid.New(), DocumentVersionID: uuid.New(), DocumentID: uuid.New(), CreatedBy: uuid.New(),
		PipelineVersion: "ingestion-v1",
		Source: objectstore.ObjectRef{
			Bucket: objectstore.BucketKnowledgeSources, ObjectKey: "knowledge-source/object", ETag: "etag",
			SizeBytes: int64(len(content)), SHA256: knowledge.SHA256Hex(string(content)),
			MediaType: mediaType, OriginalName: "manual.md",
		},
	}
}

type memoryStore struct {
	source   []byte
	artifact []byte
}

func (s *memoryStore) Put(_ context.Context, input objectstore.PutInput) (objectstore.ObjectRef, error) {
	content, err := io.ReadAll(input.Content)
	if err != nil {
		return objectstore.ObjectRef{}, err
	}
	s.artifact = content
	return objectstore.ObjectRef{
		Bucket: input.Bucket, ObjectKey: input.ObjectKey, ETag: "artifact-etag",
		SizeBytes: int64(len(content)), SHA256: knowledge.SHA256Hex(string(content)),
		MediaType: input.MediaType, OriginalName: input.OriginalName,
	}, nil
}

func (s *memoryStore) Get(_ context.Context, ref objectstore.ObjectRef) (objectstore.ReadResult, error) {
	return objectstore.ReadResult{
		Content: io.NopCloser(bytes.NewReader(s.source)), SizeBytes: int64(len(s.source)),
		ETag: ref.ETag, MediaType: ref.MediaType,
	}, nil
}

func (*memoryStore) Remove(context.Context, objectstore.ObjectRef) error { return nil }
func (*memoryStore) Close() error                                        { return nil }
