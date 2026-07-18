package diagnosis

import (
	"context"
	"errors"
	"testing"
	"time"
)

type memoryRunStore struct {
	runs   map[string]Run
	events map[string][]Event
}

func newMemoryRunStore() *memoryRunStore {
	return &memoryRunStore{runs: map[string]Run{}, events: map[string][]Event{}}
}

func (s *memoryRunStore) Create(_ context.Context, run Run, event Event) error {
	s.runs[run.ID] = run
	s.events[run.ID] = []Event{event}
	return nil
}

func (s *memoryRunStore) Get(_ context.Context, runID string) (Run, error) {
	run, ok := s.runs[runID]
	if !ok {
		return Run{}, ErrRunNotFound
	}
	return run, nil
}

func (s *memoryRunStore) ListEvents(_ context.Context, runID string) ([]Event, error) {
	if _, ok := s.runs[runID]; !ok {
		return nil, ErrRunNotFound
	}
	return s.events[runID], nil
}

func TestServiceStartPersistsRunAndInitialEvent(t *testing.T) {
	store := newMemoryRunStore()
	service := NewService(store)
	service.now = func() time.Time { return time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC) }
	service.newID = func() string { return "run-1" }

	run, err := service.Start(context.Background(), StartCommand{
		SubjectType: "work_order",
		SubjectID:   "WO-1001",
		Question:    "Why is the order delayed?",
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if run.Status != RunStatusQueued || run.ID != "run-1" {
		t.Fatalf("unexpected run: %#v", run)
	}
	events, err := service.Events(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(events) != 1 || events[0].Type != EventTypeRunCreated {
		t.Fatalf("unexpected events: %#v", events)
	}
}

func TestServiceStartRejectsMissingFields(t *testing.T) {
	service := NewService(newMemoryRunStore())
	_, err := service.Start(context.Background(), StartCommand{SubjectType: "work_order"})
	if !errors.Is(err, ErrInvalidRun) {
		t.Fatalf("Start() error = %v, want ErrInvalidRun", err)
	}
}
