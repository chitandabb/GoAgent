package minio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/chitandabb/GoAgent/internal/objectstore"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	minio "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Store struct {
	client           objectClient
	attachmentBucket string
	knowledgeBucket  string
	maxObjectBytes   int64
	config           config.MinIOConfig
	readyMu          sync.Mutex
	ready            bool
}

type objectClient interface {
	BucketExists(context.Context, string) (bool, error)
	MakeBucket(context.Context, string, minio.MakeBucketOptions) error
	PutObject(context.Context, string, string, io.Reader, int64, minio.PutObjectOptions) (minio.UploadInfo, error)
	RemoveObject(context.Context, string, string, minio.RemoveObjectOptions) error
}

var _ objectstore.Store = (*Store)(nil)

func Open(ctx context.Context, cfg config.MinIOConfig) (*Store, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, errors.New("minio is disabled")
	}
	accessKey, err := cfg.AccessKey()
	if err != nil {
		return nil, err
	}
	secretKey, err := cfg.SecretKey()
	if err != nil {
		return nil, err
	}
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: cfg.UseTLS,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("create minio client: %w", err)
	}
	store := &Store{
		client: client, attachmentBucket: cfg.AttachmentBucket,
		knowledgeBucket: cfg.KnowledgeSourceBucket, maxObjectBytes: cfg.MaxObjectBytes,
		config: cfg,
	}
	checkCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutMillis)*time.Millisecond)
	defer cancel()
	if err := store.ensureReady(checkCtx); err != nil {
		return store, err
	}
	return store, nil
}

func (s *Store) Put(ctx context.Context, input objectstore.PutInput) (objectstore.ObjectRef, error) {
	if s == nil || s.client == nil {
		return objectstore.ObjectRef{}, errors.New("minio store is unavailable")
	}
	if err := input.Validate(); err != nil {
		return objectstore.ObjectRef{}, err
	}
	if err := s.ensureReady(ctx); err != nil {
		return objectstore.ObjectRef{}, fmt.Errorf("prepare minio store: %w", err)
	}
	if input.SizeBytes > s.maxObjectBytes {
		return objectstore.ObjectRef{}, fmt.Errorf("object size %d exceeds configured limit %d", input.SizeBytes, s.maxObjectBytes)
	}
	bucket, err := s.bucketName(input.Bucket)
	if err != nil {
		return objectstore.ObjectRef{}, err
	}
	hasher := sha256.New()
	reader := io.TeeReader(input.Content, hasher)
	options := minio.PutObjectOptions{ContentType: input.MediaType}
	options.SetMatchETagExcept("*")
	info, err := s.client.PutObject(ctx, bucket, input.ObjectKey, reader, input.SizeBytes, options)
	if err != nil {
		return objectstore.ObjectRef{}, fmt.Errorf("put minio object: %w", err)
	}
	if info.Size != input.SizeBytes {
		_ = s.removeVersion(ctx, bucket, info.Key, info.VersionID)
		return objectstore.ObjectRef{}, fmt.Errorf("uploaded object size %d does not match declared size %d", info.Size, input.SizeBytes)
	}
	ref := objectstore.ObjectRef{
		Bucket: input.Bucket, ObjectKey: info.Key, VersionID: info.VersionID,
		ETag: info.ETag, SizeBytes: info.Size, SHA256: hex.EncodeToString(hasher.Sum(nil)),
		MediaType: input.MediaType, OriginalName: input.OriginalName,
	}
	if err := ref.Validate(); err != nil {
		_ = s.removeVersion(ctx, bucket, info.Key, info.VersionID)
		return objectstore.ObjectRef{}, err
	}
	return ref, nil
}

func (s *Store) Remove(ctx context.Context, ref objectstore.ObjectRef) error {
	if s == nil || s.client == nil {
		return errors.New("minio store is unavailable")
	}
	if err := ref.Validate(); err != nil {
		return err
	}
	bucket, err := s.bucketName(ref.Bucket)
	if err != nil {
		return err
	}
	return s.removeVersion(ctx, bucket, ref.ObjectKey, ref.VersionID)
}

func (s *Store) Close() error { return nil }

func (s *Store) ensureBucket(ctx context.Context, bucket string, cfg config.MinIOConfig) error {
	exists, err := s.client.BucketExists(ctx, bucket)
	if err != nil {
		return err
	}
	if !exists {
		if !cfg.AutoCreateBuckets {
			return fmt.Errorf("bucket does not exist and autoCreateBuckets is disabled")
		}
		if err := s.client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: cfg.Region}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureReady(ctx context.Context) error {
	s.readyMu.Lock()
	defer s.readyMu.Unlock()
	if s.ready {
		return nil
	}
	for _, bucket := range []string{s.config.AttachmentBucket, s.config.KnowledgeSourceBucket} {
		if err := s.ensureBucket(ctx, bucket, s.config); err != nil {
			return fmt.Errorf("ensure minio bucket %q: %w", bucket, err)
		}
	}
	s.ready = true
	return nil
}

func (s *Store) bucketName(bucket objectstore.Bucket) (string, error) {
	switch bucket {
	case objectstore.BucketAttachments:
		return s.attachmentBucket, nil
	case objectstore.BucketKnowledgeSources:
		return s.knowledgeBucket, nil
	default:
		return "", errors.New("object store bucket is invalid")
	}
}

func (s *Store) removeVersion(ctx context.Context, bucket, key, versionID string) error {
	return s.client.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{VersionID: strings.TrimSpace(versionID)})
}
