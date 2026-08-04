package minio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/objectstore"
	minio "github.com/minio/minio-go/v7"
)

type fakeObjectReader struct {
	*bytes.Reader
	info    minio.ObjectInfo
	statErr error
	closed  bool
}

func (r *fakeObjectReader) Stat() (minio.ObjectInfo, error) { return r.info, r.statErr }
func (r *fakeObjectReader) Close() error {
	r.closed = true
	return nil
}

type fakeObjectClient struct {
	putInfo       minio.UploadInfo
	putErr        error
	putBucket     string
	putKey        string
	putContent    string
	removeBucket  string
	removeKey     string
	removeVersion string
	removeCalls   int
}

func (f *fakeObjectClient) BucketExists(context.Context, string) (bool, error) { return true, nil }
func (f *fakeObjectClient) MakeBucket(context.Context, string, minio.MakeBucketOptions) error {
	return nil
}
func (f *fakeObjectClient) PutObject(_ context.Context, bucket, key string, reader io.Reader, _ int64, _ minio.PutObjectOptions) (minio.UploadInfo, error) {
	f.putBucket = bucket
	f.putKey = key
	content, err := io.ReadAll(reader)
	if err != nil {
		return minio.UploadInfo{}, err
	}
	f.putContent = string(content)
	return f.putInfo, f.putErr
}
func (f *fakeObjectClient) RemoveObject(_ context.Context, bucket, key string, options minio.RemoveObjectOptions) error {
	f.removeCalls++
	f.removeBucket = bucket
	f.removeKey = key
	f.removeVersion = options.VersionID
	return nil
}

