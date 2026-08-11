//go:build integration

package rabbitmq

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversationmemoryworker"
	"github.com/chitandabb/GoAgent/internal/platform/config"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"
)

func TestMemoryConsumerReceivesAndAcknowledgesDelivery(t *testing.T) {
	url := os.Getenv("MESGUARD_TEST_RABBITMQ_URL")
	if url == "" {
		t.Skip("MESGUARD_TEST_RABBITMQ_URL is not configured")
	}
	t.Setenv("MESGUARD_TEST_RABBITMQ_URL_ACTIVE", url)
	suffix := uuid.NewString()[:8]
	cfg := config.RabbitMQConfig{
		Enabled: true, URLEnv: "MESGUARD_TEST_RABBITMQ_URL_ACTIVE",
		Exchange:                     "mesguard.memory.worker.test." + suffix,
		DiagnosisQueue:               "mesguard.memory.worker.test.diagnosis." + suffix,
		DiagnosisRoutingKey:          "diagnosis.execute",
		ConversationQueue:            "mesguard.memory.worker.test.conversation." + suffix,
		ConversationRoutingKey:       "conversation.turn.execute",
		MemoryCompactionQueue:        "mesguard.memory.worker.test.queue." + suffix,
		MemoryCompactionRoutingKey:   "conversation.memory.compact",
		KnowledgeIngestionQueue:      "mesguard.memory.worker.test.knowledge." + suffix,
		KnowledgeIngestionRoutingKey: "knowledge.ingest",
		RelayBatchSize:               1, RelayPollIntervalMillis: 100, RelayLeaseMillis: 10000,
		PublishConfirmTimeoutMillis: 2000, WorkerLeaseMillis: 30000,
		WorkerRenewIntervalMillis: 5000, WorkerMaxAttempts: 4,
	}
	processor := &capturingMemoryProcessor{received: make(chan conversationmemoryworker.IncomingMessage, 1)}
	consumer, err := OpenMemoryConsumer(cfg, processor, "memory-worker-test-"+suffix, zap.NewNop())
	if err != nil {
		t.Fatalf("OpenMemoryConsumer(): %v", err)
	}
	inner := consumer.consumer
	t.Cleanup(func() {
		for _, retry := range conversationRetryQueues(cfg.MemoryCompactionQueue) {
			_, _ = inner.channel.QueueDelete(retry.name, false, false, false)
		}
		_, _ = inner.channel.QueueDelete(conversationDeadQueue(cfg.MemoryCompactionQueue), false, false, false)
		_, _ = inner.channel.QueueDelete(cfg.MemoryCompactionQueue, false, false, false)
		_ = inner.channel.ExchangeDelete(cfg.Exchange, false, false)
		_ = consumer.Close()
	})

	runCtx, cancelRun := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- consumer.Run(runCtx) }()
	messageID, correlationID := uuid.NewString(), uuid.NewString()
	publishCtx, cancelPublish := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelPublish()
	confirmation, err := inner.channel.PublishWithDeferredConfirmWithContext(
		publishCtx, cfg.Exchange, cfg.MemoryCompactionRoutingKey, false, false,
		amqp.Publishing{
			ContentType: "application/json", DeliveryMode: amqp.Persistent,
			MessageId: messageID, CorrelationId: correlationID,
			Type: conversationmemoryworker.MessageType, Body: []byte(`{"fixture":true}`),
		},
	)
	if err != nil {
		t.Fatalf("publish memory delivery: %v", err)
	}
	acked, err := confirmation.WaitContext(publishCtx)
	if err != nil || !acked {
		t.Fatalf("wait memory delivery confirm: ack=%v err=%v", acked, err)
	}
	select {
	case received := <-processor.received:
		if received.MessageID != messageID || received.CorrelationID != correlationID ||
			received.Type != conversationmemoryworker.MessageType {
			t.Fatalf("received memory delivery = %+v", received)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("memory consumer did not receive the published delivery")
	}
	cancelRun()
	if runErr := <-runDone; runErr != nil && !errors.Is(runErr, context.Canceled) {
		t.Fatalf("MemoryConsumer.Run() error = %v", runErr)
	}
}

type capturingMemoryProcessor struct {
	received chan conversationmemoryworker.IncomingMessage
}

func (p *capturingMemoryProcessor) Process(
	_ context.Context,
	message conversationmemoryworker.IncomingMessage,
) conversationmemoryworker.Outcome {
	p.received <- message
	return conversationmemoryworker.Outcome{Action: conversationmemoryworker.ActionAck}
}
