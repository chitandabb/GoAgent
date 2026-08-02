package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/messaging"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type OutboxEventRepository struct {
	db *gorm.DB
}

var _ messaging.OutboxRepository = (*OutboxEventRepository)(nil)

func NewOutboxEventRepository(db *gorm.DB) *OutboxEventRepository {
	return &OutboxEventRepository{db: db}
}

func (r *OutboxEventRepository) ClaimOutboxEvents(
	ctx context.Context,
	owner string,
	now, lockedUntil time.Time,
	limit int,
) ([]messaging.OutboxEvent, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("outbox event repository is unavailable")
	}
	owner = strings.TrimSpace(owner)
	if owner == "" || len(owner) > 128 || limit < 1 || limit > 100 || !lockedUntil.After(now) {
		return nil, errors.New("outbox claim is invalid")
	}
	var records []outboxEventRecord
	result := ResolveDB(ctx, r.db).Raw(`
WITH candidates AS (
    SELECT id
    FROM outbox_events
    WHERE published_at IS NULL
      AND available_at <= ?
      AND (locked_until IS NULL OR locked_until <= ?)
    ORDER BY available_at ASC, created_at ASC, id ASC
    FOR UPDATE SKIP LOCKED
    LIMIT ?
)
UPDATE outbox_events AS event
SET locked_at = ?, locked_by = ?, locked_until = ?
FROM candidates
WHERE event.id = candidates.id
RETURNING event.id, event.event_type, event.aggregate_type, event.aggregate_id,
          event.correlation_id, event.causation_id, event.payload,
          event.payload_schema_version, event.attempt_count,
          event.available_at, event.created_at`,
		now.UTC(), now.UTC(), limit, now.UTC(), owner, lockedUntil.UTC(),
	).Scan(&records)
	if result.Error != nil {
		return nil, TranslateError(result.Error)
	}
	events := make([]messaging.OutboxEvent, 0, len(records))
	for _, record := range records {
		event, err := outboxEventFromRecord(record)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].AvailableAt.Equal(events[j].AvailableAt) {
			if events[i].CreatedAt.Equal(events[j].CreatedAt) {
				return events[i].ID.String() < events[j].ID.String()
			}
			return events[i].CreatedAt.Before(events[j].CreatedAt)
		}
		return events[i].AvailableAt.Before(events[j].AvailableAt)
	})
	return events, nil
}

func (r *OutboxEventRepository) MarkOutboxPublished(
	ctx context.Context,
	eventID uuid.UUID,
	owner string,
	publishedAt time.Time,
) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("outbox event repository is unavailable")
	}
	owner = strings.TrimSpace(owner)
	if eventID == uuid.Nil || owner == "" {
		return false, errors.New("outbox publish confirmation is invalid")
	}
	result := ResolveDB(ctx, r.db).Exec(`
UPDATE outbox_events
SET published_at = ?, locked_at = NULL, locked_by = NULL, locked_until = NULL, last_error = NULL
WHERE id = ? AND published_at IS NULL AND locked_by = ?`, publishedAt.UTC(), eventID, owner)
	if result.Error != nil {
		return false, TranslateError(result.Error)
	}
	return result.RowsAffected == 1, nil
}

func (r *OutboxEventRepository) MarkOutboxFailed(
	ctx context.Context,
	eventID uuid.UUID,
	owner string,
	failedAt, nextAvailableAt time.Time,
	safeError string,
) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("outbox event repository is unavailable")
	}
	owner = strings.TrimSpace(owner)
	safeError = strings.TrimSpace(safeError)
	if eventID == uuid.Nil || owner == "" || safeError == "" || nextAvailableAt.Before(failedAt) {
		return false, errors.New("outbox publish failure is invalid")
	}
	result := ResolveDB(ctx, r.db).Exec(`
UPDATE outbox_events
SET attempt_count = attempt_count + 1, available_at = ?, last_error = ?,
    locked_at = NULL, locked_by = NULL, locked_until = NULL
WHERE id = ? AND published_at IS NULL AND locked_by = ?`,
		nextAvailableAt.UTC(), safeError, eventID, owner,
	)
	if result.Error != nil {
		return false, TranslateError(result.Error)
	}
	return result.RowsAffected == 1, nil
}

type outboxEventRecord struct {
	ID                   uuid.UUID  `gorm:"column:id"`
	EventType            string     `gorm:"column:event_type"`
	AggregateType        string     `gorm:"column:aggregate_type"`
	AggregateID          uuid.UUID  `gorm:"column:aggregate_id"`
	CorrelationID        uuid.UUID  `gorm:"column:correlation_id"`
	CausationID          *uuid.UUID `gorm:"column:causation_id"`
	Payload              []byte     `gorm:"column:payload"`
	PayloadSchemaVersion int        `gorm:"column:payload_schema_version"`
	AttemptCount         int        `gorm:"column:attempt_count"`
	AvailableAt          time.Time  `gorm:"column:available_at"`
	CreatedAt            time.Time  `gorm:"column:created_at"`
}

func outboxEventFromRecord(record outboxEventRecord) (messaging.OutboxEvent, error) {
	if !json.Valid(record.Payload) {
		return messaging.OutboxEvent{}, fmt.Errorf("outbox event %s has invalid payload", record.ID)
	}
	return messaging.OutboxEvent{
		ID: record.ID, EventType: record.EventType, AggregateType: record.AggregateType,
		AggregateID: record.AggregateID, CorrelationID: record.CorrelationID,
		CausationID: record.CausationID, Payload: append(json.RawMessage(nil), record.Payload...),
		PayloadSchemaVersion: record.PayloadSchemaVersion, AttemptCount: record.AttemptCount,
		AvailableAt: record.AvailableAt, CreatedAt: record.CreatedAt,
	}, nil
}
