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

func TestChangePasswordSuccess(t *testing.T) {
	now := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	userID := uuid.New()
	user := &User{
		ID:           userID,
		Username:     "analyst01",
		PasswordHash: "stored-hash",
		Role:         RoleAnalyst,
		Status:       UserStatusActive,
	}
	users := &stubUserRepository{user: user}
	sessions := &stubSessionRepository{}
	passwords := &stubPasswordHasher{
		hashResult: "new-hash",
		verify: func(password, encodedHash string) (bool, error) {
			return password == "current-password" && encodedHash == "stored-hash", nil
		},
	}
	service, err := newChangePasswordService(users, sessions, passwords, fixedClock{now: now})
	if err != nil {
		t.Fatalf("newChangePasswordService(): %v", err)
	}

	err = service.ChangePassword(context.Background(), ChangePasswordInput{
		UserID: userID, CurrentPassword: "current-password", NewPassword: "new-password-1",
	})
	if err != nil {
		t.Fatalf("ChangePassword(): %v", err)
	}

	if users.updatedPasswordHash != "new-hash" || users.updatedPasswordAt != now {
		t.Fatalf("password update = hash %q at %v, want new-hash at %v", users.updatedPasswordHash, users.updatedPasswordAt, now)
	}
	if sessions.revokedAllFor != userID || sessions.revokedAllAt != now {
		t.Fatalf("session revocation = user %v at %v, want %v at %v", sessions.revokedAllFor, sessions.revokedAllAt, userID, now)
	}
}

func TestChangePasswordRejectsWrongCurrentPasswordWithoutUpdate(t *testing.T) {
	user := &User{ID: uuid.New(), PasswordHash: "stored-hash", Status: UserStatusActive}
	passwords := &stubPasswordHasher{
		verify: func(string, string) (bool, error) { return false, nil },
	}
	service, err := newChangePasswordService(
		&stubUserRepository{user: user}, &stubSessionRepository{}, passwords, fixedClock{now: time.Now()},
	)
	if err != nil {
		t.Fatalf("newChangePasswordService(): %v", err)
	}

	err = service.ChangePassword(context.Background(), ChangePasswordInput{
		UserID: user.ID, CurrentPassword: "wrong-password", NewPassword: "new-password-1",
	})
	appErr := apperror.Normalize(err)
	if appErr.Code != apperror.CodeUnauthorized {
		t.Fatalf("ChangePassword() code = %d, want %d", appErr.Code, apperror.CodeUnauthorized)
	}
	if passwords.verifyCalls != 1 {
		t.Fatalf("Verify() calls = %d, want 1", passwords.verifyCalls)
	}
}

func TestChangePasswordRejectsUnknownUser(t *testing.T) {
	service, err := newChangePasswordService(
		&stubUserRepository{findErr: repository.ErrNotFound},
		&stubSessionRepository{},
		&stubPasswordHasher{},
		fixedClock{now: time.Now()},
	)
	if err != nil {
		t.Fatalf("newChangePasswordService(): %v", err)
	}

	err = service.ChangePassword(context.Background(), ChangePasswordInput{
		UserID: uuid.New(), CurrentPassword: "current-password", NewPassword: "new-password-1",
	})
	appErr := apperror.Normalize(err)
	if appErr.Code != apperror.CodeUnauthorized {
		t.Fatalf("ChangePassword() code = %d, want %d", appErr.Code, apperror.CodeUnauthorized)
	}
}

func TestChangePasswordRejectsInvalidNewPassword(t *testing.T) {
	user := &User{ID: uuid.New(), PasswordHash: "stored-hash", Status: UserStatusActive}
	passwords := &stubPasswordHasher{
		verify: func(string, string) (bool, error) { return true, nil },
	}
	service, err := newChangePasswordService(
		&stubUserRepository{user: user}, &stubSessionRepository{}, passwords, fixedClock{now: time.Now()},
	)
	if err != nil {
		t.Fatalf("newChangePasswordService(): %v", err)
	}

	for _, newPassword := range []string{"short", ""} {
		err := service.ChangePassword(context.Background(), ChangePasswordInput{
			UserID: user.ID, CurrentPassword: "current-password", NewPassword: newPassword,
		})
		appErr := apperror.Normalize(err)
		if appErr.Code != apperror.CodeValidationFailed {
			t.Fatalf("ChangePassword(%q) code = %d, want %d", newPassword, appErr.Code, apperror.CodeValidationFailed)
		}
	}
}

func TestChangePasswordWrapsRepositoryFailureAsInternalError(t *testing.T) {
	databaseErr := errors.New("database unavailable")
	service, err := newChangePasswordService(
		&stubUserRepository{findErr: databaseErr},
		&stubSessionRepository{},
		&stubPasswordHasher{},
		fixedClock{now: time.Now()},
	)
	if err != nil {
		t.Fatalf("newChangePasswordService(): %v", err)
	}

	err = service.ChangePassword(context.Background(), ChangePasswordInput{
		UserID: uuid.New(), CurrentPassword: "current-password", NewPassword: "new-password-1",
	})
	appErr := apperror.Normalize(err)
	if appErr.Code != apperror.CodeInternal {
		t.Fatalf("ChangePassword() code = %d, want %d", appErr.Code, apperror.CodeInternal)
	}
	if !errors.Is(err, databaseErr) {
		t.Fatalf("ChangePassword() error = %v, want original error in chain", err)
	}
}
