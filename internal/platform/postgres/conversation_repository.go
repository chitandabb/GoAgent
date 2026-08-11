package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/contextgovernance"
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
var _ conversation.AsyncRepository = (*ConversationRepository)(nil)

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
       user_message_id, assistant_message_id, attempt_count, lease_owner, lease_expires_at,
       completed_at, failure_code, retry_at, created_at, updated_at
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
					Status: conversation.TurnStatusCompleted, Created: false,
				}
				return nil
			case conversation.TurnStatusQueued:
				return conversation.ErrTurnInProgress
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
				if err := appendConversationTurnEvent(tx, existing.ID, input.Message.ConversationID, conversation.TurnEventRunning, map[string]any{
					"attemptCount": existing.AttemptCount + 1,
					"reclaimed":    true,
				}, startedAt); err != nil {
					return err
				}
				result = conversation.BeginTurnResult{
					TurnID: existing.ID, UserMessage: userMessage,
					Status: conversation.TurnStatusRunning, Created: false,
				}
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
				if err := appendConversationTurnEvent(tx, existing.ID, input.Message.ConversationID, conversation.TurnEventRunning, map[string]any{
					"attemptCount": existing.AttemptCount + 1,
				}, startedAt); err != nil {
					return err
				}
				result = conversation.BeginTurnResult{
					TurnID: existing.ID, UserMessage: userMessage,
					Status: conversation.TurnStatusRunning, Created: false,
				}
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
    WHERE conversation_id = ? AND status IN (?, ?)
)`, input.Message.ConversationID, conversation.TurnStatusQueued, conversation.TurnStatusRunning).Scan(&active)
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
		if err := appendConversationTurnEvent(tx, turnID, input.Message.ConversationID, conversation.TurnEventRunning, map[string]any{
			"attemptCount": 1,
		}, startedAt); err != nil {
			return err
		}
		result = conversation.BeginTurnResult{
			TurnID: turnID, UserMessage: message,
			Status: conversation.TurnStatusRunning, Created: true,
		}
		return nil
	})
	if err != nil {
		return conversation.BeginTurnResult{}, TranslateError(err)
	}
	return result, nil
}

// AcceptTurn is the asynchronous counterpart of BeginTurn. The transaction
// owns the user message, queued turn and execute event together, so a relay
// crash cannot leave a turn accepted without a durable delivery intent.
func (r *ConversationRepository) AcceptTurn(
	ctx context.Context,
	userID uuid.UUID,
	input conversation.BeginTurnInput,
) (conversation.BeginTurnResult, error) {
	if r == nil || r.db == nil {
		return conversation.BeginTurnResult{}, errors.New("conversation repository is unavailable")
	}
	if userID == uuid.Nil || input.Message.ConversationID == uuid.Nil ||
		input.Message.Role != conversation.MessageRoleUser ||
		strings.TrimSpace(input.IdempotencyKey) == "" || strings.TrimSpace(input.RequestFingerprint) == "" ||
		input.StartedAt.IsZero() || input.ExecutionMode != conversation.TurnExecutionAsynchronous ||
		input.CorrelationID == uuid.Nil {
		return conversation.BeginTurnResult{}, conversation.ErrInvalidMessage
	}
	if _, err := uuid.Parse(strings.TrimSpace(input.IdempotencyKey)); err != nil {
		return conversation.BeginTurnResult{}, conversation.ErrInvalidMessage
	}
	acceptedAt := input.StartedAt.UTC()
	var result conversation.BeginTurnResult
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		if err := lockConversation(tx, userID, input.Message.ConversationID); err != nil {
			return err
		}

		var existing conversationTurnRecord
		query := tx.Raw(`
SELECT id, conversation_id, user_id, idempotency_key, request_fingerprint, status,
       user_message_id, assistant_message_id, attempt_count, lease_owner, lease_expires_at,
       completed_at, failure_code, retry_at, created_at, updated_at
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
					Status: conversation.TurnStatusCompleted,
				}
				return nil
			case conversation.TurnStatusQueued:
				result = conversation.BeginTurnResult{
					TurnID: existing.ID, UserMessage: userMessage, Status: existing.Status,
				}
				return nil
			case conversation.TurnStatusRunning:
				if existing.LeaseExpiresAt != nil && existing.LeaseExpiresAt.After(acceptedAt) {
					result = conversation.BeginTurnResult{
						TurnID: existing.ID, UserMessage: userMessage, Status: existing.Status,
					}
					return nil
				}
				if err := markTurnFailed(tx, existing.ID, acceptedAt); err != nil {
					return err
				}
				if err := ensureNoActiveTurn(tx, input.Message.ConversationID, existing.ID, acceptedAt); err != nil {
					return err
				}
				if err := ensureLatestUserMessage(tx, input.Message.ConversationID, existing.UserMessageID); err != nil {
					return err
				}
				if err := reopenQueuedTurn(tx, existing.ID, acceptedAt); err != nil {
					return err
				}
				if err := appendConversationTurnEvent(tx, existing.ID, input.Message.ConversationID, conversation.TurnEventQueued, map[string]any{
					"requeued": true,
				}, acceptedAt); err != nil {
					return err
				}
				if err := appendConversationTurnOutbox(tx, existing.ID, input.CorrelationID, acceptedAt); err != nil {
					return err
				}
				result = conversation.BeginTurnResult{
					TurnID: existing.ID, UserMessage: userMessage,
					Status: conversation.TurnStatusQueued,
				}
				return nil
			case conversation.TurnStatusFailed:
				if err := ensureNoActiveTurn(tx, input.Message.ConversationID, existing.ID, acceptedAt); err != nil {
					return err
				}
				if err := ensureLatestUserMessage(tx, input.Message.ConversationID, existing.UserMessageID); err != nil {
					return err
				}
				if err := reopenQueuedTurn(tx, existing.ID, acceptedAt); err != nil {
					return err
				}
				if err := appendConversationTurnEvent(tx, existing.ID, input.Message.ConversationID, conversation.TurnEventQueued, map[string]any{
					"requeued": true,
				}, acceptedAt); err != nil {
					return err
				}
				if err := appendConversationTurnOutbox(tx, existing.ID, input.CorrelationID, acceptedAt); err != nil {
					return err
				}
				result = conversation.BeginTurnResult{
					TurnID: existing.ID, UserMessage: userMessage,
					Status: conversation.TurnStatusQueued,
				}
				return nil
			default:
				return errors.New("conversation turn has an invalid status")
			}
		}

		if err := expireRunningTurns(tx, input.Message.ConversationID, acceptedAt); err != nil {
			return err
		}
		if err := ensureNoActiveTurn(tx, input.Message.ConversationID, uuid.Nil, acceptedAt); err != nil {
			return err
		}
		messageID, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("generate conversation message id: %w", err)
		}
		message, err := insertConversationMessage(tx, userID, input.Message, messageID, acceptedAt)
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
     user_message_id, attempt_count, lease_owner, lease_expires_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, 0, NULL, NULL, ?, ?)`,
			turnID, input.Message.ConversationID, userID, strings.TrimSpace(input.IdempotencyKey),
			strings.TrimSpace(input.RequestFingerprint), conversation.TurnStatusQueued, messageID,
			acceptedAt, acceptedAt)
		if query.Error != nil {
			return TranslateError(query.Error)
		}
		if err := appendConversationTurnEvent(tx, turnID, input.Message.ConversationID, conversation.TurnEventQueued, map[string]any{}, acceptedAt); err != nil {
			return err
		}
		if err := appendConversationTurnOutbox(tx, turnID, input.CorrelationID, acceptedAt); err != nil {
			return err
		}
		result = conversation.BeginTurnResult{
			TurnID: turnID, UserMessage: message, Status: conversation.TurnStatusQueued, Created: true,
		}
		return nil
	})
	if err != nil {
		return conversation.BeginTurnResult{}, TranslateError(err)
	}
	return result, nil
}

// ClaimTurn fences a worker before loading the durable Agent input. The turn
// row is locked while queued, failed, or expired-running state is converted to
// a new running attempt.
func (r *ConversationRepository) ClaimTurn(
	ctx context.Context,
	turnID uuid.UUID,
	workerID string,
	claimedAt, leaseExpiresAt time.Time,
) (conversation.TurnExecution, error) {
	if r == nil || r.db == nil {
		return conversation.TurnExecution{}, errors.New("conversation repository is unavailable")
	}
	workerID = strings.TrimSpace(workerID)
	claimedAt = claimedAt.UTC()
	leaseExpiresAt = leaseExpiresAt.UTC()
	if turnID == uuid.Nil || workerID == "" || len(workerID) > 128 || claimedAt.IsZero() || !leaseExpiresAt.After(claimedAt) {
		return conversation.TurnExecution{}, conversation.ErrInvalidMessage
	}
	var execution conversation.TurnExecution
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		var turn conversationTurnRecord
		query := tx.Raw(`
