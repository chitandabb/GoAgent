package httptransport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type knowledgeTaskUseCaseStub struct {
	detail          knowledge.IngestionTaskDetail
	getErr          error
	cancelResult    knowledge.IngestionCancelResult
	cancelErr       error
	cancelRequested uuid.UUID
}

func (s *knowledgeTaskUseCaseStub) Get(context.Context, uuid.UUID) (knowledge.IngestionTaskDetail, error) {
	return s.detail, s.getErr
}

func (s *knowledgeTaskUseCaseStub) Cancel(
	_ context.Context, _ uuid.UUID, requestedBy uuid.UUID,
) (knowledge.IngestionCancelResult, error) {
	s.cancelRequested = requestedBy
	return s.cancelResult, s.cancelErr
}

func TestKnowledgeIngestionTaskGetReturnsProgressWithoutCheckpointOrObjectKey(t *testing.T) {
	adminID := uuid.New()
	detail := validKnowledgeTaskDetail()
	useCase := &knowledgeTaskUseCaseStub{detail: detail}
	router := newKnowledgeTaskTestRouter(t, useCase, adminID, true)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/admin/knowledge-ingestion-tasks/"+detail.ID.String(), nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"progressPercent":45`) ||
		strings.Contains(response.Body.String(), "checkpoint") || strings.Contains(response.Body.String(), "objectKey") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestKnowledgeIngestionTaskCancelReturnsAcceptedThenOKOnReplay(t *testing.T) {
	adminID := uuid.New()
	detail := validKnowledgeTaskDetail()
	detail.Status = knowledge.IngestionCancelRequested
	now := time.Now().UTC()
	detail.CancelRequestedAt = &now
	useCase := &knowledgeTaskUseCaseStub{cancelResult: knowledge.IngestionCancelResult{Task: detail, Changed: true}}
	router := newKnowledgeTaskTestRouter(t, useCase, adminID, true)
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/admin/knowledge-ingestion-tasks/"+detail.ID.String()+"/cancel", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || useCase.cancelRequested != adminID ||
		!strings.Contains(response.Body.String(), `"cancellationChanged":true`) {
		t.Fatalf("response = %d %s requestedBy=%s", response.Code, response.Body.String(), useCase.cancelRequested)
	}

	useCase.cancelResult.Changed = false
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/admin/knowledge-ingestion-tasks/"+detail.ID.String()+"/cancel", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("replay response = %d %s", response.Code, response.Body.String())
	}
}

func TestKnowledgeIngestionTaskRoutesRequireAdmin(t *testing.T) {
	detail := validKnowledgeTaskDetail()
	useCase := &knowledgeTaskUseCaseStub{detail: detail}
	router := newKnowledgeTaskTestRouter(t, useCase, uuid.New(), false)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/admin/knowledge-ingestion-tasks/"+detail.ID.String(), nil))
	if response.Code != http.StatusForbidden {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func newKnowledgeTaskTestRouter(
	t *testing.T, useCase knowledgeIngestionTaskUseCase, userID uuid.UUID, admin bool,
) *gin.Engine {
	t.Helper()
	routes, err := NewKnowledgeIngestionTaskRoutes(
		useCase, identityMiddleware(userID, admin), func(c *gin.Context) { c.Next() },
	)
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
}

func validKnowledgeTaskDetail() knowledge.IngestionTaskDetail {
	now := time.Now().UTC()
	return knowledge.IngestionTaskDetail{
		ID: uuid.New(), DocumentVersionID: uuid.New(), DocumentID: uuid.New(),
		Status: knowledge.IngestionRunning, Stage: knowledge.IngestionStageParsing,
		AttemptCount: 1, MaxAttempts: 3, ProgressPercent: 45,
		StartedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
}
