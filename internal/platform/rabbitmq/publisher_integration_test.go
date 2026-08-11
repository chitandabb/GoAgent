//go:build integration

package rabbitmq

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/messaging"
	"github.com/chitandabb/GoAgent/internal/platform/config"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
)

func TestPublisherAgainstRabbitMQ(t *testing.T) {
	url := os.Getenv("MESGUARD_TEST_RABBITMQ_URL")
	if url == "" {
		t.Skip("MESGUARD_TEST_RABBITMQ_URL is not configured")
	}
	t.Setenv("MESGUARD_TEST_RABBITMQ_URL_ACTIVE", url)
	suffix := uuid.NewString()[:8]
	exchange := "mesguard.test." + suffix
	queue := "mesguard.test.diagnosis." + suffix
	cfg := config.RabbitMQConfig{
		Enabled: true, URLEnv: "MESGUARD_TEST_RABBITMQ_URL_ACTIVE", Exchange: exchange,
		DiagnosisQueue: queue, DiagnosisRoutingKey: "diagnosis.execute",
		ConversationQueue: queue + ".conversation", ConversationRoutingKey: "conversation.turn.execute",
		MemoryCompactionQueue: queue + ".memory", MemoryCompactionRoutingKey: "conversation.memory.compact",
		KnowledgeIngestionQueue: queue + ".knowledge", KnowledgeIngestionRoutingKey: "knowledge.ingest",
		RelayBatchSize: 1, RelayPollIntervalMillis: 100, RelayLeaseMillis: 10000,
		PublishConfirmTimeoutMillis: 1000, WorkerLeaseMillis: 30000,
		WorkerRenewIntervalMillis: 5000, WorkerMaxAttempts: 4,
	}
	publisher, err := OpenPublisher(cfg)
	if err != nil {
		t.Fatalf("OpenPublisher(): %v", err)
	}
	t.Cleanup(func() { _ = publisher.Close() })

	connection, err := amqp.Dial(url)
	if err != nil {
		t.Fatalf("dial inspection connection: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	channel, err := connection.Channel()
	if err != nil {
		t.Fatalf("open inspection channel: %v", err)
	}
	t.Cleanup(func() {
		_, _ = channel.QueueDelete(queue+".conversation", false, false, false)
		_, _ = channel.QueueDelete(queue+".memory", false, false, false)
		_, _ = channel.QueueDelete(queue+".knowledge", false, false, false)
		_, _ = channel.QueueDelete(queue, false, false, false)
		_ = channel.ExchangeDelete(exchange, false, false)
		_ = channel.Close()
	})

	eventID := uuid.New()
	aggregateID := uuid.New()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = publisher.Publish(ctx, messaging.OutboxEvent{
		ID: eventID, EventType: "diagnosis.execute", AggregateType: "diagnosis_task",
		AggregateID: aggregateID, CorrelationID: uuid.New(),
		Payload:              json.RawMessage(`{"taskId":"` + aggregateID.String() + `"}`),
		PayloadSchemaVersion: 1, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Publish(): %v", err)
	}
	delivery, ok, err := channel.Get(queue, false)
	if err != nil {
		t.Fatalf("get published message: %v", err)
	}
	if !ok {
		t.Fatal("publisher confirm succeeded but queue is empty")
	}
	if delivery.MessageId != eventID.String() || delivery.Type != "diagnosis.execute" || !json.Valid(delivery.Body) {
		t.Fatalf("delivery messageId=%q type=%q body=%s", delivery.MessageId, delivery.Type, delivery.Body)
	}
	if err := delivery.Ack(false); err != nil {
		t.Fatalf("ack inspection delivery: %v", err)
	}

	memoryEventID, jobID, conversationID := uuid.New(), uuid.New(), uuid.New()
	err = publisher.Publish(ctx, messaging.OutboxEvent{
		ID: memoryEventID, EventType: "conversation.memory.compact", AggregateType: "conversation_memory_job",
		AggregateID: jobID, CorrelationID: uuid.New(),
		Payload:              json.RawMessage(`{"jobId":"` + jobID.String() + `","conversationId":"` + conversationID.String() + `"}`),
		PayloadSchemaVersion: 1, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Publish(memory compaction): %v", err)
	}
	memoryDelivery, ok, err := channel.Get(queue+".memory", false)
	if err != nil || !ok {
		t.Fatalf("get published memory message: ok=%v err=%v", ok, err)
	}
	if memoryDelivery.MessageId != memoryEventID.String() || memoryDelivery.Type != "conversation.memory.compact" ||
		!json.Valid(memoryDelivery.Body) {
		t.Fatalf("memory delivery messageId=%q type=%q body=%s",
			memoryDelivery.MessageId, memoryDelivery.Type, memoryDelivery.Body)
	}
	if err := memoryDelivery.Ack(false); err != nil {
		t.Fatalf("ack inspection memory delivery: %v", err)
	}
}
