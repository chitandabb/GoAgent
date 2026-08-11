package conversationmemoryworker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/messaging"

	"github.com/google/uuid"
)

const (
	MessageType   = messaging.EventTypeConversationMemoryCompact
	SchemaVersion = 1
)

var ErrInvalidMessage = errors.New("conversation memory worker message is invalid")

type IncomingMessage struct {
	ContentType   string
	MessageID     string
	CorrelationID string
	Type          string
	Body          []byte
}

type Message struct {
	MessageID      uuid.UUID
	CorrelationID  uuid.UUID
	JobID          uuid.UUID
	ConversationID uuid.UUID
	OccurredAt     time.Time
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

type compactionPayload struct {
	JobID          string `json:"jobId"`
	ConversationID string `json:"conversationId"`
}

func ParseMessage(input IncomingMessage) (Message, error) {
	if !strings.EqualFold(strings.TrimSpace(input.ContentType), "application/json") {
		return Message{}, fmt.Errorf("%w: content type", ErrInvalidMessage)
	}
	var envelope messageEnvelope
	if err := decodeStrictJSON(input.Body, &envelope); err != nil {
		return Message{}, fmt.Errorf("%w: envelope: %v", ErrInvalidMessage, err)
	}
	if envelope.MessageType != MessageType || envelope.SchemaVersion != SchemaVersion {
		return Message{}, fmt.Errorf("%w: unsupported type or schema", ErrInvalidMessage)
	}
	messageID, err := uuid.Parse(envelope.MessageID)
	if err != nil {
		return Message{}, fmt.Errorf("%w: message id", ErrInvalidMessage)
	}
	correlationID, err := uuid.Parse(envelope.CorrelationID)
	if err != nil {
		return Message{}, fmt.Errorf("%w: correlation id", ErrInvalidMessage)
	}
	if envelope.CausationID != nil {
		if _, err := uuid.Parse(*envelope.CausationID); err != nil {
			return Message{}, fmt.Errorf("%w: causation id", ErrInvalidMessage)
		}
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, envelope.OccurredAt)
	if err != nil {
		return Message{}, fmt.Errorf("%w: occurred at", ErrInvalidMessage)
	}
	var payload compactionPayload
	if err := decodeStrictJSON(envelope.Payload, &payload); err != nil {
		return Message{}, fmt.Errorf("%w: payload: %v", ErrInvalidMessage, err)
	}
	jobID, err := uuid.Parse(payload.JobID)
	if err != nil {
		return Message{}, fmt.Errorf("%w: job id", ErrInvalidMessage)
	}
	conversationID, err := uuid.Parse(payload.ConversationID)
	if err != nil {
		return Message{}, fmt.Errorf("%w: conversation id", ErrInvalidMessage)
	}
	if input.MessageID != envelope.MessageID || input.CorrelationID != envelope.CorrelationID ||
		input.Type != envelope.MessageType {
		return Message{}, fmt.Errorf("%w: AMQP properties do not match envelope", ErrInvalidMessage)
	}
	return Message{
		MessageID: messageID, CorrelationID: correlationID, JobID: jobID,
		ConversationID: conversationID, OccurredAt: occurredAt.UTC(),
	}, nil
}

func decodeStrictJSON(raw []byte, target any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return errors.New("JSON value is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
