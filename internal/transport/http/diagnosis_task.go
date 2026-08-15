package httptransport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	OpenEventStream(ctx context.Context, actor diagnosis.TaskActor, taskID uuid.UUID) (diagnosis.TaskEventStream, error)
	Cancel(ctx context.Context, actor diagnosis.TaskActor, taskID uuid.UUID) (diagnosis.TaskCancelResult, error)
}

type DiagnosisTaskRoutes struct {
	useCase              diagnosisTaskUseCase
	auth                 gin.HandlerFunc
	csrf                 gin.HandlerFunc
	lifecycle            context.Context
	ssePollInterval      time.Duration
	sseHeartbeatInterval time.Duration
}

const (
	taskEventSSERetryMillis     = 3000
	defaultSSEPollInterval      = time.Second
	defaultSSEHeartbeatInterval = 15 * time.Second
)

func NewDiagnosisTaskRoutes(
	lifecycle context.Context,
	useCase diagnosisTaskUseCase,
	authMiddleware gin.HandlerFunc,
	csrfMiddleware gin.HandlerFunc,
) (*DiagnosisTaskRoutes, error) {
	if lifecycle == nil || useCase == nil || authMiddleware == nil || csrfMiddleware == nil {
		return nil, errors.New("diagnosis task route dependencies are nil")
	}
	return &DiagnosisTaskRoutes{
		useCase: useCase, auth: authMiddleware, csrf: csrfMiddleware, lifecycle: lifecycle,
		ssePollInterval: defaultSSEPollInterval, sseHeartbeatInterval: defaultSSEHeartbeatInterval,
	}, nil
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
	actor := diagnosis.TaskActor{
		UserID: identity.User.ID, IsAdmin: identity.User.IsAdmin(),
	}
	if acceptsEventStream(c.GetHeader("Accept")) {
		afterSeq, cursorErr := taskEventStreamAfterSeq(c, query.AfterSeq)
		if cursorErr != nil {
			AbortWithError(c, cursorErr)
			return
		}
		batchLimit := query.Limit
		if batchLimit == 0 {
			batchLimit = diagnosis.DefaultTaskEventLimit
		}
		r.streamEvents(c, actor, taskID, afterSeq, batchLimit)
		return
	}
	page, err := r.useCase.ListEvents(c.Request.Context(), actor, taskID, query.AfterSeq, query.Limit)
	if err != nil {
		AbortWithError(c, translateDiagnosisTaskError("list diagnosis task events", err))
		return
	}
	items := make([]diagnosisTaskEventResponse, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, diagnosisTaskEventResponseFrom(item))
	}
	WriteSuccess(c, diagnosisTaskEventsResponse{
		Items: items, AfterSeq: page.AfterSeq, NextAfterSeq: page.NextAfterSeq, HasMore: page.HasMore,
	})
}

func (r *DiagnosisTaskRoutes) streamEvents(
	c *gin.Context,
	actor diagnosis.TaskActor,
	taskID uuid.UUID,
	afterSeq int64,
	batchLimit int,
) {
	stream, err := r.useCase.OpenEventStream(c.Request.Context(), actor, taskID)
	if err != nil {
		AbortWithError(c, translateDiagnosisTaskError("open diagnosis task event stream", err))
		return
	}
	if stream == nil {
		AbortWithError(c, apperror.Wrap(apperror.CodeInternal, errors.New("diagnosis task event stream is nil")))
		return
	}
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		AbortWithError(c, apperror.Wrap(apperror.CodeInternal, errors.New("streaming is not supported")))
		return
	}
	page, err := stream.Next(c.Request.Context(), afterSeq, batchLimit)
	if err != nil {
		AbortWithError(c, translateDiagnosisTaskError("read diagnosis task event stream", err))
		return
	}

	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	if _, err := fmt.Fprintf(c.Writer, "retry: %d\n\n", taskEventSSERetryMillis); err != nil {
		return
	}
	flusher.Flush()

	pollInterval := r.ssePollInterval
	if pollInterval <= 0 {
		pollInterval = defaultSSEPollInterval
	}
	heartbeatInterval := r.sseHeartbeatInterval
	if heartbeatInterval <= 0 {
		heartbeatInterval = defaultSSEHeartbeatInterval
	}
	poll := time.NewTicker(pollInterval)
	heartbeat := time.NewTicker(heartbeatInterval)
	defer poll.Stop()
	defer heartbeat.Stop()

	var sessionTimer *time.Timer
	var sessionExpired <-chan time.Time
	if expiresAt := identityAbsoluteExpiry(c); !expiresAt.IsZero() {
		remaining := time.Until(expiresAt)
		if remaining <= 0 {
			return
		}
		sessionTimer = time.NewTimer(remaining)
		sessionExpired = sessionTimer.C
		defer sessionTimer.Stop()
	}

	cursor := afterSeq
	initialTerminal := stream.InitialStatus().IsTerminal()
	for {
		terminalSent, writeErr := writeTaskEventPage(c.Writer, page)
		if writeErr != nil {
			return
		}
		if page.NextAfterSeq > cursor {
			cursor = page.NextAfterSeq
		}
		if len(page.Items) > 0 {
			flusher.Flush()
		}
		if terminalSent || (initialTerminal && !page.HasMore) {
			return
		}
		if page.HasMore {
			page, err = stream.Next(c.Request.Context(), cursor, batchLimit)
			if err != nil {
				r.writeTaskEventStreamError(c, err)
				flusher.Flush()
				return
			}
			continue
		}

		select {
		case <-c.Request.Context().Done():
			return
		case <-r.lifecycle.Done():
			return
		case <-sessionExpired:
			return
		case <-heartbeat.C:
			if _, err := io.WriteString(c.Writer, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-poll.C:
			page, err = stream.Next(c.Request.Context(), cursor, batchLimit)
			if err != nil {
				r.writeTaskEventStreamError(c, err)
				flusher.Flush()
				return
			}
		}
	}
}

