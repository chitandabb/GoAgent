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

func TestDiagnosisTaskRoutesCreateReturnsAcceptedAndLocation(t *testing.T) {
	ownerID := uuid.New()
	caseID := uuid.New()
	taskID := uuid.New()
	useCase := &diagnosisTaskUseCaseStub{createResult: diagnosis.TaskCreateResult{
		Task: diagnosis.DiagnosisTask{ID: taskID, ExternalCaseID: caseID, Status: diagnosis.TaskPending, CreatedAt: time.Date(2026, 8, 2, 4, 0, 0, 0, time.UTC)},
	}}
	routes, err := NewDiagnosisTaskRoutes(useCase, identityMiddleware(ownerID, false), func(c *gin.Context) { c.Next() })
	if err != nil {
		t.Fatalf("NewDiagnosisTaskRoutes(): %v", err)
	}
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/diagnosis-tasks", strings.NewReader(`{
"externalCaseId":"`+caseID.String()+`",
"expectedSourceFingerprint":"sha256:source",
"requestText":"请检查数据库",
"requestScope":{"timeRange":{"from":"today"}}
}`))
	request.Header.Set("Content-Type", "application/json")
	idempotencyKey := uuid.NewString()
	request.Header.Set("Idempotency-Key", idempotencyKey)
	request.Header.Set("X-Request-ID", uuid.NewString())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted || response.Header().Get("Location") != "/api/v1/diagnosis-tasks/"+taskID.String() {
		t.Fatalf("status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	if useCase.gotActor.UserID != ownerID || useCase.gotInput.ExternalCaseID != caseID || useCase.gotInput.IdempotencyKey != idempotencyKey {
		t.Fatalf("use case args actor=%+v input=%+v", useCase.gotActor, useCase.gotInput)
	}
}

func TestDiagnosisTaskRoutesRejectsMissingIdempotencyKey(t *testing.T) {
	caseID := uuid.New()
	useCase := &diagnosisTaskUseCaseStub{}
	routes, _ := NewDiagnosisTaskRoutes(useCase, identityMiddleware(uuid.New(), false), func(c *gin.Context) { c.Next() })
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/diagnosis-tasks", strings.NewReader(`{"externalCaseId":"`+caseID.String()+`","expectedSourceFingerprint":"sha256:source","requestText":"检查"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "Idempotency-Key") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if useCase.createCalls != 0 {
		t.Fatalf("create calls = %d, want 0", useCase.createCalls)
	}
}

func TestDiagnosisTaskRoutesMapsSourceChangedConflict(t *testing.T) {
	caseID := uuid.New()
	useCase := &diagnosisTaskUseCaseStub{createErr: diagnosis.ErrSourceChanged}
	routes, _ := NewDiagnosisTaskRoutes(useCase, identityMiddleware(uuid.New(), false), func(c *gin.Context) { c.Next() })
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/diagnosis-tasks", strings.NewReader(`{"externalCaseId":"`+caseID.String()+`","expectedSourceFingerprint":"sha256:source","requestText":"检查"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", uuid.NewString())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `40923`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDiagnosisTaskRoutesGetPassesActor(t *testing.T) {
	ownerID := uuid.New()
	taskID := uuid.New()
	useCase := &diagnosisTaskUseCaseStub{getTask: diagnosis.DiagnosisTask{ID: taskID, CreatedBy: ownerID, Status: diagnosis.TaskPending}}
	routes, _ := NewDiagnosisTaskRoutes(useCase, identityMiddleware(ownerID, false), func(c *gin.Context) { c.Next() })
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/diagnosis-tasks/"+taskID.String(), nil))
	if response.Code != http.StatusOK || useCase.gotActor.UserID != ownerID || useCase.gotTaskID != taskID {
		t.Fatalf("status=%d actor=%+v taskID=%s body=%s", response.Code, useCase.gotActor, useCase.gotTaskID, response.Body.String())
	}
}

