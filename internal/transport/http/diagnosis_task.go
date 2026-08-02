package httptransport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/chitandabb/GoAgent/internal/apperror"
	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/chitandabb/GoAgent/internal/repository"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type diagnosisTaskUseCase interface {
	Create(ctx context.Context, actor diagnosis.TaskActor, input diagnosis.CreateTaskInput) (diagnosis.TaskCreateResult, error)
	Get(ctx context.Context, actor diagnosis.TaskActor, taskID uuid.UUID) (diagnosis.DiagnosisTask, error)
	ListEvents(ctx context.Context, actor diagnosis.TaskActor, taskID uuid.UUID, afterSeq int64, limit int) (diagnosis.TaskEventPage, error)
	Cancel(ctx context.Context, actor diagnosis.TaskActor, taskID uuid.UUID) (diagnosis.TaskCancelResult, error)
}

type DiagnosisTaskRoutes struct {
	useCase diagnosisTaskUseCase
	auth    gin.HandlerFunc
	csrf    gin.HandlerFunc
}

func NewDiagnosisTaskRoutes(
	useCase diagnosisTaskUseCase,
	authMiddleware gin.HandlerFunc,
	csrfMiddleware gin.HandlerFunc,
) (*DiagnosisTaskRoutes, error) {
	if useCase == nil || authMiddleware == nil || csrfMiddleware == nil {
		return nil, errors.New("diagnosis task route dependencies are nil")
	}
	return &DiagnosisTaskRoutes{useCase: useCase, auth: authMiddleware, csrf: csrfMiddleware}, nil
}

func (r *DiagnosisTaskRoutes) Register(api *gin.RouterGroup) {
	protected := api.Group("/diagnosis-tasks")
	protected.Use(r.auth)
	protected.GET("/:taskId/events", r.listEvents)
	protected.GET("/:taskId", r.get)

	commands := protected.Group("")
	commands.Use(r.csrf)
	commands.POST("", r.create)
	commands.POST("/:taskId/cancel", r.cancel)
}

type diagnosisTaskCreateRequest struct {
	ExternalCaseID            string                       `json:"externalCaseId" binding:"required,uuid"`
	ExpectedSourceFingerprint string                       `json:"expectedSourceFingerprint" binding:"required,max=128"`
	EvidenceDataSourceIDs     []string                     `json:"evidenceDataSourceIds"`
	RequestText               string                       `json:"requestText" binding:"required,max=20000"`
	RequestScope              map[string]any               `json:"requestScope"`
	RequestScopeSchemaVersion int                          `json:"requestScopeSchemaVersion" binding:"omitempty,min=1"`
	Attachments               []diagnosisTaskAttachmentReq `json:"attachments"`
	RetryOfTaskID             string                       `json:"retryOfTaskId"`
}

type diagnosisTaskAttachmentReq struct {
	AttachmentID string `json:"attachmentId" binding:"required,uuid"`
	Purpose      string `json:"purpose" binding:"required,max=64"`
}

type diagnosisTaskCreateResponse struct {
	TaskID    string               `json:"taskId"`
	Status    diagnosis.TaskStatus `json:"status"`
	Replayed  bool                 `json:"replayed"`
	CreatedAt string               `json:"createdAt"`
}

