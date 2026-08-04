package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/objectstore"
	"github.com/google/uuid"
)

type Scope string

const (
	ScopeGlobal   Scope = "global"
	ScopePersonal Scope = "personal"
)

type ElementType string

const (
	ElementText             ElementType = "text"
	ElementTable            ElementType = "table"
	ElementOCRText          ElementType = "ocr_text"
	ElementImageDescription ElementType = "image_description"
)

type Document struct {
	ID          uuid.UUID
	Scope       Scope
	OwnerUserID *uuid.UUID
	Title       string
	CreatedBy   uuid.UUID
	CreatedAt   time.Time
}

type CreateDocumentInput struct {
	ID          uuid.UUID
	Scope       Scope
	OwnerUserID *uuid.UUID
	Title       string
	CreatedBy   uuid.UUID
}

func (i CreateDocumentInput) Validate() error {
	if i.ID == uuid.Nil || i.CreatedBy == uuid.Nil {
		return errors.New("knowledge document id and creator are required")
	}
	if strings.TrimSpace(i.Title) == "" || i.Title != strings.TrimSpace(i.Title) {
		return errors.New("knowledge document title must be non-empty and trimmed")
	}
	if len([]rune(i.Title)) > 512 {
		return errors.New("knowledge document title is too long")
	}
	switch i.Scope {
	case ScopeGlobal:
		if i.OwnerUserID != nil {
			return errors.New("global knowledge document cannot have an owner")
		}
	case ScopePersonal:
		if i.OwnerUserID == nil || *i.OwnerUserID == uuid.Nil {
			return errors.New("personal knowledge document owner is required")
		}
	default:
		return errors.New("knowledge document scope is invalid")
	}
	return nil
}

type ChunkDraft struct {
	PageNumber    *int
	ElementIndex  *int
	ElementType   ElementType
	SectionPath   []string
	ContentText   string
	SearchText    string
	ContentSHA256 string
	Metadata      json.RawMessage
}

func (c ChunkDraft) Validate() error {
	if c.PageNumber != nil && *c.PageNumber < 1 {
		return errors.New("knowledge chunk page number must be positive")
	}
	if c.ElementIndex != nil && *c.ElementIndex < 0 {
		return errors.New("knowledge chunk element index must not be negative")
	}
	switch c.ElementType {
	case ElementText, ElementTable, ElementOCRText, ElementImageDescription:
	default:
		return errors.New("knowledge chunk element type is invalid")
	}
	if strings.TrimSpace(c.ContentText) == "" || c.ContentText != strings.TrimSpace(c.ContentText) {
		return errors.New("knowledge chunk content must be non-empty and trimmed")
	}
	if strings.TrimSpace(c.SearchText) == "" || c.SearchText != strings.TrimSpace(c.SearchText) {
		return errors.New("knowledge chunk search text must be non-empty and trimmed")
	}
	if !validSHA256Hex(c.ContentSHA256) {
		return errors.New("knowledge chunk sha256 is invalid")
	}
	for _, value := range c.SectionPath {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return errors.New("knowledge chunk section path must be non-empty and trimmed")
		}
	}
	if len(c.Metadata) > 0 {
		var object map[string]any
		if err := json.Unmarshal(c.Metadata, &object); err != nil || object == nil {
			return errors.New("knowledge chunk metadata must be a JSON object")
		}
	}
	return nil
}

type PublishVersionInput struct {
	ID              uuid.UUID
	DocumentID      uuid.UUID
	SourceMediaType string
	SourceSizeBytes int64
	SourceSHA256    string
	ParserVersion   string
	ParserMetadata  json.RawMessage
	CreatedBy       uuid.UUID
	Chunks          []ChunkDraft
}

