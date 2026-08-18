package knowledge

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrIngestionTaskStateConflict = errors.New("knowledge ingestion task state conflict")

// DefaultDocumentListPageSize / MaxDocumentListPageSize 是知识文档列表分页边界。
const (
	DefaultDocumentListPageSize = 20
	MaxDocumentListPageSize     = 100
)

// DocumentListItem 是文档列表行：文档身份 + 最新版本号 + 最新解析任务状态。
// 列表读取 global 范围（企业知识库），由管理员维护。
type DocumentListItem struct {
	DocumentID      uuid.UUID
	Title           string
	Scope           Scope
	Version         int
	TaskID          uuid.UUID
	Status          IngestionTaskStatus
	Stage           IngestionStage
	ProgressPercent int
	AttemptCount    int
	MaxAttempts     int
	CreatedAt       time.Time
}

// DocumentListPage 是文档列表的分页结果。
type DocumentListPage struct {
	Items    []DocumentListItem
	Total    int64
	Page     int
	PageSize int
}

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
	ListDocuments(context.Context, int, int) (DocumentListPage, error)
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

// ListDocuments 返回企业知识库文档分页列表（按创建时间倒序）。
// 列表与分页参数归一化后透传给仓储层。
func (s *IngestionTaskControlService) ListDocuments(ctx context.Context, page, pageSize int) (DocumentListPage, error) {
	if s == nil || s.repository == nil {
		return DocumentListPage{}, errors.New("knowledge ingestion task control repository is unavailable")
	}
	page, pageSize = normalizeDocumentListPagination(page, pageSize)
	return s.repository.ListDocuments(ctx, page, pageSize)
}

func normalizeDocumentListPagination(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = DefaultDocumentListPageSize
	} else if pageSize > MaxDocumentListPageSize {
		pageSize = MaxDocumentListPageSize
	}
	return page, pageSize
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
