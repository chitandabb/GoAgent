package attachment

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
	"github.com/chitandabb/GoAgent/internal/objectstore"
	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/google/uuid"
)

func TestServiceUploadPersistsObjectReferenceAndReplays(t *testing.T) {
	userID, conversationID, key := uuid.New(), uuid.New(), uuid.New()
	content := []byte("connection timeout")
	sha := sha256Text(content)
	repo := &attachmentRepositoryStub{}
	store := &attachmentStoreStub{putRef: objectstore.ObjectRef{
		Bucket: objectstore.BucketAttachments, ObjectKey: "attachments/object", ETag: "etag",
		SizeBytes: int64(len(content)), SHA256: sha, MediaType: "text/plain", OriginalName: "error.log",
	}}
	parser, err := knowledgeparser.NewRouter(knowledgeparser.TextParser{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(repo, store, parser, 1024)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := UploadFingerprint(ScopeSession, &conversationID, "error.log", "text/plain", int64(len(content)), sha)
	input := UploadInput{
		OwnerUserID: userID, Scope: ScopeSession, ConversationID: &conversationID,
		IdempotencyKey: key, RequestFingerprint: fingerprint, OriginalName: "error.log",
		MediaType: "text/plain", SizeBytes: int64(len(content)), ContentSHA256: sha,
		Content: bytes.NewReader(content), CreatedAt: time.Now().UTC(),
	}
	first, err := service.Upload(context.Background(), input)
	if err != nil {
		t.Fatalf("Upload(): %v", err)
	}
	if first.Replayed || repo.created.ID == uuid.Nil || store.putCalls != 1 ||
		repo.created.Ref.ObjectKey == "" || repo.created.Ref.SHA256 != sha {
		t.Fatalf("result=%+v created=%+v putCalls=%d", first, repo.created, store.putCalls)
	}

	repo.existing = &repo.created
	input.Content = strings.NewReader(string(content))
	replayed, err := service.Upload(context.Background(), input)
	if err != nil {
		t.Fatalf("replay Upload(): %v", err)
	}
	if !replayed.Replayed || replayed.Attachment.ID != first.Attachment.ID || store.putCalls != 1 {
		t.Fatalf("replayed=%+v putCalls=%d", replayed, store.putCalls)
	}

	conflict := input
	conflict.RequestFingerprint = strings.Repeat("f", 64)
	if _, err := service.Upload(context.Background(), conflict); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting Upload() error=%v", err)
	}
}

func TestServiceReadForMessageUsesMessageGateAndTruncates(t *testing.T) {
	userID, conversationID, messageID, attachmentID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	content := []byte("abcdef")
	item := validAttachment(attachmentID, userID, conversationID, content)
	repo := &attachmentRepositoryStub{messageReadable: &item}
	store := &attachmentStoreStub{getContent: content, getRef: item.Ref}
	parser, err := knowledgeparser.NewRouter(knowledgeparser.TextParser{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(repo, store, parser, 1024)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ReadForMessage(
		context.Background(), userID, conversationID, messageID, attachmentID, 3,
	)
	if err != nil {
		t.Fatalf("ReadForMessage(): %v", err)
	}
	if repo.gotMessageID != messageID || len(result.Elements) != 1 ||
		result.Elements[0].ContentText != "abc" || !result.Truncated {
		t.Fatalf("result=%+v messageID=%s", result, repo.gotMessageID)
	}

	repo.messageReadable = nil
	if _, err := service.ReadForMessage(
		context.Background(), userID, conversationID, messageID, attachmentID, 3,
	); !errors.Is(err, ErrAttachmentForbidden) {
		t.Fatalf("ungated ReadForMessage() error=%v", err)
	}
}

type attachmentRepositoryStub struct {
	existing        *Attachment
	messageReadable *Attachment
	created         Attachment
	gotMessageID    uuid.UUID
}

func (s *attachmentRepositoryStub) FindByIdempotency(context.Context, uuid.UUID, uuid.UUID) (Attachment, error) {
	if s.existing == nil {
		return Attachment{}, repository.ErrNotFound
	}
	return *s.existing, nil
}

func (s *attachmentRepositoryStub) Create(_ context.Context, item Attachment) error {
	s.created = item
	return nil
}

func (s *attachmentRepositoryStub) GetReadable(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (Attachment, error) {
	return Attachment{}, repository.ErrNotFound
}

func (s *attachmentRepositoryStub) GetMessageReadable(
	_ context.Context, _, _ uuid.UUID, messageID, _ uuid.UUID,
) (Attachment, error) {
	s.gotMessageID = messageID
	if s.messageReadable == nil {
		return Attachment{}, repository.ErrNotFound
	}
	return *s.messageReadable, nil
}

func (s *attachmentRepositoryStub) GetTaskReadable(
	context.Context, uuid.UUID, uuid.UUID, uuid.UUID,
) (Attachment, error) {
	return Attachment{}, repository.ErrNotFound
}

type attachmentStoreStub struct {
	putRef     objectstore.ObjectRef
	getRef     objectstore.ObjectRef
	getContent []byte
	putCalls   int
}

func (s *attachmentStoreStub) Put(_ context.Context, input objectstore.PutInput) (objectstore.ObjectRef, error) {
	s.putCalls++
	if !strings.HasPrefix(input.ObjectKey, "attachments/") {
		return objectstore.ObjectRef{}, errors.New("unexpected object key")
	}
	if _, err := io.ReadAll(input.Content); err != nil {
		return objectstore.ObjectRef{}, err
	}
	ref := s.putRef
	ref.ObjectKey = input.ObjectKey
	return ref, nil
}

func (s *attachmentStoreStub) Get(_ context.Context, ref objectstore.ObjectRef) (objectstore.ReadResult, error) {
	if ref.ObjectKey != s.getRef.ObjectKey {
		return objectstore.ReadResult{}, errors.New("unexpected ref")
	}
	return objectstore.ReadResult{
		Content: io.NopCloser(bytes.NewReader(s.getContent)), SizeBytes: int64(len(s.getContent)),
		ETag: ref.ETag, MediaType: ref.MediaType,
	}, nil
}

func (*attachmentStoreStub) Remove(context.Context, objectstore.ObjectRef) error { return nil }
func (*attachmentStoreStub) Close() error                                        { return nil }

func validAttachment(id, userID, conversationID uuid.UUID, content []byte) Attachment {
	createdAt := time.Now().UTC()
	return Attachment{
		ID: id, OwnerUserID: userID, Scope: ScopeSession, ConversationID: &conversationID,
		Status: StatusUploaded, IdempotencyKey: uuid.New(), RequestFingerprint: strings.Repeat("a", 64),
		Ref: objectstore.ObjectRef{
			Bucket: objectstore.BucketAttachments, ObjectKey: "attachments/object", ETag: "etag",
			SizeBytes: int64(len(content)), SHA256: sha256Text(content), MediaType: "text/plain",
			OriginalName: "error.log",
		},
		UploadedAt: createdAt, CreatedAt: createdAt, UpdatedAt: createdAt,
	}
}

func sha256Text(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}
