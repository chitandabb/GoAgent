//go:build integration

package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestConversationMemoryCoordinatorAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("MESGUARD_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MESGUARD_TEST_POSTGRES_DSN is not configured")
	}
	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test postgres: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get test postgres sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(3)
	t.Cleanup(func() { _ = sqlDB.Close() })

	coordinator := NewConversationMemoryCoordinator(db)
	conversationID := uuid.New()
	ownerEntered := make(chan struct{})
	releaseOwner := make(chan struct{})
	ownerDone := make(chan error, 1)
	go func() {
		ownerDone <- coordinator.WithinConversation(context.Background(), conversationID, func(context.Context) error {
			close(ownerEntered)
			<-releaseOwner
			return nil
		})
	}()
	<-ownerEntered

	waiterEntered := make(chan struct{})
	waiterDone := make(chan error, 1)
	go func() {
		waiterDone <- coordinator.WithinConversation(context.Background(), conversationID, func(context.Context) error {
			close(waiterEntered)
			return nil
		})
	}()
	select {
	case <-waiterEntered:
		t.Fatal("same Conversation acquired two PostgreSQL advisory locks concurrently")
	case <-time.After(100 * time.Millisecond):
	}

	differentCtx, cancelDifferent := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelDifferent()
	if err := coordinator.WithinConversation(differentCtx, uuid.New(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("different Conversation should run concurrently: %v", err)
	}

	close(releaseOwner)
	if err := <-ownerDone; err != nil {
		t.Fatalf("owner coordinator error: %v", err)
	}
	select {
	case <-waiterEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("same Conversation waiter did not acquire the released advisory lock")
	}
	if err := <-waiterDone; err != nil {
		t.Fatalf("waiter coordinator error: %v", err)
	}
}