func (r *DiagnosisTaskRoutes) create(c *gin.Context) {
	request, ok := BindJSON[diagnosisTaskCreateRequest](c)
	if !ok {
		return
	}
	identity, ok := identityFromContext(c)
	if !ok {
		AbortWithError(c, apperror.New(apperror.CodeUnauthorized))
		return
	}

	idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if _, err := uuid.Parse(idempotencyKey); err != nil {
		AbortWithError(c, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
			Field: "Idempotency-Key", Reason: "必须是合法的 UUID",
		}}))
		return
	}
	externalCaseID, err := uuid.Parse(request.ExternalCaseID)
	if err != nil {
		AbortWithError(c, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
			Field: "externalCaseId", Reason: "必须是合法的 UUID",
		}}))
		return
	}
	evidenceDataSourceIDs, err := parseUUIDList("evidenceDataSourceIds", request.EvidenceDataSourceIDs)
	if err != nil {
		AbortWithError(c, err)
		return
	}
	var retryOfTaskID *uuid.UUID
	if value := strings.TrimSpace(request.RetryOfTaskID); value != "" {
		parsed, parseErr := uuid.Parse(value)
		if parseErr != nil {
			AbortWithError(c, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
				Field: "retryOfTaskId", Reason: "必须是合法的 UUID",
			}}))
			return
		}
		retryOfTaskID = &parsed
	}
	attachments := make([]diagnosis.TaskAttachment, 0, len(request.Attachments))
	for _, attachment := range request.Attachments {
		attachmentID, parseErr := uuid.Parse(attachment.AttachmentID)
		if parseErr != nil {
			AbortWithError(c, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
				Field: "attachments.attachmentId", Reason: "必须是合法的 UUID",
			}}))
			return
		}
		attachments = append(attachments, diagnosis.TaskAttachment{AttachmentID: attachmentID, Purpose: strings.TrimSpace(attachment.Purpose)})
	}

	correlationID := uuid.New()
	if requestID := RequestIDFromContext(c); requestID != "" {
		if parsed, parseErr := uuid.Parse(requestID); parseErr == nil {
			correlationID = parsed
		}
	}
	result, err := r.useCase.Create(c.Request.Context(), diagnosis.TaskActor{
		UserID: identity.User.ID, IsAdmin: identity.User.IsAdmin(),
	}, diagnosis.CreateTaskInput{
		ExternalCaseID: externalCaseID, ExpectedSourceFingerprint: strings.TrimSpace(request.ExpectedSourceFingerprint),
		EvidenceDataSourceIDs: evidenceDataSourceIDs, RequestText: request.RequestText,
		RequestScope: request.RequestScope, RequestScopeSchemaVersion: request.RequestScopeSchemaVersion,
		Attachments: attachments, RetryOfTaskID: retryOfTaskID, IdempotencyKey: idempotencyKey,
		CorrelationID: correlationID,
	})
	if err != nil {
		AbortWithError(c, translateDiagnosisTaskError("create diagnosis task", err))
		return
	}
	c.Header("Location", "/api/v1/diagnosis-tasks/"+result.Task.ID.String())
	WriteSuccessWithStatus(c, http.StatusAccepted, diagnosisTaskCreateResponse{
		TaskID: result.Task.ID.String(), Status: result.Task.Status, Replayed: result.Replayed,
		CreatedAt: result.Task.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
	})
}

func (r *DiagnosisTaskRoutes) get(c *gin.Context) {
	taskID, err := uuid.Parse(c.Param("taskId"))
	if err != nil {
		AbortWithError(c, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
			Field: "taskId", Reason: "必须是合法的 UUID",
		}}))
		return
	}
	identity, ok := identityFromContext(c)
	if !ok {
		AbortWithError(c, apperror.New(apperror.CodeUnauthorized))
		return
	}
	task, err := r.useCase.Get(c.Request.Context(), diagnosis.TaskActor{
		UserID: identity.User.ID, IsAdmin: identity.User.IsAdmin(),
	}, taskID)
	if err != nil {
		AbortWithError(c, translateDiagnosisTaskError("get diagnosis task", err))
		return
	}
	WriteSuccess(c, diagnosisTaskResponseFrom(task))
}

type diagnosisTaskEventsQuery struct {
	AfterSeq int64 `form:"afterSeq" binding:"omitempty,min=0"`
	Limit    int   `form:"limit" binding:"omitempty,min=1,max=200"`
}

