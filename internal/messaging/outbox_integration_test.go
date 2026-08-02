//go:build integration

package messaging_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/messaging"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	platformrabbitmq "github.com/chitandabb/GoAgent/internal/platform/rabbitmq"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestOutboxRelayFromPostgresToRabbitMQ(t *testing.T) {
	postgresDSN := os.Getenv("MESGUARD_TEST_POSTGRES_DSN")
	rabbitURL := os.Getenv("MESGUARD_TEST_RABBITMQ_URL")
	if postgresDSN == "" || rabbitURL == "" {
		t.Skip("PostgreSQL and RabbitMQ integration URLs are not configured")
	}
	db, err := gorm.Open(gormpostgres.Open(postgresDSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get postgres sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		t.Fatalf("begin fixture transaction: %v", tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	if err := tx.Exec(`
UPDATE outbox_events
SET locked_at = now(), locked_by = 'integration-isolation', locked_until = now() + interval '10 minutes'
WHERE published_at IS NULL`).Error; err != nil {
		t.Fatalf("isolate existing outbox events: %v", err)
	}

	eventID := uuid.New()
	aggregateID := uuid.New()
	correlationID := uuid.New()
	payload, _ := json.Marshal(map[string]any{"taskId": aggregateID.String()})
	if err := tx.Exec(`
INSERT INTO outbox_events
    (id, event_type, aggregate_type, aggregate_id, correlation_id, payload,
     payload_schema_version, attempt_count, available_at, requeue_count, created_at)
VALUES (?, 'diagnosis.execute', 'diagnosis_task', ?, ?, ?, 1, 0, now(), 0, now())`,
		eventID, aggregateID, correlationID, payload).Error; err != nil {
		t.Fatalf("insert outbox event: %v", err)
	}

	t.Setenv("MESGUARD_TEST_RABBITMQ_URL_ACTIVE", rabbitURL)
	suffix := uuid.NewString()[:8]
	exchange := "mesguard.relay.test." + suffix
	queue := "mesguard.relay.test.queue." + suffix
	rabbitConfig := config.RabbitMQConfig{
		Enabled: true, URLEnv: "MESGUARD_TEST_RABBITMQ_URL_ACTIVE", Exchange: exchange,
		DiagnosisQueue: queue, DiagnosisRoutingKey: "diagnosis.execute",
		RelayBatchSize: 1, RelayPollIntervalMillis: 100, RelayLeaseMillis: 10000,
		PublishConfirmTimeoutMillis: 1000,
	}
	publisher, err := platformrabbitmq.OpenPublisher(rabbitConfig)
	if err != nil {
		t.Fatalf("OpenPublisher(): %v", err)
	}
	t.Cleanup(func() { _ = publisher.Close() })
	inspectionConnection, err := amqp.Dial(rabbitURL)
	if err != nil {
		t.Fatalf("dial inspection connection: %v", err)
	}
	t.Cleanup(func() { _ = inspectionConnection.Close() })
	inspectionChannel, err := inspectionConnection.Channel()
	if err != nil {
		t.Fatalf("open inspection channel: %v", err)
	}
	t.Cleanup(func() {
		_, _ = inspectionChannel.QueueDelete(queue, false, false, false)
		_ = inspectionChannel.ExchangeDelete(exchange, false, false)
		_ = inspectionChannel.Close()
	})

	relay, err := messaging.NewOutboxRelay(
		platformpostgres.NewOutboxEventRepository(tx), publisher,
		messaging.RelayConfig{
			Owner: "relay-integration-" + suffix, BatchSize: 1,
			LeaseDuration: 10 * time.Second, PublishTimeout: time.Second,
		},
	)
	if err != nil {
		t.Fatalf("NewOutboxRelay(): %v", err)
	}
	stats, err := relay.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce(): %v", err)
	}
	if stats.Claimed != 1 || stats.Published != 1 || stats.Failed != 0 {
		t.Fatalf("relay stats = %+v", stats)
	}
	var published bool
	if err := tx.Raw(`SELECT published_at IS NOT NULL FROM outbox_events WHERE id = ?`, eventID).Scan(&published).Error; err != nil {
		t.Fatalf("read outbox published state: %v", err)
	}
	if !published {
		t.Fatal("outbox event was not marked published after confirm")
	}
	delivery, ok, err := inspectionChannel.Get(queue, false)
	if err != nil {
		t.Fatalf("get relayed message: %v", err)
	}
	if !ok || delivery.MessageId != eventID.String() {
		t.Fatalf("delivery ok=%v messageId=%q, want %s", ok, delivery.MessageId, eventID)
	}
	if err := delivery.Ack(false); err != nil {
		t.Fatalf("ack relayed message: %v", err)
	}
}
