package postgresdiagnosis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"GopherAI/internal/diagnosis"

	"gorm.io/gorm"
)

type RunStore struct {
	db *gorm.DB
}

func NewRunStore(db *gorm.DB) *RunStore {
	return &RunStore{db: db}
}

func (s *RunStore) Create(ctx context.Context, run diagnosis.Run, firstEvent diagnosis.Event) error {
	payload, err := json.Marshal(firstEvent.Payload)
	if err != nil {
		return fmt.Errorf("marshal diagnostic event payload: %w", err)
	}
	runRecord := runRecordFromDomain(run)
	eventRecord := eventRecord{
		RunID:     firstEvent.RunID,
		Sequence:  firstEvent.Sequence,
		EventType: string(firstEvent.Type),
		Payload:   payload,
		CreatedAt: firstEvent.CreatedAt,
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&runRecord).Error; err != nil {
			return err
		}
		return tx.Create(&eventRecord).Error
	})
}

func (s *RunStore) Get(ctx context.Context, runID string) (diagnosis.Run, error) {
	var record runRecord
	err := s.db.WithContext(ctx).Where("id = ?", runID).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return diagnosis.Run{}, diagnosis.ErrRunNotFound
	}
	if err != nil {
		return diagnosis.Run{}, fmt.Errorf("get diagnostic run: %w", err)
	}
	return record.toDomain(), nil
}

func (s *RunStore) ListEvents(ctx context.Context, runID string) ([]diagnosis.Event, error) {
	if _, err := s.Get(ctx, runID); err != nil {
		return nil, err
	}
	var records []eventRecord
	if err := s.db.WithContext(ctx).
		Where("run_id = ?", runID).
		Order("sequence ASC").
		Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list diagnostic events: %w", err)
	}
	events := make([]diagnosis.Event, 0, len(records))
	for _, record := range records {
		event, err := record.toDomain()
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

type runRecord struct {
	ID          string    `gorm:"column:id;primaryKey"`
	SubjectType string    `gorm:"column:subject_type"`
	SubjectID   string    `gorm:"column:subject_id"`
	Request     string    `gorm:"column:request_text"`
	Status      string    `gorm:"column:status"`
	Summary     string    `gorm:"column:summary"`
	Error       string    `gorm:"column:error_text"`
	CreatedAt   time.Time `gorm:"column:created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

func (runRecord) TableName() string { return "mesguard_diagnostic_runs" }

func runRecordFromDomain(run diagnosis.Run) runRecord {
	return runRecord{
		ID:          run.ID,
		SubjectType: run.SubjectType,
		SubjectID:   run.SubjectID,
		Request:     run.Request,
		Status:      string(run.Status),
		Summary:     run.Summary,
		Error:       run.Error,
		CreatedAt:   run.CreatedAt,
		UpdatedAt:   run.UpdatedAt,
	}
}

func (r runRecord) toDomain() diagnosis.Run {
	return diagnosis.Run{
		ID:          r.ID,
		SubjectType: r.SubjectType,
		SubjectID:   r.SubjectID,
		Request:     r.Request,
		Status:      diagnosis.RunStatus(r.Status),
		Summary:     r.Summary,
		Error:       r.Error,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
}

type eventRecord struct {
	ID        int64     `gorm:"column:id"`
	RunID     string    `gorm:"column:run_id"`
	Sequence  int64     `gorm:"column:sequence"`
	EventType string    `gorm:"column:event_type"`
	Payload   []byte    `gorm:"column:payload"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

func (eventRecord) TableName() string { return "mesguard_diagnostic_events" }

func (r eventRecord) toDomain() (diagnosis.Event, error) {
	payload := map[string]any{}
	if err := json.Unmarshal(r.Payload, &payload); err != nil {
		return diagnosis.Event{}, fmt.Errorf("decode diagnostic event %d payload: %w", r.ID, err)
	}
	return diagnosis.Event{
		RunID:     r.RunID,
		Sequence:  r.Sequence,
		Type:      diagnosis.EventType(r.EventType),
		Payload:   payload,
		CreatedAt: r.CreatedAt,
	}, nil
}
