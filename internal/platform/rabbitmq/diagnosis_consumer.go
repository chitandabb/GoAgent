package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/diagnosisworker"
	"github.com/chitandabb/GoAgent/internal/platform/config"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"
)

type DiagnosisProcessor interface {
	Process(ctx context.Context, message diagnosisworker.IncomingMessage) diagnosisworker.Outcome
}

type DiagnosisConsumer struct {
	config      config.RabbitMQConfig
	processor   DiagnosisProcessor
	consumerTag string
	log         *zap.Logger

	connection *amqp.Connection
	channel    *amqp.Channel
	deliveries <-chan amqp.Delivery
}

func OpenDiagnosisConsumer(
	cfg config.RabbitMQConfig,
	processor DiagnosisProcessor,
	consumerTag string,
	log *zap.Logger,
) (*DiagnosisConsumer, error) {
	if processor == nil || log == nil {
		return nil, errors.New("diagnosis consumer processor and logger are required")
	}
	consumerTag = strings.TrimSpace(consumerTag)
	if consumerTag == "" || len(consumerTag) > 128 {
		return nil, errors.New("diagnosis consumer tag is invalid")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	url, err := cfg.URL()
	if err != nil {
		return nil, err
	}
	consumer := &DiagnosisConsumer{
		config: cfg, processor: processor, consumerTag: consumerTag, log: log,
	}
	if err := consumer.connect(url); err != nil {
		_ = consumer.Close()
		return nil, err
	}
	return consumer, nil
}

func (c *DiagnosisConsumer) Run(ctx context.Context) error {
	if c == nil || c.channel == nil || c.deliveries == nil {
		return errors.New("diagnosis consumer is unavailable")
	}
	for {
		select {
		case <-ctx.Done():
			_ = c.channel.Cancel(c.consumerTag, false)
			return ctx.Err()
		case delivery, ok := <-c.deliveries:
			if !ok {
				return errors.New("rabbitmq diagnosis delivery channel closed")
			}
			outcome := c.processor.Process(ctx, diagnosisworker.IncomingMessage{
				ContentType: delivery.ContentType, MessageID: delivery.MessageId,
				CorrelationID: delivery.CorrelationId, Type: delivery.Type,
				Body: append([]byte(nil), delivery.Body...),
			})
			if err := c.applyOutcome(ctx, delivery, outcome); err != nil {
				return err
			}
			c.log.Info("diagnosis message handled",
				zap.String("message_id", delivery.MessageId),
				zap.String("action", string(outcome.Action)),
				zap.Duration("retry_delay", outcome.RetryDelay),
				zap.String("reason", outcome.Reason),
			)
		}
	}
}

func (c *DiagnosisConsumer) Close() error {
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

func (c *DiagnosisConsumer) connect(url string) error {
	connection, err := amqp.Dial(url)
	if err != nil {
		return fmt.Errorf("connect rabbitmq diagnosis consumer: %w", err)
	}
	channel, err := connection.Channel()
	if err != nil {
		_ = connection.Close()
		return fmt.Errorf("open rabbitmq diagnosis consumer channel: %w", err)
	}
	cleanup := func(err error) error {
		_ = channel.Close()
		_ = connection.Close()
		return err
	}
	if err := declareDiagnosisTopology(channel, c.config); err != nil {
		return cleanup(err)
	}
	if err := channel.Qos(1, 0, false); err != nil {
		return cleanup(fmt.Errorf("set diagnosis consumer qos: %w", err))
	}
	if err := channel.Confirm(false); err != nil {
		return cleanup(fmt.Errorf("enable diagnosis retry publisher confirms: %w", err))
	}
	deliveries, err := channel.Consume(
		c.config.DiagnosisQueue, c.consumerTag,
		false, false, false, false, nil,
	)
	if err != nil {
		return cleanup(fmt.Errorf("consume rabbitmq diagnosis queue: %w", err))
	}
	c.connection = connection
	c.channel = channel
	c.deliveries = deliveries
	return nil
}

func declareDiagnosisTopology(channel *amqp.Channel, cfg config.RabbitMQConfig) error {
	if err := channel.ExchangeDeclare(cfg.Exchange, "direct", true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare rabbitmq diagnosis exchange: %w", err)
	}
	if _, err := channel.QueueDeclare(cfg.DiagnosisQueue, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare rabbitmq diagnosis queue: %w", err)
	}
	if err := channel.QueueBind(cfg.DiagnosisQueue, cfg.DiagnosisRoutingKey, cfg.Exchange, false, nil); err != nil {
		return fmt.Errorf("bind rabbitmq diagnosis queue: %w", err)
	}
	for _, retry := range diagnosisRetryQueues(cfg.DiagnosisQueue) {
		arguments := amqp.Table{
			"x-message-ttl":             int32(retry.delay / time.Millisecond),
			"x-dead-letter-exchange":    cfg.Exchange,
			"x-dead-letter-routing-key": cfg.DiagnosisRoutingKey,
		}
		if _, err := channel.QueueDeclare(retry.name, true, false, false, false, arguments); err != nil {
			return fmt.Errorf("declare rabbitmq diagnosis retry queue %s: %w", retry.name, err)
		}
	}
	if _, err := channel.QueueDeclare(diagnosisDeadQueue(cfg.DiagnosisQueue), true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare rabbitmq diagnosis dead queue: %w", err)
	}
	return nil
}

func (c *DiagnosisConsumer) applyOutcome(
	ctx context.Context,
	delivery amqp.Delivery,
	outcome diagnosisworker.Outcome,
) error {
	switch outcome.Action {
	case diagnosisworker.ActionAck:
		return delivery.Ack(false)
	case diagnosisworker.ActionRequeue:
		return delivery.Nack(false, true)
	case diagnosisworker.ActionRetry:
		queue, ok := diagnosisRetryQueue(c.config.DiagnosisQueue, outcome.RetryDelay)
		if !ok {
			queue = diagnosisRetryQueues(c.config.DiagnosisQueue)[0].name
		}
		if err := c.publishCopy(ctx, queue, delivery, outcome, false); err != nil {
			_ = delivery.Nack(false, true)
			return err
		}
		return delivery.Ack(false)
	case diagnosisworker.ActionDeadLetter:
		if err := c.publishCopy(ctx, diagnosisDeadQueue(c.config.DiagnosisQueue), delivery, outcome, true); err != nil {
			_ = delivery.Nack(false, true)
			return err
		}
		return delivery.Ack(false)
	default:
		return delivery.Nack(false, true)
	}
}

func (c *DiagnosisConsumer) publishCopy(
	ctx context.Context,
	queue string,
	delivery amqp.Delivery,
	outcome diagnosisworker.Outcome,
	dead bool,
) error {
	headers := cloneHeaders(delivery.Headers)
	headers["mesguard_last_reason"] = truncateHeader(outcome.Reason)
	headers["mesguard_original_queue"] = c.config.DiagnosisQueue
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
			UserId: delivery.UserId, AppId: "mesguard-diagnosis-worker",
			Body: append([]byte(nil), delivery.Body...),
		},
	)
	if err != nil {
		return fmt.Errorf("publish diagnosis retry/dead copy: %w", err)
	}
	acked, err := confirmation.WaitContext(publishCtx)
	if err != nil {
		return fmt.Errorf("wait diagnosis retry/dead confirm: %w", err)
	}
	if !acked {
		return ErrPublishNack
	}
	return nil
}

type retryQueue struct {
	name  string
	delay time.Duration
}

func diagnosisRetryQueues(base string) []retryQueue {
	return []retryQueue{
		{name: base + ".retry.30s", delay: 30 * time.Second},
		{name: base + ".retry.2m", delay: 2 * time.Minute},
		{name: base + ".retry.10m", delay: 10 * time.Minute},
	}
}

func diagnosisRetryQueue(base string, delay time.Duration) (string, bool) {
	for _, retry := range diagnosisRetryQueues(base) {
		if retry.delay == delay {
			return retry.name, true
		}
	}
	return "", false
}

func diagnosisDeadQueue(base string) string {
	return base + ".dead"
}

func cloneHeaders(source amqp.Table) amqp.Table {
	result := make(amqp.Table, len(source)+3)
	for key, value := range source {
		result[key] = value
	}
	return result
}

func truncateHeader(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 512 {
		return value
	}
	return strings.ToValidUTF8(value[:512], "?")
}
