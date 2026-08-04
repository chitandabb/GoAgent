package httptransport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/chitandabb/GoAgent/internal/apperror"
	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type knowledgeIngestionTaskUseCase interface {
	Get(context.Context, uuid.UUID) (knowledge.IngestionTaskDetail, error)
	Cancel(context.Context, uuid.UUID, uuid.UUID) (knowledge.IngestionCancelResult, error)
}

type KnowledgeIngestionTaskRoutes struct {
	useCase knowledgeIngestionTaskUseCase
	auth    gin.HandlerFunc
	csrf    gin.HandlerFunc
}

func NewKnowledgeIngestionTaskRoutes(
	useCase knowledgeIngestionTaskUseCase,
	authMiddleware, csrfMiddleware gin.HandlerFunc,
) (*KnowledgeIngestionTaskRoutes, error) {
	if useCase == nil || authMiddleware == nil || csrfMiddleware == nil {
		return nil, errors.New("knowledge ingestion task route dependencies are nil")
	}
	return &KnowledgeIngestionTaskRoutes{useCase: useCase, auth: authMiddleware, csrf: csrfMiddleware}, nil
}

func (r *KnowledgeIngestionTaskRoutes) Register(api *gin.RouterGroup) {
	routes := api.Group("/admin/knowledge-ingestion-tasks")
	routes.Use(r.auth)
	routes.GET("/:taskId", r.get)
	routes.POST("/:taskId/cancel", r.csrf, r.cancel)
}

type knowledgeIngestionTaskResponse struct {
	TaskID              string                        `json:"taskId"`
	DocumentVersionID   string                        `json:"documentVersionId"`
	DocumentID          string                        `json:"documentId"`
	Status              knowledge.IngestionTaskStatus `json:"status"`
	Stage               knowledge.IngestionStage      `json:"stage"`
	AttemptCount        int                           `json:"attemptCount"`
	MaxAttempts         int                           `json:"maxAttempts"`
	ProgressPercent     int                           `json:"progressPercent"`
	CancelRequestedAt   *string                       `json:"cancelRequestedAt,omitempty"`
	LastError           *knowledgeIngestionError      `json:"lastError,omitempty"`
	StartedAt           *string                       `json:"startedAt,omitempty"`
	CompletedAt         *string                       `json:"completedAt,omitempty"`
	CreatedAt           string                        `json:"createdAt"`
	UpdatedAt           string                        `json:"updatedAt"`
	CancellationChanged bool                          `json:"cancellationChanged,omitempty"`
}

type knowledgeIngestionError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (r *KnowledgeIngestionTaskRoutes) get(c *gin.Context) {
	_, taskID, ok := requireAdminKnowledgeTask(c)
	if !ok {
		return
	}
	detail, err := r.useCase.Get(c.Request.Context(), taskID)
	if err != nil {
		AbortWithError(c, translateKnowledgeTaskControlError(err))
		return
	}
	WriteSuccess(c, knowledgeTaskResponse(detail, false))
}

func (r *KnowledgeIngestionTaskRoutes) cancel(c *gin.Context) {
	userID, taskID, ok := requireAdminKnowledgeTask(c)
	if !ok {
		return
	}
	result, err := r.useCase.Cancel(c.Request.Context(), taskID, userID)
	if err != nil {
		AbortWithError(c, translateKnowledgeTaskControlError(err))
		return
	}
	response := knowledgeTaskResponse(result.Task, result.Changed)
	if result.Changed {
		WriteSuccessWithStatus(c, http.StatusAccepted, response)
		return
	}
	WriteSuccess(c, response)
}

func requireAdminKnowledgeTask(c *gin.Context) (userID uuid.UUID, taskID uuid.UUID, ok bool) {
	current, exists := identityFromContext(c)
	if !exists {
		AbortWithError(c, apperror.New(apperror.CodeUnauthorized))
		return uuid.Nil, uuid.Nil, false
	}
	if !current.User.IsAdmin() {
		AbortWithError(c, apperror.New(apperror.CodeForbidden))
		return uuid.Nil, uuid.Nil, false
	}
	parsed, err := uuid.Parse(c.Param("taskId"))
	if err != nil {
		AbortWithError(c, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
			Field: "taskId", Reason: "必须是合法的 UUID",
		}}))
		return uuid.Nil, uuid.Nil, false
	}
	return current.User.ID, parsed, true
}

func knowledgeTaskResponse(detail knowledge.IngestionTaskDetail, changed bool) knowledgeIngestionTaskResponse {
	response := knowledgeIngestionTaskResponse{
		TaskID: detail.ID.String(), DocumentVersionID: detail.DocumentVersionID.String(),
		DocumentID: detail.DocumentID.String(), Status: detail.Status, Stage: detail.Stage,
		AttemptCount: detail.AttemptCount, MaxAttempts: detail.MaxAttempts,
		ProgressPercent: detail.ProgressPercent, CreatedAt: detail.CreatedAt.UTC().Format(timeRFC3339Nano),
		UpdatedAt: detail.UpdatedAt.UTC().Format(timeRFC3339Nano), CancellationChanged: changed,
	}
	response.CancelRequestedAt = formatOptionalTime(detail.CancelRequestedAt)
	response.StartedAt = formatOptionalTime(detail.StartedAt)
	response.CompletedAt = formatOptionalTime(detail.CompletedAt)
	if detail.LastErrorCode != "" || detail.LastErrorMessage != "" {
		response.LastError = &knowledgeIngestionError{Code: detail.LastErrorCode, Message: detail.LastErrorMessage}
	}
	return response
}

func formatOptionalTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(timeRFC3339Nano)
	return &formatted
}

func translateKnowledgeTaskControlError(err error) error {
	switch {
	case errors.Is(err, repository.ErrNotFound):
		return apperror.New(apperror.CodeNotFound)
	case errors.Is(err, knowledge.ErrIngestionTaskStateConflict):
		return apperror.New(apperror.CodeTaskStateConflict)
	default:
		return apperror.Wrap(apperror.CodeInternal, fmt.Errorf("control knowledge ingestion task: %w", err))
	}
}

var _ knowledgeIngestionTaskUseCase = (*knowledge.IngestionTaskControlService)(nil)
