package rabbitmq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/chitandabb/GoAgent/internal/messaging"
	"github.com/chitandabb/GoAgent/internal/platform/config"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
)

var ErrPublishNack = errors.New("rabbitmq publisher confirm returned nack")

type Publisher struct {
	config config.RabbitMQConfig
	url    string

	mu         sync.Mutex
	connection *amqp.Connection
	channel    *amqp.Channel
}

var _ messaging.OutboxPublisher = (*Publisher)(nil)

func OpenPublisher(cfg config.RabbitMQConfig) (*Publisher, error) {
	if !cfg.Enabled {
		return nil, errors.New("rabbitmq is disabled")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	url, err := cfg.URL()
	if err != nil {
		return nil, err
	}
	publisher := &Publisher{config: cfg, url: url}
	publisher.mu.Lock()
	err = publisher.connectLocked()
	publisher.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return publisher, nil
}

func (p *Publisher) Publish(ctx context.Context, event messaging.OutboxEvent) error {
	if p == nil {
		return errors.New("rabbitmq publisher is unavailable")
	}
	routingKey, publishing, err := p.buildPublishing(event)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnectedLocked(); err != nil {
		return err
	}
	confirmation, err := p.channel.PublishWithDeferredConfirmWithContext(
		ctx, p.config.Exchange, routingKey, false, false, publishing,
	)
	if err != nil {
		p.resetLocked()
		return fmt.Errorf("publish rabbitmq message: %w", err)
	}
	acked, err := confirmation.WaitContext(ctx)
	if err != nil {
		p.resetLocked()
		return fmt.Errorf("wait rabbitmq publisher confirm: %w", err)
	}
	if !acked {
		return ErrPublishNack
	}
	return nil
}

func (p *Publisher) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var closeErr error
	if p.channel != nil && !p.channel.IsClosed() {
		closeErr = errors.Join(closeErr, p.channel.Close())
	}
	if p.connection != nil && !p.connection.IsClosed() {
		closeErr = errors.Join(closeErr, p.connection.Close())
	}
	p.channel = nil
	p.connection = nil
	return closeErr
}

func (p *Publisher) ensureConnectedLocked() error {
	if p.connection != nil && !p.connection.IsClosed() && p.channel != nil && !p.channel.IsClosed() {
		return nil
	}
	p.resetLocked()
	return p.connectLocked()
}

func (p *Publisher) connectLocked() error {
	connection, err := amqp.Dial(p.url)
	if err != nil {
		return fmt.Errorf("connect rabbitmq: %w", err)
	}
	channel, err := connection.Channel()
	if err != nil {
		_ = connection.Close()
		return fmt.Errorf("open rabbitmq channel: %w", err)
	}
	cleanup := func(err error) error {
		_ = channel.Close()
		_ = connection.Close()
		return err
	}
	if err := channel.ExchangeDeclare(p.config.Exchange, "direct", true, false, false, false, nil); err != nil {
		return cleanup(fmt.Errorf("declare rabbitmq exchange: %w", err))
	}
	if _, err := channel.QueueDeclare(p.config.DiagnosisQueue, true, false, false, false, nil); err != nil {
		return cleanup(fmt.Errorf("declare rabbitmq diagnosis queue: %w", err))
	}
	if err := channel.QueueBind(
		p.config.DiagnosisQueue, p.config.DiagnosisRoutingKey, p.config.Exchange, false, nil,
	); err != nil {
		return cleanup(fmt.Errorf("bind rabbitmq diagnosis queue: %w", err))
	}
	if _, err := channel.QueueDeclare(p.config.ConversationQueue, true, false, false, false, nil); err != nil {
		return cleanup(fmt.Errorf("declare rabbitmq conversation queue: %w", err))
	}
	if err := channel.QueueBind(
		p.config.ConversationQueue, p.config.ConversationRoutingKey, p.config.Exchange, false, nil,
	); err != nil {
		return cleanup(fmt.Errorf("bind rabbitmq conversation queue: %w", err))
	}
	if _, err := channel.QueueDeclare(p.config.MemoryCompactionQueue, true, false, false, false, nil); err != nil {
		return cleanup(fmt.Errorf("declare rabbitmq memory compaction queue: %w", err))
	}
	if err := channel.QueueBind(
		p.config.MemoryCompactionQueue, p.config.MemoryCompactionRoutingKey, p.config.Exchange, false, nil,
	); err != nil {
		return cleanup(fmt.Errorf("bind rabbitmq memory compaction queue: %w", err))
	}
	if _, err := channel.QueueDeclare(p.config.KnowledgeIngestionQueue, true, false, false, false, nil); err != nil {
		return cleanup(fmt.Errorf("declare rabbitmq knowledge ingestion queue: %w", err))
	}
	if err := channel.QueueBind(
		p.config.KnowledgeIngestionQueue, p.config.KnowledgeIngestionRoutingKey,
		p.config.Exchange, false, nil,
	); err != nil {
		return cleanup(fmt.Errorf("bind rabbitmq knowledge ingestion queue: %w", err))
	}
	if err := channel.Confirm(false); err != nil {
		return cleanup(fmt.Errorf("enable rabbitmq publisher confirms: %w", err))
	}
	p.connection = connection
	p.channel = channel
	return nil
}

