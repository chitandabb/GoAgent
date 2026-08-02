package diagnosis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/externalcase"
	"github.com/chitandabb/GoAgent/internal/repository"

	"github.com/google/uuid"
)

// TaskStatus 是 DiagnosisTask 的持久化状态。
type TaskStatus string

const (
	TaskPending         TaskStatus = "pending"
	TaskRunning         TaskStatus = "running"
	TaskCancelRequested TaskStatus = "cancel_requested"
	TaskSucceeded       TaskStatus = "succeeded"
	TaskFailed          TaskStatus = "failed"
	TaskCancelled       TaskStatus = "cancelled"
)

var (
	ErrTaskForbidden          = errors.New("diagnosis task is forbidden")
	ErrInvalidTask            = errors.New("diagnosis task is invalid")
	ErrTaskStateConflict      = errors.New("diagnosis task state conflicts with the requested operation")
	ErrSourceChanged          = errors.New("external case source has changed")
	ErrIdempotencyConflict    = errors.New("diagnosis task idempotency key conflicts")
	ErrAttachmentsUnsupported = errors.New("diagnosis task attachments are not implemented")
)

// TaskActor 是任务查询和创建所需的最小权限上下文。
type TaskActor struct {
	UserID  uuid.UUID
	IsAdmin bool
}

// TaskAttachment 是任务请求中预留的附件关联输入。
// 当前附件表和对象存储流程尚未落地，因此非空附件会被明确拒绝，不能静默丢弃。
type TaskAttachment struct {
	AttachmentID uuid.UUID
	Purpose      string
}

// CreateTaskInput 是创建异步诊断任务的业务输入。
type CreateTaskInput struct {
	ExternalCaseID            uuid.UUID
	ExpectedSourceFingerprint string
	EvidenceDataSourceIDs     []uuid.UUID
	RequestText               string
	RequestScope              map[string]any
	RequestScopeSchemaVersion int
	Attachments               []TaskAttachment
	RetryOfTaskID             *uuid.UUID
	IdempotencyKey            string
	CorrelationID             uuid.UUID
}

// CaseSnapshotRecord 是已经脱敏并序列化的工单快照，避免 Repository 直接接触 ERP 模型。
type CaseSnapshotRecord struct {
	Payload              json.RawMessage
	PayloadSchemaVersion int
	ContentHash          string
	SourceReadAt         time.Time
	RedactionStatus      string
	TruncationStatus     string
}

// CreateTaskRecord 是事务 Repository 需要写入的完整事实集合。
type CreateTaskRecord struct {
	CreatedBy                 uuid.UUID
	ExternalCaseID            uuid.UUID
	RetryOfTaskID             *uuid.UUID
	IdempotencyKey            string
	RequestFingerprint        string
	RequestText               string
	RequestScope              json.RawMessage
	RequestScopeSchemaVersion int
	EvidenceDataSourceIDs     []uuid.UUID
	Snapshot                  CaseSnapshotRecord
	CorrelationID             uuid.UUID
	CreatedAt                 time.Time
}

// DiagnosisTask 是任务查询返回的安全摘要，不包含模型 Prompt、原始 SQL 或敏感证据。
type DiagnosisTask struct {
	ID                        uuid.UUID
	CreatedBy                 uuid.UUID
	ExternalCaseID            uuid.UUID
	CaseSnapshotID            uuid.UUID
	RetryOfTaskID             *uuid.UUID
	RequestText               string
	RequestScope              map[string]any
	RequestScopeSchemaVersion int
	Status                    TaskStatus
	AttemptCount              int
	LastErrorCode             string
	LastErrorMessage          string
	StartedAt                 *time.Time
	CompletedAt               *time.Time
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
	ReportID                  *uuid.UUID
}

type TaskCreateResult struct {
	Task     DiagnosisTask
	Replayed bool
}

// TaskRepository 由 PostgreSQL 适配器实现，并保证创建时的多表事务边界。
type TaskRepository interface {
	CreateTask(ctx context.Context, input CreateTaskRecord) (TaskCreateResult, error)
	GetTask(ctx context.Context, taskID uuid.UUID) (DiagnosisTask, error)
	ListTaskEvents(ctx context.Context, taskID uuid.UUID, afterSeq int64, limit int) (TaskEventPage, error)
	CancelTask(ctx context.Context, taskID, requestedBy uuid.UUID, requestedAt time.Time) (TaskCancelResult, error)
}

