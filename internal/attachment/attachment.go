// Package attachment owns user-uploaded files and their bounded read boundary.
// Object storage details remain behind objectstore.Store; callers only receive
// stable attachment metadata and bounded extracted content.
package attachment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
	"github.com/chitandabb/GoAgent/internal/objectstore"
	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/google/uuid"
)

type Scope string

const (
	ScopeSession  Scope = "session"
	ScopePersonal Scope = "personal"
)

type Status string

const StatusUploaded Status = "uploaded"

const (
	MaxPurposeRunes       = 64
	MaxAttachmentsPerItem = 8
	DefaultReadRunes      = 12_000
	MaxReadRunes          = 32_000
)

var (
	ErrInvalidAttachment      = errors.New("attachment is invalid")
	ErrIdempotencyConflict    = errors.New("attachment idempotency key conflicts")
	ErrAttachmentForbidden    = errors.New("attachment is not readable in this context")
	ErrObjectStoreUnavailable = errors.New("attachment object store is unavailable")
)

type Attachment struct {
	ID                 uuid.UUID
	OwnerUserID        uuid.UUID
	Scope              Scope
	ConversationID     *uuid.UUID
	Status             Status
	IdempotencyKey     uuid.UUID
	RequestFingerprint string
	Ref                objectstore.ObjectRef
	UploadedAt         time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type UploadInput struct {
	OwnerUserID        uuid.UUID
	Scope              Scope
	ConversationID     *uuid.UUID
	IdempotencyKey     uuid.UUID
	RequestFingerprint string
	OriginalName       string
	MediaType          string
	SizeBytes          int64
	ContentSHA256      string
	Content            io.Reader
	CreatedAt          time.Time
}

func (i UploadInput) Validate() error {
	if i.OwnerUserID == uuid.Nil || i.IdempotencyKey == uuid.Nil || i.Content == nil || i.SizeBytes <= 0 {
		return ErrInvalidAttachment
	}
	if i.Scope != ScopeSession && i.Scope != ScopePersonal {
		return ErrInvalidAttachment
	}
	if i.Scope == ScopeSession && (i.ConversationID == nil || *i.ConversationID == uuid.Nil) {
		return ErrInvalidAttachment
	}
	if i.Scope == ScopePersonal && i.ConversationID != nil {
		return ErrInvalidAttachment
	}
	if strings.TrimSpace(i.OriginalName) == "" || i.OriginalName != strings.TrimSpace(i.OriginalName) || len([]rune(i.OriginalName)) > 512 {
		return ErrInvalidAttachment
	}
	if strings.TrimSpace(i.MediaType) == "" || i.MediaType != strings.TrimSpace(i.MediaType) {
		return ErrInvalidAttachment
	}
	contentDigest, contentErr := hex.DecodeString(strings.TrimSpace(i.ContentSHA256))
	fingerprintDigest, fingerprintErr := hex.DecodeString(strings.TrimSpace(i.RequestFingerprint))
	if contentErr != nil || len(contentDigest) != sha256.Size ||
		fingerprintErr != nil || len(fingerprintDigest) != sha256.Size {
		return ErrInvalidAttachment
	}
	return nil
}

type Repository interface {
	FindByIdempotency(context.Context, uuid.UUID, uuid.UUID) (Attachment, error)
	Create(context.Context, Attachment) error
	GetReadable(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (Attachment, error)
	GetMessageReadable(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) (Attachment, error)
	GetTaskReadable(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (Attachment, error)
}

type Reader interface {
	ReadForMessage(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, int) (ReadResult, error)
	ReadForTask(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, int) (ReadResult, error)
}

type Parser interface {
	Parse(context.Context, knowledgeparser.Input) (knowledgeparser.Result, error)
}

type Service struct {
	repository Repository
	store      objectstore.Store
	parser     Parser
	maxBytes   int64
	clock      func() time.Time
}

func NewService(repository Repository, store objectstore.Store, parser Parser, maxBytes int64) (*Service, error) {
	if repository == nil || store == nil || parser == nil || maxBytes < 1 {
		return nil, errors.New("attachment service dependencies are invalid")
	}
	return &Service{
		repository: repository, store: store, parser: parser, maxBytes: maxBytes,
		clock: func() time.Time { return time.Now().UTC() },
	}, nil
}

type UploadResult struct {
	Attachment Attachment
	Replayed   bool
}

func (s *Service) Upload(ctx context.Context, input UploadInput) (UploadResult, error) {
	if s == nil || s.repository == nil || s.store == nil {
		return UploadResult{}, errors.New("attachment service is unavailable")
	}
	if err := input.Validate(); err != nil || input.SizeBytes > s.maxBytes {
		return UploadResult{}, ErrInvalidAttachment
	}
	if existing, err := s.repository.FindByIdempotency(ctx, input.OwnerUserID, input.IdempotencyKey); err == nil {
		if existing.RequestFingerprint != input.RequestFingerprint {
			return UploadResult{}, ErrIdempotencyConflict
		}
		return UploadResult{Attachment: existing, Replayed: true}, nil
	} else if !errors.Is(err, repository.ErrNotFound) {
		return UploadResult{}, err
	}

	createdAt := input.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = s.clock().UTC()
	}
	attachmentID, err := uuid.NewV7()
	if err != nil {
		return UploadResult{}, fmt.Errorf("generate attachment id: %w", err)
	}
	objectKey, err := objectstore.NewObjectKey(objectstore.BucketAttachments, attachmentID, createdAt)
	if err != nil {
		return UploadResult{}, err
	}
	ref, err := s.store.Put(ctx, objectstore.PutInput{
		Bucket: objectstore.BucketAttachments, ObjectKey: objectKey, Content: input.Content,
		SizeBytes: input.SizeBytes, MediaType: input.MediaType, OriginalName: input.OriginalName,
	})
	if err != nil {
		return UploadResult{}, errors.Join(ErrObjectStoreUnavailable, err)
	}
	if ref.Bucket != objectstore.BucketAttachments || ref.SizeBytes != input.SizeBytes ||
		ref.SHA256 != input.ContentSHA256 || ref.MediaType != input.MediaType ||
		ref.OriginalName != input.OriginalName {
		_ = s.store.Remove(ctx, ref)
		return UploadResult{}, ErrInvalidAttachment
	}
	item := Attachment{
		ID: attachmentID, OwnerUserID: input.OwnerUserID, Scope: input.Scope,
		ConversationID: cloneUUID(input.ConversationID), Status: StatusUploaded,
		IdempotencyKey: input.IdempotencyKey, RequestFingerprint: input.RequestFingerprint,
		Ref: ref, UploadedAt: createdAt, CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	if err := s.repository.Create(ctx, item); err != nil {
		_ = s.store.Remove(ctx, ref)
		if errors.Is(err, repository.ErrConflict) {
			if existing, readErr := s.repository.FindByIdempotency(ctx, input.OwnerUserID, input.IdempotencyKey); readErr == nil && existing.RequestFingerprint == input.RequestFingerprint {
				return UploadResult{Attachment: existing, Replayed: true}, nil
			}
			return UploadResult{}, ErrIdempotencyConflict
		}
		return UploadResult{}, err
	}
	return UploadResult{Attachment: item}, nil
}

type ReadResult struct {
	Attachment       Attachment
	ParserVersion    string
	Elements         []Element
	VisualAssetCount int
	Truncated        bool
}

type Element struct {
	Index       int
	PageNumber  *int
	ElementType string
	SectionPath []string
	ContentText string
}

func (s *Service) Read(ctx context.Context, userID, conversationID, attachmentID uuid.UUID, maxRunes int) (ReadResult, error) {
	if s == nil || s.repository == nil || s.store == nil || s.parser == nil {
		return ReadResult{}, errors.New("attachment service is unavailable")
	}
	if userID == uuid.Nil || conversationID == uuid.Nil || attachmentID == uuid.Nil {
		return ReadResult{}, ErrInvalidAttachment
	}
	if maxRunes < 1 {
		maxRunes = DefaultReadRunes
	}
	if maxRunes > MaxReadRunes {
		maxRunes = MaxReadRunes
	}
	item, err := s.repository.GetReadable(ctx, userID, conversationID, attachmentID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ReadResult{}, ErrAttachmentForbidden
		}
		return ReadResult{}, err
	}
	return s.read(ctx, item, maxRunes)
}

func (s *Service) ReadForMessage(
	ctx context.Context,
	userID, conversationID, messageID, attachmentID uuid.UUID,
	maxRunes int,
) (ReadResult, error) {
	if s == nil || s.repository == nil || s.store == nil || s.parser == nil {
		return ReadResult{}, errors.New("attachment service is unavailable")
	}
	if userID == uuid.Nil || conversationID == uuid.Nil || messageID == uuid.Nil || attachmentID == uuid.Nil {
		return ReadResult{}, ErrInvalidAttachment
	}
	if maxRunes < 1 {
		maxRunes = DefaultReadRunes
	}
	if maxRunes > MaxReadRunes {
		maxRunes = MaxReadRunes
	}
	item, err := s.repository.GetMessageReadable(ctx, userID, conversationID, messageID, attachmentID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ReadResult{}, ErrAttachmentForbidden
		}
		return ReadResult{}, err
	}
	return s.read(ctx, item, maxRunes)
}

func (s *Service) ReadForTask(
	ctx context.Context,
	userID, taskID, attachmentID uuid.UUID,
	maxRunes int,
) (ReadResult, error) {
	if s == nil || s.repository == nil || s.store == nil || s.parser == nil {
		return ReadResult{}, errors.New("attachment service is unavailable")
	}
	if userID == uuid.Nil || taskID == uuid.Nil || attachmentID == uuid.Nil {
		return ReadResult{}, ErrInvalidAttachment
	}
	if maxRunes < 1 {
		maxRunes = DefaultReadRunes
	}
	if maxRunes > MaxReadRunes {
		maxRunes = MaxReadRunes
	}
	item, err := s.repository.GetTaskReadable(ctx, userID, taskID, attachmentID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ReadResult{}, ErrAttachmentForbidden
		}
		return ReadResult{}, err
	}
	return s.read(ctx, item, maxRunes)
}

