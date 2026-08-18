package httptransport

import (
	"context"
	"errors"
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
	routes, err := NewDiagnosisTaskRoutes(context.Background(), useCase, identityMiddleware(ownerID, false), func(c *gin.Context) { c.Next() })
	if err != nil {
		t.Fatalf("NewDiagnosisTaskRoutes(): %v", err)
	}
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/diagnosis-tasks", strings.NewReader(`{
"externalCaseId":"`+caseID.String()+`",
"expectedSourceFingerprint":"sha256:source",
"requestText":"请检查数据库"
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
	routes, _ := NewDiagnosisTaskRoutes(context.Background(), useCase, identityMiddleware(uuid.New(), false), func(c *gin.Context) { c.Next() })
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

func TestDiagnosisTaskRoutesRejectsAttachmentsOutsideConversation(t *testing.T) {
	caseID := uuid.New()
	useCase := &diagnosisTaskUseCaseStub{}
	routes, _ := NewDiagnosisTaskRoutes(context.Background(), useCase, identityMiddleware(uuid.New(), false), func(c *gin.Context) { c.Next() })
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/diagnosis-tasks", strings.NewReader(`{
"externalCaseId":"`+caseID.String()+`",
"expectedSourceFingerprint":"sha256:source",
"requestText":"检查",
"attachments":[{"attachmentId":"`+uuid.NewString()+`","purpose":"log"}]
}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", uuid.NewString())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"field":"attachments"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if useCase.createCalls != 0 {
		t.Fatalf("create calls = %d, want 0", useCase.createCalls)
	}
}

func TestDiagnosisTaskRoutesMapsSourceChangedConflict(t *testing.T) {
	caseID := uuid.New()
	useCase := &diagnosisTaskUseCaseStub{createErr: diagnosis.ErrSourceChanged}
	routes, _ := NewDiagnosisTaskRoutes(context.Background(), useCase, identityMiddleware(uuid.New(), false), func(c *gin.Context) { c.Next() })
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
	routes, _ := NewDiagnosisTaskRoutes(context.Background(), useCase, identityMiddleware(ownerID, false), func(c *gin.Context) { c.Next() })
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
	routes, _ := NewDiagnosisTaskRoutes(context.Background(), useCase, identityMiddleware(ownerID, false), func(c *gin.Context) { c.Next() })
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/diagnosis-tasks/"+taskID.String()+"/events?afterSeq=7&limit=25", nil))
	if response.Code != http.StatusOK || useCase.gotAfterSeq != 7 || useCase.gotEventLimit != 25 ||
		!strings.Contains(response.Body.String(), `"nextAfterSeq":8`) || !strings.Contains(response.Body.String(), `"eventType":"task_started"`) {
		t.Fatalf("status=%d afterSeq=%d limit=%d body=%s", response.Code, useCase.gotAfterSeq, useCase.gotEventLimit, response.Body.String())
	}
}

