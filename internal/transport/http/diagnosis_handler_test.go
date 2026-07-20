package httptransport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/diagnosis"
)

type testRunStore struct {
	runs   map[string]diagnosis.Run
	events map[string][]diagnosis.Event
}

func newTestRunStore() *testRunStore {
	return &testRunStore{runs: map[string]diagnosis.Run{}, events: map[string][]diagnosis.Event{}}
}

func (s *testRunStore) Create(_ context.Context, run diagnosis.Run, event diagnosis.Event) error {
	s.runs[run.ID] = run
	s.events[run.ID] = []diagnosis.Event{event}
	return nil
}

func (s *testRunStore) Get(_ context.Context, runID string) (diagnosis.Run, error) {
	run, ok := s.runs[runID]
	if !ok {
		return diagnosis.Run{}, diagnosis.ErrRunNotFound
	}
	return run, nil
}

func (s *testRunStore) ListEvents(_ context.Context, runID string) ([]diagnosis.Event, error) {
	if _, ok := s.runs[runID]; !ok {
		return nil, diagnosis.ErrRunNotFound
	}
	return s.events[runID], nil
}

func TestCreateDiagnosticRun(t *testing.T) {
	router := NewRouter(diagnosis.NewService(newTestRunStore()), func(context.Context) error { return nil })
	request := httptest.NewRequest(http.MethodPost, "/api/v1/diagnostic-runs", strings.NewReader(`{
		"subjectType":"work_order",
		"subjectId":"WO-1001",
		"question":"Why is production delayed?"
	}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"status":"queued"`) {
		t.Fatalf("response = %s", response.Body.String())
	}
}
