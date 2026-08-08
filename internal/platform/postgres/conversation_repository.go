package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/repository"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ConversationRepository persists conversation facts and their structured references.
// A conversation is scoped by user_id; task lifecycle remains owned by diagnosis_tasks.
type ConversationRepository struct {
	db *gorm.DB
}

var _ conversation.Repository = (*ConversationRepository)(nil)

func NewConversationRepository(db *gorm.DB) *ConversationRepository {
	return &ConversationRepository{db: db}
}

func (r *ConversationRepository) Create(
	ctx context.Context,
	userID uuid.UUID,
	input conversation.CreateInput,
	createdAt time.Time,
) (conversation.Conversation, error) {
	if r == nil || r.db == nil {
		return conversation.Conversation{}, errors.New("conversation repository is unavailable")
	}
	if userID == uuid.Nil {
		return conversation.Conversation{}, conversation.ErrInvalidConversation
	}
	id, err := uuid.NewV7()
	if err != nil {
		return conversation.Conversation{}, fmt.Errorf("generate conversation id: %w", err)
	}
	createdAt = createdAt.UTC()
	result := ResolveDB(ctx, r.db).Exec(`
INSERT INTO conversations (id, user_id, title, status, created_at, updated_at)
VALUES (?, ?, ?, 'active', ?, ?)`, id, userID, input.Title, createdAt, createdAt)
	if result.Error != nil {
		return conversation.Conversation{}, TranslateError(result.Error)
	}
	return conversation.Conversation{
		ID: id, UserID: userID, Title: input.Title, Status: conversation.StatusActive,
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}, nil
}

