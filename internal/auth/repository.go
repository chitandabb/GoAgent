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
