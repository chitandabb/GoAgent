package auth

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/apperror"
	"github.com/chitandabb/GoAgent/internal/repository"

	"github.com/google/uuid"
)

func TestSessionServiceAuthenticate(t *testing.T) {
	now := time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC)
	user := &User{ID: uuid.New(), Status: UserStatusActive, Role: RoleAnalyst}
	session := &Session{
		ID:                uuid.New(),
		UserID:            user.ID,
		CSRFTokenHash:     HashToken("csrf-token"),
		IdleExpiresAt:     now.Add(time.Hour),
		AbsoluteExpiresAt: now.Add(12 * time.Hour),
		LastSeenAt:        now.Add(-time.Minute),
	}
	sessions := &stubSessionRepository{active: session}
	service, err := newSessionService(
		&stubUserRepository{user: user},
		sessions,
		DefaultSessionPolicy(),
		fixedClock{now: now},
	)
	if err != nil {
		t.Fatalf("newSessionService(): %v", err)
	}

	identity, err := service.Authenticate(context.Background(), "session-token")
	if err != nil {
		t.Fatalf("Authenticate(): %v", err)
	}
	if identity.User.ID != user.ID || identity.Session.ID != session.ID {
		t.Fatalf("identity = %+v, want current user and session", identity)
	}
	if !bytes.Equal(sessions.gotTokenHash, HashToken("session-token")) {
		t.Fatalf("token hash = %x, want SHA-256 hash", sessions.gotTokenHash)
	}
	if sessions.refreshed {
		t.Fatal("recent session activity was refreshed too early")
	}
}

func TestSessionServiceRefreshesIdleExpiryWithoutExceedingAbsoluteExpiry(t *testing.T) {
	now := time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC)
	user := &User{ID: uuid.New(), Status: UserStatusActive}
	absoluteExpiresAt := now.Add(time.Hour)
	sessions := &stubSessionRepository{active: &Session{
		ID:                uuid.New(),
		UserID:            user.ID,
		IdleExpiresAt:     now.Add(10 * time.Minute),
		AbsoluteExpiresAt: absoluteExpiresAt,
		LastSeenAt:        now.Add(-6 * time.Minute),
	}}
	service, err := newSessionService(
		&stubUserRepository{user: user},
		sessions,
		DefaultSessionPolicy(),
		fixedClock{now: now},
	)
	if err != nil {
		t.Fatalf("newSessionService(): %v", err)
	}

	identity, err := service.Authenticate(context.Background(), "session-token")
	if err != nil {
		t.Fatalf("Authenticate(): %v", err)
	}
	if !sessions.refreshed {
		t.Fatal("stale session activity was not refreshed")
	}
	if sessions.refreshIdleUntil != absoluteExpiresAt || identity.Session.IdleExpiresAt != absoluteExpiresAt {
		t.Fatalf("idle expiry = %v/%v, want absolute cap %v", sessions.refreshIdleUntil, identity.Session.IdleExpiresAt, absoluteExpiresAt)
	}
}

func TestSessionServiceRejectsInvalidSessionAndDisabledUser(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name     string
		user     *User
		sessions *stubSessionRepository
	}{
		{name: "session not found", user: &User{Status: UserStatusActive}, sessions: &stubSessionRepository{findErr: repository.ErrNotFound}},
		{
			name: "disabled user",
			user: &User{ID: uuid.New(), Status: UserStatusDisabled},
			sessions: &stubSessionRepository{active: &Session{
				ID: uuid.New(), UserID: uuid.New(), LastSeenAt: now,
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, err := newSessionService(
				&stubUserRepository{user: tt.user},
				tt.sessions,
				DefaultSessionPolicy(),
				fixedClock{now: now},
			)
			if err != nil {
				t.Fatalf("newSessionService(): %v", err)
			}
			_, err = service.Authenticate(context.Background(), "invalid")
			if code := apperror.Normalize(err).Code; code != apperror.CodeUnauthorized {
				t.Fatalf("Authenticate() code = %d, want %d", code, apperror.CodeUnauthorized)
			}
		})
	}
}

func TestSessionServiceValidateCSRFAndLogout(t *testing.T) {
	sessionID := uuid.New()
	sessions := &stubSessionRepository{}
	service, err := newSessionService(
		&stubUserRepository{},
		sessions,
		DefaultSessionPolicy(),
		fixedClock{now: time.Now()},
	)
	if err != nil {
		t.Fatalf("newSessionService(): %v", err)
	}
	identity := Identity{Session: Session{ID: sessionID, CSRFTokenHash: HashToken("csrf-token")}}

	if err := service.ValidateCSRF(identity, "csrf-token"); err != nil {
		t.Fatalf("ValidateCSRF(valid): %v", err)
	}
	if code := apperror.Normalize(service.ValidateCSRF(identity, "wrong-token")).Code; code != apperror.CodeForbidden {
		t.Fatalf("ValidateCSRF(invalid) code = %d, want %d", code, apperror.CodeForbidden)
	}
	if err := service.Logout(context.Background(), identity); err != nil {
		t.Fatalf("Logout(): %v", err)
	}
	if sessions.revokedID != sessionID {
		t.Fatalf("revoked session = %s, want %s", sessions.revokedID, sessionID)
	}
}
