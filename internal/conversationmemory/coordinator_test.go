package conversationmemory

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestLocalCoordinatorSerializesSameConversation(t *testing.T) {
	coordinator := NewLocalCoordinator()
	conversationID := uuid.New()
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 2)

	go func() {
		done <- coordinator.WithinConversation(context.Background(), conversationID, func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	var secondEntered atomic.Bool
	go func() {
		done <- coordinator.WithinConversation(context.Background(), conversationID, func(context.Context) error {
			secondEntered.Store(true)
			return nil
		})
	}()

	time.Sleep(20 * time.Millisecond)
	if secondEntered.Load() {
		t.Fatal("second callback entered before the first released the Conversation")
	}
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("WithinConversation() error = %v", err)
		}
	}
	if !secondEntered.Load() {
		t.Fatal("second callback never entered")
	}
}

func TestLocalCoordinatorAllowsDifferentConversationsInParallel(t *testing.T) {
	coordinator := NewLocalCoordinator()
	firstEntered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- coordinator.WithinConversation(context.Background(), uuid.New(), func(context.Context) error {
			close(firstEntered)
			<-release
			return nil
		})
	}()
	<-firstEntered

	secondEntered := make(chan struct{})
	if err := coordinator.WithinConversation(context.Background(), uuid.New(), func(context.Context) error {
		close(secondEntered)
		return nil
	}); err != nil {
		t.Fatalf("second WithinConversation() error = %v", err)
	}
	select {
	case <-secondEntered:
	default:
		t.Fatal("different Conversation was unnecessarily serialized")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first WithinConversation() error = %v", err)
	}
}

func TestLocalCoordinatorCancelsWaiterWithoutRunningCallback(t *testing.T) {
	coordinator := NewLocalCoordinator()
	conversationID := uuid.New()
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- coordinator.WithinConversation(context.Background(), conversationID, func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	waiterCtx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := coordinator.WithinConversation(waiterCtx, conversationID, func(context.Context) error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("waiter error/called = %v/%t", err, called)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("owner WithinConversation() error = %v", err)
	}
}
