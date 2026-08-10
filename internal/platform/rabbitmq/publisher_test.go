package rabbitmq

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/messaging"
	"github.com/chitandabb/GoAgent/internal/platform/config"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
)

func TestPublisherBuildPublishingUsesStableMessageEnvelope(t *testing.T) {
	eventID := uuid.New()
	correlationID := uuid.New()
	aggregateID := uuid.New()
	createdAt := time.Date(2026, 8, 2, 11, 0, 0, 0, time.UTC)
	publisher := &Publisher{config: config.RabbitMQConfig{DiagnosisRoutingKey: "diagnosis.execute"}}
	routingKey, message, err := publisher.buildPublishing(messaging.OutboxEvent{
		ID: eventID, EventType: "diagnosis.execute", AggregateType: "diagnosis_task",
		AggregateID: aggregateID, CorrelationID: correlationID,
		Payload:              json.RawMessage(`{"taskId":"` + aggregateID.String() + `"}`),
		PayloadSchemaVersion: 1, CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatalf("buildPublishing(): %v", err)
	}
	if routingKey != "diagnosis.execute" || message.DeliveryMode != amqp.Persistent ||
		message.MessageId != eventID.String() || message.CorrelationId != correlationID.String() {
		t.Fatalf("routingKey=%q message=%+v", routingKey, message)
	}
	var envelope messageEnvelope
	if err := json.Unmarshal(message.Body, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.MessageID != eventID.String() || envelope.MessageType != "diagnosis.execute" ||
		envelope.CorrelationID != correlationID.String() || string(envelope.Payload) == "" {
		t.Fatalf("envelope=%+v", envelope)
	}
}

func TestPublisherBuildPublishingRejectsUnsupportedEvent(t *testing.T) {
	publisher := &Publisher{config: config.RabbitMQConfig{DiagnosisRoutingKey: "diagnosis.execute"}}
	_, _, err := publisher.buildPublishing(messaging.OutboxEvent{
		ID: uuid.New(), EventType: "unknown", CorrelationID: uuid.New(),
		Payload: json.RawMessage(`{}`), PayloadSchemaVersion: 1, CreatedAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("buildPublishing() accepted unsupported event type")
	}
}

func TestPublisherBuildPublishingRoutesKnowledgeIngestion(t *testing.T) {
	taskID := uuid.New()
	publisher := &Publisher{config: config.RabbitMQConfig{KnowledgeIngestionRoutingKey: "knowledge.ingest"}}
	routingKey, message, err := publisher.buildPublishing(messaging.OutboxEvent{
		ID: uuid.New(), EventType: "knowledge.ingest", AggregateType: "knowledge_ingestion_task",
		AggregateID: taskID, CorrelationID: uuid.New(),
		Payload:              json.RawMessage(`{"taskId":"` + taskID.String() + `"}`),
		PayloadSchemaVersion: 1, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("buildPublishing(): %v", err)
	}
	if routingKey != "knowledge.ingest" || message.Type != "knowledge.ingest" {
		t.Fatalf("routingKey = %q, message type = %q", routingKey, message.Type)
	}
}

func TestPublisherBuildPublishingRoutesConversationTurn(t *testing.T) {
	turnID := uuid.New()
	publisher := &Publisher{config: config.RabbitMQConfig{ConversationRoutingKey: "conversation.turn.execute"}}
	routingKey, message, err := publisher.buildPublishing(messaging.OutboxEvent{
		ID: uuid.New(), EventType: "conversation.turn.execute", AggregateType: "conversation_turn",
		AggregateID: turnID, CorrelationID: uuid.New(),
		Payload:              json.RawMessage(`{"turnId":"` + turnID.String() + `"}`),
		PayloadSchemaVersion: 1, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("buildPublishing(): %v", err)
	}
	if routingKey != "conversation.turn.execute" || message.Type != "conversation.turn.execute" {
		t.Fatalf("routingKey = %q, message type = %q", routingKey, message.Type)
	}
}
