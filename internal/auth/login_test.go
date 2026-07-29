package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/apperror"
	"github.com/chitandabb/GoAgent/internal/repository"

	"github.com/google/uuid"
)

func TestLoginSuccess(t *testing.T) {
	now := time.Date(2026, 7, 28, 9, 30, 0, 0, time.UTC)
	user := &User{
		ID:           uuid.New(),
		Username:     "analyst01",
		PasswordHash: "stored-password-hash",
		Role:         RoleAnalyst,
		Status:       UserStatusActive,
	}
	users := &stubUserRepository{user: user}
	sessions := &stubSessionRepository{}
	passwords := &stubPasswordHasher{
		hashResult: "dummy-password-hash",
		verify: func(password, encodedHash string) (bool, error) {
			return password == "correct-password" && encodedHash == user.PasswordHash, nil
		},
	}
	tokens := &stubTokenIssuer{tokens: []Token{
		{Raw: "raw-session-token", Hash: []byte("session-hash")},
		{Raw: "raw-csrf-token", Hash: []byte("csrf-hash")},
	}}
	service, err := newLoginService(
		users,
		sessions,
		passwords,
		tokens,
		DefaultSessionPolicy(),
		fixedClock{now: now},
	)
	if err != nil {
		t.Fatalf("newLoginService(): %v", err)
	}

	result, err := service.Login(context.Background(), LoginInput{
		Username: "  Analyst01  ",
		Password: "correct-password",
	})
	if err != nil {
		t.Fatalf("Login(): %v", err)
	}

	if users.gotUsername != "analyst01" {
		t.Fatalf("repository username = %q, want analyst01", users.gotUsername)
	}
	if result.User.ID != user.ID {
		t.Fatalf("result user ID = %s, want %s", result.User.ID, user.ID)
	}
	if result.SessionToken != "raw-session-token" || result.CSRFToken != "raw-csrf-token" {
		t.Fatalf("result tokens = %q/%q, want raw tokens", result.SessionToken, result.CSRFToken)
	}
	if result.IdleExpiresAt != now.Add(2*time.Hour) {
		t.Fatalf("idle expiry = %v, want %v", result.IdleExpiresAt, now.Add(2*time.Hour))
	}
	if result.AbsoluteExpiresAt != now.Add(12*time.Hour) {
		t.Fatalf("absolute expiry = %v, want %v", result.AbsoluteExpiresAt, now.Add(12*time.Hour))
	}
	if sessions.created == nil {
		t.Fatal("session was not persisted")
	}
	if string(sessions.created.TokenHash) != "session-hash" || string(sessions.created.CSRFTokenHash) != "csrf-hash" {
		t.Fatalf("persisted token hashes = %q/%q, want generated hashes", sessions.created.TokenHash, sessions.created.CSRFTokenHash)
	}
	if sessions.created.UserID != user.ID || sessions.created.LastSeenAt != now || sessions.created.CreatedAt != now {
		t.Fatalf("persisted session = %+v, want user and fixed timestamps", sessions.created)
	}
}

func TestLoginRejectsInvalidCredentialsWithoutAccountDisclosure(t *testing.T) {
	tests := []struct {
		name       string
		user       *User
		findErr    error
		passwordOK bool
	}{
		{name: "user not found", findErr: repository.ErrNotFound},
		{
			name:       "wrong password",
			user:       &User{ID: uuid.New(), PasswordHash: "stored", Status: UserStatusActive},
			passwordOK: false,
		},
		{
			name:       "disabled user",
			user:       &User{ID: uuid.New(), PasswordHash: "stored", Status: UserStatusDisabled},
			passwordOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			passwords := &stubPasswordHasher{
				hashResult: "dummy",
				verify: func(string, string) (bool, error) {
					return tt.passwordOK, nil
				},
			}
			sessions := &stubSessionRepository{}
			service, err := newLoginService(
				&stubUserRepository{user: tt.user, findErr: tt.findErr},
				sessions,
				passwords,
				&stubTokenIssuer{},
				DefaultSessionPolicy(),
				fixedClock{now: time.Now()},
			)
			if err != nil {
				t.Fatalf("newLoginService(): %v", err)
			}

			_, err = service.Login(context.Background(), LoginInput{Username: "some-user", Password: "secret"})
			appErr := apperror.Normalize(err)
			if appErr.Code != apperror.CodeUnauthorized || appErr.Message != invalidCredentialsMessage {
				t.Fatalf("Login() error = code %d message %q, want common unauthorized error", appErr.Code, appErr.Message)
			}
			if passwords.verifyCalls != 1 {
				t.Fatalf("Verify() calls = %d, want 1", passwords.verifyCalls)
			}
			if sessions.created != nil {
				t.Fatal("invalid credentials created a session")
			}
		})
	}
}

