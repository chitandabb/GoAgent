//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestOutboxEventRepositoryAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("MESGUARD_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MESGUARD_TEST_POSTGRES_DSN is not configured")
	}
	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test postgres: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get test postgres sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	now := time.Now().UTC()
	firstID := uuid.New()
	secondID := uuid.New()
	t.Cleanup(func() {
		_ = db.WithContext(context.Background()).Exec(`DELETE FROM outbox_events WHERE id IN (?, ?)`, firstID, secondID).Error
	})
	for _, eventID := range []uuid.UUID{firstID, secondID} {
		aggregateID := uuid.New()
		payload, _ := json.Marshal(map[string]any{"taskId": aggregateID.String()})
		if err := db.WithContext(ctx).Exec(`
INSERT INTO outbox_events
    (id, event_type, aggregate_type, aggregate_id, correlation_id, payload,
     payload_schema_version, attempt_count, available_at, requeue_count, created_at)
VALUES (?, 'diagnosis.execute', 'diagnosis_task', ?, ?, ?, 1, 0, ?, 0, ?)`,
			eventID, aggregateID, uuid.New(), payload, now, now).Error; err != nil {
			t.Fatalf("insert outbox event: %v", err)
		}
	}

	repository := NewOutboxEventRepository(db)
	firstBatch, err := repository.ClaimOutboxEvents(ctx, "relay-1", now, now.Add(time.Minute), 1)
	if err != nil {
		t.Fatalf("ClaimOutboxEvents(first): %v", err)
	}
	if len(firstBatch) != 1 || firstBatch[0].EventType != "diagnosis.execute" || !json.Valid(firstBatch[0].Payload) {
		t.Fatalf("first batch = %+v", firstBatch)
	}
	secondBatch, err := repository.ClaimOutboxEvents(ctx, "relay-2", now, now.Add(time.Minute), 10)
	if err != nil {
		t.Fatalf("ClaimOutboxEvents(second): %v", err)
	}
	if len(secondBatch) != 1 || secondBatch[0].ID == firstBatch[0].ID {
		t.Fatalf("second batch = %+v, first = %+v", secondBatch, firstBatch)
	}

	failureAt := now.Add(time.Second)
	nextAvailableAt := failureAt.Add(2 * time.Second)
	updated, err := repository.MarkOutboxFailed(ctx, firstBatch[0].ID, "relay-1", failureAt, nextAvailableAt, "publisher timeout")
	if err != nil || !updated {
		t.Fatalf("MarkOutboxFailed() updated=%v err=%v", updated, err)
	}
	early, err := repository.ClaimOutboxEvents(ctx, "relay-3", failureAt, failureAt.Add(time.Minute), 10)
	if err != nil {
		t.Fatalf("ClaimOutboxEvents(early): %v", err)
	}
	if len(early) != 0 {
		t.Fatalf("early retry claimed events: %+v", early)
	}
	retry, err := repository.ClaimOutboxEvents(ctx, "relay-3", nextAvailableAt, nextAvailableAt.Add(time.Minute), 10)
	if err != nil {
		t.Fatalf("ClaimOutboxEvents(retry): %v", err)
	}
	if len(retry) != 1 || retry[0].ID != firstBatch[0].ID || retry[0].AttemptCount != 1 {
		t.Fatalf("retry batch = %+v", retry)
	}
	staleUpdated, err := repository.MarkOutboxPublished(ctx, retry[0].ID, "relay-1", nextAvailableAt)
	if err != nil || staleUpdated {
		t.Fatalf("stale MarkOutboxPublished() updated=%v err=%v", staleUpdated, err)
	}
	published, err := repository.MarkOutboxPublished(ctx, retry[0].ID, "relay-3", nextAvailableAt)
	if err != nil || !published {
		t.Fatalf("MarkOutboxPublished(retry) updated=%v err=%v", published, err)
	}
	published, err = repository.MarkOutboxPublished(ctx, secondBatch[0].ID, "relay-2", nextAvailableAt)
	if err != nil || !published {
		t.Fatalf("MarkOutboxPublished(second) updated=%v err=%v", published, err)
	}
}
