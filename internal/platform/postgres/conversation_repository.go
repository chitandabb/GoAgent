package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

func (r *ConversationRepository) BeginTurn(
	ctx context.Context,
	userID uuid.UUID,
	input conversation.BeginTurnInput,
) (conversation.BeginTurnResult, error) {
	if r == nil || r.db == nil {
		return conversation.BeginTurnResult{}, errors.New("conversation repository is unavailable")
	}
	if userID == uuid.Nil || input.Message.ConversationID == uuid.Nil ||
		input.Message.Role != conversation.MessageRoleUser || strings.TrimSpace(input.IdempotencyKey) == "" ||
		strings.TrimSpace(input.RequestFingerprint) == "" || input.StartedAt.IsZero() || input.LeaseExpiresAt.IsZero() {
		return conversation.BeginTurnResult{}, conversation.ErrInvalidMessage
	}
	if _, err := uuid.Parse(strings.TrimSpace(input.IdempotencyKey)); err != nil {
		return conversation.BeginTurnResult{}, conversation.ErrInvalidMessage
	}
	startedAt := input.StartedAt.UTC()
	leaseExpiresAt := input.LeaseExpiresAt.UTC()
	if !leaseExpiresAt.After(startedAt) {
		return conversation.BeginTurnResult{}, conversation.ErrInvalidMessage
	}
	var result conversation.BeginTurnResult
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		var owner struct {
			ID     uuid.UUID           `gorm:"column:id"`
			Status conversation.Status `gorm:"column:status"`
		}
		query := tx.Raw(`
SELECT id, status
FROM conversations
WHERE id = ? AND user_id = ?
FOR UPDATE`, input.Message.ConversationID, userID).Scan(&owner)
		if query.Error != nil {
			return TranslateError(query.Error)
		}
		if query.RowsAffected == 0 {
			return repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
		}
		if owner.Status == conversation.StatusArchived {
			return conversation.ErrConversationArchived
		}

		var existing conversationTurnRecord
		query = tx.Raw(`
SELECT id, conversation_id, user_id, idempotency_key, request_fingerprint, status,
       user_message_id, assistant_message_id, attempt_count, lease_expires_at,
       completed_at, created_at, updated_at
FROM conversation_turns
WHERE conversation_id = ? AND idempotency_key = ?
FOR UPDATE`, input.Message.ConversationID, strings.TrimSpace(input.IdempotencyKey)).Scan(&existing)
		if query.Error != nil {
			return TranslateError(query.Error)
		}
		if query.RowsAffected == 1 {
			if existing.RequestFingerprint != strings.TrimSpace(input.RequestFingerprint) {
				return conversation.ErrTurnIdempotencyConflict
			}
			userMessage, err := loadConversationMessage(tx, userID, input.Message.ConversationID, existing.UserMessageID)
			if err != nil {
				return err
			}
			switch existing.Status {
			case conversation.TurnStatusCompleted:
				if existing.AssistantMessageID == nil {
					return errors.New("completed conversation turn has no assistant message")
				}
				assistantMessage, err := loadConversationMessage(tx, userID, input.Message.ConversationID, *existing.AssistantMessageID)
				if err != nil {
					return err
				}
				result = conversation.BeginTurnResult{
					TurnID: existing.ID, UserMessage: userMessage, AssistantMessage: &assistantMessage,
					Created: false,
				}
				return nil
			case conversation.TurnStatusRunning:
				if existing.LeaseExpiresAt != nil && existing.LeaseExpiresAt.After(startedAt) {
					return conversation.ErrTurnInProgress
				}
				if err := markTurnFailed(tx, existing.ID, startedAt); err != nil {
					return err
				}
				if err := ensureNoRunningTurn(tx, input.Message.ConversationID, existing.ID, startedAt); err != nil {
					return err
				}
				if err := ensureLatestUserMessage(tx, input.Message.ConversationID, existing.UserMessageID); err != nil {
					return err
				}
				if err := reopenTurn(tx, existing.ID, existing.AttemptCount+1, startedAt, leaseExpiresAt); err != nil {
					return err
				}
				result = conversation.BeginTurnResult{TurnID: existing.ID, UserMessage: userMessage, Created: false}
				return nil
			case conversation.TurnStatusFailed:
				if err := ensureNoRunningTurn(tx, input.Message.ConversationID, existing.ID, startedAt); err != nil {
					return err
				}
				if err := ensureLatestUserMessage(tx, input.Message.ConversationID, existing.UserMessageID); err != nil {
					return err
				}
				if err := reopenTurn(tx, existing.ID, existing.AttemptCount+1, startedAt, leaseExpiresAt); err != nil {
					return err
				}
				result = conversation.BeginTurnResult{TurnID: existing.ID, UserMessage: userMessage, Created: false}
				return nil
			default:
				return errors.New("conversation turn has an invalid status")
			}
		}

		if err := expireRunningTurns(tx, input.Message.ConversationID, startedAt); err != nil {
			return err
		}
		var active bool
		query = tx.Raw(`
SELECT EXISTS(
    SELECT 1 FROM conversation_turns
    WHERE conversation_id = ? AND status = ? AND lease_expires_at > ?
)`, input.Message.ConversationID, conversation.TurnStatusRunning, startedAt).Scan(&active)
		if query.Error != nil {
			return TranslateError(query.Error)
		}
		if active {
			return conversation.ErrTurnInProgress
		}

		messageID, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("generate conversation message id: %w", err)
		}
		message, err := insertConversationMessage(tx, userID, input.Message, messageID, startedAt)
		if err != nil {
			return err
		}
		turnID, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("generate conversation turn id: %w", err)
		}
		query = tx.Exec(`
INSERT INTO conversation_turns
    (id, conversation_id, user_id, idempotency_key, request_fingerprint, status,
     user_message_id, attempt_count, lease_expires_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)`,
			turnID, input.Message.ConversationID, userID, strings.TrimSpace(input.IdempotencyKey),
			strings.TrimSpace(input.RequestFingerprint), conversation.TurnStatusRunning, messageID,
			leaseExpiresAt, startedAt, startedAt)
		if query.Error != nil {
			return TranslateError(query.Error)
		}
		result = conversation.BeginTurnResult{TurnID: turnID, UserMessage: message, Created: true}
		return nil
	})
	if err != nil {
		return conversation.BeginTurnResult{}, TranslateError(err)
	}
	return result, nil
}

