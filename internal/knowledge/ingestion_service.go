package knowledge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/objectstore"
	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/google/uuid"
)

type QueueSourceInput struct {
	DocumentID           uuid.UUID
	CreatedBy            uuid.UUID
	CorrelationID        uuid.UUID
	Content              io.Reader
	SizeBytes            int64
	MediaType            string
	OriginalName         string
	PipelineVersion      string
	MaxAttempts          int
	IdempotencyKey       string
	RequestFingerprint   string
	ExpectedSourceSHA256 string
	NewDocument          *CreateDocumentInput
}

var (
	ErrObjectStoreUnavailable = errors.New("knowledge object store is unavailable")
	ErrSourceChanged          = errors.New("knowledge source changed during upload")
)

func (i QueueSourceInput) Validate() error {
	if i.DocumentID == uuid.Nil || i.CreatedBy == uuid.Nil || i.CorrelationID == uuid.Nil {
		return errors.New("knowledge source document, creator and correlation ids are required")
	}
	if i.Content == nil || i.SizeBytes < 0 {
		return errors.New("knowledge source content and non-negative size are required")
	}
	if strings.TrimSpace(i.MediaType) == "" || i.MediaType != strings.TrimSpace(i.MediaType) {
		return errors.New("knowledge source media type is required and trimmed")
	}
	if strings.TrimSpace(i.OriginalName) == "" || i.OriginalName != strings.TrimSpace(i.OriginalName) || len([]rune(i.OriginalName)) > 512 {
		return errors.New("knowledge source original name is required, trimmed and bounded")
	}
	if strings.TrimSpace(i.PipelineVersion) == "" || i.PipelineVersion != strings.TrimSpace(i.PipelineVersion) {
		return errors.New("knowledge source pipeline version is required and trimmed")
	}
	if i.MaxAttempts < 1 || i.MaxAttempts > 10 {
		return errors.New("knowledge source max attempts must be between 1 and 10")
	}
	if _, err := uuid.Parse(strings.TrimSpace(i.IdempotencyKey)); err != nil {
		return errors.New("knowledge source idempotency key must be a UUID")
	}
	if !validSHA256Hex(i.RequestFingerprint) || !validSHA256Hex(i.ExpectedSourceSHA256) {
		return errors.New("knowledge source request or content fingerprint is invalid")
	}
	if i.NewDocument != nil {
		if err := i.NewDocument.Validate(); err != nil {
			return err
		}
		if i.NewDocument.ID != i.DocumentID || i.NewDocument.CreatedBy != i.CreatedBy {
			return errors.New("knowledge source new document identity does not match the request")
		}
	}
	return nil
}

type IngestionService struct {
	store      objectstore.Store
	repository IngestionRepository
	clock      func() time.Time
}

func NewIngestionService(store objectstore.Store, repository IngestionRepository) (*IngestionService, error) {
	if store == nil || repository == nil {
		return nil, errors.New("knowledge ingestion service dependencies are required")
	}
	return &IngestionService{
		store: store, repository: repository,
		clock: func() time.Time { return time.Now().UTC() },
	}, nil
}

func (s *IngestionService) QueueSource(ctx context.Context, input QueueSourceInput) (QueueVersionResult, error) {
	if s == nil || s.store == nil || s.repository == nil {
		return QueueVersionResult{}, errors.New("knowledge ingestion service is unavailable")
	}
	if err := input.Validate(); err != nil {
		return QueueVersionResult{}, err
	}
	previous, previousFingerprint, err := s.repository.FindQueuedVersionByIdempotency(
		ctx, input.CreatedBy, input.IdempotencyKey,
	)
	if err == nil {
		if previousFingerprint != input.RequestFingerprint {
			return QueueVersionResult{}, ErrIdempotencyConflict
		}
		previous.Replayed = true
		return previous, nil
	}
	if !errors.Is(err, repository.ErrNotFound) {
		return QueueVersionResult{}, fmt.Errorf("find knowledge ingestion idempotency key: %w", err)
	}
	versionID, err := uuid.NewV7()
	if err != nil {
		return QueueVersionResult{}, fmt.Errorf("generate knowledge version id: %w", err)
	}
	taskID, err := uuid.NewV7()
	if err != nil {
		return QueueVersionResult{}, fmt.Errorf("generate knowledge ingestion task id: %w", err)
	}
	outboxEventID, err := uuid.NewV7()
	if err != nil {
		return QueueVersionResult{}, fmt.Errorf("generate knowledge ingestion outbox id: %w", err)
	}
	createdAt := s.clock().UTC()
	objectKey, err := objectstore.NewObjectKey(objectstore.BucketKnowledgeSources, versionID, createdAt)
	if err != nil {
		return QueueVersionResult{}, err
	}
	ref, err := s.store.Put(ctx, objectstore.PutInput{
		Bucket: objectstore.BucketKnowledgeSources, ObjectKey: objectKey,
		Content: input.Content, SizeBytes: input.SizeBytes,
		MediaType: input.MediaType, OriginalName: input.OriginalName,
	})
	if err != nil {
		return QueueVersionResult{}, errors.Join(ErrObjectStoreUnavailable, fmt.Errorf("store knowledge source: %w", err))
	}
	if ref.SHA256 != input.ExpectedSourceSHA256 {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		cleanupErr := s.store.Remove(cleanupCtx, ref)
		cancel()
		mismatchErr := ErrSourceChanged
		if cleanupErr != nil {
			return QueueVersionResult{}, errors.Join(mismatchErr, fmt.Errorf("remove changed knowledge source: %w", cleanupErr))
		}
		return QueueVersionResult{}, mismatchErr
	}
	queued, queueErr := s.repository.QueueVersion(ctx, QueueVersionInput{
		VersionID: versionID, TaskID: taskID, OutboxEventID: outboxEventID,
		CorrelationID: input.CorrelationID, DocumentID: input.DocumentID,
		CreatedBy: input.CreatedBy, Source: ref, PipelineVersion: input.PipelineVersion,
		MaxAttempts: input.MaxAttempts, IdempotencyKey: input.IdempotencyKey,
		RequestFingerprint: input.RequestFingerprint, NewDocument: input.NewDocument,
		CreatedAt: createdAt,
	})
	if queueErr == nil {
		return queued, nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	cleanupErr := s.store.Remove(cleanupCtx, ref)
	cancel()
	if cleanupErr != nil {
		return QueueVersionResult{}, errors.Join(
			fmt.Errorf("queue knowledge source: %w", queueErr),
			fmt.Errorf("remove unreferenced knowledge source version: %w", cleanupErr),
		)
	}
	if errors.Is(queueErr, repository.ErrConflict) {
		previous, previousFingerprint, lookupErr := s.repository.FindQueuedVersionByIdempotency(
			ctx, input.CreatedBy, input.IdempotencyKey,
		)
		if lookupErr == nil {
			if previousFingerprint != input.RequestFingerprint {
				return QueueVersionResult{}, ErrIdempotencyConflict
			}
			previous.Replayed = true
			return previous, nil
		}
	}
	return QueueVersionResult{}, fmt.Errorf("queue knowledge source: %w", queueErr)
}