// ExternalCaseReader 只暴露任务创建时需要的重新读取能力。
type ExternalCaseReader interface {
	Get(ctx context.Context, id uuid.UUID) (*externalcase.ExternalCase, error)
}

type DiagnosisTaskService struct {
	repository TaskRepository
	cases      ExternalCaseReader
	clock      func() time.Time
}

func NewDiagnosisTaskService(repository TaskRepository, cases ExternalCaseReader) (*DiagnosisTaskService, error) {
	if repository == nil || cases == nil {
		return nil, errors.New("diagnosis task dependencies are nil")
	}
	return &DiagnosisTaskService{repository: repository, cases: cases, clock: func() time.Time { return time.Now().UTC() }}, nil
}

// Create 先在 PostgreSQL 事务外重读 ERP 工单，再把快照、任务、首事件和 Outbox 一起落库。
func (s *DiagnosisTaskService) Create(ctx context.Context, actor TaskActor, input CreateTaskInput) (TaskCreateResult, error) {
	if s == nil || s.repository == nil || s.cases == nil {
		return TaskCreateResult{}, errors.New("diagnosis task service is unavailable")
	}
	input, requestScope, err := normalizeCreateTaskInput(actor, input)
	if err != nil {
		return TaskCreateResult{}, err
	}

	item, err := s.cases.Get(ctx, input.ExternalCaseID)
	if err != nil {
		return TaskCreateResult{}, err
	}
	if item == nil || item.ID != input.ExternalCaseID {
		return TaskCreateResult{}, repository.ErrNotFound
	}
	if item.SourceFingerprint != input.ExpectedSourceFingerprint {
		return TaskCreateResult{}, ErrSourceChanged
	}

	requestScopeJSON, err := json.Marshal(requestScope)
	if err != nil {
		return TaskCreateResult{}, fmt.Errorf("marshal task request scope: %w", err)
	}
	requestFingerprint, err := taskRequestFingerprint(input, requestScopeJSON)
	if err != nil {
		return TaskCreateResult{}, fmt.Errorf("fingerprint diagnosis task request: %w", err)
	}
	now := s.clock().UTC()
	snapshot, err := buildCaseSnapshot(*item, now)
	if err != nil {
		return TaskCreateResult{}, fmt.Errorf("build case snapshot: %w", err)
	}
	correlationID := input.CorrelationID
	if correlationID == uuid.Nil {
		correlationID = uuid.New()
	}
	return s.repository.CreateTask(ctx, CreateTaskRecord{
		CreatedBy:                 actor.UserID,
		ExternalCaseID:            input.ExternalCaseID,
		RetryOfTaskID:             input.RetryOfTaskID,
		IdempotencyKey:            input.IdempotencyKey,
		RequestFingerprint:        requestFingerprint,
		RequestText:               input.RequestText,
		RequestScope:              requestScopeJSON,
		RequestScopeSchemaVersion: input.RequestScopeSchemaVersion,
		EvidenceDataSourceIDs:     append([]uuid.UUID(nil), input.EvidenceDataSourceIDs...),
		Snapshot:                  snapshot,
		CorrelationID:             correlationID,
		CreatedAt:                 now,
	})
}

func (s *DiagnosisTaskService) Get(ctx context.Context, actor TaskActor, taskID uuid.UUID) (DiagnosisTask, error) {
	if s == nil || s.repository == nil {
		return DiagnosisTask{}, errors.New("diagnosis task service is unavailable")
	}
	if actor.UserID == uuid.Nil || taskID == uuid.Nil {
		return DiagnosisTask{}, ErrTaskForbidden
	}
	task, err := s.repository.GetTask(ctx, taskID)
	if err != nil {
		return DiagnosisTask{}, err
	}
	if !actor.IsAdmin && task.CreatedBy != actor.UserID {
		return DiagnosisTask{}, ErrTaskForbidden
	}
	return task, nil
}

