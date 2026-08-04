package knowledge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/objectstore"
	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/google/uuid"
)

type ingestionStoreStub struct {
	ref         objectstore.ObjectRef
	putInput    objectstore.PutInput
	removeRef   objectstore.ObjectRef
	removeErr   error
	removeCalls int
	putCalls    int
}

func (s *ingestionStoreStub) Put(_ context.Context, input objectstore.PutInput) (objectstore.ObjectRef, error) {
	s.putCalls++
	s.putInput = input
	return s.ref, nil
}
func (s *ingestionStoreStub) Get(context.Context, objectstore.ObjectRef) (objectstore.ReadResult, error) {
	return objectstore.ReadResult{}, errors.New("not implemented by upload test stub")
}

func TestIngestionServiceReplaysBeforeObjectUpload(t *testing.T) {
	taskID, versionID := uuid.New(), uuid.New()
	fingerprint := SHA256Hex("request")
	repository := &ingestionRepositoryStub{
		lookupResult: QueueVersionResult{
			Version: DocumentVersion{ID: versionID, DocumentID: uuid.New(), Version: 2},
			Task:    IngestionTask{ID: taskID, DocumentVersionID: versionID, Status: IngestionPending},
		},
		lookupFingerprint: fingerprint,
	}
	store := &ingestionStoreStub{}
	service, _ := NewIngestionService(store, repository)
	result, err := service.QueueSource(context.Background(), QueueSourceInput{
		DocumentID: repository.lookupResult.Version.DocumentID,
		CreatedBy:  uuid.New(), CorrelationID: uuid.New(), Content: strings.NewReader("content"),
		SizeBytes: 7, MediaType: "text/plain", OriginalName: "manual.txt",
		PipelineVersion: "ingestion-v1", MaxAttempts: 3, IdempotencyKey: uuid.NewString(),
		RequestFingerprint: fingerprint, ExpectedSourceSHA256: SHA256Hex("content"),
	})
	if err != nil {
		t.Fatalf("QueueSource replay: %v", err)
	}
	if !result.Replayed || result.Task.ID != taskID || store.putCalls != 0 {
		t.Fatalf("result = %+v putCalls = %d", result, store.putCalls)
	}
}

func TestIngestionServiceRejectsChangedIdempotentRequestBeforeObjectUpload(t *testing.T) {
	repository := &ingestionRepositoryStub{
		lookupResult:      QueueVersionResult{Task: IngestionTask{ID: uuid.New()}},
		lookupFingerprint: SHA256Hex("old-request"),
	}
	store := &ingestionStoreStub{}
	service, _ := NewIngestionService(store, repository)
	_, err := service.QueueSource(context.Background(), QueueSourceInput{
		DocumentID: uuid.New(), CreatedBy: uuid.New(), CorrelationID: uuid.New(),
		Content: strings.NewReader("content"), SizeBytes: 7, MediaType: "text/plain",
		OriginalName: "manual.txt", PipelineVersion: "ingestion-v1", MaxAttempts: 3,
		IdempotencyKey: uuid.NewString(), RequestFingerprint: SHA256Hex("new-request"),
		ExpectedSourceSHA256: SHA256Hex("content"),
	})
	if !errors.Is(err, ErrIdempotencyConflict) || store.putCalls != 0 {
		t.Fatalf("error = %v putCalls = %d", err, store.putCalls)
	}
}
func (s *ingestionStoreStub) Remove(_ context.Context, ref objectstore.ObjectRef) error {
	s.removeCalls++
	s.removeRef = ref
	return s.removeErr
}
func (s *ingestionStoreStub) Close() error { return nil }

type ingestionRepositoryStub struct {
	input             QueueVersionInput
	err               error
	lookupResult      QueueVersionResult
	lookupFingerprint string
	lookupErr         error
}

func (r *ingestionRepositoryStub) QueueVersion(_ context.Context, input QueueVersionInput) (QueueVersionResult, error) {
	r.input = input
	return QueueVersionResult{Version: DocumentVersion{ID: input.VersionID}, Task: IngestionTask{ID: input.TaskID}}, r.err
}

