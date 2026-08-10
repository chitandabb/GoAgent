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

// TaskEventType 是 DiagnosisTask 生命周期事件的持久化协议类型。
// 数据库仍保存字符串值，但生产代码必须使用这些常量，避免状态机协议散落在各层。
type TaskEventType string

const (
	TaskEventCreated         TaskEventType = "task_created"
	TaskEventCancelRequested TaskEventType = "task_cancel_requested"
	TaskEventStarted         TaskEventType = "task_started"
	TaskEventReclaimed       TaskEventType = "task_reclaimed"
	TaskEventRetryScheduled  TaskEventType = "task_retry_scheduled"
	TaskEventSucceeded       TaskEventType = "task_succeeded"
	TaskEventFailed          TaskEventType = "task_failed"
	TaskEventCancelled       TaskEventType = "task_cancelled"
	TaskEventRequeued        TaskEventType = "task_requeued"
)

type terminalTaskTransition struct {
	status TaskStatus
	event  TaskEventType
}

// terminalTaskTransitions 是终态状态与终态事件对应关系的唯一来源。
func terminalTaskTransitions() [3]terminalTaskTransition {
	return [3]terminalTaskTransition{
		{status: TaskSucceeded, event: TaskEventSucceeded},
		{status: TaskFailed, event: TaskEventFailed},
		{status: TaskCancelled, event: TaskEventCancelled},
	}
}

func (s TaskStatus) IsTerminal() bool {
	_, ok := s.TerminalEvent()
	return ok
}

func (s TaskStatus) TerminalEvent() (TaskEventType, bool) {
	for _, transition := range terminalTaskTransitions() {
		if transition.status == s {
			return transition.event, true
		}
	}
	return "", false
}

func (e TaskEventType) IsTerminal() bool {
	_, ok := e.TerminalStatus()
	return ok
}

func (e TaskEventType) TerminalStatus() (TaskStatus, bool) {
	for _, transition := range terminalTaskTransitions() {
		if transition.event == e {
			return transition.status, true
		}
	}
	return "", false
}

var (
	ErrTaskForbidden             = errors.New("diagnosis task is forbidden")
	ErrInvalidTask               = errors.New("diagnosis task is invalid")
	ErrTaskStateConflict         = errors.New("diagnosis task state conflicts with the requested operation")
	ErrSourceChanged             = errors.New("external case source has changed")
	ErrIdempotencyConflict       = errors.New("diagnosis task idempotency key conflicts")
	ErrAttachmentContextRequired = errors.New("diagnosis task attachments require a conversation message context")
	ErrTaskAttachmentForbidden   = errors.New("diagnosis task attachment is not authorized by the source message")
)

const (
	RequestScopeKeyRequestedSkill      = "requestedSkill"
	RequestScopeKeyAllowedCapabilities = "allowedCapabilities"

	RequestedSkillTicketDiagnosis   = "ticket-diagnosis"
	RequestedSkillCodeInvestigation = "code-investigation"
	RequestedSkillSQLInvestigation  = "sql-investigation"
)

// TaskCapability 是创建任务时冻结的业务调查能力，不表示对应外部依赖当前健康。
type TaskCapability string

const (
	TaskCapabilityCase       TaskCapability = "case"
	TaskCapabilityCode       TaskCapability = "code"
	TaskCapabilitySQL        TaskCapability = "sql"
	TaskCapabilityKnowledge  TaskCapability = "knowledge"
	TaskCapabilityWebSearch  TaskCapability = "web_search"
	TaskCapabilityAttachment TaskCapability = "attachment"
)

func (c TaskCapability) Valid() bool {
	return c == TaskCapabilityCase || c == TaskCapabilityCode || c == TaskCapabilitySQL ||
		c == TaskCapabilityKnowledge || c == TaskCapabilityWebSearch || c == TaskCapabilityAttachment
}

// TaskActor 是任务查询和创建所需的最小权限上下文。
type TaskActor struct {
	UserID  uuid.UUID
	IsAdmin bool
}

const (
	MaxTaskAttachments            = 8
	MaxTaskAttachmentPurposeRunes = 64
)

// TaskAttachment 是新任务冻结的会话附件引用。
type TaskAttachment struct {
	AttachmentID uuid.UUID
	Purpose      string
}

// TaskAttachmentSource 绑定附件授权来源。没有最新用户消息上下文的直接任务接口
// 不能仅凭 attachment UUID 把会话附件提升为任务证据。
type TaskAttachmentSource struct {
	ConversationID uuid.UUID
	MessageID      uuid.UUID
}

