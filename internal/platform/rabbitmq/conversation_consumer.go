package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversationworker"
	"github.com/chitandabb/GoAgent/internal/platform/config"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"
)

type ConversationProcessor interface {
	Process(ctx context.Context, message conversationworker.IncomingMessage) conversationworker.Outcome
}

type ConversationConsumer struct {
	config      config.RabbitMQConfig
	processor   ConversationProcessor
	consumerTag string
	appID       string
	queue       string
	routingKey  string
	workerKind  string
	log         *zap.Logger

	connection *amqp.Connection
	channel    *amqp.Channel
	deliveries <-chan amqp.Delivery
}

func OpenConversationConsumer(
	cfg config.RabbitMQConfig,
	processor ConversationProcessor,
	consumerTag string,
	log *zap.Logger,
) (*ConversationConsumer, error) {
	return openWorkerConsumer(
		cfg, processor, consumerTag, cfg.ConversationQueue, cfg.ConversationRoutingKey,
		"conversation", "mesguard-conversation-worker", log,
	)
}

func openWorkerConsumer(
	cfg config.RabbitMQConfig,
	processor ConversationProcessor,
	consumerTag, queue, routingKey, workerKind, appID string,
	log *zap.Logger,
) (*ConversationConsumer, error) {
	if processor == nil || log == nil {
		return nil, errors.New("worker consumer processor and logger are required")
	}
	consumerTag, queue = strings.TrimSpace(consumerTag), strings.TrimSpace(queue)
	routingKey, workerKind = strings.TrimSpace(routingKey), strings.TrimSpace(workerKind)
	appID = strings.TrimSpace(appID)
	if consumerTag == "" || len(consumerTag) > 128 || queue == "" || routingKey == "" ||
		workerKind == "" || len(workerKind) > 64 || appID == "" || len(appID) > 128 {
		return nil, errors.New("worker consumer identity or topology is invalid")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	url, err := cfg.URL()
	if err != nil {
		return nil, err
	}
	consumer := &ConversationConsumer{
		config: cfg, processor: processor, consumerTag: consumerTag, appID: appID,
		queue: queue, routingKey: routingKey, workerKind: workerKind, log: log,
	}
	if err := consumer.connect(url); err != nil {
		_ = consumer.Close()
		return nil, err
	}
	return consumer, nil
}

func (c *ConversationConsumer) Run(ctx context.Context) error {
	if c == nil || c.channel == nil || c.deliveries == nil {
		return errors.New("worker consumer is unavailable")
	}
	for {
		select {
		case <-ctx.Done():
			_ = c.channel.Cancel(c.consumerTag, false)
			return ctx.Err()
		case delivery, ok := <-c.deliveries:
			if !ok {
				return fmt.Errorf("rabbitmq %s delivery channel closed", c.workerKind)
			}
			outcome := c.processor.Process(ctx, conversationworker.IncomingMessage{
				ContentType: delivery.ContentType, MessageID: delivery.MessageId,
				CorrelationID: delivery.CorrelationId, Type: delivery.Type,
				Body: append([]byte(nil), delivery.Body...),
			})
			if err := c.applyOutcome(ctx, delivery, outcome); err != nil {
				return err
			}
			c.log.Info("worker message handled",
				zap.String("worker_kind", c.workerKind),
				zap.String("message_id", delivery.MessageId),
				zap.String("action", string(outcome.Action)),
				zap.Duration("retry_delay", outcome.RetryDelay),
				zap.String("reason", outcome.Reason),
			)
		}
	}
}

func (c *ConversationConsumer) Close() error {
	if c == nil {
		return nil
	}
	var closeErr error
	if c.channel != nil && !c.channel.IsClosed() {
		closeErr = errors.Join(closeErr, c.channel.Close())
	}
	if c.connection != nil && !c.connection.IsClosed() {
		closeErr = errors.Join(closeErr, c.connection.Close())
	}
	c.channel = nil
	c.connection = nil
	c.deliveries = nil
	return closeErr
}

func (c *ConversationConsumer) connect(url string) error {
	connection, err := amqp.Dial(url)
	if err != nil {
		return fmt.Errorf("connect rabbitmq %s consumer: %w", c.workerKind, err)
	}
	channel, err := connection.Channel()
	if err != nil {
		_ = connection.Close()
		return fmt.Errorf("open rabbitmq %s consumer channel: %w", c.workerKind, err)
	}
	cleanup := func(err error) error {
		_ = channel.Close()
		_ = connection.Close()
		return err
	}
	if err := declareWorkerTopology(channel, c.config, c.queue, c.routingKey, c.workerKind); err != nil {
		return cleanup(err)
	}
	if err := channel.Qos(1, 0, false); err != nil {
		return cleanup(fmt.Errorf("set %s consumer qos: %w", c.workerKind, err))
	}
	if err := channel.Confirm(false); err != nil {
		return cleanup(fmt.Errorf("enable %s retry publisher confirms: %w", c.workerKind, err))
	}
	deliveries, err := channel.Consume(
		c.queue, c.consumerTag,
		false, false, false, false, nil,
	)
	if err != nil {
		return cleanup(fmt.Errorf("consume rabbitmq %s queue: %w", c.workerKind, err))
	}
	c.connection = connection
	c.channel = channel
	c.deliveries = deliveries
	return nil
}

func declareWorkerTopology(
	channel *amqp.Channel,
	cfg config.RabbitMQConfig,
	queue, routingKey, workerKind string,
) error {
	if err := channel.ExchangeDeclare(cfg.Exchange, "direct", true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare rabbitmq %s exchange: %w", workerKind, err)
	}
	if _, err := channel.QueueDeclare(queue, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare rabbitmq %s queue: %w", workerKind, err)
	}
	if err := channel.QueueBind(queue, routingKey, cfg.Exchange, false, nil); err != nil {
		return fmt.Errorf("bind rabbitmq %s queue: %w", workerKind, err)
	}
	for _, retry := range conversationRetryQueues(queue) {
		arguments := amqp.Table{
			"x-message-ttl":             int32(retry.delay / time.Millisecond),
			"x-dead-letter-exchange":    cfg.Exchange,
			"x-dead-letter-routing-key": routingKey,
		}
		if _, err := channel.QueueDeclare(retry.name, true, false, false, false, arguments); err != nil {
			return fmt.Errorf("declare rabbitmq %s retry queue %s: %w", workerKind, retry.name, err)
		}
	}
	if _, err := channel.QueueDeclare(conversationDeadQueue(queue), true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare rabbitmq %s dead queue: %w", workerKind, err)
	}
	return nil
}

func (c *ConversationConsumer) applyOutcome(
	ctx context.Context,
	delivery amqp.Delivery,
	outcome conversationworker.Outcome,
) error {
	switch outcome.Action {
	case conversationworker.ActionAck:
		return delivery.Ack(false)
	case conversationworker.ActionRequeue:
		return delivery.Nack(false, true)
	case conversationworker.ActionRetry:
		queue, ok := conversationRetryQueue(c.queue, outcome.RetryDelay)
		if !ok {
			queue = conversationRetryQueues(c.queue)[0].name
		}
		if err := c.publishCopy(ctx, queue, delivery, outcome, false); err != nil {
			_ = delivery.Nack(false, true)
			return err
		}
		return delivery.Ack(false)
	case conversationworker.ActionDeadLetter:
		if err := c.publishCopy(ctx, conversationDeadQueue(c.queue), delivery, outcome, true); err != nil {
			_ = delivery.Nack(false, true)
			return err
		}
		return delivery.Ack(false)
	default:
		return delivery.Nack(false, true)
	}
}

func (c *ConversationConsumer) publishCopy(
	ctx context.Context,
	queue string,
	delivery amqp.Delivery,
	outcome conversationworker.Outcome,
	dead bool,
) error {
	headers := cloneHeaders(delivery.Headers)
	headers["mesguard_last_reason"] = truncateHeader(outcome.Reason)
	headers["mesguard_original_queue"] = c.queue
	if dead {
		headers["mesguard_dead_lettered_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	} else {
		headers["mesguard_retry_delay_ms"] = int64(outcome.RetryDelay / time.Millisecond)
	}
	publishCtx, cancel := context.WithTimeout(
		ctx, time.Duration(c.config.PublishConfirmTimeoutMillis)*time.Millisecond,
	)
	defer cancel()
	confirmation, err := c.channel.PublishWithDeferredConfirmWithContext(
		publishCtx, "", queue, false, false,
		amqp.Publishing{
			Headers: headers, ContentType: delivery.ContentType,
			ContentEncoding: delivery.ContentEncoding, DeliveryMode: amqp.Persistent,
			Priority: delivery.Priority, CorrelationId: delivery.CorrelationId,
			ReplyTo: delivery.ReplyTo, Expiration: "", MessageId: delivery.MessageId,
			Timestamp: delivery.Timestamp, Type: delivery.Type,
			UserId: delivery.UserId, AppId: c.appID,
			Body: append([]byte(nil), delivery.Body...),
		},
	)
	if err != nil {
		return fmt.Errorf("publish %s retry/dead copy: %w", c.workerKind, err)
	}
	acked, err := confirmation.WaitContext(publishCtx)
	if err != nil {
		return fmt.Errorf("wait %s retry/dead confirm: %w", c.workerKind, err)
	}
	if !acked {
		return ErrPublishNack
	}
	return nil
}

func conversationRetryQueues(base string) []retryQueue {
	return []retryQueue{
		{name: base + ".retry.30s", delay: 30 * time.Second},
		{name: base + ".retry.2m", delay: 2 * time.Minute},
		{name: base + ".retry.10m", delay: 10 * time.Minute},
	}
}

func conversationRetryQueue(base string, delay time.Duration) (string, bool) {
	for _, retry := range conversationRetryQueues(base) {
		if retry.delay == delay {
			return retry.name, true
		}
	}
	return "", false
}

func conversationDeadQueue(base string) string {
	return base + ".dead"
}
