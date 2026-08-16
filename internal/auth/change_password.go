package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/chitandabb/GoAgent/internal/apperror"
	"github.com/chitandabb/GoAgent/internal/repository"

	"github.com/google/uuid"
)

// ChangePasswordInput 是用户主动改密的业务输入。
type ChangePasswordInput struct {
	UserID          uuid.UUID
	CurrentPassword string
	NewPassword     string
}

// ChangePasswordService 校验当前密码后更新密码哈希，并撤销该用户全部会话，
// 使旧 Session 与旧 CSRF 令牌立即失效（改密即登出）。
type ChangePasswordService struct {
	users    UserRepository
	sessions SessionRepository
	password PasswordHasher
	clock    Clock
}

// NewChangePasswordService 创建改密服务。
func NewChangePasswordService(
	users UserRepository,
	sessions SessionRepository,
	password PasswordHasher,
) (*ChangePasswordService, error) {
	return newChangePasswordService(users, sessions, password, systemClock{})
}

func newChangePasswordService(
	users UserRepository,
	sessions SessionRepository,
	password PasswordHasher,
	clock Clock,
) (*ChangePasswordService, error) {
	if users == nil {
		return nil, errors.New("user repository is nil")
	}
	if sessions == nil {
		return nil, errors.New("session repository is nil")
	}
	if password == nil {
		return nil, errors.New("password hasher is nil")
	}
	if clock == nil {
		return nil, errors.New("clock is nil")
	}
	return &ChangePasswordService{users: users, sessions: sessions, password: password, clock: clock}, nil
}

// ChangePassword 校验当前密码，写入新密码哈希，并撤销该用户全部会话。
// 当前密码错误与用户不存在返回相同的 Unauthorized，避免泄露账号状态。
func (s *ChangePasswordService) ChangePassword(ctx context.Context, input ChangePasswordInput) error {
	if input.UserID == uuid.Nil {
		return changePasswordUnauthorizedError()
	}
	user, err := s.users.FindByID(ctx, input.UserID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return changePasswordUnauthorizedError()
		}
		return apperror.Wrap(apperror.CodeInternal, fmt.Errorf("find change password user: %w", err))
	}
	matches, err := s.password.Verify(input.CurrentPassword, user.PasswordHash)
	if err != nil {
		return apperror.Wrap(apperror.CodeInternal, fmt.Errorf("verify current password: %w", err))
	}
	if !matches {
		return changePasswordUnauthorizedError()
	}
	if len(input.NewPassword) < 8 || len(input.NewPassword) > 256 {
		return apperror.NewWithFields(apperror.CodeValidationFailed, []apperror.FieldError{{
			Field: "newPassword", Reason: "密码长度必须在 8 到 256 字节之间",
		}})
	}

	newHash, err := s.password.Hash(input.NewPassword)
	if err != nil {
		return apperror.Wrap(apperror.CodeInternal, fmt.Errorf("hash new password: %w", err))
	}
	now := s.clock.Now()
	if err := s.users.UpdatePassword(ctx, input.UserID, newHash, now); err != nil {
		return apperror.Wrap(apperror.CodeInternal, fmt.Errorf("update user password: %w", err))
	}
	if err := s.sessions.RevokeAllByUserID(ctx, input.UserID, now); err != nil {
		return apperror.Wrap(apperror.CodeInternal, fmt.Errorf("revoke sessions after password change: %w", err))
	}
	return nil
}

func changePasswordUnauthorizedError() error {
	return apperror.NewWithMessage(apperror.CodeUnauthorized, invalidCredentialsMessage)
}
