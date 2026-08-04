package httptransport

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
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

type knowledgeIngestionUseCaseStub struct {
	input  knowledge.QueueSourceInput
	bytes  []byte
	result knowledge.QueueVersionResult
	err    error
	calls  int
}

func (s *knowledgeIngestionUseCaseStub) QueueSource(_ context.Context, input knowledge.QueueSourceInput) (knowledge.QueueVersionResult, error) {
	s.calls++
	s.input = input
	if input.Content != nil {
		s.bytes, _ = io.ReadAll(input.Content)
	}
	return s.result, s.err
}

func TestKnowledgeIngestionCreateDocumentQueuesGlobalVersion(t *testing.T) {
	userID := uuid.New()
	documentID, versionID, taskID := uuid.New(), uuid.New(), uuid.New()
	createdAt := time.Date(2026, 8, 4, 2, 3, 4, 0, time.UTC)
	useCase := &knowledgeIngestionUseCaseStub{result: knowledge.QueueVersionResult{
		Version: knowledge.DocumentVersion{ID: versionID, DocumentID: documentID, Version: 1, CreatedAt: createdAt},
		Task: knowledge.IngestionTask{
			ID: taskID, DocumentVersionID: versionID, Status: knowledge.IngestionPending,
			Stage: knowledge.IngestionStageUploaded, MaxAttempts: 3, CreatedAt: createdAt,
		},
	}}
	router := newKnowledgeIngestionTestRouter(t, useCase, userID, true, 1024)
	request := knowledgeMultipartRequest(t, http.MethodPost, "/api/v1/admin/knowledge-documents", map[string]string{
		"title": "ERP 运维手册",
	}, "file", "manual.md", []byte("# timeout\ncheck gateway"))
	request.Header.Set("Idempotency-Key", uuid.NewString())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"replayed":false`) {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if useCase.calls != 1 || useCase.input.CreatedBy != userID || useCase.input.NewDocument == nil ||
		useCase.input.NewDocument.Scope != knowledge.ScopeGlobal || useCase.input.NewDocument.Title != "ERP 运维手册" {
		t.Fatalf("input = %+v", useCase.input)
	}
	if string(useCase.bytes) != "# timeout\ncheck gateway" || useCase.input.MediaType != "text/markdown; charset=utf-8" ||
		useCase.input.ExpectedSourceSHA256 == "" || useCase.input.RequestFingerprint == "" {
		t.Fatalf("uploaded input = %+v bytes = %q", useCase.input, useCase.bytes)
	}
}

func TestKnowledgeIngestionReplayReturnsOK(t *testing.T) {
	createdAt := time.Now().UTC()
	versionID := uuid.New()
	useCase := &knowledgeIngestionUseCaseStub{result: knowledge.QueueVersionResult{
		Version: knowledge.DocumentVersion{ID: versionID, DocumentID: uuid.New(), Version: 2, CreatedAt: createdAt},
		Task: knowledge.IngestionTask{ID: uuid.New(), DocumentVersionID: versionID, Status: knowledge.IngestionPending,
			Stage: knowledge.IngestionStageUploaded, CreatedAt: createdAt},
		Replayed: true,
	}}
	router := newKnowledgeIngestionTestRouter(t, useCase, uuid.New(), true, 1024)
	request := knowledgeMultipartRequest(t, http.MethodPost, "/api/v1/admin/knowledge-documents", map[string]string{
		"title": "Manual",
	}, "file", "manual.txt", []byte("content"))
	request.Header.Set("Idempotency-Key", uuid.NewString())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"replayed":true`) {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestKnowledgeIngestionRequiresAdministrator(t *testing.T) {
	useCase := &knowledgeIngestionUseCaseStub{}
	router := newKnowledgeIngestionTestRouter(t, useCase, uuid.New(), false, 1024)
	request := knowledgeMultipartRequest(t, http.MethodPost, "/api/v1/admin/knowledge-documents", nil,
		"file", "manual.txt", []byte("content"))
	request.Header.Set("Idempotency-Key", uuid.NewString())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || useCase.calls != 0 {
		t.Fatalf("response = %d %s calls = %d", response.Code, response.Body.String(), useCase.calls)
	}
}

func TestKnowledgeIngestionRejectsInvalidSignatureAndOversize(t *testing.T) {
	tests := []struct {
		name, fileName string
		content        []byte
		limit          int64
	}{
		{name: "PDF signature mismatch", fileName: "manual.pdf", content: []byte("not-pdf"), limit: 1024},
		{name: "file too large", fileName: "manual.txt", content: []byte("too large"), limit: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			useCase := &knowledgeIngestionUseCaseStub{}
			router := newKnowledgeIngestionTestRouter(t, useCase, uuid.New(), true, tt.limit)
			request := knowledgeMultipartRequest(t, http.MethodPost, "/api/v1/admin/knowledge-documents", map[string]string{
				"title": "Manual",
			}, "file", tt.fileName, tt.content)
			request.Header.Set("Idempotency-Key", uuid.NewString())
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || useCase.calls != 0 {
				t.Fatalf("response = %d %s calls = %d", response.Code, response.Body.String(), useCase.calls)
			}
		})
	}
}

func TestKnowledgeIngestionMapsIdempotencyConflict(t *testing.T) {
	useCase := &knowledgeIngestionUseCaseStub{err: knowledge.ErrIdempotencyConflict}
	router := newKnowledgeIngestionTestRouter(t, useCase, uuid.New(), true, 1024)
	request := knowledgeMultipartRequest(t, http.MethodPost, "/api/v1/admin/knowledge-documents", map[string]string{
		"title": "Manual",
	}, "file", "manual.txt", []byte("content"))
	request.Header.Set("Idempotency-Key", uuid.NewString())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":40911`) {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestKnowledgeIngestionNewVersionRejectsTitleMutation(t *testing.T) {
	useCase := &knowledgeIngestionUseCaseStub{}
	router := newKnowledgeIngestionTestRouter(t, useCase, uuid.New(), true, 1024)
	request := knowledgeMultipartRequest(t, http.MethodPost,
		"/api/v1/admin/knowledge-documents/"+uuid.NewString()+"/versions", map[string]string{
			"title": "renamed",
		}, "file", "manual.txt", []byte("content"))
	request.Header.Set("Idempotency-Key", uuid.NewString())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || useCase.calls != 0 {
		t.Fatalf("response = %d %s calls = %d", response.Code, response.Body.String(), useCase.calls)
	}
}

func newKnowledgeIngestionTestRouter(
	t *testing.T, useCase knowledgeIngestionUseCase, userID uuid.UUID, admin bool, maxBytes int64,
) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	routes, err := NewKnowledgeIngestionRoutes(
		useCase, identityMiddleware(userID, admin), func(c *gin.Context) { c.Next() },
		maxBytes, "ingestion-v1", 3,
	)
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
}

func knowledgeMultipartRequest(
	t *testing.T, method, target string, fields map[string]string,
	fileField, fileName string, content []byte,
) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile(fileField, fileName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, target, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}
