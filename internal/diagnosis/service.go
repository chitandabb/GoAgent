package diagnosis

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalidRun  = errors.New("invalid diagnostic run")
	ErrRunNotFound = errors.New("diagnostic run not found")
)

type StartCommand struct {
	SubjectType string
	SubjectID   string
	Question    string
}

type Service struct {
	runs  RunStore
	now   func() time.Time
	newID func() string
}

func NewService(runs RunStore) *Service {
	return &Service{
		runs:  runs,
		now:   func() time.Time { return time.Now().UTC() },
		newID: uuid.NewString,
	}
}

func (s *Service) Start(ctx context.Context, command StartCommand) (Run, error) {
	command.SubjectType = strings.TrimSpace(command.SubjectType)
	command.SubjectID = strings.TrimSpace(command.SubjectID)
	command.Question = strings.TrimSpace(command.Question)
	if command.SubjectType == "" || command.SubjectID == "" || command.Question == "" {
		return Run{}, fmt.Errorf("%w: subjectType, subjectId, and question are required", ErrInvalidRun)
	}
	if len(command.Question) > 8000 {
		return Run{}, fmt.Errorf("%w: question exceeds 8000 characters", ErrInvalidRun)
	}

	now := s.now()
	run := Run{
		ID:          s.newID(),
		SubjectType: command.SubjectType,
		SubjectID:   command.SubjectID,
		Request:     command.Question,
		Status:      RunStatusQueued,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	event := Event{
		RunID:     run.ID,
		Sequence:  1,
		Type:      EventTypeRunCreated,
		Payload:   map[string]any{"status": run.Status},
		CreatedAt: now,
	}
	if err := s.runs.Create(ctx, run, event); err != nil {
		return Run{}, fmt.Errorf("create diagnostic run: %w", err)
	}
	return run, nil
}

func (s *Service) Get(ctx context.Context, runID string) (Run, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return Run{}, fmt.Errorf("%w: run id is required", ErrInvalidRun)
	}
	return s.runs.Get(ctx, runID)
}

func (s *Service) Events(ctx context.Context, runID string) ([]Event, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, fmt.Errorf("%w: run id is required", ErrInvalidRun)
	}
	return s.runs.ListEvents(ctx, runID)
}