func (r *DiagnosisTaskRoutes) writeTaskEventStreamError(c *gin.Context, cause error) {
	_ = c.Error(apperror.Wrap(apperror.CodeInternal, fmt.Errorf("read diagnosis task event stream: %w", cause)))
	_ = writeSSEFrame(c.Writer, "", "error", map[string]any{
		"code": int(apperror.CodeInternal), "message": apperror.CodeInternal.Message(),
		"requestId": RequestIDFromContext(c),
	})
}

func diagnosisTaskEventResponseFrom(item diagnosis.TaskEvent) diagnosisTaskEventResponse {
	payload := item.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	return diagnosisTaskEventResponse{
		Seq: item.Seq, EventType: string(item.EventType), Payload: payload,
		PayloadSchemaVersion: item.PayloadSchemaVersion,
		CreatedAt:            item.CreatedAt.UTC().Format(timeRFC3339Nano),
	}
}

func writeTaskEventPage(writer io.Writer, page diagnosis.TaskEventPage) (bool, error) {
	terminalSent := false
	for _, item := range page.Items {
		if item.Seq < 1 || strings.TrimSpace(string(item.EventType)) == "" || strings.ContainsAny(string(item.EventType), "\r\n") {
			return false, errors.New("task event stream payload is invalid")
		}
		if err := writeSSEFrame(writer, strconv.FormatInt(item.Seq, 10), string(item.EventType), diagnosisTaskEventResponseFrom(item)); err != nil {
			return false, err
		}
		terminalSent = terminalSent || item.EventType.IsTerminal()
	}
	return terminalSent, nil
}

func writeSSEFrame(writer io.Writer, id, event string, data any) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if id != "" {
		if _, err := fmt.Fprintf(writer, "id: %s\n", id); err != nil {
			return err
		}
	}
	if event != "" {
		if _, err := fmt.Fprintf(writer, "event: %s\n", event); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(writer, "data: %s\n\n", encoded)
	return err
}

func acceptsEventStream(accept string) bool {
	for _, value := range strings.Split(accept, ",") {
		mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(value))
		if err != nil || !strings.EqualFold(mediaType, "text/event-stream") {
			continue
		}
		if quality, ok := params["q"]; ok {
			parsed, parseErr := strconv.ParseFloat(quality, 64)
			if parseErr != nil || parsed <= 0 {
				continue
			}
		}
		return true
	}
	return false
}

func taskEventStreamAfterSeq(c *gin.Context, queryAfterSeq int64) (int64, error) {
	value := strings.TrimSpace(c.GetHeader("Last-Event-ID"))
	if value == "" {
		return queryAfterSeq, nil
	}
	afterSeq, err := strconv.ParseInt(value, 10, 64)
	if err != nil || afterSeq < 0 {
		return 0, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
			Field: "Last-Event-ID", Reason: "必须是非负整数",
		}})
	}
	return afterSeq, nil
}

