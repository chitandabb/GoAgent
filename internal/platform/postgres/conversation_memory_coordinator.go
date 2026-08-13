package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversationmemory"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ConversationMemoryCoordinator struct{ db *gorm.DB }

var _ conversationmemory.Coordinator = (*ConversationMemoryCoordinator)(nil)

func NewConversationMemoryCoordinator(db *gorm.DB) *ConversationMemoryCoordinator {
	return &ConversationMemoryCoordinator{db: db}
}

func (c *ConversationMemoryCoordinator) WithinConversation(ctx context.Context, conversationID uuid.UUID, fn func(context.Context) error) error {
	if c == nil || c.db == nil || conversationID == uuid.Nil || fn == nil {
		return errors.New("conversation memory coordinator is unavailable")
	}
	return c.db.WithContext(ctx).Connection(func(connection *gorm.DB) (resultErr error) {
		if err := connection.Exec(`SELECT pg_advisory_lock(hashtextextended(?, 0))`, conversationID.String()).Error; err != nil {
			return TranslateError(err)
		}
		defer func() {
			unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			var unlocked bool
			unlockErr := connection.WithContext(unlockCtx).Raw(
				`SELECT pg_advisory_unlock(hashtextextended(?, 0))`, conversationID.String(),
			).Scan(&unlocked).Error
			if unlockErr != nil {
				unlockErr = fmt.Errorf("unlock conversation memory coordinator: %w", TranslateError(unlockErr))
			} else if !unlocked {
				unlockErr = errors.New("conversation memory coordinator lock was not held during unlock")
			}
			resultErr = errors.Join(resultErr, unlockErr)
		}()
		return fn(context.WithValue(ctx, transactionContextKey{}, connection))
	})
}
