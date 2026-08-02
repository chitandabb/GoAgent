package diagnosisworker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	DiagnosisMessageType   = "diagnosis.execute"
	DiagnosisSchemaVersion = 1
)

var ErrInvalidMessage = errors.New("diagnosis worker message is invalid")

// IncomingMessage 同时保留 AMQP properties 和 JSON 信封。两处标识必须一致，
// 避免排障元数据与实际执行载荷指向不同任务。
type IncomingMessage struct {
	ContentType   string
	MessageID     string
	CorrelationID string
	Type          string
	Body          []byte
}

type Message struct {
	MessageID     uuid.UUID
	CorrelationID uuid.UUID
	TaskID        uuid.UUID
	OccurredAt    time.Time
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

type diagnosisPayload struct {
	TaskID string `json:"taskId"`
}

func ParseMessage(input IncomingMessage) (Message, error) {
	if !strings.EqualFold(strings.TrimSpace(input.ContentType), "application/json") {
		return Message{}, fmt.Errorf("%w: content type", ErrInvalidMessage)
	}
	var envelope messageEnvelope
	if err := decodeStrictJSON(input.Body, &envelope); err != nil {
		return Message{}, fmt.Errorf("%w: envelope: %v", ErrInvalidMessage, err)
	}
	if envelope.MessageType != DiagnosisMessageType || envelope.SchemaVersion != DiagnosisSchemaVersion {
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
	var payload diagnosisPayload
	if err := decodeStrictJSON(envelope.Payload, &payload); err != nil {
		return Message{}, fmt.Errorf("%w: payload: %v", ErrInvalidMessage, err)
	}
	taskID, err := uuid.Parse(payload.TaskID)
	if err != nil {
		return Message{}, fmt.Errorf("%w: task id", ErrInvalidMessage)
	}
	if input.MessageID != envelope.MessageID || input.CorrelationID != envelope.CorrelationID ||
		input.Type != envelope.MessageType {
		return Message{}, fmt.Errorf("%w: AMQP properties do not match envelope", ErrInvalidMessage)
	}
	return Message{
		MessageID: messageID, CorrelationID: correlationID,
		TaskID: taskID, OccurredAt: occurredAt.UTC(),
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