func (r *ConversationRepository) CompleteTurn(
	ctx context.Context,
	userID, turnID uuid.UUID,
	assistantContent string,
	completedAt time.Time,
) (conversation.ConversationTurn, error) {
	if r == nil || r.db == nil {
		return conversation.ConversationTurn{}, errors.New("conversation repository is unavailable")
	}
	assistantContent = strings.TrimSpace(assistantContent)
	if userID == uuid.Nil || turnID == uuid.Nil || assistantContent == "" || len([]rune(assistantContent)) > conversation.MaxContentRunes {
		return conversation.ConversationTurn{}, conversation.ErrInvalidMessage
	}
	completedAt = completedAt.UTC()
	var result conversation.ConversationTurn
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		var turnOwner struct {
			ConversationID uuid.UUID `gorm:"column:conversation_id"`
		}
		query := tx.Raw(`
SELECT conversation_id
FROM conversation_turns
WHERE id = ? AND user_id = ?`, turnID, userID).Scan(&turnOwner)
		if query.Error != nil {
			return TranslateError(query.Error)
		}
		if query.RowsAffected == 0 {
			return repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
		}
		if err := lockConversation(tx, userID, turnOwner.ConversationID); err != nil {
			return err
		}
		var turn conversationTurnRecord
		query = tx.Raw(`
SELECT id, conversation_id, user_id, idempotency_key, request_fingerprint, status,
       user_message_id, assistant_message_id, attempt_count, lease_expires_at,
       completed_at, created_at, updated_at
FROM conversation_turns
WHERE id = ? AND user_id = ?
FOR UPDATE`, turnID, userID).Scan(&turn)
		if query.Error != nil {
			return TranslateError(query.Error)
		}
		if query.RowsAffected == 0 {
			return repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
		}
		if turn.Status == conversation.TurnStatusCompleted {
			if turn.AssistantMessageID == nil {
				return errors.New("completed conversation turn has no assistant message")
			}
			userMessage, err := loadConversationMessage(tx, userID, turn.ConversationID, turn.UserMessageID)
			if err != nil {
				return err
			}
			assistantMessage, err := loadConversationMessage(tx, userID, turn.ConversationID, *turn.AssistantMessageID)
			if err != nil {
				return err
			}
			result = conversation.ConversationTurn{UserMessage: userMessage, AssistantMessage: assistantMessage}
			return nil
		}
		if turn.Status != conversation.TurnStatusRunning || turn.LeaseExpiresAt == nil || !turn.LeaseExpiresAt.After(completedAt) {
			return conversation.ErrTurnLeaseLost
		}
		if err := ensureLatestUserMessage(tx, turn.ConversationID, turn.UserMessageID); err != nil {
			return err
		}
		userMessage, err := loadConversationMessage(tx, userID, turn.ConversationID, turn.UserMessageID)
		if err != nil {
			return err
		}
		assistantMessageID, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("generate assistant message id: %w", err)
		}
		assistantMessage, err := insertConversationMessage(tx, userID, conversation.AppendMessageInput{
			ConversationID: turn.ConversationID, Role: conversation.MessageRoleAssistant,
			Content: assistantContent, TaskReferences: createdTaskReferences(userMessage),
		}, assistantMessageID, completedAt)
		if err != nil {
			return err
		}
		query = tx.Exec(`
UPDATE conversation_turns
SET status = ?, assistant_message_id = ?, lease_expires_at = NULL,
    completed_at = ?, updated_at = ?
WHERE id = ? AND user_id = ? AND status = ?`,
			conversation.TurnStatusCompleted, assistantMessageID, completedAt, completedAt,
			turnID, userID, conversation.TurnStatusRunning)
		if query.Error != nil {
			return TranslateError(query.Error)
		}
		if query.RowsAffected != 1 {
			return conversation.ErrTurnLeaseLost
		}
		result = conversation.ConversationTurn{UserMessage: userMessage, AssistantMessage: assistantMessage}
		return nil
	})
	if err != nil {
		return conversation.ConversationTurn{}, TranslateError(err)
	}
	return result, nil
}

