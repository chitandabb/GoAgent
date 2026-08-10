package conversationworker

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestParseMessageRequiresEnvelopeAndAMQPIdentityToMatch(t *testing.T) {
	messageID, correlationID, turnID := uuid.New(), uuid.New(), uuid.New()
	occurredAt := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	body, err := json.Marshal(messageEnvelope{
		MessageID: messageID.String(), MessageType: MessageType, SchemaVersion: SchemaVersion,
		OccurredAt: occurredAt.Format(time.RFC3339Nano), CorrelationID: correlationID.String(),
		Payload: json.RawMessage(`{"turnId":"` + turnID.String() + `"}`),
	})
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	parsed, err := ParseMessage(IncomingMessage{
		ContentType: "application/json", MessageID: messageID.String(),
		CorrelationID: correlationID.String(), Type: MessageType, Body: body,
	})
	if err != nil {
		t.Fatalf("ParseMessage(): %v", err)
	}
	if parsed.TurnID != turnID || parsed.MessageID != messageID || !parsed.OccurredAt.Equal(occurredAt) {
		t.Fatalf("parsed message = %+v", parsed)
	}

	_, err = ParseMessage(IncomingMessage{
		ContentType: "application/json", MessageID: uuid.NewString(),
		CorrelationID: correlationID.String(), Type: MessageType, Body: body,
	})
	if !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("mismatched ParseMessage() error = %v, want ErrInvalidMessage", err)
	}
}

func TestParseMessageRejectsUnknownPayloadField(t *testing.T) {
	messageID, correlationID := uuid.New(), uuid.New()
	body, err := json.Marshal(messageEnvelope{
		MessageID: messageID.String(), MessageType: MessageType, SchemaVersion: SchemaVersion,
		OccurredAt: time.Now().UTC().Format(time.RFC3339Nano), CorrelationID: correlationID.String(),
		Payload: json.RawMessage(`{"turnId":"` + uuid.NewString() + `","extra":true}`),
	})
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	_, err = ParseMessage(IncomingMessage{
		ContentType: "application/json", MessageID: messageID.String(),
		CorrelationID: correlationID.String(), Type: MessageType, Body: body,
	})
	if !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("ParseMessage() error = %v, want ErrInvalidMessage", err)
	}
}