func (p *Publisher) resetLocked() {
	if p.channel != nil && !p.channel.IsClosed() {
		_ = p.channel.Close()
	}
	if p.connection != nil && !p.connection.IsClosed() {
		_ = p.connection.Close()
	}
	p.channel = nil
	p.connection = nil
}

type messageEnvelope struct {
	MessageID     string          `json:"messageId"`
	MessageType   string          `json:"messageType"`
	SchemaVersion int             `json:"schemaVersion"`
	OccurredAt    string          `json:"occurredAt"`
	CorrelationID string          `json:"correlationId"`
	CausationID   *string         `json:"causationId"`
	Payload       json.RawMessage `json:"payload"`
}

func (p *Publisher) buildPublishing(event messaging.OutboxEvent) (string, amqp.Publishing, error) {
	if event.ID == uuid.Nil || event.AggregateID == uuid.Nil || event.CorrelationID == uuid.Nil ||
		event.PayloadSchemaVersion < 1 || !json.Valid(event.Payload) {
		return "", amqp.Publishing{}, errors.New("outbox event is invalid")
	}
	routingKey := ""
	switch event.EventType {
	case "diagnosis.execute":
		routingKey = p.config.DiagnosisRoutingKey
	case "conversation.turn.execute":
		routingKey = p.config.ConversationRoutingKey
	case messaging.EventTypeConversationMemoryCompact:
		routingKey = p.config.MemoryCompactionRoutingKey
	case "knowledge.ingest":
		routingKey = p.config.KnowledgeIngestionRoutingKey
	default:
		return "", amqp.Publishing{}, fmt.Errorf("unsupported outbox event type %q", event.EventType)
	}
	var causationID *string
	if event.CausationID != nil {
		value := event.CausationID.String()
		causationID = &value
	}
	body, err := json.Marshal(messageEnvelope{
		MessageID: event.ID.String(), MessageType: event.EventType,
		SchemaVersion: event.PayloadSchemaVersion, OccurredAt: event.CreatedAt.UTC().Format(time.RFC3339Nano),
		CorrelationID: event.CorrelationID.String(), CausationID: causationID,
		Payload: append(json.RawMessage(nil), event.Payload...),
	})
	if err != nil {
		return "", amqp.Publishing{}, fmt.Errorf("marshal rabbitmq message envelope: %w", err)
	}
	return routingKey, amqp.Publishing{
		Headers: amqp.Table{
			"schema_version": int32(event.PayloadSchemaVersion),
			"aggregate_type": event.AggregateType,
			"aggregate_id":   event.AggregateID.String(),
		},
		ContentType: "application/json", ContentEncoding: "utf-8",
		DeliveryMode: amqp.Persistent, MessageId: event.ID.String(),
		CorrelationId: event.CorrelationID.String(), Type: event.EventType,
		Timestamp: event.CreatedAt.UTC(), AppId: "mesguard-outbox-relay", Body: body,
	}, nil
}
