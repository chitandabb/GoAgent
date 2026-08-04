//go:build integration

package minio

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/objectstore"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/google/uuid"
	minio "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func TestStoreCreatesPrivateBucketsAndRoundTripsImmutableObject(t *testing.T) {
	endpoint := os.Getenv("MESGUARD_TEST_MINIO_ENDPOINT")
	accessKey := os.Getenv("MESGUARD_TEST_MINIO_ACCESS_KEY")
	secretKey := os.Getenv("MESGUARD_TEST_MINIO_SECRET_KEY")
	if endpoint == "" || accessKey == "" || secretKey == "" {
		t.Skip("MESGUARD_TEST_MINIO_ENDPOINT and credentials are not configured")
	}
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	attachmentBucket := "mesguard-test-attachments-" + suffix
	knowledgeBucket := "mesguard-test-knowledge-" + suffix
	cfg := config.MinIOConfig{
		Enabled: true, Endpoint: endpoint,
		AccessKeyEnv: "MESGUARD_TEST_MINIO_ACCESS_KEY", SecretKeyEnv: "MESGUARD_TEST_MINIO_SECRET_KEY",
		Region: "us-east-1", AttachmentBucket: attachmentBucket,
		KnowledgeSourceBucket: knowledgeBucket, AutoCreateBuckets: true,
		TimeoutMillis: 5_000, MaxObjectBytes: 1024,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	client, err := minio.New(endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(accessKey, secretKey, ""), Secure: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.RemoveBucket(context.Background(), attachmentBucket)
		_ = client.RemoveBucket(context.Background(), knowledgeBucket)
	})
	content := "versioned source"
	ref, err := store.Put(ctx, objectstore.PutInput{
		Bucket: objectstore.BucketKnowledgeSources, ObjectKey: "knowledge-source/integration-object",
		Content: strings.NewReader(content), SizeBytes: int64(len(content)),
		MediaType: "text/plain", OriginalName: "source.txt",
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if ref.VersionID != "" || ref.SHA256 == "" {
		t.Fatalf("incomplete object ref: %+v", ref)
	}
	read, err := store.Get(ctx, ref)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	roundTrip, err := io.ReadAll(read.Content)
	closeErr := read.Content.Close()
	if err != nil || closeErr != nil || string(roundTrip) != content {
		t.Fatalf("round trip content=%q readErr=%v closeErr=%v", roundTrip, err, closeErr)
	}
	if err := store.Remove(ctx, ref); err != nil {
		t.Fatalf("Remove: %v", err)
	}
}
