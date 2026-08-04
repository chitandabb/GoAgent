package knowledge

import (
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/objectstore"
	"github.com/google/uuid"
)

func TestQueueVersionInputValidate(t *testing.T) {
	valid := QueueVersionInput{
		VersionID: uuid.New(), TaskID: uuid.New(), OutboxEventID: uuid.New(),
		CorrelationID: uuid.New(), DocumentID: uuid.New(), CreatedBy: uuid.New(),
		Source: objectstore.ObjectRef{
			Bucket: objectstore.BucketKnowledgeSources, ObjectKey: "knowledge-source/object",
			VersionID: "version", ETag: "etag", SizeBytes: 12,
			SHA256: SHA256Hex("source bytes"), MediaType: "application/pdf",
		},
		PipelineVersion: "ingestion-v1", MaxAttempts: 3,
		IdempotencyKey: uuid.NewString(), RequestFingerprint: SHA256Hex("request"),
		CreatedAt: time.Now().UTC(),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate valid input: %v", err)
	}
	invalid := valid
	invalid.Source.Bucket = objectstore.BucketAttachments
	if err := invalid.Validate(); err == nil {
		t.Fatal("Validate accepted attachment bucket as a knowledge source")
	}
	invalid = valid
	invalid.MaxAttempts = 0
	if err := invalid.Validate(); err == nil {
		t.Fatal("Validate accepted zero max attempts")
	}
}
