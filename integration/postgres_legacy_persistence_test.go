package integration_test

import (
	"GopherAI/common/postgres"
	messageDAO "GopherAI/dao/message"
	sessionDAO "GopherAI/dao/session"
	userDAO "GopherAI/dao/user"
	"GopherAI/model"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestLegacyPersistenceUsesPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("requires the local PostgreSQL development service")
	}

	t.Setenv("CONFIG_FILE", "../config/config.toml")
	if os.Getenv("MESGUARD_POSTGRES_PASSWORD") == "" {
		t.Setenv("MESGUARD_POSTGRES_PASSWORD", "mesguard_dev_password")
	}
	if err := postgres.InitPostgres(); err != nil {
		t.Fatalf("initialize PostgreSQL: %v", err)
	}

	suffix := fmt.Sprintf("%x", time.Now().UnixNano())
	username := "pg" + suffix
	sessionID := "session-" + suffix
	t.Cleanup(func() {
		postgres.DB.Unscoped().Where("session_id = ?", sessionID).Delete(&model.Message{})
		postgres.DB.Unscoped().Where("id = ?", sessionID).Delete(&model.Session{})
		postgres.DB.Unscoped().Where("username = ?", username).Delete(&model.User{})
	})

	createdUser, created := userDAO.Register(username, username+"@example.test", "test-password")
	if !created {
		t.Fatal("create user")
	}
	if createdUser.Username != username {
		t.Fatalf("unexpected user: got %q want %q", createdUser.Username, username)
	}

	createdSession, err := sessionDAO.CreateSession(&model.Session{
		ID:       sessionID,
		UserName: username,
		Title:    "PostgreSQL migration test",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if createdSession.ID != sessionID {
		t.Fatalf("unexpected session: got %q want %q", createdSession.ID, sessionID)
	}

	content := "persisted through PostgreSQL"
	if _, err := messageDAO.CreateMessage(&model.Message{
		SessionID: sessionID,
		UserName:  username,
		Content:   content,
		IsUser:    true,
	}); err != nil {
		t.Fatalf("create message: %v", err)
	}

	messages, err := messageDAO.GetMessagesBySessionID(sessionID)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Content != content {
		t.Fatalf("unexpected persisted messages: %s", fmt.Sprint(messages))
	}
}
