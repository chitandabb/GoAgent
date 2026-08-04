// Package knowledgeingestion implements the deterministic ingestion pipeline
// behind the generic knowledgeworker.Executor boundary.
package knowledgeingestion

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
	"github.com/chitandabb/GoAgent/internal/knowledgeworker"
	"github.com/chitandabb/GoAgent/internal/objectstore"
	"github.com/google/uuid"
)

const elementArtifactSchemaVersion = 1

type Config struct {
	MaxSourceBytes   int64
	MaxArtifactBytes int64
	ChunkOptions     knowledge.TextChunkOptions
	Clock            func() time.Time
	NewID            func() uuid.UUID
}

type Parser interface {
	Parse(context.Context, knowledgeparser.Input) (knowledgeparser.Result, error)
}

type Executor struct {
	store            objectstore.Store
	parser           Parser
	maxSourceBytes   int64
	maxArtifactBytes int64
	chunkOptions     knowledge.TextChunkOptions
	clock            func() time.Time
	newID            func() uuid.UUID
}

func NewExecutor(store objectstore.Store, parser Parser, cfg Config) (*Executor, error) {
	if store == nil || parser == nil {
		return nil, errors.New("knowledge ingestion executor dependencies are nil")
	}
	if cfg.MaxSourceBytes < 1 || cfg.MaxArtifactBytes < 1 {
		return nil, errors.New("knowledge ingestion executor byte limits must be positive")
	}
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}
	if cfg.NewID == nil {
		cfg.NewID = uuid.New
	}
	return &Executor{
		store: store, parser: parser, maxSourceBytes: cfg.MaxSourceBytes,
		maxArtifactBytes: cfg.MaxArtifactBytes, chunkOptions: cfg.ChunkOptions,
		clock: cfg.Clock, newID: cfg.NewID,
	}, nil
}

func (e *Executor) Execute(
	ctx context.Context,
	task knowledgeworker.Task,
	report func(context.Context, knowledgeworker.CheckpointUpdate) error,
) (knowledgeworker.ExecutionResult, error) {
	if e == nil || e.store == nil || e.parser == nil || report == nil {
		return knowledgeworker.ExecutionResult{}, errors.New("knowledge ingestion executor is unavailable")
	}
	if task.Source.SizeBytes > e.maxSourceBytes {
		return knowledgeworker.ExecutionResult{}, permanentError("source exceeds configured parser limit")
	}
	if err := report(ctx, checkpoint(knowledge.IngestionStageScanning, 10, map[string]any{
		"sourceSizeBytes": task.Source.SizeBytes,
	})); err != nil {
		return knowledgeworker.ExecutionResult{}, err
	}

	content, err := e.readVerifiedSource(ctx, task.Source)
	if err != nil {
		return knowledgeworker.ExecutionResult{}, err
	}
	if err := report(ctx, checkpoint(knowledge.IngestionStageParsing, 30, map[string]any{
		"sourceSha256": task.Source.SHA256, "sourceVerified": true,
	})); err != nil {
		return knowledgeworker.ExecutionResult{}, err
	}
	parsed, err := e.parser.Parse(ctx, knowledgeparser.Input{
		MediaType: task.Source.MediaType, OriginalName: task.Source.OriginalName, Content: content,
	})
	if err != nil {
		if errors.Is(err, knowledgeparser.ErrUnsupportedMediaType) || errors.Is(err, knowledgeparser.ErrInvalidContent) ||
			errors.Is(err, knowledgeparser.ErrResourceLimit) {
			return knowledgeworker.ExecutionResult{}, permanentError(err.Error())
		}
		return knowledgeworker.ExecutionResult{}, err
	}
	if err := report(ctx, checkpoint(knowledge.IngestionStageChunking, 60, map[string]any{
		"elementCount": len(parsed.Elements), "parserVersion": parsed.ParserVersion,
	})); err != nil {
		return knowledgeworker.ExecutionResult{}, err
	}
	chunks, err := knowledge.ChunkElements(parsed.Elements, e.chunkOptions)
	if err != nil {
		return knowledgeworker.ExecutionResult{}, permanentError(err.Error())
	}

	artifactBytes, parserMetadata, err := buildElementArtifact(task, parsed, len(chunks))
	if err != nil {
		return knowledgeworker.ExecutionResult{}, permanentError(err.Error())
	}
	if int64(len(artifactBytes)) > e.maxArtifactBytes {
		return knowledgeworker.ExecutionResult{}, permanentError("element artifact exceeds configured object limit")
	}
	artifactKey, err := objectstore.NewObjectKey(
		objectstore.BucketKnowledgeArtifacts, e.newID(), e.clock().UTC(),
	)
	if err != nil {
		return knowledgeworker.ExecutionResult{}, err
	}
	artifact, err := e.store.Put(ctx, objectstore.PutInput{
		Bucket: objectstore.BucketKnowledgeArtifacts, ObjectKey: artifactKey,
		Content: bytes.NewReader(artifactBytes), SizeBytes: int64(len(artifactBytes)),
		MediaType: "application/json", OriginalName: task.Source.OriginalName + ".elements.json",
	})
	if err != nil {
		return knowledgeworker.ExecutionResult{}, fmt.Errorf("store element artifact: %w", err)
	}
	if err := report(ctx, checkpoint(knowledge.IngestionStageIndexing, 85, map[string]any{
		"artifactSha256": artifact.SHA256, "chunkCount": len(chunks), "elementCount": len(parsed.Elements),
	})); err != nil {
		e.cleanupArtifact(artifact)
		return knowledgeworker.ExecutionResult{}, err
	}
	finalCheckpoint, err := json.Marshal(map[string]any{
		"artifactSha256": artifact.SHA256, "chunkCount": len(chunks), "elementCount": len(parsed.Elements),
	})
	if err != nil {
		e.cleanupArtifact(artifact)
		return knowledgeworker.ExecutionResult{}, err
	}
	return knowledgeworker.ExecutionResult{
		ParserVersion: parsed.ParserVersion, ParserMetadata: parserMetadata,
		Checkpoint: finalCheckpoint, Artifact: artifact, Chunks: chunks,
	}, nil
}