SELECT id, conversation_id, user_id, idempotency_key, request_fingerprint, status,
       user_message_id, assistant_message_id, attempt_count, lease_owner, lease_expires_at,
       completed_at, failure_code, retry_at, created_at, updated_at
FROM conversation_turns
WHERE id = ?
FOR UPDATE`, turnID).Scan(&turn)
		if query.Error != nil {
			return TranslateError(query.Error)
		}
		if query.RowsAffected == 0 {
			return repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
		}
		switch turn.Status {
		case conversation.TurnStatusCompleted:
			return conversation.ErrTurnAlreadyCompleted
		case conversation.TurnStatusRunning:
			if turn.LeaseExpiresAt != nil && turn.LeaseExpiresAt.After(claimedAt) {
				return conversation.ErrTurnInProgress
			}
		case conversation.TurnStatusQueued:
			if turn.RetryAt != nil && turn.RetryAt.After(claimedAt) {
				return conversation.ErrTurnInProgress
			}
		case conversation.TurnStatusFailed:
		default:
			return errors.New("conversation turn has an invalid status")
		}
		if err := ensureNoActiveTurn(tx, turn.ConversationID, turn.ID, claimedAt); err != nil {
			return err
		}
		if err := ensureLatestUserMessage(tx, turn.ConversationID, turn.UserMessageID); err != nil {
			return err
		}
		var user struct {
			Role   auth.Role       `gorm:"column:role"`
			Status auth.UserStatus `gorm:"column:status"`
		}
		query = tx.Raw(`SELECT role, status FROM users WHERE id = ?`, turn.UserID).Scan(&user)
		if query.Error != nil {
			return TranslateError(query.Error)
		}
		if query.RowsAffected == 0 {
			return repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
		}
		if !user.Status.Valid() || !user.Role.Valid() {
			return errors.New("conversation turn actor is unavailable")
		}
		userMessage, err := loadConversationMessage(tx, turn.UserID, turn.ConversationID, turn.UserMessageID)
		if err != nil {
			return err
		}
		history, err := loadConversationHistory(tx, turn.UserID, turn.ConversationID, userMessage.Seq)
		if err != nil {
			return err
		}
		query = tx.Exec(`
UPDATE conversation_turns
SET status = ?, lease_owner = ?, lease_expires_at = ?, attempt_count = attempt_count + 1,
    failure_code = NULL, retry_at = NULL, updated_at = ?
WHERE id = ? AND status = ?`,
			conversation.TurnStatusRunning, workerID, leaseExpiresAt, claimedAt, turnID, turn.Status)
		if query.Error != nil {
			return TranslateError(query.Error)
		}
		if query.RowsAffected != 1 {
			return conversation.ErrTurnLeaseLost
		}
		if err := appendConversationTurnEvent(tx, turnID, turn.ConversationID, conversation.TurnEventRunning, map[string]any{
			"attemptCount": turn.AttemptCount + 1,
			"reclaimed":    turn.Status == conversation.TurnStatusRunning,
		}, claimedAt); err != nil {
			return err
		}
		var currentConversation conversationRecord
		query = tx.Raw(`
SELECT id, user_id, title, status, last_message_at, created_at, updated_at
FROM conversations
WHERE id = ? AND user_id = ?`, turn.ConversationID, turn.UserID).Scan(&currentConversation)
		if query.Error != nil {
			return TranslateError(query.Error)
		}
		if query.RowsAffected == 0 {
			return repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
		}
		execution = conversation.TurnExecution{
			TurnID:       turnID,
			Turn:         conversation.ConversationTurn{UserMessage: userMessage},
			Conversation: conversationFromRecord(currentConversation),
			Actor:        conversation.Actor{UserID: turn.UserID, IsAdmin: user.Role == auth.RoleAdmin},
			History:      history, AttemptCount: turn.AttemptCount + 1,
		}
		return nil
	})
	if err != nil {
		return conversation.TurnExecution{}, TranslateError(err)
	}
	return execution, nil
}

func (r *ConversationRepository) RenewTurnExecution(
	ctx context.Context,
	turnID uuid.UUID,
	workerID string,
	renewedAt, leaseExpiresAt time.Time,
) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("conversation repository is unavailable")
	}
	workerID = strings.TrimSpace(workerID)
	renewedAt = renewedAt.UTC()
	leaseExpiresAt = leaseExpiresAt.UTC()
	if turnID == uuid.Nil || workerID == "" || len(workerID) > 128 || renewedAt.IsZero() || !leaseExpiresAt.After(renewedAt) {
		return false, conversation.ErrInvalidMessage
	}
	query := ResolveDB(ctx, r.db).Exec(`
UPDATE conversation_turns
SET lease_expires_at = ?, updated_at = ?
WHERE id = ? AND status = ? AND lease_owner = ? AND lease_expires_at > ?`,
		leaseExpiresAt, renewedAt, turnID, conversation.TurnStatusRunning, workerID, renewedAt)
	if query.Error != nil {
		return false, TranslateError(query.Error)
	}
	return query.RowsAffected == 1, nil
}

func (r *ConversationRepository) CompleteTurnExecution(
	ctx context.Context,
	userID, turnID uuid.UUID,
	workerID string,
	response conversation.AgentResponse,
	completedAt time.Time,
) (conversation.ConversationTurn, error) {
	if r == nil || r.db == nil {
		return conversation.ConversationTurn{}, errors.New("conversation repository is unavailable")
	}
	workerID = strings.TrimSpace(workerID)
	response.Content = strings.TrimSpace(response.Content)
	completedAt = completedAt.UTC()
	if userID == uuid.Nil || turnID == uuid.Nil || workerID == "" || response.Validate() != nil || completedAt.IsZero() {
		return conversation.ConversationTurn{}, conversation.ErrInvalidMessage
	}
	var result conversation.ConversationTurn
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		var turn conversationTurnRecord
		query := tx.Raw(`
SELECT id, conversation_id, user_id, idempotency_key, request_fingerprint, status,
       user_message_id, assistant_message_id, attempt_count, lease_owner, lease_expires_at,
       completed_at, failure_code, retry_at, created_at, updated_at
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
		if turn.Status != conversation.TurnStatusRunning || turn.LeaseOwner == nil || *turn.LeaseOwner != workerID ||
			turn.LeaseExpiresAt == nil || !turn.LeaseExpiresAt.After(completedAt) {
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
			Content: response.Content, TaskReferences: createdTaskReferences(userMessage),
			Citations: response.Citations,
		}, assistantMessageID, completedAt)
		if err != nil {
			return err
		}
		if err := insertConversationRunObservation(tx, turnID, response.RunObservation, "", completedAt); err != nil {
			return err
		}
		query = tx.Exec(`
UPDATE conversation_turns
SET status = ?, assistant_message_id = ?, lease_owner = NULL, lease_expires_at = NULL,
    completed_at = ?, failure_code = NULL, retry_at = NULL, updated_at = ?
WHERE id = ? AND user_id = ? AND status = ? AND lease_owner = ? AND lease_expires_at > ?`,
			conversation.TurnStatusCompleted, assistantMessageID, completedAt, completedAt,
			turnID, userID, conversation.TurnStatusRunning, workerID, completedAt)
		if query.Error != nil {
			return TranslateError(query.Error)
		}
		if query.RowsAffected != 1 {
			return conversation.ErrTurnLeaseLost
		}
		if err := appendConversationTurnEvent(tx, turnID, turn.ConversationID, conversation.TurnEventCompleted, map[string]any{
			"assistantMessageId": assistantMessageID.String(),
			"citationCount":      len(response.Citations),
		}, completedAt); err != nil {
			return err
		}
		result = conversation.ConversationTurn{UserMessage: userMessage, AssistantMessage: assistantMessage}
		return nil
	})
	if err != nil {
		return conversation.ConversationTurn{}, TranslateError(err)
	}
	return result, nil
}