func (r *ConversationRepository) Get(
	ctx context.Context,
	userID, conversationID uuid.UUID,
) (conversation.Conversation, error) {
	if r == nil || r.db == nil {
		return conversation.Conversation{}, errors.New("conversation repository is unavailable")
	}
	if userID == uuid.Nil || conversationID == uuid.Nil {
		return conversation.Conversation{}, conversation.ErrInvalidConversation
	}
	var record conversationRecord
	result := ResolveDB(ctx, r.db).Raw(`
SELECT id, user_id, title, status, last_message_at, created_at, updated_at
FROM conversations
WHERE id = ? AND user_id = ?`, conversationID, userID).Scan(&record)
	if result.Error != nil {
		return conversation.Conversation{}, TranslateError(result.Error)
	}
	if result.RowsAffected == 0 {
		return conversation.Conversation{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
	}
	return conversationFromRecord(record), nil
}

func (r *ConversationRepository) List(
	ctx context.Context,
	userID uuid.UUID,
	query conversation.ListQuery,
) (conversation.ListResult, error) {
	if r == nil || r.db == nil {
		return conversation.ListResult{}, errors.New("conversation repository is unavailable")
	}
	if userID == uuid.Nil {
		return conversation.ListResult{}, conversation.ErrInvalidConversation
	}
	query.Normalize()
	db := ResolveDB(ctx, r.db)
	var total int64
	if err := db.Raw(`SELECT COUNT(*) FROM conversations WHERE user_id = ?`, userID).Scan(&total).Error; err != nil {
		return conversation.ListResult{}, TranslateError(err)
	}
	var records []conversationRecord
	result := db.Raw(`
SELECT id, user_id, title, status, last_message_at, created_at, updated_at
FROM conversations
WHERE user_id = ?
ORDER BY COALESCE(last_message_at, updated_at) DESC, id DESC
LIMIT ? OFFSET ?`, userID, query.PageSize, (query.Page-1)*query.PageSize).Scan(&records)
	if result.Error != nil {
		return conversation.ListResult{}, TranslateError(result.Error)
	}
	items := make([]conversation.Conversation, 0, len(records))
	for _, record := range records {
		items = append(items, conversationFromRecord(record))
	}
	return conversation.ListResult{Items: items, Total: int(total)}, nil
}

func (r *ConversationRepository) ListMessages(
	ctx context.Context,
	userID, conversationID uuid.UUID,
	query conversation.MessageQuery,
) (conversation.MessagePage, error) {
	if r == nil || r.db == nil {
		return conversation.MessagePage{}, errors.New("conversation repository is unavailable")
	}
	if userID == uuid.Nil || conversationID == uuid.Nil {
		return conversation.MessagePage{}, conversation.ErrInvalidConversation
	}
	query.Normalize()
	db := ResolveDB(ctx, r.db)
	var conversationExists bool
	if err := db.Raw(`SELECT EXISTS(SELECT 1 FROM conversations WHERE id = ? AND user_id = ?)`, conversationID, userID).Scan(&conversationExists).Error; err != nil {
		return conversation.MessagePage{}, TranslateError(err)
	}
	if !conversationExists {
		return conversation.MessagePage{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
	}
	var records []messageRecord
	result := db.Raw(`
SELECT id, conversation_id, seq, role, content, content_schema_version, created_at
FROM conversation_messages
WHERE conversation_id = ? AND seq > ?
ORDER BY seq ASC
LIMIT ?`, conversationID, query.AfterSeq, query.Limit+1).Scan(&records)
	if result.Error != nil {
		return conversation.MessagePage{}, TranslateError(result.Error)
	}
	hasMore := len(records) > query.Limit
	if hasMore {
		records = records[:query.Limit]
	}
	items := make([]conversation.Message, 0, len(records))
	for _, record := range records {
		items = append(items, messageFromRecord(record))
	}
	if err := r.loadReferences(db, items); err != nil {
		return conversation.MessagePage{}, err
	}
	nextAfterSeq := query.AfterSeq
	if len(items) > 0 {
		nextAfterSeq = items[len(items)-1].Seq
	}
	return conversation.MessagePage{Items: items, AfterSeq: query.AfterSeq, NextAfterSeq: nextAfterSeq, HasMore: hasMore}, nil
}

func (r *ConversationRepository) GetMessage(
	ctx context.Context,
	userID, conversationID, messageID uuid.UUID,
) (conversation.Message, error) {
	if r == nil || r.db == nil {
		return conversation.Message{}, errors.New("conversation repository is unavailable")
	}
	if userID == uuid.Nil || conversationID == uuid.Nil || messageID == uuid.Nil {
		return conversation.Message{}, conversation.ErrInvalidMessage
	}
	var record messageRecord
	result := ResolveDB(ctx, r.db).Raw(`
SELECT message.id, message.conversation_id, message.seq, message.role,
       message.content, message.content_schema_version, message.created_at
FROM conversation_messages message
JOIN conversations conversation ON conversation.id = message.conversation_id
WHERE message.id = ? AND message.conversation_id = ? AND conversation.user_id = ?`,
		messageID, conversationID, userID).Scan(&record)
	if result.Error != nil {
		return conversation.Message{}, TranslateError(result.Error)
	}
	if result.RowsAffected == 0 {
		return conversation.Message{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
	}
	messages := []conversation.Message{messageFromRecord(record)}
	if err := r.loadReferences(ResolveDB(ctx, r.db), messages); err != nil {
		return conversation.Message{}, err
	}
	return messages[0], nil
}

func (r *ConversationRepository) GetLatestMessage(
	ctx context.Context,
	userID, conversationID uuid.UUID,
) (conversation.Message, error) {
	if r == nil || r.db == nil {
		return conversation.Message{}, errors.New("conversation repository is unavailable")
	}
	if userID == uuid.Nil || conversationID == uuid.Nil {
		return conversation.Message{}, conversation.ErrInvalidMessage
	}
	var record messageRecord
	result := ResolveDB(ctx, r.db).Raw(`
SELECT message.id, message.conversation_id, message.seq, message.role,
       message.content, message.content_schema_version, message.created_at
FROM conversation_messages message
JOIN conversations conversation ON conversation.id = message.conversation_id
WHERE message.conversation_id = ? AND conversation.user_id = ?
ORDER BY message.seq DESC
LIMIT 1`, conversationID, userID).Scan(&record)
	if result.Error != nil {
		return conversation.Message{}, TranslateError(result.Error)
	}
	if result.RowsAffected == 0 {
		return conversation.Message{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
	}
	messages := []conversation.Message{messageFromRecord(record)}
	if err := r.loadReferences(ResolveDB(ctx, r.db), messages); err != nil {
		return conversation.Message{}, err
	}
	return messages[0], nil
}

func (r *ConversationRepository) AppendTaskReference(
	ctx context.Context,
	userID, messageID, taskID uuid.UUID,
	kind conversation.ReferenceKind,
	createdAt time.Time,
) error {
	if r == nil || r.db == nil {
		return errors.New("conversation repository is unavailable")
	}
	if userID == uuid.Nil || messageID == uuid.Nil || taskID == uuid.Nil ||
		(kind != conversation.ReferenceKindCreated && kind != conversation.ReferenceKindReferenced) {
		return conversation.ErrInvalidMessage
	}
	createdAt = createdAt.UTC()
	db := ResolveDB(ctx, r.db)
	result := db.Exec(`
INSERT INTO conversation_task_references (message_id, task_id, reference_kind, created_at)
SELECT message.id, task.id, ?, ?
FROM conversation_messages message
JOIN conversations conversation ON conversation.id = message.conversation_id
JOIN diagnosis_tasks task ON task.id = ? AND task.created_by = ?
WHERE message.id = ? AND conversation.user_id = ?
ON CONFLICT (message_id, task_id) DO NOTHING`,
		kind, createdAt, taskID, userID, messageID, userID)
	if result.Error != nil {
		return TranslateError(result.Error)
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var alreadyExists bool
	if err := db.Raw(`
SELECT EXISTS(
    SELECT 1
    FROM conversation_task_references reference
    JOIN conversation_messages message ON message.id = reference.message_id
    JOIN conversations conversation ON conversation.id = message.conversation_id
    JOIN diagnosis_tasks task ON task.id = reference.task_id
    WHERE reference.message_id = ? AND reference.task_id = ? AND conversation.user_id = ? AND task.created_by = ?
)`, messageID, taskID, userID, userID).Scan(&alreadyExists).Error; err != nil {
		return TranslateError(err)
	}
	if alreadyExists {
		return nil
	}
	return repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
}

func (r *ConversationRepository) AppendMessage(
	ctx context.Context,
	userID uuid.UUID,
	input conversation.AppendMessageInput,
	createdAt time.Time,
) (conversation.Message, error) {
	if r == nil || r.db == nil {
		return conversation.Message{}, errors.New("conversation repository is unavailable")
	}
	if userID == uuid.Nil || input.ConversationID == uuid.Nil {
		return conversation.Message{}, conversation.ErrInvalidMessage
	}
	id, err := uuid.NewV7()
	if err != nil {
		return conversation.Message{}, fmt.Errorf("generate conversation message id: %w", err)
	}
	createdAt = createdAt.UTC()
	var message conversation.Message
	err = ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		var ownerStatus struct {
			ID     uuid.UUID           `gorm:"column:id"`
			Status conversation.Status `gorm:"column:status"`
		}
		result := tx.Raw(`
SELECT id, status
FROM conversations
WHERE id = ? AND user_id = ?
FOR UPDATE`, input.ConversationID, userID).Scan(&ownerStatus)
		if result.Error != nil {
			return TranslateError(result.Error)
		}
		if result.RowsAffected == 0 {
			return repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
		}
		if ownerStatus.Status == conversation.StatusArchived {
			return conversation.ErrConversationArchived
		}
		var nextSeq int64
		if err := tx.Raw(`
SELECT COALESCE(MAX(seq), 0) + 1
FROM conversation_messages
WHERE conversation_id = ?`, input.ConversationID).Scan(&nextSeq).Error; err != nil {
			return TranslateError(err)
		}
		result = tx.Exec(`
INSERT INTO conversation_messages
    (id, conversation_id, seq, role, content, content_schema_version, created_at)
VALUES (?, ?, ?, ?, ?, 1, ?)`, id, input.ConversationID, nextSeq, input.Role, input.Content, createdAt)
		if result.Error != nil {
			return TranslateError(result.Error)
		}
		for _, ref := range input.CaseReferences {
			result = tx.Exec(`
INSERT INTO conversation_case_references (message_id, external_case_id, reference_kind, created_at)
SELECT ?, id, ?, ?
FROM external_cases
WHERE id = ?`, id, ref.Kind, createdAt, ref.ExternalCaseID)
			if result.Error != nil {
				return TranslateError(result.Error)
			}
			if result.RowsAffected != 1 {
				return repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
			}
		}
		for _, ref := range input.TaskReferences {
			result = tx.Exec(`
INSERT INTO conversation_task_references (message_id, task_id, reference_kind, created_at)
SELECT ?, id, ?, ?
FROM diagnosis_tasks
WHERE id = ? AND created_by = ?`, id, ref.Kind, createdAt, ref.TaskID, userID)
			if result.Error != nil {
				return TranslateError(result.Error)
			}
			if result.RowsAffected != 1 {
				return repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
			}
		}
		result = tx.Exec(`
UPDATE conversations
SET last_message_at = ?, updated_at = ?
WHERE id = ? AND user_id = ?`, createdAt, createdAt, input.ConversationID, userID)
		if result.Error != nil {
			return TranslateError(result.Error)
		}
		message = conversation.Message{
			ID: id, ConversationID: input.ConversationID, Seq: nextSeq,
			Role: input.Role, Content: input.Content, ContentSchemaVersion: 1,
			CaseReferences: append([]conversation.CaseReference(nil), input.CaseReferences...),
			TaskReferences: append([]conversation.TaskReference(nil), input.TaskReferences...),
			CreatedAt:      createdAt,
		}
		return nil
	})
	if err != nil {
		return conversation.Message{}, TranslateError(err)
	}
	return message, nil
}

func (r *ConversationRepository) loadReferences(db *gorm.DB, messages []conversation.Message) error {
	if len(messages) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(messages))
	index := make(map[uuid.UUID]int, len(messages))
	for i := range messages {
		ids[i] = messages[i].ID
		index[messages[i].ID] = i
	}
	var caseRefs []caseReferenceRecord
	if err := db.Raw(`
SELECT message_id, external_case_id, reference_kind
FROM conversation_case_references
WHERE message_id IN ?`, ids).Scan(&caseRefs).Error; err != nil {
		return TranslateError(err)
	}
	for _, ref := range caseRefs {
		if i, ok := index[ref.MessageID]; ok {
			messages[i].CaseReferences = append(messages[i].CaseReferences, conversation.CaseReference{
				ExternalCaseID: ref.ExternalCaseID, Kind: ref.Kind,
			})
		}
	}
	var taskRefs []taskReferenceRecord
	if err := db.Raw(`
SELECT message_id, task_id, reference_kind
FROM conversation_task_references
WHERE message_id IN ?`, ids).Scan(&taskRefs).Error; err != nil {
		return TranslateError(err)
	}
	for _, ref := range taskRefs {
		if i, ok := index[ref.MessageID]; ok {
			messages[i].TaskReferences = append(messages[i].TaskReferences, conversation.TaskReference{
				TaskID: ref.TaskID, Kind: ref.Kind,
			})
		}
	}
	return nil
}

type conversationRecord struct {
	ID            uuid.UUID           `gorm:"column:id"`
	UserID        uuid.UUID           `gorm:"column:user_id"`
	Title         string              `gorm:"column:title"`
	Status        conversation.Status `gorm:"column:status"`
	LastMessageAt *time.Time          `gorm:"column:last_message_at"`
	CreatedAt     time.Time           `gorm:"column:created_at"`
	UpdatedAt     time.Time           `gorm:"column:updated_at"`
}

func conversationFromRecord(record conversationRecord) conversation.Conversation {
	return conversation.Conversation{
		ID: record.ID, UserID: record.UserID, Title: record.Title, Status: record.Status,
		LastMessageAt: record.LastMessageAt, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
}

type messageRecord struct {
	ID                   uuid.UUID                `gorm:"column:id"`
	ConversationID       uuid.UUID                `gorm:"column:conversation_id"`
	Seq                  int64                    `gorm:"column:seq"`
	Role                 conversation.MessageRole `gorm:"column:role"`
	Content              string                   `gorm:"column:content"`
	ContentSchemaVersion int                      `gorm:"column:content_schema_version"`
	CreatedAt            time.Time                `gorm:"column:created_at"`
}

func messageFromRecord(record messageRecord) conversation.Message {
	return conversation.Message{
		ID: record.ID, ConversationID: record.ConversationID, Seq: record.Seq,
		Role: record.Role, Content: record.Content, ContentSchemaVersion: record.ContentSchemaVersion,
		CreatedAt: record.CreatedAt,
	}
}

type caseReferenceRecord struct {
	MessageID      uuid.UUID                  `gorm:"column:message_id"`
	ExternalCaseID uuid.UUID                  `gorm:"column:external_case_id"`
	Kind           conversation.ReferenceKind `gorm:"column:reference_kind"`
}

type taskReferenceRecord struct {
	MessageID uuid.UUID                  `gorm:"column:message_id"`
	TaskID    uuid.UUID                  `gorm:"column:task_id"`
	Kind      conversation.ReferenceKind `gorm:"column:reference_kind"`
}
