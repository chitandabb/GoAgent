package knowledge

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrIngestionTaskStateConflict = errors.New("knowledge ingestion task state conflict")

type IngestionTaskDetail struct {
	ID                uuid.UUID
	DocumentVersionID uuid.UUID
	DocumentID        uuid.UUID
	Status            IngestionTaskStatus
	Stage             IngestionStage
	AttemptCount      int
	MaxAttempts       int
	ProgressPercent   int
	CancelRequestedAt *time.Time
	LastErrorCode     string
	LastErrorMessage  string
	StartedAt         *time.Time
	CompletedAt       *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type IngestionCancelResult struct {
	Task    IngestionTaskDetail
	Changed bool
}

type IngestionTaskControlRepository interface {
	FindIngestionTask(context.Context, uuid.UUID) (IngestionTaskDetail, error)
	RequestIngestionCancellation(context.Context, uuid.UUID, uuid.UUID, time.Time) (IngestionCancelResult, error)
}

type IngestionTaskControlService struct {
	repository IngestionTaskControlRepository
	clock      func() time.Time
}

func NewIngestionTaskControlService(repository IngestionTaskControlRepository) (*IngestionTaskControlService, error) {
	if repository == nil {
		return nil, errors.New("knowledge ingestion task control repository is nil")
	}
	return &IngestionTaskControlService{
		repository: repository, clock: func() time.Time { return time.Now().UTC() },
	}, nil
}

func (s *IngestionTaskControlService) Get(ctx context.Context, taskID uuid.UUID) (IngestionTaskDetail, error) {
	if s == nil || s.repository == nil || taskID == uuid.Nil {
		return IngestionTaskDetail{}, errors.New("knowledge ingestion task query is invalid")
	}
	return s.repository.FindIngestionTask(ctx, taskID)
}

func (s *IngestionTaskControlService) Cancel(
	ctx context.Context,
	taskID, requestedBy uuid.UUID,
) (IngestionCancelResult, error) {
	if s == nil || s.repository == nil || taskID == uuid.Nil || requestedBy == uuid.Nil {
		return IngestionCancelResult{}, errors.New("knowledge ingestion cancellation is invalid")
	}
	return s.repository.RequestIngestionCancellation(ctx, taskID, requestedBy, s.clock().UTC())
}

func (d IngestionTaskDetail) Validate() error {
	if d.ID == uuid.Nil || d.DocumentVersionID == uuid.Nil || d.DocumentID == uuid.Nil ||
		d.AttemptCount < 0 || d.MaxAttempts < 1 || d.AttemptCount > d.MaxAttempts ||
		d.ProgressPercent < 0 || d.ProgressPercent > 100 || d.CreatedAt.IsZero() || d.UpdatedAt.IsZero() {
		return errors.New("knowledge ingestion task detail is invalid")
	}
	switch d.Status {
	case IngestionPending, IngestionRunning, IngestionRetryWait, IngestionCancelRequested,
		IngestionSucceeded, IngestionPartialSucceeded, IngestionFailed, IngestionCancelled:
	default:
		return errors.New("knowledge ingestion task status is invalid")
	}
	switch d.Stage {
	case IngestionStageUploaded, IngestionStageScanning, IngestionStageParsing,
		IngestionStageChunking, IngestionStageIndexing, IngestionStagePublishing,
		IngestionStageCompleted:
	default:
		return errors.New("knowledge ingestion task stage is invalid")
	}
	if strings.TrimSpace(d.LastErrorCode) != d.LastErrorCode || strings.TrimSpace(d.LastErrorMessage) != d.LastErrorMessage {
		return errors.New("knowledge ingestion task error fields are not normalized")
	}
	return nil
}
