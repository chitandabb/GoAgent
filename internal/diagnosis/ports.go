package diagnosis

import "context"

// RunStore defines the persistence boundary for durable diagnostic state and
// its event stream. Implementations must persist a new run and its first event
// atomically.
type RunStore interface {
	Create(ctx context.Context, run Run, firstEvent Event) error
	Get(ctx context.Context, runID string) (Run, error)
	ListEvents(ctx context.Context, runID string) ([]Event, error)
}

// MESReader is the future read-only boundary for the diagnosed MES database.
// The application depends on business data, not a SQL Server driver.
type MESReader interface {
	ReadSubject(ctx context.Context, subjectType, subjectID string) (map[string]any, error)
}

// Agent is the future LLM boundary. Eino is one implementation of this port.
type Agent interface {
	Diagnose(ctx context.Context, input AgentInput) (AgentOutput, error)
}

type AgentInput struct {
	SubjectType string
	SubjectID   string
	Question    string
	MESData     map[string]any
}

type AgentOutput struct {
	Summary string
}