func (r *ConversationRepository) FailTurnExecution(
	ctx context.Context,
	userID, turnID uuid.UUID,
	workerID string,
	failure *conversation.AgentRunFailureRecord,
	failedAt time.Time,
) error {
	if r == nil || r.db == nil {
		return errors.New("conversation repository is unavailable")
	}
	workerID = strings.TrimSpace(workerID)
	failedAt = failedAt.UTC()
	if userID == uuid.Nil || turnID == uuid.Nil || workerID == "" || failedAt.IsZero() ||
		(failure != nil && failure.Validate() != nil) {
		return conversation.ErrInvalidMessage
	}
	failureCode := "agent_execution_failed"
	if failure != nil {
		failureCode = failure.ErrorType
	}
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		query := tx.Exec(`
UPDATE conversation_turns
SET status = ?, lease_owner = NULL, lease_expires_at = NULL, failure_code = ?, retry_at = NULL, updated_at = ?
WHERE id = ? AND user_id = ? AND status = ? AND lease_owner = ? AND lease_expires_at > ?`,
			conversation.TurnStatusFailed, failureCode, failedAt, turnID, userID,
			conversation.TurnStatusRunning, workerID, failedAt)
		if query.Error != nil {
			return TranslateError(query.Error)
		}
		if query.RowsAffected != 1 {
			return conversation.ErrTurnLeaseLost
		}
		var turn struct {
			ConversationID uuid.UUID `gorm:"column:conversation_id"`
		}
		query = tx.Raw(`SELECT conversation_id FROM conversation_turns WHERE id = ? AND user_id = ?`, turnID, userID).Scan(&turn)
		if query.Error != nil {
			return TranslateError(query.Error)
		}
		if query.RowsAffected != 1 {
			return repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
		}
		if failure != nil {
			if err := insertConversationRunObservation(
				tx, turnID, &failure.Observation, failure.ErrorType, failedAt,
			); err != nil {
				return err
			}
		}
		return appendConversationTurnEvent(tx, turnID, turn.ConversationID, conversation.TurnEventFailed, map[string]any{
			"failureCode": failureCode,
			"retryable":   false,
		}, failedAt)
	})
	if err != nil {
		return TranslateError(err)
	}
	return nil
}

func (r *ConversationRepository) QueueTurnRetry(
	ctx context.Context,
	userID, turnID uuid.UUID,
	workerID string,
	scheduledAt, retryAt time.Time,
) error {
	if r == nil || r.db == nil {
		return errors.New("conversation repository is unavailable")
	}
	workerID = strings.TrimSpace(workerID)
	scheduledAt = scheduledAt.UTC()
	retryAt = retryAt.UTC()
	if userID == uuid.Nil || turnID == uuid.Nil || workerID == "" || scheduledAt.IsZero() || !retryAt.After(scheduledAt) {
		return conversation.ErrInvalidMessage
	}
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		query := tx.Exec(`
UPDATE conversation_turns
SET status = ?, lease_owner = NULL, lease_expires_at = NULL,
    failure_code = ?, retry_at = ?, updated_at = ?
WHERE id = ? AND user_id = ? AND status = ? AND lease_owner = ? AND lease_expires_at > ?`,
			conversation.TurnStatusQueued, "agent_execution_failed", retryAt, scheduledAt,
			turnID, userID, conversation.TurnStatusRunning, workerID, scheduledAt)
		if query.Error != nil {
			return TranslateError(query.Error)
		}
		if query.RowsAffected != 1 {
			return conversation.ErrTurnLeaseLost
		}
		var turn struct {
			ConversationID uuid.UUID `gorm:"column:conversation_id"`
			AttemptCount   int       `gorm:"column:attempt_count"`
		}
		query = tx.Raw(`SELECT conversation_id, attempt_count FROM conversation_turns WHERE id = ? AND user_id = ?`, turnID, userID).Scan(&turn)
		if query.Error != nil {
			return TranslateError(query.Error)
		}
		if query.RowsAffected != 1 {
			return repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
		}
		return appendConversationTurnEvent(tx, turnID, turn.ConversationID, conversation.TurnEventRetryScheduled, map[string]any{
			"attemptCount":      turn.AttemptCount,
			"retryAfterSeconds": int64(retryAt.Sub(scheduledAt) / time.Second),
			"failureCode":       "agent_execution_failed",
		}, scheduledAt)
	})
	if err != nil {
		return TranslateError(err)
	}
	return nil
}

func reopenQueuedTurn(tx *gorm.DB, turnID uuid.UUID, queuedAt time.Time) error {
	result := tx.Exec(`
UPDATE conversation_turns
SET status = ?, lease_owner = NULL, lease_expires_at = NULL, assistant_message_id = NULL,
    completed_at = NULL, failure_code = NULL, retry_at = NULL, updated_at = ?
WHERE id = ? AND status = ?`,
		conversation.TurnStatusQueued, queuedAt, turnID, conversation.TurnStatusFailed)
	if result.Error != nil {
		return TranslateError(result.Error)
	}
	if result.RowsAffected != 1 {
		return errors.New("conversation turn could not be requeued")
	}
	// The run ledger represents the current terminal outcome of a Turn. A
	// deliberate replay reopens that same Turn, so its previous failed attempt
	// must be removed atomically before a later completion/failure can replace it.
	result = tx.Exec(`DELETE FROM conversation_turn_run_observations WHERE turn_id = ?`, turnID)
	if result.Error != nil {
		return TranslateError(result.Error)
	}
	return nil
}

func appendConversationTurnOutbox(tx *gorm.DB, turnID, correlationID uuid.UUID, createdAt time.Time) error {
	if turnID == uuid.Nil || correlationID == uuid.Nil {
		return conversation.ErrInvalidMessage
	}
	payload, err := json.Marshal(map[string]string{"turnId": turnID.String()})
	if err != nil {
		return fmt.Errorf("marshal conversation turn outbox payload: %w", err)
	}
	outboxID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate conversation turn outbox id: %w", err)
	}
	result := tx.Exec(`
INSERT INTO outbox_events
    (id, event_type, aggregate_type, aggregate_id, correlation_id, causation_id,
     payload, payload_schema_version, attempt_count, available_at, requeue_count, created_at)
VALUES (?, 'conversation.turn.execute', 'conversation_turn', ?, ?, NULL, ?, 1, 0, ?, 0, ?)`,
		outboxID, turnID, correlationID, payload, createdAt.UTC(), createdAt.UTC())
	if result.Error != nil {
		return TranslateError(result.Error)
	}
	return nil
}

func appendConversationTurnEvent(
	tx *gorm.DB,
	turnID, conversationID uuid.UUID,
	eventType conversation.TurnEventType,
	payload map[string]any,
	createdAt time.Time,
) error {
	if tx == nil || turnID == uuid.Nil || conversationID == uuid.Nil || !eventType.Valid() || createdAt.IsZero() {
		return conversation.ErrInvalidMessage
	}
	if payload == nil {
		payload = map[string]any{}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal conversation turn event payload: %w", err)
	}
	var nextSeq int64
	query := tx.Raw(`
SELECT COALESCE(MAX(seq), 0) + 1
FROM conversation_turn_events
WHERE turn_id = ?`, turnID).Scan(&nextSeq)
	if query.Error != nil {
		return TranslateError(query.Error)
	}
	if nextSeq < 1 {
		return errors.New("conversation turn event sequence is invalid")
	}
	query = tx.Exec(`
INSERT INTO conversation_turn_events
    (turn_id, conversation_id, seq, event_type, payload, payload_schema_version, created_at)
VALUES (?, ?, ?, ?, ?, 1, ?)`,
		turnID, conversationID, nextSeq, eventType, encoded, createdAt.UTC())
	if query.Error != nil {
		return TranslateError(query.Error)
	}
	return nil
}