func normalizeCreateTaskInput(actor TaskActor, input CreateTaskInput) (CreateTaskInput, map[string]any, error) {
	if actor.UserID == uuid.Nil {
		return CreateTaskInput{}, nil, ErrTaskForbidden
	}
	if input.ExternalCaseID == uuid.Nil || strings.TrimSpace(input.ExpectedSourceFingerprint) == "" {
		return CreateTaskInput{}, nil, ErrInvalidTask
	}
	if len(input.ExpectedSourceFingerprint) > 128 || len([]rune(strings.TrimSpace(input.RequestText))) > 20000 {
		return CreateTaskInput{}, nil, ErrInvalidTask
	}
	if strings.TrimSpace(input.RequestText) == "" {
		return CreateTaskInput{}, nil, ErrInvalidTask
	}
	if key := strings.TrimSpace(input.IdempotencyKey); key == "" || len(key) > 128 {
		return CreateTaskInput{}, nil, ErrInvalidTask
	} else {
		input.IdempotencyKey = key
	}
	if input.RequestScopeSchemaVersion == 0 {
		input.RequestScopeSchemaVersion = 1
	}
	if input.RequestScopeSchemaVersion < 1 {
		return CreateTaskInput{}, nil, ErrInvalidTask
	}
	if len(input.Attachments) > 0 {
		return CreateTaskInput{}, nil, ErrAttachmentsUnsupported
	}
	for _, dataSourceID := range input.EvidenceDataSourceIDs {
		if dataSourceID == uuid.Nil {
			return CreateTaskInput{}, nil, ErrInvalidTask
		}
	}

	input.ExpectedSourceFingerprint = strings.TrimSpace(input.ExpectedSourceFingerprint)
	input.RequestText = strings.TrimSpace(input.RequestText)
	input.EvidenceDataSourceIDs = uniqueSortedUUIDs(input.EvidenceDataSourceIDs)
	if input.RequestScope == nil {
		input.RequestScope = map[string]any{}
	}
	if _, err := json.Marshal(input.RequestScope); err != nil {
		return CreateTaskInput{}, nil, ErrInvalidTask
	}
	return input, input.RequestScope, nil
}

func uniqueSortedUUIDs(values []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(values))
	result := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		if value == uuid.Nil {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].String() < result[j].String() })
	return result
}

