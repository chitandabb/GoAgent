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

func TestAdminUsersListPassesFilterAndPagination(t *testing.T) {
	now := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	users := &stubUserRepository{
		listUsers: []User{{ID: uuid.New(), Username: "analyst01", Role: RoleAnalyst, Status: UserStatusActive}},
		listTotal: 1,
	}
	service, err := newAdminUsersService(users, &stubSessionRepository{}, &stubPasswordHasher{}, fixedClock{now: now})
	if err != nil {
		t.Fatalf("newAdminUsersService(): %v", err)
	}

	status := UserStatusActive
	role := RoleAnalyst
	page, err := service.List(context.Background(), ListUsersInput{
		Status: &status, Role: &role, Page: 2, PageSize: 10,
	})
	if err != nil {
		t.Fatalf("List(): %v", err)
	}

	if page.Total != 1 || page.Page != 2 || page.PageSize != 10 || len(page.Items) != 1 {
		t.Fatalf("page = %+v", page)
	}
	if users.listGotStatus == nil || *users.listGotStatus != status {
		t.Fatalf("list status filter = %v, want %v", users.listGotStatus, status)
	}
	if users.listGotRole == nil || *users.listGotRole != role {
		t.Fatalf("list role filter = %v, want %v", users.listGotRole, role)
	}
	if users.listGotPage != 2 || users.listGotPageSize != 10 {
		t.Fatalf("list pagination = page %d size %d", users.listGotPage, users.listGotPageSize)
	}
}

func TestAdminUsersListNormalizesPagination(t *testing.T) {
	users := &stubUserRepository{listTotal: 0}
	service, err := newAdminUsersService(users, &stubSessionRepository{}, &stubPasswordHasher{}, fixedClock{now: time.Now()})
	if err != nil {
		t.Fatalf("newAdminUsersService(): %v", err)
	}

	if _, err := service.List(context.Background(), ListUsersInput{Page: 0, PageSize: 0}); err != nil {
		t.Fatalf("List(): %v", err)
	}
	if users.listGotPage != 1 || users.listGotPageSize != 20 {
		t.Fatalf("normalized pagination = page %d size %d, want 1/20", users.listGotPage, users.listGotPageSize)
	}
}

func TestAdminUsersCreateForcesPasswordChange(t *testing.T) {
	now := time.Date(2026, 8, 10, 10, 30, 0, 0, time.UTC)
	users := &stubUserRepository{}
	passwords := &stubPasswordHasher{hashResult: "argon2id-hash"}
	service, err := newAdminUsersService(users, &stubSessionRepository{}, passwords, fixedClock{now: now})
	if err != nil {
		t.Fatalf("newAdminUsersService(): %v", err)
	}

	user, err := service.Create(context.Background(), CreateUserInput{
		Username: "  NewAnalyst01 ", DisplayName: "新分析员", Password: "temp-pass-123", Role: RoleAnalyst,
	})
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}

	if user.Username != "newanalyst01" || user.DisplayName != "新分析员" {
		t.Fatalf("created user = %+v", user)
	}
	if !user.MustChangePassword {
		t.Fatal("MustChangePassword = false, want true for admin-created users")
	}
	if users.createdUser == nil || users.createdUser.PasswordHash != "argon2id-hash" ||
		users.createdUser.Status != UserStatusActive || users.createdUser.Role != RoleAnalyst {
		t.Fatalf("persisted user = %+v", users.createdUser)
	}
	if users.createdUser.CreatedAt != now || users.createdUser.UpdatedAt != now {
		t.Fatalf("created timestamps = %v/%v, want %v", users.createdUser.CreatedAt, users.createdUser.UpdatedAt, now)
	}
}

func TestAdminUsersCreateValidatesInput(t *testing.T) {
	service, err := newAdminUsersService(
		&stubUserRepository{}, &stubSessionRepository{}, &stubPasswordHasher{}, fixedClock{now: time.Now()},
	)
	if err != nil {
		t.Fatalf("newAdminUsersService(): %v", err)
	}

	invalid := []CreateUserInput{
		{Username: "  ", DisplayName: "x", Password: "temp-pass-123", Role: RoleAnalyst},
		{Username: "user01", DisplayName: " ", Password: "temp-pass-123", Role: RoleAnalyst},
		{Username: "user01", DisplayName: "x", Password: "short", Role: RoleAnalyst},
		{Username: "user01", DisplayName: "x", Password: "temp-pass-123", Role: "superuser"},
	}
	for _, input := range invalid {
		_, err := service.Create(context.Background(), input)
		appErr := apperror.Normalize(err)
		if appErr.Code != apperror.CodeValidationFailed {
			t.Fatalf("Create(%+v) code = %d, want %d", input, appErr.Code, apperror.CodeValidationFailed)
		}
	}
}

func TestAdminUsersCreateMapsUsernameConflict(t *testing.T) {
	users := &stubUserRepository{createErr: repository.ErrConflict}
	service, err := newAdminUsersService(
		users, &stubSessionRepository{}, &stubPasswordHasher{}, fixedClock{now: time.Now()},
	)
	if err != nil {
		t.Fatalf("newAdminUsersService(): %v", err)
	}

	_, err = service.Create(context.Background(), CreateUserInput{
		Username: "analyst01", DisplayName: "x", Password: "temp-pass-123", Role: RoleAnalyst,
	})
	if appErr := apperror.Normalize(err); appErr.Code != apperror.CodeConflict {
		t.Fatalf("Create() code = %d, want %d", appErr.Code, apperror.CodeConflict)
	}
}

