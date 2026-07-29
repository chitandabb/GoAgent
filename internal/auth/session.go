package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/chitandabb/GoAgent/internal/apperror"
	"github.com/chitandabb/GoAgent/internal/repository"
)

const sessionRefreshInterval = 5 * time.Minute

// Identity 是认证成功后放入请求上下文的当前身份。
type Identity struct {
	User    User
	Session Session
}

// SessionService 负责恢复、校验和撤销服务端 Session。
type SessionService struct {
	users    UserRepository
	sessions SessionRepository
	policy   SessionPolicy
	clock    Clock
}

// NewSessionService 创建生产环境 Session 服务。
func NewSessionService(users UserRepository, sessions SessionRepository, policy SessionPolicy) (*SessionService, error) {
	return newSessionService(users, sessions, policy, systemClock{})
}

func newSessionService(
	users UserRepository,
	sessions SessionRepository,
	policy SessionPolicy,
	clock Clock,
) (*SessionService, error) {
	if users == nil {
		return nil, errors.New("user repository is nil")
	}
	if sessions == nil {
		return nil, errors.New("session repository is nil")
	}
	if clock == nil {
		return nil, errors.New("clock is nil")
	}
	if err := policy.validate(); err != nil {
		return nil, err
	}
	return &SessionService{users: users, sessions: sessions, policy: policy, clock: clock}, nil
}

// Authenticate 根据浏览器提交的原始 Token 恢复当前用户，并按窗口刷新空闲有效期。
func (s *SessionService) Authenticate(ctx context.Context, rawToken string) (Identity, error) {
	if rawToken == "" {
		return Identity{}, unauthorizedError()
	}
	now := s.clock.Now()
	session, err := s.sessions.FindActiveByTokenHash(ctx, HashToken(rawToken), now)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return Identity{}, unauthorizedError()
		}
		return Identity{}, apperror.Wrap(apperror.CodeInternal, fmt.Errorf("find active session: %w", err))
	}
	user, err := s.users.FindByID(ctx, session.UserID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return Identity{}, unauthorizedError()
		}
		return Identity{}, apperror.Wrap(apperror.CodeInternal, fmt.Errorf("find session user: %w", err))
	}
	if !user.CanLogin() {
		return Identity{}, unauthorizedError()
	}

	if !now.Before(session.LastSeenAt.Add(sessionRefreshInterval)) {
		idleExpiresAt := now.Add(s.policy.IdleDuration())
		if idleExpiresAt.After(session.AbsoluteExpiresAt) {
			idleExpiresAt = session.AbsoluteExpiresAt
		}
		if err := s.sessions.RefreshActivity(
			ctx,
			session.ID,
			now.Add(-sessionRefreshInterval),
			now,
			idleExpiresAt,
		); err != nil {
			return Identity{}, apperror.Wrap(apperror.CodeInternal, fmt.Errorf("refresh session activity: %w", err))
		}
		session.LastSeenAt = now
		session.IdleExpiresAt = idleExpiresAt
	}
	return Identity{User: *user, Session: *session}, nil
}

// ValidateCSRF 使用常量时间比较当前 Session 保存的摘要。
func (*SessionService) ValidateCSRF(identity Identity, rawToken string) error {
	if rawToken == "" {
		return apperror.New(apperror.CodeForbidden)
	}
	want := identity.Session.CSRFTokenHash
	got := HashToken(rawToken)
	if len(want) != len(got) || subtle.ConstantTimeCompare(want, got) != 1 {
		return apperror.New(apperror.CodeForbidden)
	}
	return nil
}

// Logout 幂等撤销当前 Session。
func (s *SessionService) Logout(ctx context.Context, identity Identity) error {
	if err := s.sessions.Revoke(ctx, identity.Session.ID, s.clock.Now()); err != nil {
		return apperror.Wrap(apperror.CodeInternal, fmt.Errorf("revoke session: %w", err))
	}
	return nil
}

func unauthorizedError() error {
	return apperror.New(apperror.CodeUnauthorized)
}
