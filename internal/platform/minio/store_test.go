package minio

import (
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