type diagnosisTaskEventResponse struct {
	Seq                  int64          `json:"seq"`
	EventType            string         `json:"eventType"`
	Payload              map[string]any `json:"payload"`
	PayloadSchemaVersion int            `json:"payloadSchemaVersion"`
	CreatedAt            string         `json:"createdAt"`
}

type diagnosisTaskEventsResponse struct {
	Items        []diagnosisTaskEventResponse `json:"items"`
	AfterSeq     int64                        `json:"afterSeq"`
	NextAfterSeq int64                        `json:"nextAfterSeq"`
	HasMore      bool                         `json:"hasMore"`
}

func (r *DiagnosisTaskRoutes) listEvents(c *gin.Context) {
	taskID, err := uuid.Parse(c.Param("taskId"))
	if err != nil {
		AbortWithError(c, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
			Field: "taskId", Reason: "必须是合法的 UUID",
		}}))
		return
	}
	query, ok := BindQuery[diagnosisTaskEventsQuery](c)
	if !ok {
		return
	}
	identity, ok := identityFromContext(c)
	if !ok {
		AbortWithError(c, apperror.New(apperror.CodeUnauthorized))
		return
	}
	page, err := r.useCase.ListEvents(c.Request.Context(), diagnosis.TaskActor{
		UserID: identity.User.ID, IsAdmin: identity.User.IsAdmin(),
	}, taskID, query.AfterSeq, query.Limit)
	if err != nil {
		AbortWithError(c, translateDiagnosisTaskError("list diagnosis task events", err))
		return
	}
	items := make([]diagnosisTaskEventResponse, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, diagnosisTaskEventResponse{
			Seq: item.Seq, EventType: item.EventType, Payload: item.Payload,
			PayloadSchemaVersion: item.PayloadSchemaVersion,
			CreatedAt:            item.CreatedAt.UTC().Format(timeRFC3339Nano),
		})
	}
	WriteSuccess(c, diagnosisTaskEventsResponse{
		Items: items, AfterSeq: page.AfterSeq, NextAfterSeq: page.NextAfterSeq, HasMore: page.HasMore,
	})
}

func (r *DiagnosisTaskRoutes) cancel(c *gin.Context) {
	taskID, err := uuid.Parse(c.Param("taskId"))
	if err != nil {
		AbortWithError(c, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
			Field: "taskId", Reason: "必须是合法的 UUID",
		}}))
		return
	}
	identity, ok := identityFromContext(c)
	if !ok {
		AbortWithError(c, apperror.New(apperror.CodeUnauthorized))
		return
	}
	result, err := r.useCase.Cancel(c.Request.Context(), diagnosis.TaskActor{
		UserID: identity.User.ID, IsAdmin: identity.User.IsAdmin(),
	}, taskID)
	if err != nil {
		AbortWithError(c, translateDiagnosisTaskError("cancel diagnosis task", err))
		return
	}
	status := http.StatusOK
	if result.Changed {
		status = http.StatusAccepted
	}
	WriteSuccessWithStatus(c, status, diagnosisTaskResponseFrom(result.Task))
}

type diagnosisTaskResponse struct {
	TaskID                    string               `json:"taskId"`
	ExternalCaseID            string               `json:"externalCaseId"`
	CaseSnapshotID            string               `json:"caseSnapshotId"`
	RetryOfTaskID             string               `json:"retryOfTaskId,omitempty"`
	RequestText               string               `json:"requestText"`
	RequestScope              map[string]any       `json:"requestScope"`
	RequestScopeSchemaVersion int                  `json:"requestScopeSchemaVersion"`
	Status                    diagnosis.TaskStatus `json:"status"`
	AttemptCount              int                  `json:"attemptCount"`
	LastErrorCode             string               `json:"lastErrorCode,omitempty"`
	LastErrorMessage          string               `json:"lastErrorMessage,omitempty"`
	StartedAt                 *string              `json:"startedAt,omitempty"`
	CompletedAt               *string              `json:"completedAt,omitempty"`
	CreatedAt                 string               `json:"createdAt"`
	UpdatedAt                 string               `json:"updatedAt"`
	ReportAvailable           bool                 `json:"reportAvailable"`
	ReportID                  string               `json:"reportId,omitempty"`
}

