//go:build integration

package rabbitmq

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversationworker"
	"github.com/chitandabb/GoAgent/internal/platform/config"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"
)

func TestConversationConsumerPublishesConfirmedRetryAndDeadCopies(t *testing.T) {
	url := os.Getenv("MESGUARD_TEST_RABBITMQ_URL")
	if url == "" {
		t.Skip("MESGUARD_TEST_RABBITMQ_URL is not configured")
	}
	t.Setenv("MESGUARD_TEST_RABBITMQ_URL_ACTIVE", url)
	suffix := uuid.NewString()[:8]
	cfg := config.RabbitMQConfig{
		Enabled: true, URLEnv: "MESGUARD_TEST_RABBITMQ_URL_ACTIVE",
		Exchange:                     "mesguard.conversation.worker.test." + suffix,
		DiagnosisQueue:               "mesguard.conversation.worker.test.diagnosis." + suffix,
		DiagnosisRoutingKey:          "diagnosis.execute",
		ConversationQueue:            "mesguard.conversation.worker.test.queue." + suffix,
		ConversationRoutingKey:       "conversation.turn.execute",
		MemoryCompactionQueue:        "mesguard.conversation.worker.test.memory." + suffix,
		MemoryCompactionRoutingKey:   "conversation.memory.compact",
		KnowledgeIngestionQueue:      "mesguard.conversation.worker.test.knowledge." + suffix,
		KnowledgeIngestionRoutingKey: "knowledge.ingest",
		RelayBatchSize:               1, RelayPollIntervalMillis: 100, RelayLeaseMillis: 10000,
		PublishConfirmTimeoutMillis: 2000, WorkerLeaseMillis: 30000,
		WorkerRenewIntervalMillis: 5000, WorkerMaxAttempts: 4,
	}
	consumer, err := OpenConversationConsumer(
		cfg, fixedConversationProcessor{}, "conversation-worker-test-"+suffix, zap.NewNop(),
	)
	if err != nil {
		t.Fatalf("OpenConversationConsumer(): %v", err)
	}
	t.Cleanup(func() {
		for _, retry := range conversationRetryQueues(cfg.ConversationQueue) {
			_, _ = consumer.channel.QueueDelete(retry.name, false, false, false)
		}
		_, _ = consumer.channel.QueueDelete(conversationDeadQueue(cfg.ConversationQueue), false, false, false)
		_, _ = consumer.channel.QueueDelete(cfg.ConversationQueue, false, false, false)
		_ = consumer.channel.ExchangeDelete(cfg.Exchange, false, false)
		_ = consumer.Close()
	})

	acknowledger := &recordingAcknowledger{}
	delivery := amqp.Delivery{
		Acknowledger: acknowledger, DeliveryTag: 1,
		Headers:     amqp.Table{"aggregate_type": "conversation_turn"},
		ContentType: "application/json", DeliveryMode: amqp.Persistent,
		MessageId: uuid.NewString(), CorrelationId: uuid.NewString(),
		Type: conversationworker.MessageType, Timestamp: time.Now().UTC(),
		Body: []byte(`{"message":"fixture"}`),
	}
	if err := consumer.applyOutcome(context.Background(), delivery, conversationworker.Outcome{
		Action: conversationworker.ActionRetry, RetryDelay: 30 * time.Second, Reason: "temporary",
	}); err != nil {
		t.Fatalf("apply retry outcome: %v", err)
	}
	if acknowledger.acks != 1 || acknowledger.nacks != 0 {
		t.Fatalf("retry acknowledgement = ack:%d nack:%d", acknowledger.acks, acknowledger.nacks)
	}
	retryDelivery, ok, err := consumer.channel.Get(cfg.ConversationQueue+".retry.30s", true)
	if err != nil || !ok || retryDelivery.MessageId != delivery.MessageId {
		t.Fatalf("retry delivery = ok:%v id:%s err:%v", ok, retryDelivery.MessageId, err)
	}

	delivery.DeliveryTag = 2
	if err := consumer.applyOutcome(context.Background(), delivery, conversationworker.Outcome{
		Action: conversationworker.ActionDeadLetter, Reason: "invalid schema",
	}); err != nil {
		t.Fatalf("apply dead outcome: %v", err)
	}
	deadDelivery, ok, err := consumer.channel.Get(conversationDeadQueue(cfg.ConversationQueue), true)
	if err != nil || !ok || deadDelivery.MessageId != delivery.MessageId {
		t.Fatalf("dead delivery = ok:%v id:%s err:%v", ok, deadDelivery.MessageId, err)
	}
	if acknowledger.acks != 2 || acknowledger.nacks != 0 {
		t.Fatalf("final acknowledgement = ack:%d nack:%d", acknowledger.acks, acknowledger.nacks)
	}
}

type fixedConversationProcessor struct{}

func (fixedConversationProcessor) Process(context.Context, conversationworker.IncomingMessage) conversationworker.Outcome {
	return conversationworker.Outcome{Action: conversationworker.ActionAck}
}
