package diagnosis

import "time"

type RunStatus string

const (
	RunStatusQueued    RunStatus = "queued"
	RunStatusRunning   RunStatus = "running"
	RunStatusSucceeded RunStatus = "succeeded"
	RunStatusFailed    RunStatus = "failed"
)

type Run struct {
	ID          string
	SubjectType string
	SubjectID   string
	Request     string
	Status      RunStatus
	Summary     string
	Error       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type EventType string

const EventTypeRunCreated EventType = "run.created"

type Event struct {
	RunID     string
	Sequence  int64
	Type      EventType
	Payload   map[string]any
	CreatedAt time.Time
}