func diagnosisTaskResponseFrom(task diagnosis.DiagnosisTask) diagnosisTaskResponse {
	response := diagnosisTaskResponse{
		TaskID: task.ID.String(), ExternalCaseID: task.ExternalCaseID.String(), CaseSnapshotID: task.CaseSnapshotID.String(),
		RequestText: task.RequestText, RequestScope: task.RequestScope,
		RequestScopeSchemaVersion: task.RequestScopeSchemaVersion, Status: task.Status,
		AttemptCount: task.AttemptCount, LastErrorCode: task.LastErrorCode,
		LastErrorMessage: task.LastErrorMessage, CreatedAt: task.CreatedAt.UTC().Format(timeRFC3339Nano),
		UpdatedAt: task.UpdatedAt.UTC().Format(timeRFC3339Nano), ReportAvailable: task.ReportID != nil,
	}
	if task.RetryOfTaskID != nil {
		response.RetryOfTaskID = task.RetryOfTaskID.String()
	}
	if task.StartedAt != nil {
		value := task.StartedAt.UTC().Format(timeRFC3339Nano)
		response.StartedAt = &value
	}
	if task.CompletedAt != nil {
		value := task.CompletedAt.UTC().Format(timeRFC3339Nano)
		response.CompletedAt = &value
	}
	if task.ReportID != nil {
		response.ReportID = task.ReportID.String()
	}
	return response
}

const timeRFC3339Nano = "2006-01-02T15:04:05.999999999Z07:00"

func parseUUIDList(field string, values []string) ([]uuid.UUID, error) {
	result := make([]uuid.UUID, 0, len(values))
	seen := make(map[uuid.UUID]struct{}, len(values))
	for _, value := range values {
		parsed, err := uuid.Parse(strings.TrimSpace(value))
		if err != nil {
			return nil, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
				Field: field, Reason: "必须全部是合法的 UUID",
			}})
		}
		if _, ok := seen[parsed]; ok {
			return nil, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
				Field: field, Reason: "不能包含重复的 UUID",
			}})
		}
		seen[parsed] = struct{}{}
		result = append(result, parsed)
	}
	return result, nil
}

func translateDiagnosisTaskError(operation string, err error) error {
	var appErr *apperror.Error
	if errors.As(err, &appErr) {
		return appErr
	}
	switch {
	case errors.Is(err, diagnosis.ErrTaskForbidden):
		return apperror.Wrap(apperror.CodeForbidden, err)
	case errors.Is(err, diagnosis.ErrInvalidTask):
		return apperror.Wrap(apperror.CodeValidationFailed, err)
	case errors.Is(err, diagnosis.ErrAttachmentsUnsupported):
		return apperror.NewWithMessage(apperror.CodeValidationFailed, "当前版本尚不支持任务附件，请先移除附件后重试")
	case errors.Is(err, diagnosis.ErrSourceChanged):
		return apperror.Wrap(apperror.CodeSourceChanged, err)
	case errors.Is(err, diagnosis.ErrIdempotencyConflict):
		return apperror.Wrap(apperror.CodeIdempotencyConflict, err)
	case errors.Is(err, diagnosis.ErrTaskStateConflict):
		return apperror.Wrap(apperror.CodeTaskStateConflict, err)
	case errors.Is(err, repository.ErrNotFound):
		return apperror.Wrap(apperror.CodeNotFound, err)
	default:
		return apperror.Wrap(apperror.CodeInternal, fmt.Errorf("%s: %w", operation, err))
	}
}

var _ diagnosisTaskUseCase = (*diagnosis.DiagnosisTaskService)(nil)
