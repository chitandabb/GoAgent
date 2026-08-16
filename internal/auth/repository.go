package auth

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// UserRepository 定义认证用例需要的用户持久化操作。
// 接口放在领域包中，Service 只依赖它，不需要知道底层是 GORM 还是其他实现。
type UserRepository interface {
	Create(ctx context.Context, user *User) error
	FindByID(ctx context.Context, userID uuid.UUID) (*User, error)
	FindByNormalizedUsername(ctx context.Context, username string) (*User, error)
	// UpdatePassword 更新密码哈希并清除改密标记。changedAt 同时写入
	// password_changed_at 与 updated_at。
	UpdatePassword(ctx context.Context, userID uuid.UUID, passwordHash string, changedAt time.Time) error
	// ListUsers 返回筛选后的分页用户列表与总数，按创建时间倒序。
	ListUsers(ctx context.Context, filter UserListFilter, page, pageSize int) ([]User, int64, error)
	// UpdateStatus 更新用户启用状态；用户不存在时返回 repository.ErrNotFound。
	UpdateStatus(ctx context.Context, userID uuid.UUID, status UserStatus) error
	// UpdateRole 更新用户角色；用户不存在时返回 repository.ErrNotFound。
	UpdateRole(ctx context.Context, userID uuid.UUID, role Role) error
	// ResetPassword 覆盖密码哈希并强制下次登录改密；用户不存在时返回
	// repository.ErrNotFound。
	ResetPassword(ctx context.Context, userID uuid.UUID, passwordHash string, changedAt time.Time) error
}

// Session 表示服务端保存的登录会话。
// TokenHash 与 CSRFTokenHash 只保存摘要，原始令牌仅在登录成功时返回给浏览器。
type Session struct {
	ID                uuid.UUID
	UserID            uuid.UUID
	TokenHash         []byte
	CSRFTokenHash     []byte
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
	RevokedAt         *time.Time
	LastSeenAt        time.Time
	CreatedAt         time.Time
}

// SessionRepository 定义认证用例需要的会话持久化操作。
// now 由 UseCase 传入，使有效期判断可预测且便于测试。
type SessionRepository interface {
	Create(ctx context.Context, session *Session) error
	FindActiveByTokenHash(ctx context.Context, tokenHash []byte, now time.Time) (*Session, error)
	RefreshActivity(ctx context.Context, sessionID uuid.UUID, lastSeenBefore, lastSeenAt, idleExpiresAt time.Time) error
	Revoke(ctx context.Context, sessionID uuid.UUID, revokedAt time.Time) error
	RevokeAllByUserID(ctx context.Context, userID uuid.UUID, revokedAt time.Time) error
}
