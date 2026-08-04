package knowledgeworker

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestParseMessageAcceptsMatchingStrictEnvelope(t *testing.T) {
	messageID, correlationID, taskID, versionID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	occurredAt := time.Now().UTC().Truncate(time.Microsecond)
	body := mustMessageBody(t, messageID, correlationID, taskID, versionID, occurredAt, nil)
	message, err := ParseMessage(IncomingMessage{
		ContentType: "application/json", MessageID: messageID.String(),
		CorrelationID: correlationID.String(), Type: MessageType, Body: body,
	})
	if err != nil {
		t.Fatalf("ParseMessage: %v", err)
	}
	if message.TaskID != taskID || message.DocumentVersionID != versionID || !message.OccurredAt.Equal(occurredAt) {
		t.Fatalf("message = %+v", message)
	}
}

func TestParseMessageRejectsUnknownPayloadAndPropertyMismatch(t *testing.T) {
	messageID, correlationID, taskID, versionID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	body := mustMessageBody(t, messageID, correlationID, taskID, versionID, time.Now().UTC(), map[string]any{"unexpected": true})
	_, err := ParseMessage(IncomingMessage{
		ContentType: "application/json", MessageID: messageID.String(),
		CorrelationID: correlationID.String(), Type: MessageType, Body: body,
	})
	if !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("ParseMessage error = %v", err)
	}
	body = mustMessageBody(t, messageID, correlationID, taskID, versionID, time.Now().UTC(), nil)
	_, err = ParseMessage(IncomingMessage{
		ContentType: "application/json", MessageID: uuid.NewString(),
		CorrelationID: correlationID.String(), Type: MessageType, Body: body,
	})
	if !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("ParseMessage mismatch error = %v", err)
	}
}

func mustMessageBody(
	t *testing.T,
	messageID, correlationID, taskID, versionID uuid.UUID,
	occurredAt time.Time,
	extraPayload map[string]any,
) []byte {
	t.Helper()
	payload := map[string]any{
		"taskId": taskID.String(), "documentVersionId": versionID.String(),
	}
	for key, value := range extraPayload {
		payload[key] = value
	}
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"messageId": messageID.String(), "messageType": MessageType,
		"schemaVersion": SchemaVersion, "occurredAt": occurredAt.Format(time.RFC3339Nano),
		"correlationId": correlationID.String(), "causationId": nil,
		"payload": json.RawMessage(encodedPayload),
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}
