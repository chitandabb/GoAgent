// Package objectstore defines the storage boundary used by attachments and
// knowledge source objects. It intentionally does not expose S3/MinIO types.
package objectstore

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Bucket string

const (
	BucketAttachments        Bucket = "attachments"
	BucketKnowledgeSources   Bucket = "knowledge-source"
	BucketKnowledgeArtifacts Bucket = "knowledge-artifact"
)

func (b Bucket) Valid() bool {
	return b == BucketAttachments || b == BucketKnowledgeSources || b == BucketKnowledgeArtifacts
}

type PutInput struct {
	Bucket       Bucket
	ObjectKey    string
	Content      io.Reader
	SizeBytes    int64
	MediaType    string
	OriginalName string
}

func (i PutInput) Validate() error {
	if !i.Bucket.Valid() {
		return errors.New("object store bucket is invalid")
	}
	if strings.TrimSpace(i.ObjectKey) == "" || i.ObjectKey != strings.TrimSpace(i.ObjectKey) {
		return errors.New("object store object key is required and trimmed")
	}
	if strings.HasPrefix(i.ObjectKey, "/") || strings.Contains(i.ObjectKey, "\\") || strings.Contains(i.ObjectKey, "../") || strings.Contains(i.ObjectKey, "/..") {
		return errors.New("object store object key contains an unsafe path")
	}
	if i.Content == nil {
		return errors.New("object store content is required")
	}
	if i.SizeBytes < 0 {
		return errors.New("object store size must not be negative")
	}
	if strings.TrimSpace(i.MediaType) == "" || i.MediaType != strings.TrimSpace(i.MediaType) {
		return errors.New("object store media type is required and trimmed")
	}
	if len([]rune(i.OriginalName)) > 512 {
		return errors.New("object store original name is too long")
	}
	return nil
}

type ObjectRef struct {
	Bucket       Bucket
	ObjectKey    string
	VersionID    string
	ETag         string
	SizeBytes    int64
	SHA256       string
	MediaType    string
	OriginalName string
}

func (r ObjectRef) Validate() error {
	if !r.Bucket.Valid() || strings.TrimSpace(r.ObjectKey) == "" || r.ObjectKey != strings.TrimSpace(r.ObjectKey) {
		return errors.New("object store reference is incomplete")
	}
	if strings.HasPrefix(r.ObjectKey, "/") || strings.Contains(r.ObjectKey, "\\") ||
		strings.Contains(r.ObjectKey, "../") || strings.Contains(r.ObjectKey, "/..") {
		return errors.New("object store reference contains an unsafe path")
	}
	decodedSHA256, err := hex.DecodeString(strings.TrimSpace(r.SHA256))
	if r.SizeBytes < 0 || err != nil || len(decodedSHA256) != 32 {
		return errors.New("object store reference checksum or size is invalid")
	}
	if strings.TrimSpace(r.MediaType) == "" || strings.TrimSpace(r.ETag) == "" {
		return errors.New("object store reference metadata is incomplete")
	}
	return nil
}

type ReadResult struct {
	Content   io.ReadCloser
	SizeBytes int64
	VersionID string
	ETag      string
	MediaType string
}

func (r ReadResult) Validate() error {
	if r.Content == nil || r.SizeBytes < 0 || strings.TrimSpace(r.ETag) == "" || strings.TrimSpace(r.MediaType) == "" {
		return errors.New("object store read result is incomplete")
	}
	return nil
}

type Store interface {
	Put(context.Context, PutInput) (ObjectRef, error)
	Get(context.Context, ObjectRef) (ReadResult, error)
	Remove(context.Context, ObjectRef) error
	Close() error
}

type unavailableStore struct{ cause error }

func NewUnavailableStore(cause error) Store {
	if cause == nil {
		cause = errors.New("object store is unavailable")
	}
	return &unavailableStore{cause: cause}
}

func (s *unavailableStore) Put(context.Context, PutInput) (ObjectRef, error) {
	return ObjectRef{}, s.cause
}

func (s *unavailableStore) Get(context.Context, ObjectRef) (ReadResult, error) {
	return ReadResult{}, s.cause
}

func (s *unavailableStore) Remove(context.Context, ObjectRef) error { return s.cause }
func (*unavailableStore) Close() error                              { return nil }

// NewObjectKey returns a service-generated, extension-free key. The original
// name is metadata only and cannot alter object paths or bucket selection.
func NewObjectKey(bucket Bucket, id uuid.UUID, createdAt time.Time) (string, error) {
	if !bucket.Valid() || id == uuid.Nil {
		return "", errors.New("object store bucket and id are required")
	}
	createdAt = createdAt.UTC()
	prefix := string(bucket)
	return fmt.Sprintf("%s/%04d/%02d/%02d/%s", prefix, createdAt.Year(), createdAt.Month(), createdAt.Day(), id), nil
}