func (i PublishVersionInput) Validate() error {
	if i.ID == uuid.Nil || i.DocumentID == uuid.Nil || i.CreatedBy == uuid.Nil {
		return errors.New("knowledge version id, document id and creator are required")
	}
	if strings.TrimSpace(i.SourceMediaType) == "" || i.SourceMediaType != strings.TrimSpace(i.SourceMediaType) {
		return errors.New("knowledge version media type must be non-empty and trimmed")
	}
	if i.SourceSizeBytes < 0 || !validSHA256Hex(i.SourceSHA256) {
		return errors.New("knowledge version source metadata is invalid")
	}
	if strings.TrimSpace(i.ParserVersion) == "" || i.ParserVersion != strings.TrimSpace(i.ParserVersion) {
		return errors.New("knowledge version parser version must be non-empty and trimmed")
	}
	if len(i.Chunks) == 0 || len(i.Chunks) > 10000 {
		return errors.New("knowledge version chunks are required and bounded")
	}
	if len(i.ParserMetadata) > 0 {
		var object map[string]any
		if err := json.Unmarshal(i.ParserMetadata, &object); err != nil || object == nil {
			return errors.New("knowledge version parser metadata must be a JSON object")
		}
	}
	for _, chunk := range i.Chunks {
		if err := chunk.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type DocumentVersion struct {
	ID         uuid.UUID
	DocumentID uuid.UUID
	Version    int
	CreatedAt  time.Time
}

type IngestionTaskStatus string

const (
	IngestionPending          IngestionTaskStatus = "pending"
	IngestionRunning          IngestionTaskStatus = "running"
	IngestionRetryWait        IngestionTaskStatus = "retry_wait"
	IngestionCancelRequested  IngestionTaskStatus = "cancel_requested"
	IngestionSucceeded        IngestionTaskStatus = "succeeded"
	IngestionPartialSucceeded IngestionTaskStatus = "partial_succeeded"
	IngestionFailed           IngestionTaskStatus = "failed"
	IngestionCancelled        IngestionTaskStatus = "cancelled"
)

type IngestionStage string

const (
	IngestionStageUploaded   IngestionStage = "uploaded"
	IngestionStageScanning   IngestionStage = "scanning"
	IngestionStageParsing    IngestionStage = "parsing"
	IngestionStageChunking   IngestionStage = "chunking"
	IngestionStageIndexing   IngestionStage = "indexing"
	IngestionStagePublishing IngestionStage = "publishing"
	IngestionStageCompleted  IngestionStage = "completed"
)

type QueueVersionInput struct {
	VersionID          uuid.UUID
	TaskID             uuid.UUID
	OutboxEventID      uuid.UUID
	CorrelationID      uuid.UUID
	DocumentID         uuid.UUID
	CreatedBy          uuid.UUID
	Source             objectstore.ObjectRef
	PipelineVersion    string
	MaxAttempts        int
	IdempotencyKey     string
	RequestFingerprint string
	NewDocument        *CreateDocumentInput
	CreatedAt          time.Time
}

func (i QueueVersionInput) Validate() error {
	if i.VersionID == uuid.Nil || i.TaskID == uuid.Nil || i.OutboxEventID == uuid.Nil ||
		i.CorrelationID == uuid.Nil || i.DocumentID == uuid.Nil || i.CreatedBy == uuid.Nil {
		return errors.New("knowledge ingestion ids are required")
	}
	if i.Source.Bucket != objectstore.BucketKnowledgeSources {
		return errors.New("knowledge ingestion source must use the knowledge source bucket")
	}
	if err := i.Source.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(i.PipelineVersion) == "" || i.PipelineVersion != strings.TrimSpace(i.PipelineVersion) {
		return errors.New("knowledge ingestion pipeline version is required and trimmed")
	}
	if i.MaxAttempts < 1 || i.MaxAttempts > 10 {
		return errors.New("knowledge ingestion max attempts must be between 1 and 10")
	}
	if _, err := uuid.Parse(strings.TrimSpace(i.IdempotencyKey)); err != nil {
		return errors.New("knowledge ingestion idempotency key must be a UUID")
	}
	if !validSHA256Hex(i.RequestFingerprint) {
		return errors.New("knowledge ingestion request fingerprint is invalid")
	}
	if i.NewDocument != nil {
		if err := i.NewDocument.Validate(); err != nil {
			return err
		}
		if i.NewDocument.ID != i.DocumentID || i.NewDocument.CreatedBy != i.CreatedBy {
			return errors.New("knowledge ingestion new document identity does not match the request")
		}
	}
	if i.CreatedAt.IsZero() {
		return errors.New("knowledge ingestion created time is required")
	}
	return nil
}

type IngestionTask struct {
	ID                uuid.UUID
	DocumentVersionID uuid.UUID
	Status            IngestionTaskStatus
	Stage             IngestionStage
	AttemptCount      int
	MaxAttempts       int
	CreatedAt         time.Time
}

type QueueVersionResult struct {
	Version  DocumentVersion
	Task     IngestionTask
	Replayed bool
}

var ErrIdempotencyConflict = errors.New("knowledge ingestion idempotency conflict")

type SearchResult struct {
	DocumentID        uuid.UUID
	DocumentVersionID uuid.UUID
	ChunkID           uuid.UUID
	Title             string
	Scope             Scope
	Ordinal           int
	PageNumber        *int
	ElementType       ElementType
	SectionPath       []string
	ContentText       string
	Score             float64
}

type Repository interface {
	CreateDocument(context.Context, CreateDocumentInput) (Document, error)
	PublishVersion(context.Context, PublishVersionInput) (DocumentVersion, error)
	SearchFTS(context.Context, uuid.UUID, string, int) ([]SearchResult, error)
}

type IngestionRepository interface {
	QueueVersion(context.Context, QueueVersionInput) (QueueVersionResult, error)
	FindQueuedVersionByIdempotency(context.Context, uuid.UUID, string) (QueueVersionResult, string, error)
}

func SHA256Hex(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func validSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}