// TaskAttachmentSummary 是任务查询和 Worker 上下文可见的安全元数据。
type TaskAttachmentSummary struct {
	AttachmentID    uuid.UUID
	SourceMessageID uuid.UUID
	Purpose         string
	OriginalName    string
	MediaType       string
	SizeBytes       int64
	ContentSHA256   string
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
	AttachmentSource          *TaskAttachmentSource
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
	Attachments               []TaskAttachment
	AttachmentSource          *TaskAttachmentSource
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
	Attachments               []TaskAttachmentSummary
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
		Attachments:               append([]TaskAttachment(nil), input.Attachments...),
		AttachmentSource:          cloneTaskAttachmentSource(input.AttachmentSource),
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
	if len(input.Attachments) > MaxTaskAttachments {
		return CreateTaskInput{}, nil, ErrInvalidTask
	}
	if len(input.Attachments) == 0 {
		if input.AttachmentSource != nil {
			return CreateTaskInput{}, nil, ErrInvalidTask
		}
	} else {
		if input.AttachmentSource == nil || input.AttachmentSource.ConversationID == uuid.Nil ||
			input.AttachmentSource.MessageID == uuid.Nil {
			return CreateTaskInput{}, nil, ErrAttachmentContextRequired
		}
		seenAttachments := make(map[uuid.UUID]struct{}, len(input.Attachments))
		for index := range input.Attachments {
			current := &input.Attachments[index]
			current.Purpose = strings.TrimSpace(current.Purpose)
			if current.AttachmentID == uuid.Nil || current.Purpose == "" ||
				len([]rune(current.Purpose)) > MaxTaskAttachmentPurposeRunes {
				return CreateTaskInput{}, nil, ErrInvalidTask
			}
			if _, duplicate := seenAttachments[current.AttachmentID]; duplicate {
				return CreateTaskInput{}, nil, ErrInvalidTask
			}
			seenAttachments[current.AttachmentID] = struct{}{}
		}
		sort.Slice(input.Attachments, func(i, j int) bool {
			return input.Attachments[i].AttachmentID.String() < input.Attachments[j].AttachmentID.String()
		})
	}
	for _, dataSourceID := range input.EvidenceDataSourceIDs {
		if dataSourceID == uuid.Nil {
			return CreateTaskInput{}, nil, ErrInvalidTask
		}
	}

	input.ExpectedSourceFingerprint = strings.TrimSpace(input.ExpectedSourceFingerprint)
	input.RequestText = strings.TrimSpace(input.RequestText)
	input.EvidenceDataSourceIDs = uniqueSortedUUIDs(input.EvidenceDataSourceIDs)
	requestScope, err := NormalizeTaskRequestScope(input.RequestScope)
	if err != nil {
		return CreateTaskInput{}, nil, ErrInvalidTask
	}
	if len(input.Attachments) > 0 {
		requestScope, err = addManagedTaskCapability(requestScope, TaskCapabilityAttachment)
		if err != nil {
			return CreateTaskInput{}, nil, ErrInvalidTask
		}
	}
	input.RequestScope = requestScope
	return input, requestScope, nil
}

// NormalizeTaskRequestScope 复制并校验任务创建时可由调用方声明的窄调查范围。
// 代码和 SQL 能力由调用方在已授权范围内声明；knowledge、web_search 和 attachment
// 由后端策略根据运行边界授予并写入任务快照。
func NormalizeTaskRequestScope(scope map[string]any) (map[string]any, error) {
	if scope == nil {
		scope = map[string]any{}
	}
	encoded, err := json.Marshal(scope)
	if err != nil {
		return nil, err
	}
	var normalized map[string]any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return nil, err
	}
	if normalized == nil {
		normalized = map[string]any{}
	}

	capabilities, err := TaskCapabilitiesFromRequestScope(normalized)
	if err != nil {
		return nil, err
	}
	if slicesContainsCapability(capabilities, TaskCapabilityKnowledge) ||
		slicesContainsCapability(capabilities, TaskCapabilityWebSearch) ||
		slicesContainsCapability(capabilities, TaskCapabilityAttachment) {
		return nil, errors.New("knowledge, web_search and attachment capabilities are managed by backend policy")
	}
	capabilities = append(capabilities, TaskCapabilityKnowledge, TaskCapabilityWebSearch)
	sort.Slice(capabilities, func(i, j int) bool { return capabilities[i] < capabilities[j] })
	capabilityValues := make([]string, len(capabilities))
	for index, capability := range capabilities {
		capabilityValues[index] = string(capability)
	}
	normalized[RequestScopeKeyAllowedCapabilities] = capabilityValues

	requestedSkill, err := RequestedSkillFromRequestScope(normalized)
	if err != nil {
		return nil, err
	}
	if requestedSkill != "" {
		normalized[RequestScopeKeyRequestedSkill] = requestedSkill
	}
	if requestedSkill == RequestedSkillCodeInvestigation && !slicesContainsCapability(capabilities, TaskCapabilityCode) {
		return nil, errors.New("code-investigation requires code capability")
	}
	if requestedSkill == RequestedSkillSQLInvestigation && !slicesContainsCapability(capabilities, TaskCapabilitySQL) {
		return nil, errors.New("sql-investigation requires sql capability")
	}
	return normalized, nil
}