func (r *ConversationRepository) CompleteTurn(
	ctx context.Context,
	userID, turnID uuid.UUID,
	response conversation.AgentResponse,
	completedAt time.Time,
) (conversation.ConversationTurn, error) {
	if r == nil || r.db == nil {
		return conversation.ConversationTurn{}, errors.New("conversation repository is unavailable")
	}
	response.Content = strings.TrimSpace(response.Content)
	if userID == uuid.Nil || turnID == uuid.Nil || response.Validate() != nil {
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
       user_message_id, assistant_message_id, attempt_count, lease_owner, lease_expires_at,
       completed_at, failure_code, retry_at, created_at, updated_at
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
			Content: response.Content, TaskReferences: createdTaskReferences(userMessage),
			Citations: response.Citations,
		}, assistantMessageID, completedAt)
		if err != nil {
			return err
		}
		if err := insertConversationRunObservation(tx, turnID, response.RunObservation, "", completedAt); err != nil {
			return err
		}
		query = tx.Exec(`
UPDATE conversation_turns
SET status = ?, assistant_message_id = ?, lease_owner = NULL, lease_expires_at = NULL,
    completed_at = ?, failure_code = NULL, retry_at = NULL, updated_at = ?
WHERE id = ? AND user_id = ? AND status = ?`,
			conversation.TurnStatusCompleted, assistantMessageID, completedAt, completedAt,
			turnID, userID, conversation.TurnStatusRunning)
		if query.Error != nil {
			return TranslateError(query.Error)
		}
		if query.RowsAffected != 1 {
			return conversation.ErrTurnLeaseLost
		}
		if err := appendConversationTurnEvent(tx, turnID, turn.ConversationID, conversation.TurnEventCompleted, map[string]any{
			"assistantMessageId": assistantMessageID.String(),
			"citationCount":      len(response.Citations),
		}, completedAt); err != nil {
			return err
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
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		query := tx.Exec(`
UPDATE conversation_turns
SET status = ?, lease_owner = NULL, lease_expires_at = NULL, failure_code = ?, retry_at = NULL, updated_at = ?
WHERE id = ? AND user_id = ? AND status = ?`,
			conversation.TurnStatusFailed, "agent_execution_failed", failedAt, turnID, userID, conversation.TurnStatusRunning)
		if query.Error != nil {
			return TranslateError(query.Error)
		}
		if query.RowsAffected == 0 {
			return nil
		}
		if query.RowsAffected != 1 {
			return errors.New("unexpected conversation turn failure update count")
		}
		var turn struct {
			ConversationID uuid.UUID `gorm:"column:conversation_id"`
		}
		query = tx.Raw(`SELECT conversation_id FROM conversation_turns WHERE id = ? AND user_id = ?`, turnID, userID).Scan(&turn)
		if query.Error != nil {
			return TranslateError(query.Error)
		}
		if query.RowsAffected != 1 {
			return repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
		}
		return appendConversationTurnEvent(tx, turnID, turn.ConversationID, conversation.TurnEventFailed, map[string]any{
			"failureCode": "agent_execution_failed",
			"retryable":   false,
		}, failedAt)
	})
	if err != nil {
		return TranslateError(err)
	}
	return nil
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
SET status = ?, lease_owner = NULL, lease_expires_at = NULL, failure_code = ?, retry_at = NULL, updated_at = ?
WHERE conversation_id = ? AND status = ? AND lease_expires_at <= ?`,
		conversation.TurnStatusFailed, "agent_execution_failed", now, conversationID, conversation.TurnStatusRunning, now)
	if result.Error != nil {
		return TranslateError(result.Error)
	}
	return nil
}

func ensureNoRunningTurn(tx *gorm.DB, conversationID, excludedTurnID uuid.UUID, now time.Time) error {
	return ensureNoActiveTurn(tx, conversationID, excludedTurnID, now)
}

func ensureNoActiveTurn(tx *gorm.DB, conversationID, excludedTurnID uuid.UUID, now time.Time) error {
	if err := expireOtherRunningTurns(tx, conversationID, excludedTurnID, now); err != nil {
		return err
	}
	var active bool
	query := tx.Raw(`
SELECT EXISTS(
    SELECT 1 FROM conversation_turns
    WHERE conversation_id = ? AND id <> ? AND status IN (?, ?)
)`, conversationID, excludedTurnID, conversation.TurnStatusQueued, conversation.TurnStatusRunning).Scan(&active)
	if query.Error != nil {
		return TranslateError(query.Error)
	}
	if active {
		return conversation.ErrTurnInProgress
	}
	return nil
}

func expireOtherRunningTurns(
	tx *gorm.DB,
	conversationID, excludedTurnID uuid.UUID,
	now time.Time,
) error {
	result := tx.Exec(`
UPDATE conversation_turns
SET status = ?, lease_owner = NULL, lease_expires_at = NULL, failure_code = ?, retry_at = NULL, updated_at = ?
WHERE conversation_id = ? AND id <> ? AND status = ? AND lease_expires_at <= ?`,
		conversation.TurnStatusFailed, "agent_execution_failed", now,
		conversationID, excludedTurnID, conversation.TurnStatusRunning, now)
	if result.Error != nil {
		return TranslateError(result.Error)
	}
	return nil
}

func markTurnFailed(tx *gorm.DB, turnID uuid.UUID, failedAt time.Time) error {
	result := tx.Exec(`
UPDATE conversation_turns
SET status = ?, lease_owner = NULL, lease_expires_at = NULL, failure_code = ?, retry_at = NULL, updated_at = ?
WHERE id = ? AND status = ?`,
		conversation.TurnStatusFailed, "agent_execution_failed", failedAt, turnID, conversation.TurnStatusRunning)
	if result.Error != nil {
		return TranslateError(result.Error)
	}
	return nil
}

func reopenTurn(tx *gorm.DB, turnID uuid.UUID, attemptCount int, startedAt, leaseExpiresAt time.Time) error {
	result := tx.Exec(`
UPDATE conversation_turns
SET status = ?, attempt_count = ?, lease_owner = NULL, lease_expires_at = ?, failure_code = NULL, retry_at = NULL, updated_at = ?
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
	for position, ref := range input.Attachments {
		query = tx.Exec(`
INSERT INTO conversation_message_attachments
    (message_id, conversation_id, attachment_id, position, purpose, created_at)
SELECT ?, ?, id, ?, ?, ?
FROM attachments
WHERE id = ? AND owner_user_id = ? AND processing_status = 'uploaded'
  AND (scope = 'personal' OR (scope = 'session' AND conversation_id = ?))`,
			id, input.ConversationID, position, ref.Purpose, createdAt,
			ref.AttachmentID, userID, input.ConversationID)
		if query.Error != nil {
			return conversation.Message{}, TranslateError(query.Error)
		}
		if query.RowsAffected != 1 {
			return conversation.Message{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
		}
	}
	for _, citation := range input.Citations {
		query = tx.Exec(`
INSERT INTO conversation_message_citations
    (message_id, position, source_type, source_ref, content_sha256, created_at)
VALUES (?, ?, ?, ?, ?, ?)`,
			id, citation.Position, citation.SourceType, citation.SourceRef, citation.ContentSHA256, createdAt)
		if query.Error != nil {
			return conversation.Message{}, TranslateError(query.Error)
		}
	}
	query = tx.Exec(`
UPDATE conversations
SET last_message_at = ?, updated_at = ?
WHERE id = ? AND user_id = ?`, createdAt, createdAt, input.ConversationID, userID)
	if query.Error != nil {
		return conversation.Message{}, TranslateError(query.Error)
	}
	message := conversation.Message{
		ID: id, ConversationID: input.ConversationID, Seq: nextSeq,
		Role: input.Role, Content: input.Content, ContentSchemaVersion: 1,
		CreatedAt: createdAt,
	}
	messages := []conversation.Message{message}
	if err := loadConversationReferences(tx, messages); err != nil {
		return conversation.Message{}, err
	}
	return messages[0], nil
}

