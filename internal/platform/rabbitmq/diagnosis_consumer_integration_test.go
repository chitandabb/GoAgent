//go:build integration

package rabbitmq

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/diagnosisworker"
	"github.com/chitandabb/GoAgent/internal/platform/config"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"
)

func TestDiagnosisConsumerPublishesConfirmedRetryAndDeadCopies(t *testing.T) {
	url := os.Getenv("MESGUARD_TEST_RABBITMQ_URL")
	if url == "" {
		t.Skip("MESGUARD_TEST_RABBITMQ_URL is not configured")
	}
	t.Setenv("MESGUARD_TEST_RABBITMQ_URL_ACTIVE", url)
	suffix := uuid.NewString()[:8]
	cfg := config.RabbitMQConfig{
		Enabled: true, URLEnv: "MESGUARD_TEST_RABBITMQ_URL_ACTIVE",
		Exchange:                     "mesguard.worker.test." + suffix,
		DiagnosisQueue:               "mesguard.worker.test.queue." + suffix,
		DiagnosisRoutingKey:          "diagnosis.execute",
		ConversationQueue:            "mesguard.worker.test.conversation." + suffix,
		ConversationRoutingKey:       "conversation.turn.execute",
		MemoryCompactionQueue:        "mesguard.worker.test.memory." + suffix,
		MemoryCompactionRoutingKey:   "conversation.memory.compact",
		KnowledgeIngestionQueue:      "mesguard.worker.test.knowledge." + suffix,
		KnowledgeIngestionRoutingKey: "knowledge.ingest",
		RelayBatchSize:               1, RelayPollIntervalMillis: 100, RelayLeaseMillis: 10000,
		PublishConfirmTimeoutMillis: 2000, WorkerLeaseMillis: 30000,
		WorkerRenewIntervalMillis: 5000, WorkerMaxAttempts: 4,
	}
	consumer, err := OpenDiagnosisConsumer(cfg, fixedProcessor{}, "worker-test-"+suffix, zap.NewNop())
	if err != nil {
		t.Fatalf("OpenDiagnosisConsumer(): %v", err)
	}
	t.Cleanup(func() {
		for _, retry := range diagnosisRetryQueues(cfg.DiagnosisQueue) {
			_, _ = consumer.channel.QueueDelete(retry.name, false, false, false)
		}
		_, _ = consumer.channel.QueueDelete(diagnosisDeadQueue(cfg.DiagnosisQueue), false, false, false)
		_, _ = consumer.channel.QueueDelete(cfg.DiagnosisQueue, false, false, false)
		_ = consumer.channel.ExchangeDelete(cfg.Exchange, false, false)
		_ = consumer.Close()
	})

	acknowledger := &recordingAcknowledger{}
	delivery := amqp.Delivery{
		Acknowledger: acknowledger, DeliveryTag: 1,
		Headers:     amqp.Table{"aggregate_type": "diagnosis_task"},
		ContentType: "application/json", DeliveryMode: amqp.Persistent,
		MessageId: uuid.NewString(), CorrelationId: uuid.NewString(),
		Type: diagnosisworker.DiagnosisMessageType, Timestamp: time.Now().UTC(),
		Body: []byte(`{"message":"fixture"}`),
	}
	if err := consumer.applyOutcome(context.Background(), delivery, diagnosisworker.Outcome{
		Action: diagnosisworker.ActionRetry, RetryDelay: 30 * time.Second, Reason: "temporary",
	}); err != nil {
		t.Fatalf("apply retry outcome: %v", err)
	}
	if acknowledger.acks != 1 || acknowledger.nacks != 0 {
		t.Fatalf("retry acknowledgement = ack:%d nack:%d", acknowledger.acks, acknowledger.nacks)
	}
	retryDelivery, ok, err := consumer.channel.Get(cfg.DiagnosisQueue+".retry.30s", true)
	if err != nil || !ok || retryDelivery.MessageId != delivery.MessageId {
		t.Fatalf("retry delivery = ok:%v id:%s err:%v", ok, retryDelivery.MessageId, err)
	}

	delivery.DeliveryTag = 2
	if err := consumer.applyOutcome(context.Background(), delivery, diagnosisworker.Outcome{
		Action: diagnosisworker.ActionDeadLetter, Reason: "invalid schema",
	}); err != nil {
		t.Fatalf("apply dead outcome: %v", err)
	}
	deadDelivery, ok, err := consumer.channel.Get(diagnosisDeadQueue(cfg.DiagnosisQueue), true)
	if err != nil || !ok || deadDelivery.MessageId != delivery.MessageId {
		t.Fatalf("dead delivery = ok:%v id:%s err:%v", ok, deadDelivery.MessageId, err)
	}
	if acknowledger.acks != 2 || acknowledger.nacks != 0 {
		t.Fatalf("final acknowledgement = ack:%d nack:%d", acknowledger.acks, acknowledger.nacks)
	}
}

type fixedProcessor struct{}

func (fixedProcessor) Process(context.Context, diagnosisworker.IncomingMessage) diagnosisworker.Outcome {
	return diagnosisworker.Outcome{Action: diagnosisworker.ActionAck}
}

type recordingAcknowledger struct {
	acks  int
	nacks int
}

func (a *recordingAcknowledger) Ack(uint64, bool) error {
	a.acks++
	return nil
}

func (a *recordingAcknowledger) Nack(uint64, bool, bool) error {
	a.nacks++
	return nil
}

func (a *recordingAcknowledger) Reject(uint64, bool) error {
	a.nacks++
	return nil
}
