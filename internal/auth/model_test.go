package auth

import "testing"

func TestRoleValid(t *testing.T) {
	tests := []struct {
		role Role
		want bool
	}{
		{role: RoleAnalyst, want: true},
		{role: RoleAdmin, want: true},
		{role: Role("owner"), want: false},
		{role: "", want: false},
	}

	for _, tt := range tests {
		if got := tt.role.Valid(); got != tt.want {
			t.Fatalf("Role(%q).Valid() = %v, want %v", tt.role, got, tt.want)
		}
	}
}

func TestUserStatusValid(t *testing.T) {
	tests := []struct {
		status UserStatus
		want   bool
	}{
		{status: UserStatusActive, want: true},
		{status: UserStatusDisabled, want: true},
		{status: UserStatus("deleted"), want: false},
		{status: "", want: false},
	}

	for _, tt := range tests {
		if got := tt.status.Valid(); got != tt.want {
			t.Fatalf("UserStatus(%q).Valid() = %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestUserBehavior(t *testing.T) {
	user := User{Role: RoleAdmin, Status: UserStatusActive, MustChangePassword: true}
	if !user.CanLogin() {
		t.Fatal("active user cannot log in")
	}
	if !user.IsAdmin() {
		t.Fatal("admin user was not recognized")
	}
	if !user.RequiresPasswordChange() {
		t.Fatal("password change requirement was not recognized")
	}

	user.Status = UserStatusDisabled
	if user.CanLogin() {
		t.Fatal("disabled user can log in")
	}
}

func TestNormalizeUsername(t *testing.T) {
	if got := NormalizeUsername("  Analyst01  "); got != "analyst01" {
		t.Fatalf("NormalizeUsername() = %q, want analyst01", got)
	}
}