func insertConversationRunObservation(
	tx *gorm.DB,
	turnID uuid.UUID,
	observation *conversation.AgentRunObservation,
	errorType string,
	createdAt time.Time,
) error {
	if observation == nil {
		return nil
	}
	errorType = strings.TrimSpace(errorType)
	if turnID == uuid.Nil || observation.Validate() != nil ||
		(observation.Outcome == conversation.AgentRunFailed) != (errorType != "") {
		return conversation.ErrInvalidMessage
	}
	var persistedErrorType any
	if errorType != "" {
		failure := conversation.AgentRunFailureRecord{Observation: *observation, ErrorType: errorType}
		if failure.Validate() != nil {
			return conversation.ErrInvalidMessage
		}
		persistedErrorType = errorType
	}
	channels := observation.DegradedChannels
	if channels == nil {
		channels = []string{}
	}
	degradedChannels, err := json.Marshal(channels)
	if err != nil {
		return fmt.Errorf("encode conversation run degraded channels: %w", err)
	}
	query := tx.Exec(`
INSERT INTO conversation_turn_run_observations
    (turn_id, model_provider, model_id, prompt_version, outcome,
     model_calls, prompt_tokens, completion_tokens, total_tokens, cached_tokens, reasoning_tokens,
     duration_millis, degraded_channels, sources_truncated, error_type, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CAST(? AS jsonb), ?, ?, ?)`,
		turnID, observation.ModelProvider, observation.ModelID, observation.PromptVersion, observation.Outcome,
		observation.Usage.ModelCalls, observation.Usage.PromptTokens, observation.Usage.CompletionTokens,
		observation.Usage.TotalTokens, observation.Usage.CachedTokens, observation.Usage.ReasoningTokens,
		observation.DurationMillis, string(degradedChannels), observation.SourcesTruncated,
		persistedErrorType, createdAt)
	if query.Error != nil {
		return TranslateError(query.Error)
	}
	for position, source := range observation.RetrievedSources {
		query = tx.Exec(`
INSERT INTO conversation_turn_retrieved_sources
    (turn_id, position, source_type, source_ref, content_sha256, created_at)
VALUES (?, ?, ?, ?, ?, ?)`,
			turnID, position, source.SourceType, source.SourceRef, source.ContentSHA256, createdAt)
		if query.Error != nil {
			return TranslateError(query.Error)
		}
	}
	if observation.PromptManifest != nil {
		if err := insertConversationPromptManifest(tx, turnID, *observation.PromptManifest, createdAt); err != nil {
			return err
		}
	}
	return nil
}

