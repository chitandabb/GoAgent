package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/externalcase"

	"github.com/google/uuid"
)

type stubExternalCaseGetter struct{ item *externalcase.ExternalCase }

func (s stubExternalCaseGetter) Get(context.Context, uuid.UUID) (*externalcase.ExternalCase, error) {
	return s.item, nil
}

func TestReadExternalCaseToolDoesNotExposeObjectKey(t *testing.T) {
	id := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	now := time.Now().UTC()
	current, err := NewReadExternalCaseTool(stubExternalCaseGetter{item: &externalcase.ExternalCase{
		ID: id, ExternalCaseKey: "TKT-1", Title: "timeout", Description: "request timeout",
		ReportedAt: now, SourceUpdatedAt: now, SourceFingerprint: "hash",
		Attachments: []externalcase.ExternalAttachment{{
			ExternalAttachmentKey: "A-1", FileName: "error.png", MediaType: "image/png",
			ObjectKey: "private/erp/secret.png", ContentHash: "sha256", SourceUpdatedAt: now,
		}},
	}})
	if err != nil {
		t.Fatalf("NewReadExternalCaseTool: %v", err)
	}
	result, err := current.InvokableRun(context.Background(), `{"externalCaseId":"11111111-1111-1111-1111-111111111111"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if strings.Contains(result, "private/erp/secret.png") || !strings.Contains(result, "error.png") {
		t.Fatalf("unexpected tool result: %s", result)
	}
}