func TaskCapabilitiesFromRequestScope(scope map[string]any) ([]TaskCapability, error) {
	raw, exists := scope[RequestScopeKeyAllowedCapabilities]
	if !exists || raw == nil {
		requestedSkill, err := RequestedSkillFromRequestScope(scope)
		if err != nil {
			return nil, err
		}
		switch requestedSkill {
		case RequestedSkillCodeInvestigation:
			return []TaskCapability{TaskCapabilityCase, TaskCapabilityCode}, nil
		case RequestedSkillSQLInvestigation:
			return []TaskCapability{TaskCapabilityCase, TaskCapabilitySQL}, nil
		default:
			return []TaskCapability{TaskCapabilityCase}, nil
		}
	}
	values, ok := raw.([]any)
	if !ok {
		if stringsValues, stringsOK := raw.([]string); stringsOK {
			values = make([]any, len(stringsValues))
			for index, value := range stringsValues {
				values[index] = value
			}
		} else {
			return nil, errors.New("allowedCapabilities must be an array")
		}
	}
	if len(values) == 0 || len(values) > 6 {
		return nil, errors.New("allowedCapabilities must contain between one and six values")
	}
	capabilities := make([]TaskCapability, 0, len(values))
	seen := make(map[TaskCapability]struct{}, len(values))
	for _, rawValue := range values {
		value, ok := rawValue.(string)
		if !ok {
			return nil, errors.New("allowedCapabilities values must be strings")
		}
		capability := TaskCapability(strings.TrimSpace(value))
		if !capability.Valid() {
			return nil, fmt.Errorf("invalid task capability %q", capability)
		}
		if _, duplicate := seen[capability]; duplicate {
			return nil, fmt.Errorf("duplicate task capability %q", capability)
		}
		seen[capability] = struct{}{}
		capabilities = append(capabilities, capability)
	}
	if !slicesContainsCapability(capabilities, TaskCapabilityCase) {
		return nil, errors.New("diagnosis task requires case capability")
	}
	sort.Slice(capabilities, func(i, j int) bool { return capabilities[i] < capabilities[j] })
	return capabilities, nil
}

func RequestedSkillFromRequestScope(scope map[string]any) (string, error) {
	raw, exists := scope[RequestScopeKeyRequestedSkill]
	if !exists || raw == nil {
		return "", nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", errors.New("requestedSkill must be a string")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	switch value {
	case RequestedSkillTicketDiagnosis, RequestedSkillCodeInvestigation, RequestedSkillSQLInvestigation:
		return value, nil
	default:
		return "", fmt.Errorf("invalid requestedSkill %q", value)
	}
}

func slicesContainsCapability(values []TaskCapability, target TaskCapability) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
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

func addManagedTaskCapability(scope map[string]any, capability TaskCapability) (map[string]any, error) {
	capabilities, err := TaskCapabilitiesFromRequestScope(scope)
	if err != nil {
		return nil, err
	}
	if !slicesContainsCapability(capabilities, capability) {
		capabilities = append(capabilities, capability)
		sort.Slice(capabilities, func(i, j int) bool { return capabilities[i] < capabilities[j] })
	}
	values := make([]string, len(capabilities))
	for index, current := range capabilities {
		values[index] = string(current)
	}
	scope[RequestScopeKeyAllowedCapabilities] = values
	return scope, nil
}

func cloneTaskAttachmentSource(value *TaskAttachmentSource) *TaskAttachmentSource {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
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
		ExternalCaseID            string           `json:"externalCaseId"`
		ExpectedSourceFingerprint string           `json:"expectedSourceFingerprint"`
		EvidenceDataSourceIDs     []string         `json:"evidenceDataSourceIds"`
		RequestText               string           `json:"requestText"`
		RequestScope              json.RawMessage  `json:"requestScope"`
		RequestScopeSchemaVersion int              `json:"requestScopeSchemaVersion"`
		RetryOfTaskID             string           `json:"retryOfTaskId"`
		Attachments               []TaskAttachment `json:"attachments"`
		AttachmentConversationID  string           `json:"attachmentConversationId"`
		AttachmentMessageID       string           `json:"attachmentMessageId"`
	}{
		ExternalCaseID: input.ExternalCaseID.String(), ExpectedSourceFingerprint: input.ExpectedSourceFingerprint,
		EvidenceDataSourceIDs: dataSourceIDs, RequestText: input.RequestText, RequestScope: requestScopeJSON,
		RequestScopeSchemaVersion: input.RequestScopeSchemaVersion, RetryOfTaskID: retryOf,
		Attachments: append([]TaskAttachment(nil), input.Attachments...),
	}
	if input.AttachmentSource != nil {
		payload.AttachmentConversationID = input.AttachmentSource.ConversationID.String()
		payload.AttachmentMessageID = input.AttachmentSource.MessageID.String()
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
