package rabbitmq

import (
	"context"
	"errors"

	"github.com/chitandabb/GoAgent/internal/conversationmemoryworker"
	"github.com/chitandabb/GoAgent/internal/conversationworker"
	"github.com/chitandabb/GoAgent/internal/platform/config"

	"go.uber.org/zap"
)

type MemoryProcessor interface {
	Process(context.Context, conversationmemoryworker.IncomingMessage) conversationmemoryworker.Outcome
}

type MemoryConsumer struct {
	consumer *ConversationConsumer
}

func OpenMemoryConsumer(
	cfg config.RabbitMQConfig,
	processor MemoryProcessor,
	consumerTag string,
	log *zap.Logger,
) (*MemoryConsumer, error) {
	if processor == nil {
		return nil, errors.New("memory consumer processor is required")
	}
	consumer, err := openWorkerConsumer(
		cfg, memoryProcessorAdapter{processor: processor}, consumerTag,
		cfg.MemoryCompactionQueue, cfg.MemoryCompactionRoutingKey,
		"conversation_memory", "mesguard-memory-worker", log,
	)
	if err != nil {
		return nil, err
	}
	return &MemoryConsumer{consumer: consumer}, nil
}

func (c *MemoryConsumer) Run(ctx context.Context) error {
	if c == nil || c.consumer == nil {
		return errors.New("memory consumer is unavailable")
	}
	return c.consumer.Run(ctx)
}

func (c *MemoryConsumer) Close() error {
	if c == nil || c.consumer == nil {
		return nil
	}
	return c.consumer.Close()
}

type memoryProcessorAdapter struct {
	processor MemoryProcessor
}

func (a memoryProcessorAdapter) Process(
	ctx context.Context,
	message conversationworker.IncomingMessage,
) conversationworker.Outcome {
	outcome := a.processor.Process(ctx, conversationmemoryworker.IncomingMessage{
		ContentType: message.ContentType, MessageID: message.MessageID,
		CorrelationID: message.CorrelationID, Type: message.Type,
		Body: append([]byte(nil), message.Body...),
	})
	converted := conversationworker.Outcome{RetryDelay: outcome.RetryDelay, Reason: outcome.Reason}
	switch outcome.Action {
	case conversationmemoryworker.ActionAck:
		converted.Action = conversationworker.ActionAck
	case conversationmemoryworker.ActionRetry:
		converted.Action = conversationworker.ActionRetry
	case conversationmemoryworker.ActionDeadLetter:
		converted.Action = conversationworker.ActionDeadLetter
	case conversationmemoryworker.ActionRequeue:
		converted.Action = conversationworker.ActionRequeue
	default:
		converted.Action = conversationworker.ActionRequeue
	}
	return converted
}
