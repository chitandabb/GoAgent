package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// CreateUserInput 是本地账号创建命令与后续管理员用例共享的输入。
type CreateUserInput struct {
	Username    string
	DisplayName string
	Password    string
	Role        Role
}

// UserProvisioner 创建使用 Argon2id 密码哈希的本地账号。
type UserProvisioner struct {
	users    UserRepository
	password PasswordHasher
	clock    Clock
}

// NewUserProvisioner 创建本地账号服务。
func NewUserProvisioner(users UserRepository, password PasswordHasher) (*UserProvisioner, error) {
	return newUserProvisioner(users, password, systemClock{})
}

func newUserProvisioner(users UserRepository, password PasswordHasher, clock Clock) (*UserProvisioner, error) {
	if users == nil {
		return nil, errors.New("user repository is nil")
	}
	if password == nil {
		return nil, errors.New("password hasher is nil")
	}
	if clock == nil {
		return nil, errors.New("clock is nil")
	}
	return &UserProvisioner{users: users, password: password, clock: clock}, nil
}

// Create 创建启用状态且首次登录必须改密的本地账号。
func (s *UserProvisioner) Create(ctx context.Context, input CreateUserInput) (User, error) {
	input.Username = NormalizeUsername(input.Username)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if input.Username == "" || len(input.Username) > 64 {
		return User{}, errors.New("username must contain 1 to 64 characters")
	}
	if input.DisplayName == "" || len(input.DisplayName) > 128 {
		return User{}, errors.New("display name must contain 1 to 128 characters")
	}
	if len(input.Password) < 8 || len(input.Password) > 256 {
		return User{}, errors.New("password must contain 8 to 256 bytes")
	}
	if !input.Role.Valid() {
		return User{}, errors.New("role must be analyst or admin")
	}

	passwordHash, err := s.password.Hash(input.Password)
	if err != nil {
		return User{}, fmt.Errorf("hash initial password: %w", err)
	}
	userID, err := uuid.NewV7()
	if err != nil {
		return User{}, fmt.Errorf("generate user id: %w", err)
	}
	now := s.clock.Now()
	user := User{
		ID:                 userID,
		Username:           input.Username,
		DisplayName:        input.DisplayName,
		PasswordHash:       passwordHash,
		MustChangePassword: true,
		Role:               input.Role,
		Status:             UserStatusActive,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := s.users.Create(ctx, &user); err != nil {
		return User{}, fmt.Errorf("create local user: %w", err)
	}
	return user, nil
}
