package diagnosisworker

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestParseMessage(t *testing.T) {
	messageID := uuid.New()
	correlationID := uuid.New()
	taskID := uuid.New()
	body := mustEnvelope(t, messageID, correlationID, taskID, nil)

	message, err := ParseMessage(IncomingMessage{
		ContentType: "application/json", MessageID: messageID.String(),
		CorrelationID: correlationID.String(), Type: DiagnosisMessageType, Body: body,
	})
	if err != nil {
		t.Fatalf("ParseMessage(): %v", err)
	}
	if message.MessageID != messageID || message.CorrelationID != correlationID || message.TaskID != taskID {
		t.Fatalf("message = %+v", message)
	}
}

func TestParseMessageRejectsUnknownFieldsAndPropertyMismatch(t *testing.T) {
	messageID := uuid.New()
	correlationID := uuid.New()
	taskID := uuid.New()
	body := mustEnvelope(t, messageID, correlationID, taskID, map[string]any{"unexpected": true})
	_, err := ParseMessage(IncomingMessage{
		ContentType: "application/json", MessageID: messageID.String(),
		CorrelationID: correlationID.String(), Type: DiagnosisMessageType, Body: body,
	})
	if err == nil {
		t.Fatal("ParseMessage() accepted an unknown envelope field")
	}

	body = mustEnvelope(t, messageID, correlationID, taskID, nil)
	_, err = ParseMessage(IncomingMessage{
		ContentType: "application/json", MessageID: uuid.NewString(),
		CorrelationID: correlationID.String(), Type: DiagnosisMessageType, Body: body,
	})
	if err == nil {
		t.Fatal("ParseMessage() accepted mismatched AMQP properties")
	}
}

func mustEnvelope(
	t *testing.T,
	messageID, correlationID, taskID uuid.UUID,
	extra map[string]any,
) []byte {
	t.Helper()
	envelope := map[string]any{
		"messageId": messageID.String(), "messageType": DiagnosisMessageType,
		"schemaVersion": DiagnosisSchemaVersion,
		"occurredAt":    time.Now().UTC().Format(time.RFC3339Nano),
		"correlationId": correlationID.String(), "causationId": nil,
		"payload": map[string]any{"taskId": taskID.String()},
	}
	for key, value := range extra {
		envelope[key] = value
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return body
}
