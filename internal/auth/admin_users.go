package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/chitandabb/GoAgent/internal/apperror"
	"github.com/chitandabb/GoAgent/internal/repository"

	"github.com/google/uuid"
)

// ListUsersInput 是管理员用户列表查询输入。Status/Role 为空表示不过滤。
type ListUsersInput struct {
	Status   *UserStatus
	Role     *Role
	Page     int
	PageSize int
}

// UserPage 是管理员用户列表的分页结果。
type UserPage struct {
	Items    []User
	Total    int64
	Page     int
	PageSize int
}

// UserListFilter 是仓储层共享的用户筛选条件。
type UserListFilter struct {
	Status *UserStatus
	Role   *Role
}

const (
	defaultUserPageSize = 20
	maxUserPageSize     = 100
)

// AdminUsersService 实现管理员对本地账号的 CRUD 与口令重置。
// 管理员创建的用户强制首次登录改密；角色变更与口令重置会撤销目标用户全部会话。
type AdminUsersService struct {
	users    UserRepository
	sessions SessionRepository
	password PasswordHasher
	clock    Clock
}

// NewAdminUsersService 创建管理员用户服务。
func NewAdminUsersService(
	users UserRepository,
	sessions SessionRepository,
	password PasswordHasher,
) (*AdminUsersService, error) {
	return newAdminUsersService(users, sessions, password, systemClock{})
}

func newAdminUsersService(
	users UserRepository,
	sessions SessionRepository,
	password PasswordHasher,
	clock Clock,
) (*AdminUsersService, error) {
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
	return &AdminUsersService{users: users, sessions: sessions, password: password, clock: clock}, nil
}

// List 返回分页用户列表，默认第 1 页每页 20 条，最大 100 条。
func (s *AdminUsersService) List(ctx context.Context, input ListUsersInput) (UserPage, error) {
	page, pageSize := normalizeUserPagination(input.Page, input.PageSize)
	items, total, err := s.users.ListUsers(ctx, UserListFilter{
		Status: cloneUserStatus(input.Status),
		Role:   cloneRole(input.Role),
	}, page, pageSize)
	if err != nil {
		return UserPage{}, apperror.Wrap(apperror.CodeInternal, fmt.Errorf("list admin users: %w", err))
	}
	return UserPage{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

// Create 创建启用状态的账号并强制首次登录改密。
func (s *AdminUsersService) Create(ctx context.Context, input CreateUserInput) (User, error) {
	input.Username = NormalizeUsername(input.Username)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if input.Username == "" || len(input.Username) > 64 {
		return User{}, validationFieldError("username", "用户名长度必须在 1 到 64 个字符之间")
	}
	if input.DisplayName == "" || len(input.DisplayName) > 128 {
		return User{}, validationFieldError("displayName", "显示名称长度必须在 1 到 128 个字符之间")
	}
	if len(input.Password) < 8 || len(input.Password) > 256 {
		return User{}, validationFieldError("temporaryPassword", "临时密码长度必须在 8 到 256 字节之间")
	}
	if !input.Role.Valid() {
		return User{}, validationFieldError("role", "角色必须是 analyst 或 admin")
	}

	passwordHash, err := s.password.Hash(input.Password)
	if err != nil {
		return User{}, apperror.Wrap(apperror.CodeInternal, fmt.Errorf("hash temporary password: %w", err))
	}
	userID, err := uuid.NewV7()
	if err != nil {
		return User{}, apperror.Wrap(apperror.CodeInternal, fmt.Errorf("generate user id: %w", err))
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
		if errors.Is(err, repository.ErrConflict) {
			return User{}, apperror.Wrap(apperror.CodeConflict, err)
		}
		return User{}, apperror.Wrap(apperror.CodeInternal, fmt.Errorf("create admin user: %w", err))
	}
	return user, nil
}

// SetStatus 启用或禁用账号。管理员不能操作自己的账号，防止自锁。
func (s *AdminUsersService) SetStatus(ctx context.Context, actorID, userID uuid.UUID, status UserStatus) error {
	if actorID == userID {
		return apperror.New(apperror.CodeForbidden)
	}
	if !status.Valid() {
		return validationFieldError("status", "状态必须是 active 或 disabled")
	}
	if err := s.users.UpdateStatus(ctx, userID, status); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return apperror.Wrap(apperror.CodeNotFound, err)
		}
		return apperror.Wrap(apperror.CodeInternal, fmt.Errorf("update user status: %w", err))
	}
	return nil
}

// SetRole 变更用户角色并撤销其全部会话，使旧权限立即失效。
func (s *AdminUsersService) SetRole(ctx context.Context, actorID, userID uuid.UUID, role Role) error {
	if actorID == userID {
		return apperror.New(apperror.CodeForbidden)
	}
	if !role.Valid() {
		return validationFieldError("role", "角色必须是 analyst 或 admin")
	}
	if err := s.users.UpdateRole(ctx, userID, role); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return apperror.Wrap(apperror.CodeNotFound, err)
		}
		return apperror.Wrap(apperror.CodeInternal, fmt.Errorf("update user role: %w", err))
	}
	now := s.clock.Now()
	if err := s.sessions.RevokeAllByUserID(ctx, userID, now); err != nil {
		return apperror.Wrap(apperror.CodeInternal, fmt.Errorf("revoke sessions after role change: %w", err))
	}
	return nil
}

// ResetPassword 设置新的临时密码，强制下次登录改密并撤销全部会话。
func (s *AdminUsersService) ResetPassword(ctx context.Context, actorID, userID uuid.UUID, temporaryPassword string) error {
	if actorID == userID {
		return apperror.New(apperror.CodeForbidden)
	}
	if len(temporaryPassword) < 8 || len(temporaryPassword) > 256 {
		return validationFieldError("temporaryPassword", "临时密码长度必须在 8 到 256 字节之间")
	}
	passwordHash, err := s.password.Hash(temporaryPassword)
	if err != nil {
		return apperror.Wrap(apperror.CodeInternal, fmt.Errorf("hash reset password: %w", err))
	}
	now := s.clock.Now()
	if err := s.users.ResetPassword(ctx, userID, passwordHash, now); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return apperror.Wrap(apperror.CodeNotFound, err)
		}
		return apperror.Wrap(apperror.CodeInternal, fmt.Errorf("reset user password: %w", err))
	}
	if err := s.sessions.RevokeAllByUserID(ctx, userID, now); err != nil {
		return apperror.Wrap(apperror.CodeInternal, fmt.Errorf("revoke sessions after password reset: %w", err))
	}
	return nil
}

func normalizeUserPagination(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = defaultUserPageSize
	} else if pageSize > maxUserPageSize {
		pageSize = maxUserPageSize
	}
	return page, pageSize
}

func validationFieldError(field, reason string) error {
	return apperror.NewWithFields(apperror.CodeValidationFailed, []apperror.FieldError{{
		Field: field, Reason: reason,
	}})
}

func cloneUserStatus(value *UserStatus) *UserStatus {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneRole(value *Role) *Role {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