func TestLoginWrapsRepositoryFailureAsInternalError(t *testing.T) {
	databaseErr := errors.New("database unavailable")
	service, err := newLoginService(
		&stubUserRepository{findErr: databaseErr},
		&stubSessionRepository{},
		&stubPasswordHasher{hashResult: "dummy"},
		&stubTokenIssuer{},
		DefaultSessionPolicy(),
		fixedClock{now: time.Now()},
	)
	if err != nil {
		t.Fatalf("newLoginService(): %v", err)
	}

	_, err = service.Login(context.Background(), LoginInput{Username: "analyst01", Password: "secret"})
	appErr := apperror.Normalize(err)
	if appErr.Code != apperror.CodeInternal {
		t.Fatalf("Login() code = %d, want %d", appErr.Code, apperror.CodeInternal)
	}
	if !errors.Is(err, databaseErr) {
		t.Fatalf("Login() error = %v, want original database error in chain", err)
	}
}

type stubUserRepository struct {
	user        *User
	findErr     error
	gotUsername string
	createdUser *User
}

func (r *stubUserRepository) Create(_ context.Context, user *User) error {
	r.createdUser = user
	return nil
}

func (r *stubUserRepository) FindByID(context.Context, uuid.UUID) (*User, error) {
	return r.user, r.findErr
}

func (r *stubUserRepository) FindByNormalizedUsername(_ context.Context, username string) (*User, error) {
	r.gotUsername = username
	return r.user, r.findErr
}

type stubSessionRepository struct {
	created          *Session
	createErr        error
	active           *Session
	findErr          error
	gotTokenHash     []byte
	refreshed        bool
	refreshIdleUntil time.Time
	revokedID        uuid.UUID
}

func (r *stubSessionRepository) Create(_ context.Context, session *Session) error {
	r.created = session
	return r.createErr
}

func (r *stubSessionRepository) FindActiveByTokenHash(_ context.Context, tokenHash []byte, _ time.Time) (*Session, error) {
	r.gotTokenHash = tokenHash
	if r.active == nil && r.findErr == nil {
		return nil, repository.ErrNotFound
	}
	return r.active, r.findErr
}

func (r *stubSessionRepository) RefreshActivity(_ context.Context, _ uuid.UUID, _, _ time.Time, idleExpiresAt time.Time) error {
	r.refreshed = true
	r.refreshIdleUntil = idleExpiresAt
	return nil
}

func (r *stubSessionRepository) Revoke(_ context.Context, sessionID uuid.UUID, _ time.Time) error {
	r.revokedID = sessionID
	return nil
}

func (*stubSessionRepository) RevokeAllByUserID(context.Context, uuid.UUID, time.Time) error {
	return nil
}

type stubPasswordHasher struct {
	hashResult  string
	hashErr     error
	verify      func(password, encodedHash string) (bool, error)
	verifyCalls int
}

func (h *stubPasswordHasher) Hash(string) (string, error) {
	return h.hashResult, h.hashErr
}

func (h *stubPasswordHasher) Verify(password, encodedHash string) (bool, error) {
	h.verifyCalls++
	if h.verify == nil {
		return false, nil
	}
	return h.verify(password, encodedHash)
}

func (*stubPasswordHasher) NeedsRehash(string) (bool, error) {
	return false, nil
}

type stubTokenIssuer struct {
	tokens []Token
	err    error
	calls  int
}

func (i *stubTokenIssuer) Generate() (Token, error) {
	if i.err != nil {
		return Token{}, i.err
	}
	if i.calls >= len(i.tokens) {
		return Token{}, errors.New("no stub token configured")
	}
	token := i.tokens[i.calls]
	i.calls++
	return token, nil
}

type fixedClock struct {
	now time.Time
}

func (c fixedClock) Now() time.Time {
	return c.now
}
