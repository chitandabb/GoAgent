package auth

import (
	"context"
	"testing"
	"time"
)

func TestUserProvisionerCreatesNormalizedLocalUser(t *testing.T) {
	now := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	users := &stubUserRepository{}
	passwords := &stubPasswordHasher{hashResult: "argon2id-hash"}
	service, err := newUserProvisioner(users, passwords, fixedClock{now: now})
	if err != nil {
		t.Fatalf("newUserProvisioner(): %v", err)
	}

	user, err := service.Create(context.Background(), CreateUserInput{
		Username:           "  Admin01  ",
		DisplayName:        "  系统管理员  ",
		Password:           "password123",
		Role:               RoleAdmin,
		MustChangePassword: true,
	})
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}
	if users.createdUser == nil {
		t.Fatal("user was not persisted")
	}
	if user.Username != "admin01" || user.DisplayName != "系统管理员" {
		t.Fatalf("user names = %q/%q", user.Username, user.DisplayName)
	}
	if user.PasswordHash != "argon2id-hash" || user.Status != UserStatusActive || !user.MustChangePassword {
		t.Fatalf("user security fields = %+v", user)
	}
	if user.CreatedAt != now || user.UpdatedAt != now {
		t.Fatalf("user timestamps = %v/%v, want %v", user.CreatedAt, user.UpdatedAt, now)
	}
}

func TestUserProvisionerRejectsInvalidInputBeforeHashing(t *testing.T) {
	tests := []struct {
		name  string
		input CreateUserInput
	}{
		{name: "missing username", input: CreateUserInput{DisplayName: "User", Password: "password123", Role: RoleAnalyst}},
		{name: "missing display name", input: CreateUserInput{Username: "user", Password: "password123", Role: RoleAnalyst}},
		{name: "short password", input: CreateUserInput{Username: "user", DisplayName: "User", Password: "short", Role: RoleAnalyst}},
		{name: "invalid role", input: CreateUserInput{Username: "user", DisplayName: "User", Password: "password123", Role: Role("owner")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			passwords := &stubPasswordHasher{hashResult: "unused"}
			service, err := newUserProvisioner(&stubUserRepository{}, passwords, fixedClock{now: time.Now()})
			if err != nil {
				t.Fatalf("newUserProvisioner(): %v", err)
			}
			if _, err := service.Create(context.Background(), tt.input); err == nil {
				t.Fatal("Create() error = nil, want validation error")
			}
		})
	}
}