func TestDiagnosisTaskRoutesListEventsPassesCursorAndReturnsPage(t *testing.T) {
	ownerID := uuid.New()
	taskID := uuid.New()
	createdAt := time.Date(2026, 8, 2, 5, 0, 0, 0, time.UTC)
	useCase := &diagnosisTaskUseCaseStub{eventPage: diagnosis.TaskEventPage{
		Items: []diagnosis.TaskEvent{{
			TaskID: taskID, Seq: 8, EventType: "task_started", Payload: map[string]any{"attemptCount": 1},
			PayloadSchemaVersion: 1, CreatedAt: createdAt,
		}},
		AfterSeq: 7, NextAfterSeq: 8, HasMore: false,
	}}
	routes, _ := NewDiagnosisTaskRoutes(useCase, identityMiddleware(ownerID, false), func(c *gin.Context) { c.Next() })
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/diagnosis-tasks/"+taskID.String()+"/events?afterSeq=7&limit=25", nil))
	if response.Code != http.StatusOK || useCase.gotAfterSeq != 7 || useCase.gotEventLimit != 25 ||
		!strings.Contains(response.Body.String(), `"nextAfterSeq":8`) || !strings.Contains(response.Body.String(), `"eventType":"task_started"`) {
		t.Fatalf("status=%d afterSeq=%d limit=%d body=%s", response.Code, useCase.gotAfterSeq, useCase.gotEventLimit, response.Body.String())
	}
}

func TestDiagnosisTaskRoutesCancelReturnsAcceptedThenMapsStateConflict(t *testing.T) {
	ownerID := uuid.New()
	taskID := uuid.New()
	useCase := &diagnosisTaskUseCaseStub{cancelResult: diagnosis.TaskCancelResult{
		Task:    diagnosis.DiagnosisTask{ID: taskID, CreatedBy: ownerID, Status: diagnosis.TaskCancelRequested},
		Changed: true,
	}}
	routes, _ := NewDiagnosisTaskRoutes(useCase, identityMiddleware(ownerID, false), func(c *gin.Context) { c.Next() })
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/diagnosis-tasks/"+taskID.String()+"/cancel", nil))
	if response.Code != http.StatusAccepted || useCase.gotTaskID != taskID || !strings.Contains(response.Body.String(), `"status":"cancel_requested"`) {
		t.Fatalf("status=%d taskID=%s body=%s", response.Code, useCase.gotTaskID, response.Body.String())
	}

	useCase.cancelErr = diagnosis.ErrTaskStateConflict
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/diagnosis-tasks/"+taskID.String()+"/cancel", nil))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `40921`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

type diagnosisTaskUseCaseStub struct {
	createResult  diagnosis.TaskCreateResult
	createErr     error
	createCalls   int
	gotActor      diagnosis.TaskActor
	gotInput      diagnosis.CreateTaskInput
	getTask       diagnosis.DiagnosisTask
	getErr        error
	gotTaskID     uuid.UUID
	eventPage     diagnosis.TaskEventPage
	eventErr      error
	gotAfterSeq   int64
	gotEventLimit int
	cancelResult  diagnosis.TaskCancelResult
	cancelErr     error
}

func (s *diagnosisTaskUseCaseStub) Create(_ context.Context, actor diagnosis.TaskActor, input diagnosis.CreateTaskInput) (diagnosis.TaskCreateResult, error) {
	s.createCalls++
	s.gotActor, s.gotInput = actor, input
	return s.createResult, s.createErr
}

func (s *diagnosisTaskUseCaseStub) Get(_ context.Context, actor diagnosis.TaskActor, taskID uuid.UUID) (diagnosis.DiagnosisTask, error) {
	s.gotActor, s.gotTaskID = actor, taskID
	if s.getErr != nil {
		return diagnosis.DiagnosisTask{}, s.getErr
	}
	return s.getTask, nil
}

func (s *diagnosisTaskUseCaseStub) ListEvents(_ context.Context, actor diagnosis.TaskActor, taskID uuid.UUID, afterSeq int64, limit int) (diagnosis.TaskEventPage, error) {
	s.gotActor, s.gotTaskID, s.gotAfterSeq, s.gotEventLimit = actor, taskID, afterSeq, limit
	if s.eventErr != nil {
		return diagnosis.TaskEventPage{}, s.eventErr
	}
	return s.eventPage, nil
}

func (s *diagnosisTaskUseCaseStub) Cancel(_ context.Context, actor diagnosis.TaskActor, taskID uuid.UUID) (diagnosis.TaskCancelResult, error) {
	s.gotActor, s.gotTaskID = actor, taskID
	if s.cancelErr != nil {
		return diagnosis.TaskCancelResult{}, s.cancelErr
	}
	return s.cancelResult, nil
}