func taskRequestFingerprint(input CreateTaskInput, requestScopeJSON []byte) (string, error) {
	dataSourceIDs := make([]string, 0, len(input.EvidenceDataSourceIDs))
	for _, id := range input.EvidenceDataSourceIDs {
		dataSourceIDs = append(dataSourceIDs, id.String())
	}
	retryOf := ""
	if input.RetryOfTaskID != nil {
		retryOf = input.RetryOfTaskID.String()
	}
	payload := struct {
		ExternalCaseID            string          `json:"externalCaseId"`
		ExpectedSourceFingerprint string          `json:"expectedSourceFingerprint"`
		EvidenceDataSourceIDs     []string        `json:"evidenceDataSourceIds"`
		RequestText               string          `json:"requestText"`
		RequestScope              json.RawMessage `json:"requestScope"`
		RequestScopeSchemaVersion int             `json:"requestScopeSchemaVersion"`
		RetryOfTaskID             string          `json:"retryOfTaskId"`
	}{
		ExternalCaseID: input.ExternalCaseID.String(), ExpectedSourceFingerprint: input.ExpectedSourceFingerprint,
		EvidenceDataSourceIDs: dataSourceIDs, RequestText: input.RequestText, RequestScope: requestScopeJSON,
		RequestScopeSchemaVersion: input.RequestScopeSchemaVersion, RetryOfTaskID: retryOf,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

type caseSnapshotPayload struct {
	SchemaVersion     int                      `json:"schemaVersion"`
	ExternalCaseID    string                   `json:"externalCaseId"`
	DataSourceID      string                   `json:"dataSourceId"`
	ExternalCaseKey   string                   `json:"externalCaseKey"`
	CaseType          string                   `json:"caseType"`
	Title             string                   `json:"title"`
	Description       string                   `json:"description"`
	Category          string                   `json:"category"`
	Module            string                   `json:"module"`
	Status            externalcase.Status      `json:"status"`
	Priority          externalcase.Priority    `json:"priority"`
	SourceStatus      string                   `json:"sourceStatus"`
	SourcePriority    string                   `json:"sourcePriority"`
	OccurredAt        *time.Time               `json:"occurredAt"`
	ReportedAt        time.Time                `json:"reportedAt"`
	SourceUpdatedAt   time.Time                `json:"sourceUpdatedAt"`
	Customer          caseSnapshotCustomer     `json:"customer"`
	Product           caseSnapshotProduct      `json:"product"`
	Production        caseSnapshotProduction   `json:"production"`
	Environment       caseSnapshotEnvironment  `json:"environment"`
	Attributes        map[string]any           `json:"attributes"`
	Attachments       []caseSnapshotAttachment `json:"attachments"`
	SourceFingerprint string                   `json:"sourceFingerprint"`
	Truncated         bool                     `json:"truncated"`
}

type caseSnapshotAttachment struct {
	ExternalAttachmentKey string    `json:"externalAttachmentKey"`
	FileName              string    `json:"fileName"`
	MediaType             string    `json:"mediaType"`
	SizeBytes             int64     `json:"sizeBytes"`
	ContentHash           string    `json:"contentHash"`
	SourceUpdatedAt       time.Time `json:"sourceUpdatedAt"`
}

type caseSnapshotCustomer struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type caseSnapshotProduct struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

type caseSnapshotProduction struct {
	WorkOrderNo        string `json:"workOrderNo"`
	WorkpieceNo        string `json:"workpieceNo"`
	MaterialCode       string `json:"materialCode"`
	BatchNo            string `json:"batchNo"`
	SerialNo           string `json:"serialNo"`
	FactoryCode        string `json:"factoryCode"`
	WorkshopCode       string `json:"workshopCode"`
	ProductionLineCode string `json:"productionLineCode"`
	WorkstationCode    string `json:"workstationCode"`
	EquipmentCode      string `json:"equipmentCode"`
}

type caseSnapshotEnvironment struct {
	SourceSystem          string `json:"sourceSystem"`
	DeploymentEnvironment string `json:"deploymentEnvironment"`
	BusinessDatabaseAlias string `json:"businessDatabaseAlias"`
}

func buildCaseSnapshot(item externalcase.ExternalCase, readAt time.Time) (CaseSnapshotRecord, error) {
	attachments := make([]caseSnapshotAttachment, 0, len(item.Attachments))
	for _, attachment := range item.Attachments {
		// ObjectKey 是内部对象存储定位信息，快照和未来模型上下文都不能保存它。
		attachments = append(attachments, caseSnapshotAttachment{
			ExternalAttachmentKey: attachment.ExternalAttachmentKey,
			FileName:              attachment.FileName,
			MediaType:             attachment.MediaType,
			SizeBytes:             attachment.SizeBytes,
			ContentHash:           attachment.ContentHash,
			SourceUpdatedAt:       attachment.SourceUpdatedAt,
		})
	}
	attributes := item.Attributes
	if attributes == nil {
		attributes = map[string]any{}
	}
	payload := caseSnapshotPayload{
		SchemaVersion: 1, ExternalCaseID: item.ID.String(), DataSourceID: item.DataSourceID.String(),
		ExternalCaseKey: item.ExternalCaseKey, CaseType: item.CaseType, Title: item.Title,
		Description: item.Description, Category: item.Category, Module: item.Module,
		Status: item.Status, Priority: item.Priority, SourceStatus: item.SourceStatus,
		SourcePriority: item.SourcePriority, OccurredAt: item.OccurredAt, ReportedAt: item.ReportedAt,
		SourceUpdatedAt: item.SourceUpdatedAt,
		Customer:        caseSnapshotCustomer{Code: item.Customer.Code, Name: item.Customer.Name},
		Product:         caseSnapshotProduct{Code: item.Product.Code, Name: item.Product.Name, Version: item.Product.Version},
		Production: caseSnapshotProduction{
			WorkOrderNo: item.Production.WorkOrderNo, WorkpieceNo: item.Production.WorkpieceNo,
			MaterialCode: item.Production.MaterialCode, BatchNo: item.Production.BatchNo,
			SerialNo: item.Production.SerialNo, FactoryCode: item.Production.FactoryCode,
			WorkshopCode: item.Production.WorkshopCode, ProductionLineCode: item.Production.ProductionLineCode,
			WorkstationCode: item.Production.WorkstationCode, EquipmentCode: item.Production.EquipmentCode,
		},
		Environment: caseSnapshotEnvironment{
			SourceSystem: item.Environment.SourceSystem, DeploymentEnvironment: item.Environment.DeploymentEnvironment,
			BusinessDatabaseAlias: item.Environment.BusinessDatabaseAlias,
		},
		Attributes:  attributes,
		Attachments: attachments, SourceFingerprint: item.SourceFingerprint, Truncated: item.Truncated,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return CaseSnapshotRecord{}, err
	}
	sum := sha256.Sum256(encoded)
	truncationStatus := "complete"
	if item.Truncated {
		truncationStatus = "truncated"
	}
	return CaseSnapshotRecord{
		Payload: encoded, PayloadSchemaVersion: 1, ContentHash: "sha256:" + hex.EncodeToString(sum[:]),
		SourceReadAt: readAt.UTC(), RedactionStatus: "redacted", TruncationStatus: truncationStatus,
	}, nil
}
