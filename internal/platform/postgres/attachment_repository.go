package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/attachment"
	"github.com/chitandabb/GoAgent/internal/objectstore"
	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type AttachmentRepository struct {
	db *gorm.DB
}

var _ attachment.Repository = (*AttachmentRepository)(nil)

func NewAttachmentRepository(db *gorm.DB) *AttachmentRepository {
	return &AttachmentRepository{db: db}
}

func (r *AttachmentRepository) FindByIdempotency(ctx context.Context, userID, idempotencyKey uuid.UUID) (attachment.Attachment, error) {
	if r == nil || r.db == nil {
		return attachment.Attachment{}, errors.New("attachment repository is unavailable")
	}
	if userID == uuid.Nil || idempotencyKey == uuid.Nil {
		return attachment.Attachment{}, attachment.ErrInvalidAttachment
	}
	var record attachmentRecord
	result := ResolveDB(ctx, r.db).Raw(`
SELECT id, owner_user_id, scope, conversation_id, idempotency_key, upload_request_fingerprint,
       storage_bucket, storage_object_key, storage_object_version, storage_etag,
       original_filename, content_type, size_bytes, content_sha256, processing_status,
       uploaded_at, created_at, updated_at
FROM attachments
WHERE owner_user_id = ? AND idempotency_key = ?`, userID, idempotencyKey).Scan(&record)
	if result.Error != nil {
		return attachment.Attachment{}, TranslateError(result.Error)
	}
	if result.RowsAffected == 0 {
		return attachment.Attachment{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
	}
	return attachmentFromRecord(record)
}

func (r *AttachmentRepository) Create(ctx context.Context, item attachment.Attachment) error {
	if r == nil || r.db == nil {
		return errors.New("attachment repository is unavailable")
	}
	if item.ID == uuid.Nil || item.OwnerUserID == uuid.Nil || item.Ref.Bucket != objectstore.BucketAttachments {
		return attachment.ErrInvalidAttachment
	}
	var version any
	if strings.TrimSpace(item.Ref.VersionID) != "" {
		version = item.Ref.VersionID
	}
	db := ResolveDB(ctx, r.db)
	if item.Scope == attachment.ScopeSession {
		if item.ConversationID == nil || *item.ConversationID == uuid.Nil {
			return attachment.ErrInvalidAttachment
		}
		result := db.Exec(`
INSERT INTO attachments
    (id, owner_user_id, scope, conversation_id, idempotency_key, upload_request_fingerprint,
     storage_bucket, storage_object_key, storage_object_version, storage_etag,
     original_filename, content_type, size_bytes, content_sha256, processing_status,
     uploaded_at, created_at, updated_at)

SELECT ?, ?, ?, conversation.id, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
FROM conversations conversation
WHERE conversation.id = ? AND conversation.user_id = ?`,
			item.ID, item.OwnerUserID, item.Scope, item.IdempotencyKey,
			item.RequestFingerprint, item.Ref.Bucket, item.Ref.ObjectKey, version, item.Ref.ETag,
			item.Ref.OriginalName, item.Ref.MediaType, item.Ref.SizeBytes, item.Ref.SHA256,
			item.Status, item.UploadedAt.UTC(), item.CreatedAt.UTC(), item.UpdatedAt.UTC(),
			*item.ConversationID, item.OwnerUserID)
		if result.Error != nil {
			return TranslateError(result.Error)
		}
		if result.RowsAffected != 1 {
			return repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
		}
		return nil
	}
	result := db.Exec(`
INSERT INTO attachments
    (id, owner_user_id, scope, conversation_id, idempotency_key, upload_request_fingerprint,
     storage_bucket, storage_object_key, storage_object_version, storage_etag,
     original_filename, content_type, size_bytes, content_sha256, processing_status,
     uploaded_at, created_at, updated_at)
VALUES (?, ?, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.OwnerUserID, item.Scope, item.IdempotencyKey,
		item.RequestFingerprint, item.Ref.Bucket, item.Ref.ObjectKey, version, item.Ref.ETag,
		item.Ref.OriginalName, item.Ref.MediaType, item.Ref.SizeBytes, item.Ref.SHA256,
		item.Status, item.UploadedAt.UTC(), item.CreatedAt.UTC(), item.UpdatedAt.UTC())
	if result.Error != nil {
		return TranslateError(result.Error)
	}
	return nil
}

func (r *AttachmentRepository) GetReadable(ctx context.Context, userID, conversationID, attachmentID uuid.UUID) (attachment.Attachment, error) {
	if r == nil || r.db == nil {
		return attachment.Attachment{}, errors.New("attachment repository is unavailable")
	}
	if userID == uuid.Nil || conversationID == uuid.Nil || attachmentID == uuid.Nil {
		return attachment.Attachment{}, attachment.ErrInvalidAttachment
	}
	var record attachmentRecord
	result := ResolveDB(ctx, r.db).Raw(`
SELECT item.id, item.owner_user_id, item.scope, item.conversation_id, item.idempotency_key,
       item.upload_request_fingerprint, item.storage_bucket, item.storage_object_key,
       item.storage_object_version, item.storage_etag, item.original_filename, item.content_type,
       item.size_bytes, item.content_sha256, item.processing_status, item.uploaded_at,
       item.created_at, item.updated_at
FROM attachments item
JOIN conversations conversation ON conversation.id = ? AND conversation.user_id = ?
WHERE item.id = ? AND item.owner_user_id = ? AND item.processing_status = 'uploaded'
  AND (item.scope = 'personal' OR (item.scope = 'session' AND item.conversation_id = ?))`,
		conversationID, userID, attachmentID, userID, conversationID).Scan(&record)
	if result.Error != nil {
		return attachment.Attachment{}, TranslateError(result.Error)
	}
	if result.RowsAffected == 0 {
		return attachment.Attachment{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
	}
	return attachmentFromRecord(record)
}

func (r *AttachmentRepository) GetMessageReadable(
	ctx context.Context,
	userID, conversationID, messageID, attachmentID uuid.UUID,
) (attachment.Attachment, error) {
	if r == nil || r.db == nil {
		return attachment.Attachment{}, errors.New("attachment repository is unavailable")
	}
	if userID == uuid.Nil || conversationID == uuid.Nil || messageID == uuid.Nil || attachmentID == uuid.Nil {
		return attachment.Attachment{}, attachment.ErrInvalidAttachment
	}
	var record attachmentRecord
	result := ResolveDB(ctx, r.db).Raw(`
SELECT item.id, item.owner_user_id, item.scope, item.conversation_id, item.idempotency_key,
       item.upload_request_fingerprint, item.storage_bucket, item.storage_object_key,
       item.storage_object_version, item.storage_etag, item.original_filename, item.content_type,
       item.size_bytes, item.content_sha256, item.processing_status, item.uploaded_at,
       item.created_at, item.updated_at
FROM conversation_message_attachments reference
JOIN conversation_messages message
  ON message.id = reference.message_id AND message.conversation_id = reference.conversation_id
JOIN conversations conversation
  ON conversation.id = message.conversation_id AND conversation.user_id = ?
JOIN attachments item
  ON item.id = reference.attachment_id AND item.owner_user_id = conversation.user_id
WHERE reference.conversation_id = ? AND reference.message_id = ? AND reference.attachment_id = ?
  AND item.processing_status = 'uploaded'`,
		userID, conversationID, messageID, attachmentID).Scan(&record)
	if result.Error != nil {
		return attachment.Attachment{}, TranslateError(result.Error)
	}
	if result.RowsAffected == 0 {
		return attachment.Attachment{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
	}
	return attachmentFromRecord(record)
}

func (r *AttachmentRepository) GetTaskReadable(
	ctx context.Context,
	userID, taskID, attachmentID uuid.UUID,
) (attachment.Attachment, error) {
	if r == nil || r.db == nil {
		return attachment.Attachment{}, errors.New("attachment repository is unavailable")
	}
	if userID == uuid.Nil || taskID == uuid.Nil || attachmentID == uuid.Nil {
		return attachment.Attachment{}, attachment.ErrInvalidAttachment
	}
	var record attachmentRecord
	result := ResolveDB(ctx, r.db).Raw(`
SELECT item.id, item.owner_user_id, item.scope, item.conversation_id, item.idempotency_key,
       item.upload_request_fingerprint, item.storage_bucket, item.storage_object_key,
       item.storage_object_version, item.storage_etag, item.original_filename, item.content_type,
       item.size_bytes, item.content_sha256, item.processing_status, item.uploaded_at,
       item.created_at, item.updated_at
FROM diagnosis_task_attachments reference
JOIN diagnosis_tasks task ON task.id = reference.task_id AND task.created_by = ?
JOIN attachments item
  ON item.id = reference.attachment_id AND item.owner_user_id = task.created_by
WHERE reference.task_id = ? AND reference.attachment_id = ?
  AND item.processing_status = 'uploaded'`,
		userID, taskID, attachmentID).Scan(&record)
	if result.Error != nil {
		return attachment.Attachment{}, TranslateError(result.Error)
	}
	if result.RowsAffected == 0 {
		return attachment.Attachment{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
	}
	return attachmentFromRecord(record)
}

type attachmentRecord struct {
	ID                   uuid.UUID         `gorm:"column:id"`
	OwnerUserID          uuid.UUID         `gorm:"column:owner_user_id"`
	Scope                attachment.Scope  `gorm:"column:scope"`
	ConversationID       *uuid.UUID        `gorm:"column:conversation_id"`
	IdempotencyKey       uuid.UUID         `gorm:"column:idempotency_key"`
	RequestFingerprint   string            `gorm:"column:upload_request_fingerprint"`
	StorageBucket        string            `gorm:"column:storage_bucket"`
	StorageObjectKey     string            `gorm:"column:storage_object_key"`
	StorageObjectVersion *string           `gorm:"column:storage_object_version"`
	StorageETag          string            `gorm:"column:storage_etag"`
	OriginalFilename     string            `gorm:"column:original_filename"`
	ContentType          string            `gorm:"column:content_type"`
	SizeBytes            int64             `gorm:"column:size_bytes"`
	ContentSHA256        string            `gorm:"column:content_sha256"`
	Status               attachment.Status `gorm:"column:processing_status"`
	UploadedAt           time.Time         `gorm:"column:uploaded_at"`
	CreatedAt            time.Time         `gorm:"column:created_at"`
	UpdatedAt            time.Time         `gorm:"column:updated_at"`
}

func attachmentFromRecord(record attachmentRecord) (attachment.Attachment, error) {
	bucket := objectstore.Bucket(record.StorageBucket)
	version := ""
	if record.StorageObjectVersion != nil {
		version = strings.TrimSpace(*record.StorageObjectVersion)
	}
	item := attachment.Attachment{
		ID: record.ID, OwnerUserID: record.OwnerUserID, Scope: record.Scope,
		ConversationID: cloneUUID(record.ConversationID), Status: record.Status,
		IdempotencyKey: record.IdempotencyKey, RequestFingerprint: record.RequestFingerprint,
		Ref: objectstore.ObjectRef{
			Bucket: bucket, ObjectKey: record.StorageObjectKey, VersionID: version,
			ETag: record.StorageETag, SizeBytes: record.SizeBytes, SHA256: record.ContentSHA256,
			MediaType: record.ContentType, OriginalName: record.OriginalFilename,
		},
		UploadedAt: record.UploadedAt.UTC(), CreatedAt: record.CreatedAt.UTC(), UpdatedAt: record.UpdatedAt.UTC(),
	}
	if err := item.Ref.Validate(); err != nil {
		return attachment.Attachment{}, err
	}
	return item, nil
}

func cloneUUID(value *uuid.UUID) *uuid.UUID {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