func TestAdminUsersSetStatus(t *testing.T) {
	now := time.Date(2026, 8, 10, 11, 0, 0, 0, time.UTC)
	actorID := uuid.New()
	targetID := uuid.New()
	users := &stubUserRepository{}
	sessions := &stubSessionRepository{}
	service, err := newAdminUsersService(users, sessions, &stubPasswordHasher{}, fixedClock{now: now})
	if err != nil {
		t.Fatalf("newAdminUsersService(): %v", err)
	}

	if err := service.SetStatus(context.Background(), actorID, targetID, UserStatusDisabled); err != nil {
		t.Fatalf("SetStatus(): %v", err)
	}
	if users.updatedStatusUserID != targetID || users.updatedStatus != UserStatusDisabled {
		t.Fatalf("status update = %v %q", users.updatedStatusUserID, users.updatedStatus)
	}

	t.Run("cannot operate on self", func(t *testing.T) {
		err := service.SetStatus(context.Background(), actorID, actorID, UserStatusDisabled)
		if appErr := apperror.Normalize(err); appErr.Code != apperror.CodeForbidden {
			t.Fatalf("SetStatus(self) code = %d, want %d", appErr.Code, apperror.CodeForbidden)
		}
		if users.updatedStatusUserID != targetID {
			t.Fatal("self operation reached repository")
		}
	})
}

func TestAdminUsersSetRoleRevokesSessions(t *testing.T) {
	now := time.Date(2026, 8, 10, 11, 30, 0, 0, time.UTC)
	actorID := uuid.New()
	targetID := uuid.New()
	users := &stubUserRepository{}
	sessions := &stubSessionRepository{}
	service, err := newAdminUsersService(users, sessions, &stubPasswordHasher{}, fixedClock{now: now})
	if err != nil {
		t.Fatalf("newAdminUsersService(): %v", err)
	}

	if err := service.SetRole(context.Background(), actorID, targetID, RoleAdmin); err != nil {
		t.Fatalf("SetRole(): %v", err)
	}
	if users.updatedRoleUserID != targetID || users.updatedRole != RoleAdmin {
		t.Fatalf("role update = %v %q", users.updatedRoleUserID, users.updatedRole)
	}
	if sessions.revokedAllFor != targetID || sessions.revokedAllAt != now {
		t.Fatalf("session revocation = %v at %v, want %v at %v", sessions.revokedAllFor, sessions.revokedAllAt, targetID, now)
	}
}

func TestAdminUsersResetPasswordRequiresPasswordChangeAgain(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	actorID := uuid.New()
	targetID := uuid.New()
	users := &stubUserRepository{}
	sessions := &stubSessionRepository{}
	passwords := &stubPasswordHasher{hashResult: "reset-hash"}
	service, err := newAdminUsersService(users, sessions, passwords, fixedClock{now: now})
	if err != nil {
		t.Fatalf("newAdminUsersService(): %v", err)
	}

	if err := service.ResetPassword(context.Background(), actorID, targetID, "reset-pass-123"); err != nil {
		t.Fatalf("ResetPassword(): %v", err)
	}
	if users.resetPasswordHash != "reset-hash" || users.resetPasswordAt != now {
		t.Fatalf("password reset = hash %q at %v", users.resetPasswordHash, users.resetPasswordAt)
	}
	if users.resetPasswordMustChange == nil || !*users.resetPasswordMustChange {
		t.Fatal("must_change_password = false, want true after reset")
	}
	if sessions.revokedAllFor != targetID {
		t.Fatalf("session revocation = %v, want %v", sessions.revokedAllFor, targetID)
	}

	t.Run("short password is rejected", func(t *testing.T) {
		err := service.ResetPassword(context.Background(), actorID, targetID, "short")
		if appErr := apperror.Normalize(err); appErr.Code != apperror.CodeValidationFailed {
			t.Fatalf("ResetPassword(short) code = %d, want %d", appErr.Code, apperror.CodeValidationFailed)
		}
	})
}

func TestAdminUsersOperationsMapNotFound(t *testing.T) {
	actorID := uuid.New()
	targetID := uuid.New()
	users := &stubUserRepository{
		updateStatusErr: repository.ErrNotFound,
		updateRoleErr:   repository.ErrNotFound,
		resetPasswordErr: repository.ErrNotFound,
	}
	service, err := newAdminUsersService(users, &stubSessionRepository{}, &stubPasswordHasher{}, fixedClock{now: time.Now()})
	if err != nil {
		t.Fatalf("newAdminUsersService(): %v", err)
	}

	cases := []struct {
		name string
		run  func() error
	}{
		{"SetStatus", func() error { return service.SetStatus(context.Background(), actorID, targetID, UserStatusActive) }},
		{"SetRole", func() error { return service.SetRole(context.Background(), actorID, targetID, RoleAnalyst) }},
		{"ResetPassword", func() error {
			return service.ResetPassword(context.Background(), actorID, targetID, "reset-pass-123")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if appErr := apperror.Normalize(err); appErr.Code != apperror.CodeNotFound {
				t.Fatalf("%s() code = %d, want %d", tc.name, appErr.Code, apperror.CodeNotFound)
			}
			if !errors.Is(err, repository.ErrNotFound) {
				t.Fatalf("%s() error = %v, want repository.ErrNotFound in chain", tc.name, err)
			}
		})
	}
}
