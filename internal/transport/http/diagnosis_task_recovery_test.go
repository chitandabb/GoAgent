package httptransport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

func TestDiagnosisTaskRecoveryRoutesReturnsAcceptedThenReplay(t *testing.T) {
	adminID := uuid.New()
	taskID := uuid.New()
	recoveryID := uuid.New()
	now := time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC)
	useCase := &diagnosisTaskRecoveryUseCaseStub{result: diagnosis.TaskRecoveryResult{
		Recovery: diagnosis.TaskRecovery{
			ID: recoveryID, TaskID: taskID, RecoveredBy: adminID,
			PreviousAttemptCount: 4, TaskEventSeq: 7, CreatedAt: now,
		},
	}}
	routes, err := NewDiagnosisTaskRecoveryRoutes(
		useCase, identityMiddleware(adminID, true), func(c *gin.Context) { c.Next() },
	)
	if err != nil {
		t.Fatalf("NewDiagnosisTaskRecoveryRoutes(): %v", err)
	}
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	key := uuid.NewString()
	request := recoveryRequest(taskID, key, `{"reason":"模型服务已恢复"}`)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"status":"pending"`) ||
		!strings.Contains(response.Body.String(), `"previousAttemptCount":4`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if useCase.actor.UserID != adminID || !useCase.actor.IsAdmin || useCase.taskID != taskID ||
		useCase.key != key || useCase.reason != "模型服务已恢复" {
		t.Fatalf("actor=%+v task=%s key=%q reason=%q", useCase.actor, useCase.taskID, useCase.key, useCase.reason)
	}

	useCase.result.Replayed = true
	response = httptest.NewRecorder()
	router.ServeHTTP(response, recoveryRequest(taskID, key, `{"reason":"模型服务已恢复"}`))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"replayed":true`) {
		t.Fatalf("replay status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDiagnosisTaskRecoveryRoutesRejectsAnalystBeforeUseCase(t *testing.T) {
	useCase := &diagnosisTaskRecoveryUseCaseStub{}
	routes, _ := NewDiagnosisTaskRecoveryRoutes(
		useCase, identityMiddleware(uuid.New(), false), func(c *gin.Context) { c.Next() },
	)
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, recoveryRequest(uuid.New(), uuid.NewString(), `{"reason":"恢复"}`))
	if response.Code != http.StatusForbidden || useCase.calls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, useCase.calls, response.Body.String())
	}
}

func TestDiagnosisTaskRecoveryRoutesValidatesKeyAndMapsConflicts(t *testing.T) {
	taskID := uuid.New()
	tests := []struct {
		name string
		key  string
		err  error
		want int
		code string
	}{
		{name: "invalid key", key: "bad-key", want: http.StatusBadRequest, code: `"code":40001`},
		{name: "idempotency conflict", key: uuid.NewString(), err: diagnosis.ErrIdempotencyConflict, want: http.StatusConflict, code: `"code":40911`},
		{name: "state conflict", key: uuid.NewString(), err: diagnosis.ErrTaskStateConflict, want: http.StatusConflict, code: `"code":40921`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			useCase := &diagnosisTaskRecoveryUseCaseStub{err: test.err}
			routes, _ := NewDiagnosisTaskRecoveryRoutes(
				useCase, identityMiddleware(uuid.New(), true), func(c *gin.Context) { c.Next() },
			)
			router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, recoveryRequest(taskID, test.key, `{"reason":"恢复"}`))
			if response.Code != test.want || !strings.Contains(response.Body.String(), test.code) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func recoveryRequest(taskID uuid.UUID, key, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/admin/diagnosis-tasks/"+taskID.String()+"/recover", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", key)
	return request
}

type diagnosisTaskRecoveryUseCaseStub struct {
	result diagnosis.TaskRecoveryResult
	err    error
	calls  int
	actor  diagnosis.TaskActor
	taskID uuid.UUID
	key    string
	reason string
}

func (s *diagnosisTaskRecoveryUseCaseStub) Recover(
	_ context.Context,
	actor diagnosis.TaskActor,
	taskID uuid.UUID,
	key, reason string,
) (diagnosis.TaskRecoveryResult, error) {
	s.calls++
	s.actor, s.taskID, s.key, s.reason = actor, taskID, key, reason
	return s.result, s.err
}