func (r *ConversationRepository) FailTurn(
	ctx context.Context,
	userID, turnID uuid.UUID,
	failedAt time.Time,
) error {
	if r == nil || r.db == nil {
		return errors.New("conversation repository is unavailable")
	}
	if userID == uuid.Nil || turnID == uuid.Nil {
		return conversation.ErrInvalidMessage
	}
	failedAt = failedAt.UTC()
	query := ResolveDB(ctx, r.db).Exec(`
UPDATE conversation_turns
SET status = ?, lease_expires_at = NULL, updated_at = ?
WHERE id = ? AND user_id = ? AND status = ?`,
		conversation.TurnStatusFailed, failedAt, turnID, userID, conversation.TurnStatusRunning)
	if query.Error != nil {
		return TranslateError(query.Error)
	}
	if query.RowsAffected == 1 || query.RowsAffected == 0 {
		return nil
	}
	return errors.New("unexpected conversation turn failure update count")
}

func lockConversation(tx *gorm.DB, userID, conversationID uuid.UUID) error {
	var owner struct {
		ID     uuid.UUID           `gorm:"column:id"`
		Status conversation.Status `gorm:"column:status"`
	}
	query := tx.Raw(`
SELECT id, status
FROM conversations
WHERE id = ? AND user_id = ?
FOR UPDATE`, conversationID, userID).Scan(&owner)
	if query.Error != nil {
		return TranslateError(query.Error)
	}
	if query.RowsAffected == 0 {
		return repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
	}
	if owner.Status == conversation.StatusArchived {
		return conversation.ErrConversationArchived
	}
	return nil
}

func expireRunningTurns(tx *gorm.DB, conversationID uuid.UUID, now time.Time) error {
	result := tx.Exec(`
UPDATE conversation_turns
SET status = ?, lease_expires_at = NULL, updated_at = ?
WHERE conversation_id = ? AND status = ? AND lease_expires_at <= ?`,
		conversation.TurnStatusFailed, now, conversationID, conversation.TurnStatusRunning, now)
	if result.Error != nil {
		return TranslateError(result.Error)
	}
	return nil
}

