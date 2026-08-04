package objectstore

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewObjectKeyIsServerGeneratedAndPartitioned(t *testing.T) {
	id := uuid.MustParse("018f6bb7-6e72-7d44-9b0e-f6f8a4e5e9c0")
	key, err := NewObjectKey(BucketKnowledgeSources, id, time.Date(2026, 8, 4, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60)))
	if err != nil {
		t.Fatalf("NewObjectKey: %v", err)
	}
	if key != "knowledge-source/2026/08/04/018f6bb7-6e72-7d44-9b0e-f6f8a4e5e9c0" {
		t.Fatalf("key = %q", key)
	}
	if strings.Contains(key, ".pdf") {
		t.Fatal("object key must not contain the original file extension")
	}
}

func TestPutInputRejectsUnsafeOrIncompleteInput(t *testing.T) {
	valid := PutInput{
		Bucket: BucketAttachments, ObjectKey: "attachments/2026/08/object",
		Content: strings.NewReader("content"), SizeBytes: 7, MediaType: "text/plain",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate valid input: %v", err)
	}
	invalid := valid
	invalid.ObjectKey = "../secret"
	if err := invalid.Validate(); err == nil {
		t.Fatal("Validate accepted unsafe object key")
	}
}

func TestNewObjectKeySeparatesElementArtifactsFromSources(t *testing.T) {
	key, err := NewObjectKey(BucketKnowledgeArtifacts, uuid.New(), time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "knowledge-artifact/2026/08/04/") {
		t.Fatalf("artifact key = %q", key)
	}
}