func TestStorePutReturnsImmutableReference(t *testing.T) {
	content := "MESGuard knowledge source"
	digest := sha256.Sum256([]byte(content))
	client := &fakeObjectClient{putInfo: minio.UploadInfo{
		Key: "knowledge-source/2026/08/04/object", ETag: "etag-1",
		VersionID: "version-1", Size: int64(len(content)),
	}}
	store := &Store{
		client: client, attachmentBucket: "attachments-real",
		knowledgeBucket: "knowledge-real", maxObjectBytes: 1024, ready: true,
	}
	ref, err := store.Put(context.Background(), objectstore.PutInput{
		Bucket: objectstore.BucketKnowledgeSources, ObjectKey: client.putInfo.Key,
		Content: strings.NewReader(content), SizeBytes: int64(len(content)),
		MediaType: "text/markdown", OriginalName: "manual.md",
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if client.putBucket != "knowledge-real" || client.putKey != client.putInfo.Key || client.putContent != content {
		t.Fatalf("unexpected put call: %+v", client)
	}
	if ref.VersionID != "version-1" || ref.ETag != "etag-1" || ref.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("unexpected reference: %+v", ref)
	}
}

func TestStorePutRejectsOversizeBeforeUpload(t *testing.T) {
	client := &fakeObjectClient{}
	store := &Store{client: client, attachmentBucket: "attachments", knowledgeBucket: "knowledge", maxObjectBytes: 3, ready: true}
	_, err := store.Put(context.Background(), objectstore.PutInput{
		Bucket: objectstore.BucketAttachments, ObjectKey: "attachments/object",
		Content: strings.NewReader("four"), SizeBytes: 4, MediaType: "text/plain",
	})
	if err == nil || client.putKey != "" {
		t.Fatalf("Put error = %v, upload key = %q", err, client.putKey)
	}
}

func TestStorePutCleansUploadedObjectWhenReferenceIsInvalid(t *testing.T) {
	client := &fakeObjectClient{putInfo: minio.UploadInfo{
		Key: "attachments/object", ETag: "", VersionID: "", Size: 7,
	}}
	store := &Store{client: client, attachmentBucket: "attachments-real", knowledgeBucket: "knowledge", maxObjectBytes: 1024, ready: true}
	_, err := store.Put(context.Background(), objectstore.PutInput{
		Bucket: objectstore.BucketAttachments, ObjectKey: "attachments/object",
		Content: strings.NewReader("content"), SizeBytes: 7, MediaType: "text/plain",
	})
	if err == nil {
		t.Fatal("Put accepted an upload without ETag")
	}
	if client.removeCalls != 1 || client.removeBucket != "attachments-real" || client.removeKey != "attachments/object" {
		t.Fatalf("unexpected cleanup: %+v", client)
	}
}

func TestStorePutReturnsClientErrorWithoutCleanup(t *testing.T) {
	want := errors.New("put failed")
	client := &fakeObjectClient{putErr: want}
	store := &Store{client: client, attachmentBucket: "attachments", knowledgeBucket: "knowledge", maxObjectBytes: 1024, ready: true}
	_, err := store.Put(context.Background(), objectstore.PutInput{
		Bucket: objectstore.BucketAttachments, ObjectKey: "attachments/object",
		Content: strings.NewReader("content"), SizeBytes: 7, MediaType: "text/plain",
	})
	if !errors.Is(err, want) || client.removeCalls != 0 {
		t.Fatalf("Put error = %v, cleanup calls = %d", err, client.removeCalls)
	}
}

func TestStoreGetStatsAndReturnsExactReferencedObject(t *testing.T) {
	content := []byte("stored content")
	reader := &fakeObjectReader{
		Reader: bytes.NewReader(content),
		info: minio.ObjectInfo{
			Size: int64(len(content)), ETag: "etag-1", VersionID: "version-1", ContentType: "text/plain",
		},
	}
	client := &fakeObjectClient{}
	store := &Store{
		client: client, attachmentBucket: "attachments", knowledgeBucket: "knowledge", ready: true,
		getObject: func(_ context.Context, bucket, key string, options minio.GetObjectOptions) (objectReader, error) {
			if bucket != "knowledge" || key != "knowledge-source/object" || options.VersionID != "version-1" {
				t.Fatalf("GetObject(%q, %q, version=%q)", bucket, key, options.VersionID)
			}
			return reader, nil
		},
	}
	result, err := store.Get(context.Background(), objectstore.ObjectRef{
		Bucket: objectstore.BucketKnowledgeSources, ObjectKey: "knowledge-source/object",
		VersionID: "version-1", ETag: "etag-1", SizeBytes: int64(len(content)),
		SHA256: strings.Repeat("a", 64), MediaType: "text/plain", OriginalName: "manual.txt",
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, err := io.ReadAll(result.Content)
	if err != nil || string(got) != string(content) {
		t.Fatalf("content=%q err=%v", got, err)
	}
	if err := result.Content.Close(); err != nil || !reader.closed {
		t.Fatalf("Close err=%v closed=%v", err, reader.closed)
	}
}

func TestStoreGetClosesReaderWhenStatMetadataMismatches(t *testing.T) {
	reader := &fakeObjectReader{Reader: bytes.NewReader(nil), info: minio.ObjectInfo{Size: 8, ETag: "other"}}
	store := &Store{
		client: &fakeObjectClient{}, attachmentBucket: "attachments", knowledgeBucket: "knowledge", ready: true,
		getObject: func(context.Context, string, string, minio.GetObjectOptions) (objectReader, error) {
			return reader, nil
		},
	}
	_, err := store.Get(context.Background(), objectstore.ObjectRef{
		Bucket: objectstore.BucketKnowledgeSources, ObjectKey: "knowledge-source/object",
		ETag: "etag", SizeBytes: 7, SHA256: strings.Repeat("a", 64), MediaType: "text/plain",
	})
	if err == nil || !reader.closed {
		t.Fatalf("Get error=%v closed=%v", err, reader.closed)
	}
}

func TestStoreGetRequiresExactVersionWhenReferenceIsVersioned(t *testing.T) {
	for _, statVersion := range []string{"", "version-2"} {
		t.Run("stat version "+statVersion, func(t *testing.T) {
			reader := &fakeObjectReader{
				Reader: bytes.NewReader([]byte("content")),
				info: minio.ObjectInfo{
					Size: 7, ETag: "etag", VersionID: statVersion, ContentType: "text/plain",
				},
			}
			store := &Store{
				client: &fakeObjectClient{}, attachmentBucket: "attachments", knowledgeBucket: "knowledge", ready: true,
				getObject: func(context.Context, string, string, minio.GetObjectOptions) (objectReader, error) {
					return reader, nil
				},
			}
			_, err := store.Get(context.Background(), objectstore.ObjectRef{
				Bucket: objectstore.BucketKnowledgeSources, ObjectKey: "knowledge-source/object",
				VersionID: "version-1", ETag: "etag", SizeBytes: 7,
				SHA256: strings.Repeat("a", 64), MediaType: "text/plain", OriginalName: "manual.txt",
			})
			if err == nil || !reader.closed {
				t.Fatalf("Get error=%v closed=%v", err, reader.closed)
			}
		})
	}
}