func (s *Service) read(ctx context.Context, item Attachment, maxRunes int) (ReadResult, error) {
	if item.Ref.SizeBytes > s.maxBytes {
		return ReadResult{}, ErrInvalidAttachment
	}
	stored, err := s.store.Get(ctx, item.Ref)
	if err != nil {
		return ReadResult{}, errors.Join(ErrObjectStoreUnavailable, err)
	}
	defer stored.Content.Close()
	content, err := io.ReadAll(io.LimitReader(stored.Content, s.maxBytes+1))
	if err != nil {
		return ReadResult{}, err
	}
	if int64(len(content)) > s.maxBytes {
		return ReadResult{}, ErrInvalidAttachment
	}
	if int64(len(content)) != item.Ref.SizeBytes {
		return ReadResult{}, ErrInvalidAttachment
	}
	parsed, err := s.parser.Parse(ctx, knowledgeparser.Input{
		MediaType: item.Ref.MediaType, OriginalName: item.Ref.OriginalName, Content: content,
	})
	if err != nil {
		return ReadResult{}, err
	}
	result := ReadResult{
		Attachment: item, ParserVersion: parsed.ParserVersion,
		VisualAssetCount: len(parsed.VisualAssets),
	}
	used := 0
	for _, element := range parsed.Elements {
		text := strings.TrimSpace(element.ContentText)
		if text == "" {
			continue
		}
		remaining := maxRunes - used
		if remaining <= 0 {
			result.Truncated = true
			break
		}
		if len([]rune(text)) > remaining {
			text = string([]rune(text)[:remaining])
			result.Truncated = true
		}
		result.Elements = append(result.Elements, Element{
			Index: element.Index, PageNumber: cloneInt(element.PageNumber),
			ElementType: string(element.ElementType), SectionPath: append([]string(nil), element.SectionPath...),
			ContentText: text,
		})
		used += len([]rune(text))
		if result.Truncated {
			break
		}
	}
	return result, nil
}

func (s *Service) Preview(ctx context.Context, userID, conversationID, attachmentID uuid.UUID) (Attachment, error) {
	if s == nil || s.repository == nil {
		return Attachment{}, errors.New("attachment service is unavailable")
	}
	item, err := s.repository.GetReadable(ctx, userID, conversationID, attachmentID)
	if errors.Is(err, repository.ErrNotFound) {
		return Attachment{}, ErrAttachmentForbidden
	}
	return item, err
}

func cloneUUID(value *uuid.UUID) *uuid.UUID {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func UploadFingerprint(scope Scope, conversationID *uuid.UUID, originalName, mediaType string, sizeBytes int64, contentSHA256 string) string {
	conversation := ""
	if conversationID != nil {
		conversation = conversationID.String()
	}
	payload := fmt.Sprintf("attachment|%s|%s|%s|%s|%d|%s", scope, conversation, originalName, mediaType, sizeBytes, contentSHA256)
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}

var _ Reader = (*Service)(nil)
