package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/chitandabb/GoAgent/internal/apperror"
	"github.com/chitandabb/GoAgent/internal/repository"

	"github.com/google/uuid"
)

const invalidCredentialsMessage = "用户名或密码错误"

type LoginInput struct {
	Username string
	Password string
}

type LoginResult struct {
	User User
	// 结果只携带原始 Token；Hash 仅用于 Session 持久化。
	SessionToken      string
	CSRFToken         string
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
}

// SessionPolicy 定义空闲过期和绝对过期两层会话策略。
type SessionPolicy struct {
	idle     time.Duration
	absolute time.Duration
}

// NewSessionPolicy 构造并校验会话策略。
func NewSessionPolicy(idle, absolute time.Duration) (SessionPolicy, error) {
	policy := SessionPolicy{
		idle:     idle,
		absolute: absolute,
	}
	if err := policy.validate(); err != nil {
		return SessionPolicy{}, err
	}
	return policy, nil
}

func (sp SessionPolicy) validate() error {
	if sp.idle <= 0 {
		return errors.New("session idle duration must be positive")
	}
	if sp.absolute <= 0 {
		return errors.New("session absolute duration must be positive")
	}
	if sp.idle > sp.absolute {
		return errors.New("session idle duration cannot exceed absolute duration")
	}
	return nil
}

func (sp SessionPolicy) IdleDuration() time.Duration {
	return sp.idle
}

func (sp SessionPolicy) AbsoluteDuration() time.Duration {
	return sp.absolute
}

func (sp SessionPolicy) ExpiresAt(now time.Time) (
	idleExpiresAt,
	absoluteExpiresAt time.Time,
) {
	return now.Add(sp.idle), now.Add(sp.absolute)
}

// DefaultSessionPolicy 返回空闲 2 小时、绝对 12 小时的默认会话策略。
func DefaultSessionPolicy() SessionPolicy {
	policy, _ := NewSessionPolicy(2*time.Hour, 12*time.Hour)
	return policy
}

// Clock 隔离系统时间，使过期时间计算可以被稳定测试。
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time {
	return time.Now()
}

type LoginService struct {
	// 按用户名查用户 ==> 用户仓储
	userRepo UserRepository
	// 写入/撤销 Session ==> Session仓储
	sessionRepo SessionRepository
	// 校验密码 ==> 密码哈希器
	passwordHasher PasswordHasher
	// 生成随机 Token；生产环境使用 TokenGenerator，测试使用固定实现。
	tokenIssuer TokenIssuer
	// 过期时长规则 ==> 策略对象
	policy SessionPolicy
	// 当前时间由依赖提供，避免业务测试依赖真实时钟。
	clock Clock
	// 用户不存在时仍执行一次同成本密码校验，降低用户名枚举的时序差异。
	dummyPasswordHash string
}

func NewLoginService(
	userRepo UserRepository,
	sessionRepo SessionRepository,
	passwordHasher PasswordHasher,
	tokenIssuer TokenIssuer,
	policy SessionPolicy,
) (*LoginService, error) {
	return newLoginService(userRepo, sessionRepo, passwordHasher, tokenIssuer, policy, systemClock{})
}

func newLoginService(
	userRepo UserRepository,
	sessionRepo SessionRepository,
	passwordHasher PasswordHasher,
	tokenIssuer TokenIssuer,
	policy SessionPolicy,
	clock Clock,
) (*LoginService, error) {
	if userRepo == nil {
		return nil, errors.New("user repository is nil")
	}
	if sessionRepo == nil {
		return nil, errors.New("session repository is nil")
	}
	if passwordHasher == nil {
		return nil, errors.New("password hasher is nil")
	}
	if tokenIssuer == nil {
		return nil, errors.New("token issuer is nil")
	}
	if clock == nil {
		return nil, errors.New("clock is nil")
	}
	if err := policy.validate(); err != nil {
		return nil, err
	}
	dummyPasswordHash, err := passwordHasher.Hash("mesguard-dummy-login-password")
	if err != nil {
		return nil, fmt.Errorf("build dummy password hash: %w", err)
	}
	return &LoginService{
		userRepo:          userRepo,
		sessionRepo:       sessionRepo,
		passwordHasher:    passwordHasher,
		tokenIssuer:       tokenIssuer,
		policy:            policy,
		clock:             clock,
		dummyPasswordHash: dummyPasswordHash,
	}, nil
}

// Login 验证本地账号并创建服务端 Session。
func (s *LoginService) Login(ctx context.Context, input LoginInput) (LoginResult, error) {
	// 先把用户名进行标准化
	username := NormalizeUsername(input.Username)
	// 用username去数据库查用户并返回
	user, err := s.userRepo.FindByNormalizedUsername(ctx, username)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			// 即使用户不存在也执行 Argon2id，避免通过响应耗时枚举用户名(安全考量)。
			if _, verifyErr := s.passwordHasher.Verify(input.Password, s.dummyPasswordHash); verifyErr != nil {
				return LoginResult{}, apperror.Wrap(apperror.CodeInternal, verifyErr)
			}
			return LoginResult{}, invalidCredentialsError()
		}
		// 其他错误直接返回
		return LoginResult{}, apperror.Wrap(apperror.CodeInternal, fmt.Errorf("find login user: %w", err))
	}

	// 验证密码哈希
	passwordMatches, err := s.passwordHasher.Verify(input.Password, user.PasswordHash)
	if err != nil {
		return LoginResult{}, apperror.Wrap(apperror.CodeInternal, fmt.Errorf("verify login password: %w", err))
	}
	// 禁用账号与密码错误返回完全相同的信息，避免泄露账号状态。
	if !passwordMatches || !user.CanLogin() {
		return LoginResult{}, invalidCredentialsError()
	}

	// 生成 Session Token
	sessionToken, err := s.tokenIssuer.Generate()
	if err != nil {
		return LoginResult{}, apperror.Wrap(apperror.CodeInternal, fmt.Errorf("generate session token: %w", err))
	}
	// 生成 CSRF Token
	csrfToken, err := s.tokenIssuer.Generate()
	if err != nil {
		return LoginResult{}, apperror.Wrap(apperror.CodeInternal, fmt.Errorf("generate csrf token: %w", err))
	}
	// 生成 Session ID
	sessionID, err := uuid.NewV7()
	if err != nil {
		return LoginResult{}, apperror.Wrap(apperror.CodeInternal, fmt.Errorf("generate session id: %w", err))
	}

	// 创建 Session
	now := s.clock.Now()
	idleExpiresAt, absoluteExpiresAt := s.policy.ExpiresAt(now)
	session := &Session{
		ID:                sessionID,
		UserID:            user.ID,
		TokenHash:         sessionToken.Hash,
		CSRFTokenHash:     csrfToken.Hash,
		IdleExpiresAt:     idleExpiresAt,
		AbsoluteExpiresAt: absoluteExpiresAt,
		LastSeenAt:        now,
		CreatedAt:         now,
	}
	if err := s.sessionRepo.Create(ctx, session); err != nil {
		return LoginResult{}, apperror.Wrap(apperror.CodeInternal, fmt.Errorf("create login session: %w", err))
	}

	return LoginResult{
		User:              *user,
		SessionToken:      sessionToken.Raw,
		CSRFToken:         csrfToken.Raw,
		IdleExpiresAt:     session.IdleExpiresAt,
		AbsoluteExpiresAt: session.AbsoluteExpiresAt,
	}, nil
}

func invalidCredentialsError() error {
	return apperror.NewWithMessage(apperror.CodeUnauthorized, invalidCredentialsMessage)
}
