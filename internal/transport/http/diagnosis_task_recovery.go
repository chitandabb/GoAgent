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

type diagnosisTaskRecoveryUseCase interface {
	Recover(ctx context.Context, actor diagnosis.TaskActor, taskID uuid.UUID, idempotencyKey, reason string) (diagnosis.TaskRecoveryResult, error)
}

type DiagnosisTaskRecoveryRoutes struct {
	useCase diagnosisTaskRecoveryUseCase
	auth    gin.HandlerFunc
	csrf    gin.HandlerFunc
}

func NewDiagnosisTaskRecoveryRoutes(
	useCase diagnosisTaskRecoveryUseCase,
	authMiddleware gin.HandlerFunc,
	csrfMiddleware gin.HandlerFunc,
) (*DiagnosisTaskRecoveryRoutes, error) {
	if useCase == nil || authMiddleware == nil || csrfMiddleware == nil {
		return nil, errors.New("diagnosis task recovery route dependencies are nil")
	}
	return &DiagnosisTaskRecoveryRoutes{useCase: useCase, auth: authMiddleware, csrf: csrfMiddleware}, nil
}

func (r *DiagnosisTaskRecoveryRoutes) Register(api *gin.RouterGroup) {
	routes := api.Group("/admin/diagnosis-tasks")
	routes.Use(r.auth, r.csrf)
	routes.POST("/:taskId/recover", r.recover)
}

type diagnosisTaskRecoveryRequest struct {
	Reason string `json:"reason" binding:"required,max=1000"`
}

type diagnosisTaskRecoveryResponse struct {
	RecoveryID           string `json:"recoveryId"`
	TaskID               string `json:"taskId"`
	Status               string `json:"status"`
	Replayed             bool   `json:"replayed"`
	TaskEventSeq         int64  `json:"taskEventSeq"`
	PreviousAttemptCount int    `json:"previousAttemptCount"`
	RecoveredAt          string `json:"recoveredAt"`
}

func (r *DiagnosisTaskRecoveryRoutes) recover(c *gin.Context) {
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
	if !identity.User.IsAdmin() {
		AbortWithError(c, apperror.New(apperror.CodeForbidden))
		return
	}
	idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if _, err := uuid.Parse(idempotencyKey); err != nil {
		AbortWithError(c, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
			Field: "Idempotency-Key", Reason: "必须是合法的 UUID",
		}}))
		return
	}
	request, ok := BindJSON[diagnosisTaskRecoveryRequest](c)
	if !ok {
		return
	}
	result, err := r.useCase.Recover(c.Request.Context(), diagnosis.TaskActor{
		UserID: identity.User.ID, IsAdmin: true,
	}, taskID, idempotencyKey, request.Reason)
	if err != nil {
		AbortWithError(c, translateTaskRecoveryError("recover diagnosis task", err))
		return
	}
	status := http.StatusAccepted
	if result.Replayed {
		status = http.StatusOK
	}
	WriteSuccessWithStatus(c, status, diagnosisTaskRecoveryResponse{
		RecoveryID: result.Recovery.ID.String(), TaskID: result.Recovery.TaskID.String(),
		Status: string(diagnosis.TaskPending), Replayed: result.Replayed,
		TaskEventSeq:         result.Recovery.TaskEventSeq,
		PreviousAttemptCount: result.Recovery.PreviousAttemptCount,
		RecoveredAt:          result.Recovery.CreatedAt.UTC().Format(timeRFC3339Nano),
	})
}

func translateTaskRecoveryError(operation string, err error) error {
	switch {
	case errors.Is(err, repository.ErrNotFound):
		return apperror.New(apperror.CodeNotFound)
	case errors.Is(err, diagnosis.ErrTaskRecoveryForbidden):
		return apperror.New(apperror.CodeForbidden)
	case errors.Is(err, diagnosis.ErrInvalidTaskRecovery):
		return apperror.NewWithMessage(apperror.CodeValidationFailed, "恢复请求不合法")
	case errors.Is(err, diagnosis.ErrIdempotencyConflict):
		return apperror.New(apperror.CodeIdempotencyConflict)
	case errors.Is(err, diagnosis.ErrTaskStateConflict):
		return apperror.New(apperror.CodeTaskStateConflict)
	default:
		return apperror.Wrap(apperror.CodeInternal, fmt.Errorf("%s: %w", operation, err))
	}
}

var _ diagnosisTaskRecoveryUseCase = (*diagnosis.TaskRecoveryService)(nil)