func ensureNoRunningTurn(tx *gorm.DB, conversationID, excludedTurnID uuid.UUID, now time.Time) error {
	if err := expireRunningTurns(tx, conversationID, now); err != nil {
		return err
	}
	var active bool
	query := tx.Raw(`
SELECT EXISTS(
    SELECT 1 FROM conversation_turns
    WHERE conversation_id = ? AND id <> ? AND status = ? AND lease_expires_at > ?
)`, conversationID, excludedTurnID, conversation.TurnStatusRunning, now).Scan(&active)
	if query.Error != nil {
		return TranslateError(query.Error)
	}
	if active {
		return conversation.ErrTurnInProgress
	}
	return nil
}

func markTurnFailed(tx *gorm.DB, turnID uuid.UUID, failedAt time.Time) error {
	result := tx.Exec(`
UPDATE conversation_turns
SET status = ?, lease_expires_at = NULL, updated_at = ?
WHERE id = ? AND status = ?`,
		conversation.TurnStatusFailed, failedAt, turnID, conversation.TurnStatusRunning)
	if result.Error != nil {
		return TranslateError(result.Error)
	}
	return nil
}

func reopenTurn(tx *gorm.DB, turnID uuid.UUID, attemptCount int, startedAt, leaseExpiresAt time.Time) error {
	result := tx.Exec(`
UPDATE conversation_turns
SET status = ?, attempt_count = ?, lease_expires_at = ?, updated_at = ?
WHERE id = ? AND status = ?`,
		conversation.TurnStatusRunning, attemptCount, leaseExpiresAt, startedAt, turnID, conversation.TurnStatusFailed)
	if result.Error != nil {
		return TranslateError(result.Error)
	}
	if result.RowsAffected != 1 {
		return errors.New("conversation turn could not be reopened")
	}
	return nil
}

func ensureLatestUserMessage(tx *gorm.DB, conversationID, messageID uuid.UUID) error {
	var latest struct {
		ID   uuid.UUID                `gorm:"column:id"`
		Role conversation.MessageRole `gorm:"column:role"`
	}
	query := tx.Raw(`
SELECT id, role
FROM conversation_messages
WHERE conversation_id = ?
ORDER BY seq DESC
LIMIT 1`, conversationID).Scan(&latest)
	if query.Error != nil {
		return TranslateError(query.Error)
	}
	if query.RowsAffected == 0 {
		return repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
	}
	if latest.ID != messageID || latest.Role != conversation.MessageRoleUser {
		return conversation.ErrCommandNotLatest
	}
	return nil
}