func identityAbsoluteExpiry(c *gin.Context) time.Time {
	identity, ok := identityFromContext(c)
	if !ok {
		return time.Time{}
	}
	return identity.Session.AbsoluteExpiresAt.UTC()
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
	TaskID          string                            `json:"taskId"`
	ExternalCaseID  string                            `json:"externalCaseId"`
	CaseSnapshotID  string                            `json:"caseSnapshotId"`
	RetryOfTaskID   string                            `json:"retryOfTaskId,omitempty"`
	RequestText     string                            `json:"requestText"`
	Status          diagnosis.TaskStatus              `json:"status"`
	AttemptCount    int                               `json:"attemptCount"`
	LastErrorCode   string                            `json:"lastErrorCode,omitempty"`
	LastErrorMessage string                           `json:"lastErrorMessage,omitempty"`
	StartedAt       *string                           `json:"startedAt,omitempty"`
	CompletedAt     *string                           `json:"completedAt,omitempty"`
	CreatedAt       string                            `json:"createdAt"`
	UpdatedAt       string                            `json:"updatedAt"`
	ReportAvailable bool                              `json:"reportAvailable"`
	ReportID        string                            `json:"reportId,omitempty"`
	Attachments     []diagnosisTaskAttachmentResponse `json:"attachments"`
}

type diagnosisTaskAttachmentResponse struct {
	AttachmentID    string `json:"attachmentId"`
	SourceMessageID string `json:"sourceMessageId"`
	Purpose         string `json:"purpose"`
	OriginalName    string `json:"originalName"`
	MediaType       string `json:"mediaType"`
	SizeBytes       int64  `json:"sizeBytes"`
	ContentSHA256   string `json:"contentSha256"`
}

func diagnosisTaskResponseFrom(task diagnosis.DiagnosisTask) diagnosisTaskResponse {
	response := diagnosisTaskResponse{
		TaskID: task.ID.String(), ExternalCaseID: task.ExternalCaseID.String(), CaseSnapshotID: task.CaseSnapshotID.String(),
		RequestText: task.RequestText, Status: task.Status,
		AttemptCount: task.AttemptCount, LastErrorCode: task.LastErrorCode,
		LastErrorMessage: task.LastErrorMessage, CreatedAt: task.CreatedAt.UTC().Format(timeRFC3339Nano),
		UpdatedAt: task.UpdatedAt.UTC().Format(timeRFC3339Nano), ReportAvailable: task.ReportID != nil,
		Attachments: make([]diagnosisTaskAttachmentResponse, 0, len(task.Attachments)),
	}
	for _, current := range task.Attachments {
		response.Attachments = append(response.Attachments, diagnosisTaskAttachmentResponse{
			AttachmentID: current.AttachmentID.String(), SourceMessageID: current.SourceMessageID.String(),
			Purpose: current.Purpose, OriginalName: current.OriginalName, MediaType: current.MediaType,
			SizeBytes: current.SizeBytes, ContentSHA256: current.ContentSHA256,
		})
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
	case errors.Is(err, diagnosis.ErrAttachmentContextRequired):
		return apperror.NewWithMessage(apperror.CodeValidationFailed, "任务附件只能从会话最新用户消息的已授权附件创建")
	case errors.Is(err, diagnosis.ErrTaskAttachmentForbidden):
		return apperror.Wrap(apperror.CodeNotFound, err)
	case errors.Is(err, diagnosis.ErrSourceChanged):
		return apperror.Wrap(apperror.CodeSourceChanged, err)
	case errors.Is(err, diagnosis.ErrIdempotencyConflict):
		return apperror.Wrap(apperror.CodeIdempotencyConflict, err)
	case errors.Is(err, diagnosis.ErrTaskStateConflict):
		return apperror.Wrap(apperror.CodeTaskStateConflict, err)
	case errors.Is(err, diagnosis.ErrTaskReportUnavailable):
		return apperror.NewWithMessage(apperror.CodeTaskStateConflict, "当前任务尚无可读取的正式诊断报告")
	case errors.Is(err, repository.ErrNotFound):
		return apperror.Wrap(apperror.CodeNotFound, err)
	default:
		return apperror.Wrap(apperror.CodeInternal, fmt.Errorf("%s: %w", operation, err))
	}
}

var _ diagnosisTaskUseCase = (*diagnosis.DiagnosisTaskService)(nil)
