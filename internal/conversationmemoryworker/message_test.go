package conversationmemoryworker

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestParseMessageRejectsUnknownFieldsAndPropertyMismatch(t *testing.T) {
	now := time.Date(2026, 8, 11, 15, 0, 0, 0, time.UTC)
	jobID, conversationID := uuid.New(), uuid.New()

	t.Run("unknown envelope field", func(t *testing.T) {
		incoming := validIncomingMessage(t, jobID, conversationID, now)
		var envelope map[string]any
		if err := json.Unmarshal(incoming.Body, &envelope); err != nil {
			t.Fatal(err)
		}
		envelope["summary"] = "must not be accepted"
		encoded, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		incoming.Body = encoded
		if _, err := ParseMessage(incoming); !errors.Is(err, ErrInvalidMessage) {
			t.Fatalf("ParseMessage() error = %v", err)
		}
	})

	t.Run("unknown payload field", func(t *testing.T) {
		incoming := validIncomingMessage(t, jobID, conversationID, now)
		var envelope map[string]any
		if err := json.Unmarshal(incoming.Body, &envelope); err != nil {
			t.Fatal(err)
		}
		payload := envelope["payload"].(map[string]any)
		payload["snapshotPayload"] = "must not be accepted"
		encoded, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		incoming.Body = encoded
		if _, err := ParseMessage(incoming); !errors.Is(err, ErrInvalidMessage) {
			t.Fatalf("ParseMessage() error = %v", err)
		}
	})

	t.Run("AMQP property mismatch", func(t *testing.T) {
		incoming := validIncomingMessage(t, jobID, conversationID, now)
		incoming.CorrelationID = uuid.NewString()
		if _, err := ParseMessage(incoming); !errors.Is(err, ErrInvalidMessage) {
			t.Fatalf("ParseMessage() error = %v", err)
		}
	})
}