func insertConversationMessage(
	tx *gorm.DB,
	userID uuid.UUID,
	input conversation.AppendMessageInput,
	id uuid.UUID,
	createdAt time.Time,
) (conversation.Message, error) {
	var nextSeq int64
	query := tx.Raw(`
SELECT COALESCE(MAX(seq), 0) + 1
FROM conversation_messages
WHERE conversation_id = ?`, input.ConversationID).Scan(&nextSeq)
	if query.Error != nil {
		return conversation.Message{}, TranslateError(query.Error)
	}
	query = tx.Exec(`
INSERT INTO conversation_messages
    (id, conversation_id, seq, role, content, content_schema_version, created_at)
VALUES (?, ?, ?, ?, ?, 1, ?)`, id, input.ConversationID, nextSeq, input.Role, input.Content, createdAt)
	if query.Error != nil {
		return conversation.Message{}, TranslateError(query.Error)
	}
	for _, ref := range input.CaseReferences {
		query = tx.Exec(`
INSERT INTO conversation_case_references (message_id, external_case_id, reference_kind, created_at)
SELECT ?, id, ?, ?
FROM external_cases
WHERE id = ?`, id, ref.Kind, createdAt, ref.ExternalCaseID)
		if query.Error != nil {
			return conversation.Message{}, TranslateError(query.Error)
		}
		if query.RowsAffected != 1 {
			return conversation.Message{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
		}
	}
	for _, ref := range input.TaskReferences {
		query = tx.Exec(`
INSERT INTO conversation_task_references (message_id, task_id, reference_kind, created_at)
SELECT ?, id, ?, ?
FROM diagnosis_tasks
WHERE id = ? AND created_by = ?`, id, ref.Kind, createdAt, ref.TaskID, userID)
		if query.Error != nil {
			return conversation.Message{}, TranslateError(query.Error)
		}
		if query.RowsAffected != 1 {
			return conversation.Message{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
		}
	}
	query = tx.Exec(`
UPDATE conversations
SET last_message_at = ?, updated_at = ?
WHERE id = ? AND user_id = ?`, createdAt, createdAt, input.ConversationID, userID)
	if query.Error != nil {
		return conversation.Message{}, TranslateError(query.Error)
	}
	return conversation.Message{
		ID: id, ConversationID: input.ConversationID, Seq: nextSeq,
		Role: input.Role, Content: input.Content, ContentSchemaVersion: 1,
		CaseReferences: append([]conversation.CaseReference(nil), input.CaseReferences...),
		TaskReferences: append([]conversation.TaskReference(nil), input.TaskReferences...),
		CreatedAt:      createdAt,
	}, nil
}

func createdTaskReferences(message conversation.Message) []conversation.TaskReference {
	created := make([]conversation.TaskReference, 0, len(message.TaskReferences))
	for _, reference := range message.TaskReferences {
		if reference.Kind == conversation.ReferenceKindCreated {
			created = append(created, reference)
		}
	}
	return created
}

func loadConversationMessage(
	db *gorm.DB,
	userID, conversationID, messageID uuid.UUID,
) (conversation.Message, error) {
	var record messageRecord
	query := db.Raw(`
SELECT message.id, message.conversation_id, message.seq, message.role,
       message.content, message.content_schema_version, message.created_at
FROM conversation_messages message
JOIN conversations conversation ON conversation.id = message.conversation_id
WHERE message.id = ? AND message.conversation_id = ? AND conversation.user_id = ?`,
		messageID, conversationID, userID).Scan(&record)
	if query.Error != nil {
		return conversation.Message{}, TranslateError(query.Error)
	}
	if query.RowsAffected == 0 {
		return conversation.Message{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
	}
	messages := []conversation.Message{messageFromRecord(record)}
	if err := loadConversationReferences(db, messages); err != nil {
		return conversation.Message{}, err
	}
	return messages[0], nil
}

func loadConversationReferences(db *gorm.DB, messages []conversation.Message) error {
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
		if position, ok := index[ref.MessageID]; ok {
			messages[position].CaseReferences = append(messages[position].CaseReferences, conversation.CaseReference{
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
		if position, ok := index[ref.MessageID]; ok {
			messages[position].TaskReferences = append(messages[position].TaskReferences, conversation.TaskReference{
				TaskID: ref.TaskID, Kind: ref.Kind,
			})
		}
	}
	return nil
}

type conversationTurnRecord struct {
	ID                 uuid.UUID               `gorm:"column:id"`
	ConversationID     uuid.UUID               `gorm:"column:conversation_id"`
	UserID             uuid.UUID               `gorm:"column:user_id"`
	IdempotencyKey     uuid.UUID               `gorm:"column:idempotency_key"`
	RequestFingerprint string                  `gorm:"column:request_fingerprint"`
	Status             conversation.TurnStatus `gorm:"column:status"`
	UserMessageID      uuid.UUID               `gorm:"column:user_message_id"`
	AssistantMessageID *uuid.UUID              `gorm:"column:assistant_message_id"`
	AttemptCount       int                     `gorm:"column:attempt_count"`
	LeaseExpiresAt     *time.Time              `gorm:"column:lease_expires_at"`
	CompletedAt        *time.Time              `gorm:"column:completed_at"`
	CreatedAt          time.Time               `gorm:"column:created_at"`
	UpdatedAt          time.Time               `gorm:"column:updated_at"`
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
		if err := expireRunningTurns(tx, input.ConversationID, createdAt); err != nil {
			return err
		}
		var activeTurn bool
		if err := tx.Raw(`
SELECT EXISTS(
    SELECT 1 FROM conversation_turns
    WHERE conversation_id = ? AND status = ? AND lease_expires_at > ?
)`, input.ConversationID, conversation.TurnStatusRunning, createdAt).Scan(&activeTurn).Error; err != nil {
			return TranslateError(err)
		}
		if activeTurn {
			return conversation.ErrTurnInProgress
		}
		var insertErr error
		message, insertErr = insertConversationMessage(tx, userID, input, id, createdAt)
		return insertErr
	})
	if err != nil {
		return conversation.Message{}, TranslateError(err)
	}
	return message, nil
}

func (r *ConversationRepository) loadReferences(db *gorm.DB, messages []conversation.Message) error {
	return loadConversationReferences(db, messages)
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