func (e *Executor) readVerifiedSource(ctx context.Context, ref objectstore.ObjectRef) ([]byte, error) {
	if err := ref.Validate(); err != nil || ref.Bucket != objectstore.BucketKnowledgeSources {
		return nil, permanentError("source reference is invalid")
	}
	read, err := e.store.Get(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("read knowledge source: %w", err)
	}
	defer read.Content.Close()
	content, err := io.ReadAll(io.LimitReader(read.Content, e.maxSourceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read knowledge source body: %w", err)
	}
	if int64(len(content)) > e.maxSourceBytes || int64(len(content)) != ref.SizeBytes {
		return nil, permanentError("source size does not match immutable reference")
	}
	digest := sha256.Sum256(content)
	if hex.EncodeToString(digest[:]) != strings.ToLower(ref.SHA256) {
		return nil, permanentError("source sha256 does not match immutable reference")
	}
	return content, nil
}

type elementArtifact struct {
	SchemaVersion     int               `json:"schemaVersion"`
	DocumentVersionID string            `json:"documentVersionId"`
	Source            artifactSource    `json:"source"`
	ParserVersion     string            `json:"parserVersion"`
	Elements          []artifactElement `json:"elements"`
}

type artifactSource struct {
	MediaType    string `json:"mediaType"`
	OriginalName string `json:"originalName"`
	SizeBytes    int64  `json:"sizeBytes"`
	SHA256       string `json:"sha256"`
}

type artifactElement struct {
	Index       int             `json:"index"`
	PageNumber  *int            `json:"pageNumber,omitempty"`
	ElementType string          `json:"elementType"`
	SectionPath []string        `json:"sectionPath"`
	ContentText string          `json:"contentText"`
	Metadata    json.RawMessage `json:"metadata"`
}

func buildElementArtifact(
	task knowledgeworker.Task,
	parsed knowledgeparser.Result,
	chunkCount int,
) ([]byte, json.RawMessage, error) {
	elements := make([]artifactElement, 0, len(parsed.Elements))
	for _, item := range parsed.Elements {
		metadata := item.Metadata
		if len(metadata) == 0 {
			metadata = json.RawMessage(`{}`)
		}
		elements = append(elements, artifactElement{
			Index: item.Index, PageNumber: item.PageNumber, ElementType: string(item.ElementType),
			SectionPath: append([]string(nil), item.SectionPath...), ContentText: item.ContentText,
			Metadata: append(json.RawMessage(nil), metadata...),
		})
	}
	artifactBytes, err := json.Marshal(elementArtifact{
		SchemaVersion: elementArtifactSchemaVersion, DocumentVersionID: task.DocumentVersionID.String(),
		Source: artifactSource{
			MediaType: task.Source.MediaType, OriginalName: task.Source.OriginalName,
			SizeBytes: task.Source.SizeBytes, SHA256: task.Source.SHA256,
		},
		ParserVersion: parsed.ParserVersion, Elements: elements,
	})
	if err != nil {
		return nil, nil, err
	}
	var metadata map[string]any
	if err := json.Unmarshal(parsed.Metadata, &metadata); err != nil {
		return nil, nil, err
	}
	metadata["artifactSchemaVersion"] = elementArtifactSchemaVersion
	metadata["chunkCount"] = chunkCount
	metadataBytes, err := json.Marshal(metadata)
	return artifactBytes, metadataBytes, err
}

func checkpoint(stage knowledge.IngestionStage, progress int, values map[string]any) knowledgeworker.CheckpointUpdate {
	raw, err := json.Marshal(values)
	if err != nil {
		raw = json.RawMessage(`{}`)
	}
	return knowledgeworker.CheckpointUpdate{Stage: stage, ProgressPercent: progress, Checkpoint: raw}
}

func permanentError(message string) error {
	return errors.Join(knowledgeworker.ErrPermanentInput, errors.New(message))
}

func (e *Executor) cleanupArtifact(ref objectstore.ObjectRef) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = e.store.Remove(ctx, ref)
}