func TestDiagnosisTaskRoutesStreamsEventsAndHonorsLastEventID(t *testing.T) {
	ownerID := uuid.New()
	taskID := uuid.New()
	createdAt := time.Date(2026, 8, 3, 6, 0, 0, 0, time.UTC)
	stream := &diagnosisTaskEventStreamStub{
		initialStatus: diagnosis.TaskSucceeded,
		pages: []diagnosis.TaskEventPage{{
			Items: []diagnosis.TaskEvent{{
				TaskID: taskID, Seq: 8, EventType: "task_succeeded",
				Payload:              map[string]any{"reportId": uuid.NewString()},
				PayloadSchemaVersion: 1, CreatedAt: createdAt,
			}},
			AfterSeq: 7, NextAfterSeq: 8,
		}},
	}
	useCase := &diagnosisTaskUseCaseStub{eventStream: stream}
	routes, _ := NewDiagnosisTaskRoutes(context.Background(), useCase, identityMiddleware(ownerID, false), func(c *gin.Context) { c.Next() })
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/diagnosis-tasks/"+taskID.String()+"/events?afterSeq=2&limit=25", nil)
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Last-Event-ID", "7")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status=%d contentType=%q body=%s", response.Code, response.Header().Get("Content-Type"), body)
	}
	for _, expected := range []string{
		"retry: 3000\n\n", "id: 8\n", "event: task_succeeded\n",
		`"seq":8`, `"eventType":"task_succeeded"`, `"createdAt":"2026-08-03T06:00:00Z"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("SSE body missing %q: %s", expected, body)
		}
	}
	if len(stream.gotAfterSeq) != 1 || stream.gotAfterSeq[0] != 7 || stream.gotLimit[0] != 25 {
		t.Fatalf("stream cursors=%v limits=%v", stream.gotAfterSeq, stream.gotLimit)
	}
	if useCase.gotActor.UserID != ownerID || useCase.gotTaskID != taskID || useCase.openStreamCalls != 1 {
		t.Fatalf("actor=%+v taskID=%s calls=%d", useCase.gotActor, useCase.gotTaskID, useCase.openStreamCalls)
	}
}

func TestAcceptsEventStreamHonorsMediaTypeQuality(t *testing.T) {
	tests := []struct {
		accept string
		want   bool
	}{
		{accept: "text/event-stream", want: true},
		{accept: "application/json, text/event-stream; q=0.8", want: true},
		{accept: "text/event-stream; q=0", want: false},
		{accept: "application/json", want: false},
	}
	for _, test := range tests {
		if got := acceptsEventStream(test.accept); got != test.want {
			t.Errorf("acceptsEventStream(%q) = %v, want %v", test.accept, got, test.want)
		}
	}
}

func TestDiagnosisTaskRoutesRejectsInvalidLastEventIDBeforeStreaming(t *testing.T) {
	useCase := &diagnosisTaskUseCaseStub{eventStream: &diagnosisTaskEventStreamStub{}}
	routes, _ := NewDiagnosisTaskRoutes(context.Background(), useCase, identityMiddleware(uuid.New(), false), func(c *gin.Context) { c.Next() })
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/diagnosis-tasks/"+uuid.NewString()+"/events", nil)
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Last-Event-ID", "not-a-sequence")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"field":"Last-Event-ID"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if useCase.openStreamCalls != 0 {
		t.Fatalf("OpenEventStream calls = %d, want 0", useCase.openStreamCalls)
	}
}

func TestDiagnosisTaskRoutesStopsEventStreamWhenApplicationShutsDown(t *testing.T) {
	lifecycle, cancel := context.WithCancel(context.Background())
	streamStarted := make(chan struct{}, 1)
	useCase := &diagnosisTaskUseCaseStub{eventStream: &diagnosisTaskEventStreamStub{
		initialStatus: diagnosis.TaskRunning,
		nextCalled:    streamStarted,
	}}
	routes, _ := NewDiagnosisTaskRoutes(lifecycle, useCase, identityMiddleware(uuid.New(), false), func(c *gin.Context) { c.Next() })
	routes.ssePollInterval = time.Hour
	routes.sseHeartbeatInterval = time.Hour
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/diagnosis-tasks/"+uuid.NewString()+"/events", nil)
	request.Header.Set("Accept", "text/event-stream")
	response := httptest.NewRecorder()
	done := make(chan struct{})

	go func() {
		router.ServeHTTP(response, request)
		close(done)
	}()

	select {
	case <-streamStarted:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("event stream did not start")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("event stream did not stop after application cancellation")
	}
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "retry: 3000") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDiagnosisTaskRoutesReturnsSafeSSEErrorAfterStreamStarted(t *testing.T) {
	taskID := uuid.New()
	stream := &diagnosisTaskEventStreamStub{
		initialStatus: diagnosis.TaskRunning,
		pages: []diagnosis.TaskEventPage{{
			Items: []diagnosis.TaskEvent{{
				TaskID: taskID, Seq: 1, EventType: "task_started",
				Payload: map[string]any{"attemptCount": 1}, PayloadSchemaVersion: 1,
				CreatedAt: time.Date(2026, 8, 3, 6, 0, 0, 0, time.UTC),
			}},
			AfterSeq: 0, NextAfterSeq: 1, HasMore: true,
		}},
		err: errors.New("database password=must-not-leak"),
	}
	useCase := &diagnosisTaskUseCaseStub{eventStream: stream}
	routes, _ := NewDiagnosisTaskRoutes(context.Background(), useCase, identityMiddleware(uuid.New(), false), func(c *gin.Context) { c.Next() })
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/diagnosis-tasks/"+taskID.String()+"/events", nil)
	request.Header.Set("Accept", "text/event-stream")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "event: error\n") ||
		!strings.Contains(body, `"code":50000`) || strings.Contains(body, "must-not-leak") {
		t.Fatalf("status=%d body=%s", response.Code, body)
	}
}

func TestDiagnosisTaskRoutesCancelReturnsAcceptedThenMapsStateConflict(t *testing.T) {
	ownerID := uuid.New()
	taskID := uuid.New()
	useCase := &diagnosisTaskUseCaseStub{cancelResult: diagnosis.TaskCancelResult{
		Task:    diagnosis.DiagnosisTask{ID: taskID, CreatedBy: ownerID, Status: diagnosis.TaskCancelRequested},
		Changed: true,
	}}
	routes, _ := NewDiagnosisTaskRoutes(context.Background(), useCase, identityMiddleware(ownerID, false), func(c *gin.Context) { c.Next() })
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
	createResult    diagnosis.TaskCreateResult
	createErr       error
	createCalls     int
	gotActor        diagnosis.TaskActor
	gotInput        diagnosis.CreateTaskInput
	getTask         diagnosis.DiagnosisTask
	getErr          error
	gotTaskID       uuid.UUID
	eventPage       diagnosis.TaskEventPage
	eventErr        error
	eventStream     diagnosis.TaskEventStream
	openStreamErr   error
	openStreamCalls int
	gotAfterSeq     int64
	gotEventLimit   int
	cancelResult    diagnosis.TaskCancelResult
	cancelErr       error
	listPage        diagnosis.TaskListPage
	listErr         error
	gotListQuery    diagnosis.TaskListQuery
}

func (s *diagnosisTaskUseCaseStub) List(_ context.Context, query diagnosis.TaskListQuery) (diagnosis.TaskListPage, error) {
	s.gotListQuery = query
	if s.listErr != nil {
		return diagnosis.TaskListPage{}, s.listErr
	}
	return s.listPage, nil
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

func (s *diagnosisTaskUseCaseStub) OpenEventStream(
	_ context.Context,
	actor diagnosis.TaskActor,
	taskID uuid.UUID,
) (diagnosis.TaskEventStream, error) {
	s.openStreamCalls++
	s.gotActor, s.gotTaskID = actor, taskID
	if s.openStreamErr != nil {
		return nil, s.openStreamErr
	}
	return s.eventStream, nil
}

func (s *diagnosisTaskUseCaseStub) Cancel(_ context.Context, actor diagnosis.TaskActor, taskID uuid.UUID) (diagnosis.TaskCancelResult, error) {
	s.gotActor, s.gotTaskID = actor, taskID
	if s.cancelErr != nil {
		return diagnosis.TaskCancelResult{}, s.cancelErr
	}
	return s.cancelResult, nil
}

type diagnosisTaskEventStreamStub struct {
	initialStatus diagnosis.TaskStatus
	pages         []diagnosis.TaskEventPage
	err           error
	gotAfterSeq   []int64
	gotLimit      []int
	nextCalled    chan<- struct{}
}

func (s *diagnosisTaskEventStreamStub) InitialStatus() diagnosis.TaskStatus {
	return s.initialStatus
}

func (s *diagnosisTaskEventStreamStub) Next(
	_ context.Context,
	afterSeq int64,
	limit int,
) (diagnosis.TaskEventPage, error) {
	s.gotAfterSeq = append(s.gotAfterSeq, afterSeq)
	s.gotLimit = append(s.gotLimit, limit)
	if s.nextCalled != nil {
		select {
		case s.nextCalled <- struct{}{}:
		default:
		}
	}
	if len(s.pages) > 0 {
		page := s.pages[0]
		s.pages = s.pages[1:]
		return page, nil
	}
	if s.err != nil {
		return diagnosis.TaskEventPage{}, s.err
	}
	return diagnosis.TaskEventPage{AfterSeq: afterSeq, NextAfterSeq: afterSeq}, nil
}
