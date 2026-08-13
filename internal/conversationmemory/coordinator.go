package conversationmemory

import (
	"context"
	"errors"
	"sync"

	"github.com/google/uuid"
)

// Coordinator serializes compaction for one Conversation. Soft and hard
// triggers share this boundary; a waiter must reload Current Summary after it
// acquires the slot before deciding whether another model call is necessary.
type Coordinator interface {
	WithinConversation(context.Context, uuid.UUID, func(context.Context) error) error
}

type localCoordinator struct {
	mu    sync.Mutex
	locks map[uuid.UUID]*localConversationLock
}

type localConversationLock struct {
	gate chan struct{}
	refs int
}

func NewLocalCoordinator() Coordinator {
	return &localCoordinator{locks: make(map[uuid.UUID]*localConversationLock)}
}

func (c *localCoordinator) WithinConversation(ctx context.Context, conversationID uuid.UUID, fn func(context.Context) error) error {
	if c == nil || conversationID == uuid.Nil || fn == nil {
		return ErrInvalidShadowInput
	}
	c.mu.Lock()
	lock := c.locks[conversationID]
	if lock == nil {
		lock = &localConversationLock{gate: make(chan struct{}, 1)}
		lock.gate <- struct{}{}
		c.locks[conversationID] = lock
	}
	lock.refs++
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		c.releaseReference(conversationID, lock)
		return ctx.Err()
	case <-lock.gate:
	}
	defer func() {
		lock.gate <- struct{}{}
		c.releaseReference(conversationID, lock)
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(ctx)
}

func (c *localCoordinator) releaseReference(conversationID uuid.UUID, lock *localConversationLock) {
	c.mu.Lock()
	defer c.mu.Unlock()
	lock.refs--
	if lock.refs == 0 && c.locks[conversationID] == lock {
		delete(c.locks, conversationID)
	}
}

type coordinatorFunc func(context.Context, uuid.UUID, func(context.Context) error) error

func (f coordinatorFunc) WithinConversation(ctx context.Context, id uuid.UUID, fn func(context.Context) error) error {
	if f == nil {
		return errors.New("conversation memory coordinator is unavailable")
	}
	return f(ctx, id, fn)
}