func (r *ingestionRepositoryStub) FindQueuedVersionByIdempotency(
	_ context.Context, _ uuid.UUID, _ string,
) (QueueVersionResult, string, error) {
	err := r.lookupErr
	if err == nil && r.lookupResult.Task.ID == uuid.Nil {
		err = repository.ErrNotFound
	}
	return r.lookupResult, r.lookupFingerprint, err
}

func TestIngestionServiceStoresThenQueuesImmutableSource(t *testing.T) {
	ref := objectstore.ObjectRef{
		Bucket: objectstore.BucketKnowledgeSources, ObjectKey: "knowledge-source/2026/08/04/object",
		VersionID: "version", ETag: "etag", SizeBytes: 7, SHA256: SHA256Hex("content"),
		MediaType: "text/plain", OriginalName: "manual.txt",
	}
	store := &ingestionStoreStub{ref: ref}
	repository := &ingestionRepositoryStub{}
	service, err := NewIngestionService(store, repository)
	if err != nil {
		t.Fatal(err)
	}
	service.clock = func() time.Time { return time.Date(2026, 8, 4, 1, 2, 3, 0, time.UTC) }
	documentID, creatorID, correlationID := uuid.New(), uuid.New(), uuid.New()
	idempotencyKey := uuid.NewString()
	fingerprint := SHA256Hex("request")
	result, err := service.QueueSource(context.Background(), QueueSourceInput{
		DocumentID: documentID, CreatedBy: creatorID, CorrelationID: correlationID,
		Content: strings.NewReader("content"), SizeBytes: 7, MediaType: "text/plain",
		OriginalName: "manual.txt", PipelineVersion: "ingestion-v1", MaxAttempts: 3,
		IdempotencyKey: idempotencyKey, RequestFingerprint: fingerprint,
		ExpectedSourceSHA256: SHA256Hex("content"),
	})
	if err != nil {
		t.Fatalf("QueueSource: %v", err)
	}
	if result.Version.ID == uuid.Nil || result.Task.ID == uuid.Nil || repository.input.Source != ref {
		t.Fatalf("unexpected result/input: %+v %+v", result, repository.input)
	}
	if !strings.HasPrefix(store.putInput.ObjectKey, "knowledge-source/2026/08/04/") || store.removeCalls != 0 {
		t.Fatalf("unexpected store calls: %+v", store)
	}
}

func TestIngestionServiceCompensatesExactVersionWhenQueueFails(t *testing.T) {
	queueErr := errors.New("database failed")
	cleanupErr := errors.New("cleanup failed")
	ref := objectstore.ObjectRef{
		Bucket: objectstore.BucketKnowledgeSources, ObjectKey: "knowledge-source/object",
		VersionID: "version-42", ETag: "etag", SizeBytes: 7,
		SHA256: SHA256Hex("content"), MediaType: "text/plain",
	}
	store := &ingestionStoreStub{ref: ref, removeErr: cleanupErr}
	repository := &ingestionRepositoryStub{err: queueErr}
	service, _ := NewIngestionService(store, repository)
	_, err := service.QueueSource(context.Background(), QueueSourceInput{
		DocumentID: uuid.New(), CreatedBy: uuid.New(), CorrelationID: uuid.New(),
		Content: strings.NewReader("content"), SizeBytes: 7, MediaType: "text/plain",
		OriginalName: "manual.txt", PipelineVersion: "ingestion-v1", MaxAttempts: 3,
		IdempotencyKey: uuid.NewString(), RequestFingerprint: SHA256Hex("request"),
		ExpectedSourceSHA256: SHA256Hex("content"),
	})
	if !errors.Is(err, queueErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("QueueSource error = %v", err)
	}
	if store.removeCalls != 1 || store.removeRef.VersionID != "version-42" {
		t.Fatalf("cleanup = %+v", store)
	}
}
