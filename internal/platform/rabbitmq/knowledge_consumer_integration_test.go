//go:build integration

package rabbitmq

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledgeworker"
	"github.com/chitandabb/GoAgent/internal/platform/config"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"
)

func TestKnowledgeConsumerPublishesConfirmedRetryAndDeadCopies(t *testing.T) {
	url := os.Getenv("MESGUARD_TEST_RABBITMQ_URL")
	if url == "" {
		t.Skip("MESGUARD_TEST_RABBITMQ_URL is not configured")
	}
	t.Setenv("MESGUARD_TEST_RABBITMQ_URL_ACTIVE", url)
	suffix := uuid.NewString()[:8]
	cfg := config.RabbitMQConfig{
		Enabled: true, URLEnv: "MESGUARD_TEST_RABBITMQ_URL_ACTIVE",
		Exchange:                     "mesguard.knowledge.worker.test." + suffix,
		DiagnosisQueue:               "mesguard.knowledge.worker.test.diagnosis." + suffix,
		DiagnosisRoutingKey:          "diagnosis.execute",
		ConversationQueue:            "mesguard.knowledge.worker.test.conversation." + suffix,
		ConversationRoutingKey:       "conversation.turn.execute",
		KnowledgeIngestionQueue:      "mesguard.knowledge.worker.test.queue." + suffix,
		KnowledgeIngestionRoutingKey: "knowledge.ingest",
		RelayBatchSize:               1, RelayPollIntervalMillis: 100, RelayLeaseMillis: 10000,
		PublishConfirmTimeoutMillis: 2000, WorkerLeaseMillis: 30000,
		WorkerRenewIntervalMillis: 5000, WorkerMaxAttempts: 4,
	}
	consumer, err := OpenKnowledgeConsumer(
		cfg, fixedKnowledgeProcessor{}, "knowledge-worker-test-"+suffix, zap.NewNop(),
	)
	if err != nil {
		t.Fatalf("OpenKnowledgeConsumer(): %v", err)
	}
	t.Cleanup(func() {
		for _, retry := range knowledgeRetryQueues(cfg.KnowledgeIngestionQueue) {
			_, _ = consumer.channel.QueueDelete(retry.name, false, false, false)
		}
		_, _ = consumer.channel.QueueDelete(knowledgeDeadQueue(cfg.KnowledgeIngestionQueue), false, false, false)
		_, _ = consumer.channel.QueueDelete(cfg.KnowledgeIngestionQueue, false, false, false)
		_ = consumer.channel.ExchangeDelete(cfg.Exchange, false, false)
		_ = consumer.Close()
	})

	acknowledger := &recordingAcknowledger{}
	delivery := amqp.Delivery{
		Acknowledger: acknowledger, DeliveryTag: 1,
		Headers:     amqp.Table{"aggregate_type": "knowledge_document_version"},
		ContentType: "application/json", DeliveryMode: amqp.Persistent,
		MessageId: uuid.NewString(), CorrelationId: uuid.NewString(),
		Type: knowledgeworker.MessageType, Timestamp: time.Now().UTC(),
		Body: []byte(`{"message":"fixture"}`),
	}
	if err := consumer.applyOutcome(context.Background(), delivery, knowledgeworker.Outcome{
		Action: knowledgeworker.ActionRetry, RetryDelay: 2 * time.Minute, Reason: "temporary parser outage",
	}); err != nil {
		t.Fatalf("apply retry outcome: %v", err)
	}
	if acknowledger.acks != 1 || acknowledger.nacks != 0 {
		t.Fatalf("retry acknowledgement = ack:%d nack:%d", acknowledger.acks, acknowledger.nacks)
	}
	retryDelivery, ok, err := consumer.channel.Get(cfg.KnowledgeIngestionQueue+".retry.2m", true)
	if err != nil || !ok || retryDelivery.MessageId != delivery.MessageId {
		t.Fatalf("retry delivery = ok:%v id:%s err:%v", ok, retryDelivery.MessageId, err)
	}

	delivery.DeliveryTag = 2
	if err := consumer.applyOutcome(context.Background(), delivery, knowledgeworker.Outcome{
		Action: knowledgeworker.ActionDeadLetter, Reason: "unsupported document type",
	}); err != nil {
		t.Fatalf("apply dead outcome: %v", err)
	}
	deadDelivery, ok, err := consumer.channel.Get(knowledgeDeadQueue(cfg.KnowledgeIngestionQueue), true)
	if err != nil || !ok || deadDelivery.MessageId != delivery.MessageId {
		t.Fatalf("dead delivery = ok:%v id:%s err:%v", ok, deadDelivery.MessageId, err)
	}
	if acknowledger.acks != 2 || acknowledger.nacks != 0 {
		t.Fatalf("final acknowledgement = ack:%d nack:%d", acknowledger.acks, acknowledger.nacks)
	}
}

type fixedKnowledgeProcessor struct{}

func (fixedKnowledgeProcessor) Process(context.Context, knowledgeworker.IncomingMessage) knowledgeworker.Outcome {
	return knowledgeworker.Outcome{Action: knowledgeworker.ActionAck}
}