func insertConversationPromptManifest(
	tx *gorm.DB,
	turnID uuid.UUID,
	manifest contextgovernance.PromptManifest,
	createdAt time.Time,
) error {
	if turnID == uuid.Nil || manifest.Validate() != nil || createdAt.IsZero() {
		return conversation.ErrInvalidMessage
	}
	reasons := manifest.DegradedReasons
	if reasons == nil {
		reasons = []string{}
	}
	encodedReasons, err := json.Marshal(reasons)
	if err != nil {
		return fmt.Errorf("encode prompt manifest degraded reasons: %w", err)
	}
	var summarySnapshotID any
	if manifest.SummarySnapshotID != "" {
		summarySnapshotID = manifest.SummarySnapshotID
	}
	query := tx.Exec(`
INSERT INTO conversation_prompt_manifests
    (turn_id, schema_version, preflight_status, failure_stage,
     prompt_identity_available, estimate_available, prompt_epoch_id, stable_prefix_fingerprint,
     model_profile, model_profile_fingerprint, system_prompt_version, system_prompt_fingerprint,
     tool_schema_fingerprint, skill_prompt_fingerprint, summary_fingerprint,
     summary_snapshot_id, hard_compaction_triggered,
     tail_from_seq, tail_through_seq, available_input_tokens,
     estimated_prompt_tokens, estimated_upper_bound_tokens, tool_growth_reserve_tokens, estimation_method,
     soft_threshold_ratio, hard_threshold_ratio, soft_threshold_reached, hard_threshold_reached,
     exceeds_hard_window, actual_usage_available, actual_prompt_tokens, cache_hit_tokens,
     cache_miss_tokens, completion_tokens, estimation_error_ratio, preflight_duration_micros,
     run_duration_millis, context_degraded, degraded_reasons, created_at)
VALUES (
    ?, ?, ?, ?,
    ?, ?, ?, ?,
    ?, ?, ?, ?,
    ?, ?, ?, ?,
    ?, ?, ?, ?,
    ?, ?, ?, ?,
    ?, ?, ?, ?,
    ?, ?, ?, ?,
    ?, ?, ?, ?,
    ?, ?, CAST(? AS jsonb), ?
)`,
		turnID, manifest.SchemaVersion, manifest.PreflightStatus, manifest.FailureStage,
		manifest.PromptIdentityAvailable, manifest.EstimateAvailable,
		manifest.PromptEpochID, manifest.StablePrefixFingerprint,
		manifest.ModelProfile, manifest.ModelProfileFingerprint, manifest.SystemPromptVersion,
		manifest.SystemPromptFingerprint, manifest.ToolSchemaFingerprint, manifest.SkillPromptFingerprint,
		manifest.SummaryFingerprint, summarySnapshotID, manifest.HardCompactionTriggered,
		manifest.TailFromSeq, manifest.TailThroughSeq,
		manifest.AvailableInputTokens, manifest.EstimatedPromptTokens, manifest.EstimatedUpperBoundTokens,
		manifest.ToolGrowthReserveTokens, manifest.EstimationMethod,
		manifest.SoftThresholdRatio, manifest.HardThresholdRatio,
		manifest.SoftThresholdReached, manifest.HardThresholdReached, manifest.ExceedsHardWindow,
		manifest.ActualUsageAvailable, manifest.ActualPromptTokens, manifest.CacheHitTokens,
		manifest.CacheMissTokens, manifest.CompletionTokens, manifest.EstimationErrorRatio,
		manifest.PreflightDurationMicros, manifest.RunDurationMillis, manifest.ContextDegraded,
		string(encodedReasons), createdAt.UTC())
	if query.Error != nil {
		return TranslateError(query.Error)
	}
	return nil
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

func loadConversationHistory(
	db *gorm.DB,
	userID, conversationID uuid.UUID,
	throughSeq int64,
) ([]conversation.Message, error) {
	if throughSeq < 1 {
		return nil, conversation.ErrInvalidMessage
	}
	const maxExecutionHistoryMessages = 10_000
	afterSeq := int64(0)
	var activeSnapshot struct {
		ThroughSeq int64 `gorm:"column:through_seq"`
	}
	activeQuery := db.Raw(`
SELECT through_seq
FROM conversation_memory_snapshots
WHERE conversation_id = ? AND status = 'active'
LIMIT 1`, conversationID).Scan(&activeSnapshot)
	if activeQuery.Error != nil {
		return nil, TranslateError(activeQuery.Error)
	}
	if activeQuery.RowsAffected == 1 {
		afterSeq = activeSnapshot.ThroughSeq - conversation.MaxMessageLimit
		if afterSeq < 0 {
			afterSeq = 0
		}
	}
	var records []messageRecord
	query := db.Raw(`
SELECT message.id, message.conversation_id, message.seq, message.role,
       message.content, message.content_schema_version, message.created_at
FROM conversation_messages message
JOIN conversations conversation ON conversation.id = message.conversation_id
WHERE message.conversation_id = ? AND conversation.user_id = ?
  AND message.seq > ? AND message.seq <= ?
ORDER BY message.seq DESC
LIMIT ?`, conversationID, userID, afterSeq, throughSeq, maxExecutionHistoryMessages+1).Scan(&records)
	if query.Error != nil {
		return nil, TranslateError(query.Error)
	}
	if len(records) > maxExecutionHistoryMessages {
		return nil, errors.New("conversation execution history exceeds the bounded compaction input")
	}
	messages := make([]conversation.Message, len(records))
	for index, record := range records {
		messages[len(records)-1-index] = messageFromRecord(record)
	}
	if err := loadConversationReferences(db, messages); err != nil {
		return nil, err
	}
	return messages, nil
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
	var attachmentRefs []messageAttachmentRecord
	if err := db.Raw(`
SELECT reference.message_id, reference.attachment_id, reference.position, reference.purpose,
       attachment.original_filename, attachment.content_type, attachment.size_bytes,
       attachment.content_sha256, attachment.processing_status
FROM conversation_message_attachments reference
JOIN attachments attachment ON attachment.id = reference.attachment_id
WHERE reference.message_id IN ?
ORDER BY reference.message_id, reference.position`, ids).Scan(&attachmentRefs).Error; err != nil {
		return TranslateError(err)
	}
	for _, ref := range attachmentRefs {
		if position, ok := index[ref.MessageID]; ok {
			messages[position].Attachments = append(messages[position].Attachments, conversation.MessageAttachment{
				AttachmentID: ref.AttachmentID, Position: ref.Position, Purpose: ref.Purpose,
				OriginalName: ref.OriginalName, MediaType: ref.MediaType, SizeBytes: ref.SizeBytes,
				ContentSHA256: ref.ContentSHA256, Status: ref.Status,
			})
		}
	}
	var citations []messageCitationRecord
	if err := db.Raw(`
SELECT message_id, position, source_type, source_ref, content_sha256
FROM conversation_message_citations
WHERE message_id IN ?
ORDER BY message_id, position`, ids).Scan(&citations).Error; err != nil {
		return TranslateError(err)
	}
	for _, citation := range citations {
		if position, ok := index[citation.MessageID]; ok {
			messages[position].Citations = append(messages[position].Citations, conversation.MessageCitation{
				Position: citation.Position, SourceType: citation.SourceType,
				SourceRef: citation.SourceRef, ContentSHA256: citation.ContentSHA256,
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
	LeaseOwner         *string                 `gorm:"column:lease_owner"`
	LeaseExpiresAt     *time.Time              `gorm:"column:lease_expires_at"`
	CompletedAt        *time.Time              `gorm:"column:completed_at"`
	FailureCode        *string                 `gorm:"column:failure_code"`
	RetryAt            *time.Time              `gorm:"column:retry_at"`
	CreatedAt          time.Time               `gorm:"column:created_at"`
	UpdatedAt          time.Time               `gorm:"column:updated_at"`
}

type conversationRecordedRunRecord struct {
	TurnID             uuid.UUID                    `gorm:"column:turn_id"`
	ConversationID     uuid.UUID                    `gorm:"column:conversation_id"`
	UserID             uuid.UUID                    `gorm:"column:user_id"`
	Status             conversation.TurnStatus      `gorm:"column:status"`
	UserMessageID      uuid.UUID                    `gorm:"column:user_message_id"`
	AssistantMessageID *uuid.UUID                   `gorm:"column:assistant_message_id"`
	UserQuery          string                       `gorm:"column:user_query"`
	ModelProvider      string                       `gorm:"column:model_provider"`
	ModelID            string                       `gorm:"column:model_id"`
	PromptVersion      string                       `gorm:"column:prompt_version"`
	Outcome            conversation.AgentRunOutcome `gorm:"column:outcome"`
	ModelCalls         int                          `gorm:"column:model_calls"`
	PromptTokens       int                          `gorm:"column:prompt_tokens"`
	CompletionTokens   int                          `gorm:"column:completion_tokens"`
	TotalTokens        int                          `gorm:"column:total_tokens"`
	CachedTokens       int                          `gorm:"column:cached_tokens"`
	ReasoningTokens    int                          `gorm:"column:reasoning_tokens"`
	DurationMillis     int64                        `gorm:"column:duration_millis"`
	DegradedChannels   []byte                       `gorm:"column:degraded_channels"`
	SourcesTruncated   bool                         `gorm:"column:sources_truncated"`
	ErrorType          *string                      `gorm:"column:error_type"`
	CompletedAt        *time.Time                   `gorm:"column:completed_at"`
	ObservedAt         time.Time                    `gorm:"column:observed_at"`
}

type conversationRetrievedSourceRecord struct {
	Position      int                             `gorm:"column:position"`
	SourceType    conversation.CitationSourceType `gorm:"column:source_type"`
	SourceRef     string                          `gorm:"column:source_ref"`
	ContentSHA256 string                          `gorm:"column:content_sha256"`
}

type conversationPromptManifestRecord struct {
	SchemaVersion             int                                `gorm:"column:schema_version"`
	PreflightStatus           contextgovernance.PreflightStatus  `gorm:"column:preflight_status"`
	FailureStage              string                             `gorm:"column:failure_stage"`
	PromptIdentityAvailable   bool                               `gorm:"column:prompt_identity_available"`
	EstimateAvailable         bool                               `gorm:"column:estimate_available"`
	PromptEpochID             string                             `gorm:"column:prompt_epoch_id"`
	StablePrefixFingerprint   string                             `gorm:"column:stable_prefix_fingerprint"`
	ModelProfile              string                             `gorm:"column:model_profile"`
	ModelProfileFingerprint   string                             `gorm:"column:model_profile_fingerprint"`
	SystemPromptVersion       string                             `gorm:"column:system_prompt_version"`
	SystemPromptFingerprint   string                             `gorm:"column:system_prompt_fingerprint"`
	ToolSchemaFingerprint     string                             `gorm:"column:tool_schema_fingerprint"`
	SkillPromptFingerprint    string                             `gorm:"column:skill_prompt_fingerprint"`
	SummaryFingerprint        string                             `gorm:"column:summary_fingerprint"`
	SummarySnapshotID         *uuid.UUID                         `gorm:"column:summary_snapshot_id"`
	HardCompactionTriggered   bool                               `gorm:"column:hard_compaction_triggered"`
	TailFromSeq               int64                              `gorm:"column:tail_from_seq"`
	TailThroughSeq            int64                              `gorm:"column:tail_through_seq"`
	AvailableInputTokens      int                                `gorm:"column:available_input_tokens"`
	EstimatedPromptTokens     int                                `gorm:"column:estimated_prompt_tokens"`
	EstimatedUpperBoundTokens int                                `gorm:"column:estimated_upper_bound_tokens"`
	ToolGrowthReserveTokens   int                                `gorm:"column:tool_growth_reserve_tokens"`
	EstimationMethod          contextgovernance.EstimationMethod `gorm:"column:estimation_method"`
	SoftThresholdRatio        float64                            `gorm:"column:soft_threshold_ratio"`
	HardThresholdRatio        float64                            `gorm:"column:hard_threshold_ratio"`
	SoftThresholdReached      bool                               `gorm:"column:soft_threshold_reached"`
	HardThresholdReached      bool                               `gorm:"column:hard_threshold_reached"`
	ExceedsHardWindow         bool                               `gorm:"column:exceeds_hard_window"`
	ActualUsageAvailable      bool                               `gorm:"column:actual_usage_available"`
	ActualPromptTokens        int                                `gorm:"column:actual_prompt_tokens"`
	CacheHitTokens            int                                `gorm:"column:cache_hit_tokens"`
	CacheMissTokens           int                                `gorm:"column:cache_miss_tokens"`
	CompletionTokens          int                                `gorm:"column:completion_tokens"`
	EstimationErrorRatio      float64                            `gorm:"column:estimation_error_ratio"`
	PreflightDurationMicros   int64                              `gorm:"column:preflight_duration_micros"`
	RunDurationMillis         int64                              `gorm:"column:run_duration_millis"`
	ContextDegraded           bool                               `gorm:"column:context_degraded"`
	DegradedReasons           []byte                             `gorm:"column:degraded_reasons"`
}

// GetRecordedAgentRun is intentionally not part of the HTTP-facing domain
// Repository interface. It supports local/offline evaluation export by turn ID.
func (r *ConversationRepository) GetRecordedAgentRun(
	ctx context.Context,
	turnID uuid.UUID,
) (conversation.RecordedAgentRun, error) {
	if r == nil || r.db == nil {
		return conversation.RecordedAgentRun{}, errors.New("conversation repository is unavailable")
	}
	if turnID == uuid.Nil {
		return conversation.RecordedAgentRun{}, conversation.ErrInvalidMessage
	}
	db := ResolveDB(ctx, r.db)
	var record conversationRecordedRunRecord
	query := db.Raw(`
SELECT observation.turn_id, turn.conversation_id, turn.user_id, turn.user_message_id,
	   turn.status, turn.assistant_message_id, user_message.content AS user_query,
	   observation.model_provider, observation.model_id, observation.prompt_version, observation.outcome,
	   observation.model_calls, observation.prompt_tokens, observation.completion_tokens,
	   observation.total_tokens, observation.cached_tokens, observation.reasoning_tokens,
	   observation.duration_millis, observation.degraded_channels, observation.sources_truncated,
	   observation.error_type, turn.completed_at, observation.created_at AS observed_at
FROM conversation_turn_run_observations observation
JOIN conversation_turns turn ON turn.id = observation.turn_id
JOIN conversation_messages user_message ON user_message.id = turn.user_message_id
WHERE observation.turn_id = ? AND turn.status IN (?, ?)`,
		turnID, conversation.TurnStatusCompleted, conversation.TurnStatusFailed).Scan(&record)
	if query.Error != nil {
		return conversation.RecordedAgentRun{}, TranslateError(query.Error)
	}
	if query.RowsAffected == 0 {
		return conversation.RecordedAgentRun{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
	}
	var degradedChannels []string
	if err := json.Unmarshal(record.DegradedChannels, &degradedChannels); err != nil {
		return conversation.RecordedAgentRun{}, errors.New("conversation run degraded channels are invalid")
	}
	var sources []conversationRetrievedSourceRecord
	query = db.Raw(`
SELECT position, source_type, source_ref, content_sha256
FROM conversation_turn_retrieved_sources
WHERE turn_id = ?
ORDER BY position`, turnID).Scan(&sources)
	if query.Error != nil {
		return conversation.RecordedAgentRun{}, TranslateError(query.Error)
	}
	observation := conversation.AgentRunObservation{
		ModelProvider: record.ModelProvider, ModelID: record.ModelID, PromptVersion: record.PromptVersion,
		Outcome: record.Outcome, DegradedChannels: degradedChannels,
		Usage: conversation.AgentRunUsage{
			ModelCalls: record.ModelCalls, PromptTokens: record.PromptTokens,
			CompletionTokens: record.CompletionTokens, TotalTokens: record.TotalTokens,
			CachedTokens: record.CachedTokens, ReasoningTokens: record.ReasoningTokens,
		},
		DurationMillis: record.DurationMillis, SourcesTruncated: record.SourcesTruncated,
	}
	for position, source := range sources {
		if source.Position != position {
			return conversation.RecordedAgentRun{}, errors.New("conversation run retrieved source positions are invalid")
		}
		observation.RetrievedSources = append(observation.RetrievedSources, conversation.AgentRunSource{
			SourceType: source.SourceType, SourceRef: source.SourceRef, ContentSHA256: source.ContentSHA256,
		})
	}
	promptManifest, err := loadConversationPromptManifest(db, turnID)
	if err != nil {
		return conversation.RecordedAgentRun{}, err
	}
	observation.PromptManifest = promptManifest
	if observation.Validate() != nil {
		return conversation.RecordedAgentRun{}, errors.New("conversation run observation is invalid")
	}
	errorType := ""
	if record.ErrorType != nil {
		errorType = strings.TrimSpace(*record.ErrorType)
	}
	if (record.Status == conversation.TurnStatusFailed) != (observation.Outcome == conversation.AgentRunFailed) ||
		(observation.Outcome == conversation.AgentRunFailed) != (errorType != "") {
		return conversation.RecordedAgentRun{}, errors.New("conversation run terminal outcome is inconsistent")
	}
	if observation.Outcome == conversation.AgentRunFailed {
		if (conversation.AgentRunFailureRecord{Observation: observation, ErrorType: errorType}).Validate() != nil {
			return conversation.RecordedAgentRun{}, errors.New("conversation run failure record is invalid")
		}
	}
	answer := ""
	var citations []conversation.MessageCitation
	if record.AssistantMessageID != nil {
		assistantMessage, err := loadConversationMessage(
			db, record.UserID, record.ConversationID, *record.AssistantMessageID,
		)
		if err != nil {
			return conversation.RecordedAgentRun{}, err
		}
		answer = assistantMessage.Content
		citations = assistantMessage.Citations
	} else if record.Status != conversation.TurnStatusFailed {
		return conversation.RecordedAgentRun{}, errors.New("completed conversation run has no assistant message")
	}
	return conversation.RecordedAgentRun{
		TurnID: record.TurnID, ConversationID: record.ConversationID,
		UserMessageID: record.UserMessageID, AssistantMessageID: record.AssistantMessageID,
		UserQuery: record.UserQuery, Answer: answer, Citations: citations,
		Observation: observation, ErrorType: errorType, CompletedAt: record.CompletedAt,
		ObservedAt: record.ObservedAt.UTC(),
	}, nil
}

func loadConversationPromptManifest(
	db *gorm.DB,
	turnID uuid.UUID,
) (*contextgovernance.PromptManifest, error) {
	var record conversationPromptManifestRecord
	query := db.Raw(`
SELECT schema_version, preflight_status, failure_stage, prompt_identity_available, estimate_available,
       prompt_epoch_id, stable_prefix_fingerprint,
       model_profile, model_profile_fingerprint, system_prompt_version, system_prompt_fingerprint,
	       tool_schema_fingerprint, skill_prompt_fingerprint, summary_fingerprint,
	       summary_snapshot_id, hard_compaction_triggered,
       tail_from_seq, tail_through_seq, available_input_tokens,
       estimated_prompt_tokens, estimated_upper_bound_tokens, tool_growth_reserve_tokens, estimation_method,
       soft_threshold_ratio, hard_threshold_ratio, soft_threshold_reached, hard_threshold_reached,
       exceeds_hard_window, actual_usage_available, actual_prompt_tokens, cache_hit_tokens,
       cache_miss_tokens, completion_tokens, estimation_error_ratio, preflight_duration_micros,
       run_duration_millis, context_degraded, degraded_reasons
FROM conversation_prompt_manifests
WHERE turn_id = ?`, turnID).Scan(&record)
	if query.Error != nil {
		return nil, TranslateError(query.Error)
	}
	if query.RowsAffected == 0 {
		return nil, nil
	}
	var degradedReasons []string
	if err := json.Unmarshal(record.DegradedReasons, &degradedReasons); err != nil {
		return nil, errors.New("conversation prompt manifest degraded reasons are invalid")
	}
	manifest := &contextgovernance.PromptManifest{
		SchemaVersion: record.SchemaVersion, PreflightStatus: record.PreflightStatus,
		FailureStage: record.FailureStage, PromptIdentityAvailable: record.PromptIdentityAvailable,
		EstimateAvailable: record.EstimateAvailable, PromptEpochID: record.PromptEpochID,
		StablePrefixFingerprint: record.StablePrefixFingerprint, ModelProfile: record.ModelProfile,
		ModelProfileFingerprint: record.ModelProfileFingerprint,
		SystemPromptVersion:     record.SystemPromptVersion, SystemPromptFingerprint: record.SystemPromptFingerprint,
		ToolSchemaFingerprint: record.ToolSchemaFingerprint, SkillPromptFingerprint: record.SkillPromptFingerprint,
		SummaryFingerprint:      record.SummaryFingerprint,
		HardCompactionTriggered: record.HardCompactionTriggered, TailFromSeq: record.TailFromSeq,
		TailThroughSeq: record.TailThroughSeq, AvailableInputTokens: record.AvailableInputTokens,
		EstimatedPromptTokens:     record.EstimatedPromptTokens,
		EstimatedUpperBoundTokens: record.EstimatedUpperBoundTokens,
		ToolGrowthReserveTokens:   record.ToolGrowthReserveTokens, EstimationMethod: record.EstimationMethod,
		SoftThresholdRatio: record.SoftThresholdRatio, HardThresholdRatio: record.HardThresholdRatio,
		SoftThresholdReached: record.SoftThresholdReached, HardThresholdReached: record.HardThresholdReached,
		ExceedsHardWindow: record.ExceedsHardWindow, ActualUsageAvailable: record.ActualUsageAvailable,
		ActualPromptTokens: record.ActualPromptTokens, CacheHitTokens: record.CacheHitTokens,
		CacheMissTokens: record.CacheMissTokens, CompletionTokens: record.CompletionTokens,
		EstimationErrorRatio:    record.EstimationErrorRatio,
		PreflightDurationMicros: record.PreflightDurationMicros, RunDurationMillis: record.RunDurationMillis,
		ContextDegraded: record.ContextDegraded, DegradedReasons: degradedReasons,
	}
	if record.SummarySnapshotID != nil {
		manifest.SummarySnapshotID = record.SummarySnapshotID.String()
	}
	if err := manifest.Validate(); err != nil {
		return nil, errors.New("conversation prompt manifest is invalid")
	}
	return manifest, nil
}

func (r *ConversationRepository) GetTurn(
	ctx context.Context,
	userID, conversationID, turnID uuid.UUID,
) (conversation.TurnDetail, error) {
	if r == nil || r.db == nil {
		return conversation.TurnDetail{}, errors.New("conversation repository is unavailable")
	}
	if userID == uuid.Nil || conversationID == uuid.Nil || turnID == uuid.Nil {
		return conversation.TurnDetail{}, conversation.ErrInvalidMessage
	}
	var record conversationTurnRecord
	query := ResolveDB(ctx, r.db).Raw(`
SELECT id, conversation_id, user_id, idempotency_key, request_fingerprint, status,
       user_message_id, assistant_message_id, attempt_count, lease_owner, lease_expires_at,
       completed_at, failure_code, retry_at, created_at, updated_at
FROM conversation_turns
WHERE id = ? AND conversation_id = ? AND user_id = ?`, turnID, conversationID, userID).Scan(&record)
	if query.Error != nil {
		return conversation.TurnDetail{}, TranslateError(query.Error)
	}
	if query.RowsAffected == 0 {
		return conversation.TurnDetail{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
	}
	return conversationTurnDetailFromRecord(record), nil
}

func (r *ConversationRepository) ListTurnEvents(
	ctx context.Context,
	userID, conversationID, turnID uuid.UUID,
	afterSeq int64,
	limit int,
) (conversation.TurnEventPage, error) {
	if r == nil || r.db == nil {
		return conversation.TurnEventPage{}, errors.New("conversation repository is unavailable")
	}
	if userID == uuid.Nil || conversationID == uuid.Nil || turnID == uuid.Nil || afterSeq < 0 ||
		limit < 1 || limit > conversation.MaxTurnEventLimit {
		return conversation.TurnEventPage{}, conversation.ErrInvalidMessage
	}
	var owner struct {
		ID uuid.UUID `gorm:"column:id"`
	}
	query := ResolveDB(ctx, r.db).Raw(`
SELECT id
FROM conversation_turns
WHERE id = ? AND conversation_id = ? AND user_id = ?`, turnID, conversationID, userID).Scan(&owner)
	if query.Error != nil {
		return conversation.TurnEventPage{}, TranslateError(query.Error)
	}
	if query.RowsAffected == 0 {
		return conversation.TurnEventPage{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
	}
	var records []conversationTurnEventRecord
	query = ResolveDB(ctx, r.db).Raw(`
SELECT turn_id, conversation_id, seq, event_type, payload, payload_schema_version, created_at
FROM conversation_turn_events
WHERE turn_id = ? AND conversation_id = ? AND seq > ?
ORDER BY seq
LIMIT ?`, turnID, conversationID, afterSeq, limit+1).Scan(&records)
	if query.Error != nil {
		return conversation.TurnEventPage{}, TranslateError(query.Error)
	}
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	items := make([]conversation.TurnEvent, 0, len(records))
	nextAfterSeq := afterSeq
	for _, record := range records {
		item, err := conversationTurnEventFromRecord(record)
		if err != nil {
			return conversation.TurnEventPage{}, err
		}
		items = append(items, item)
		if item.Seq > nextAfterSeq {
			nextAfterSeq = item.Seq
		}
	}
	return conversation.TurnEventPage{
		Items: items, AfterSeq: afterSeq, NextAfterSeq: nextAfterSeq, HasMore: hasMore,
	}, nil
}

type conversationTurnEventRecord struct {
	TurnID               uuid.UUID `gorm:"column:turn_id"`
	ConversationID       uuid.UUID `gorm:"column:conversation_id"`
	Seq                  int64     `gorm:"column:seq"`
	EventType            string    `gorm:"column:event_type"`
	Payload              []byte    `gorm:"column:payload"`
	PayloadSchemaVersion int       `gorm:"column:payload_schema_version"`
	CreatedAt            time.Time `gorm:"column:created_at"`
}

func conversationTurnDetailFromRecord(record conversationTurnRecord) conversation.TurnDetail {
	detail := conversation.TurnDetail{
		ID: record.ID, ConversationID: record.ConversationID, UserMessageID: record.UserMessageID,
		AssistantMessageID: record.AssistantMessageID, Status: record.Status,
		AttemptCount: record.AttemptCount, RetryAt: record.RetryAt,
		CreatedAt: record.CreatedAt.UTC(), UpdatedAt: record.UpdatedAt.UTC(), CompletedAt: record.CompletedAt,
	}
	switch record.Status {
	case conversation.TurnStatusQueued:
		if record.RetryAt != nil {
			detail.FailureSummary = "助手暂时未完成处理，系统将自动重试"
		}
	case conversation.TurnStatusFailed:
		detail.FailureSummary = "助手未能完成处理，请检查输入后重试"
	}
	return detail
}

func conversationTurnEventFromRecord(record conversationTurnEventRecord) (conversation.TurnEvent, error) {
	eventType := conversation.TurnEventType(strings.TrimSpace(record.EventType))
	if record.TurnID == uuid.Nil || record.ConversationID == uuid.Nil || record.Seq < 1 || !eventType.Valid() ||
		record.PayloadSchemaVersion < 1 || record.CreatedAt.IsZero() {
		return conversation.TurnEvent{}, errors.New("conversation turn event record is invalid")
	}
	payload := map[string]any{}
	if len(record.Payload) > 0 {
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			return conversation.TurnEvent{}, fmt.Errorf("decode conversation turn event payload: %w", err)
		}
	}
	return conversation.TurnEvent{
		TurnID: record.TurnID, ConversationID: record.ConversationID, Seq: record.Seq,
		EventType: eventType, Payload: payload, PayloadSchemaVersion: record.PayloadSchemaVersion,
		CreatedAt: record.CreatedAt.UTC(),
	}, nil
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
    WHERE conversation_id = ? AND status IN (?, ?)
)`, input.ConversationID, conversation.TurnStatusQueued, conversation.TurnStatusRunning).Scan(&activeTurn).Error; err != nil {
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

type messageAttachmentRecord struct {
	MessageID     uuid.UUID `gorm:"column:message_id"`
	AttachmentID  uuid.UUID `gorm:"column:attachment_id"`
	Position      int       `gorm:"column:position"`
	Purpose       string    `gorm:"column:purpose"`
	OriginalName  string    `gorm:"column:original_filename"`
	MediaType     string    `gorm:"column:content_type"`
	SizeBytes     int64     `gorm:"column:size_bytes"`
	ContentSHA256 string    `gorm:"column:content_sha256"`
	Status        string    `gorm:"column:processing_status"`
}

type messageCitationRecord struct {
	MessageID     uuid.UUID                       `gorm:"column:message_id"`
	Position      int                             `gorm:"column:position"`
	SourceType    conversation.CitationSourceType `gorm:"column:source_type"`
	SourceRef     string                          `gorm:"column:source_ref"`
	ContentSHA256 string                          `gorm:"column:content_sha256"`
}
