package auth

import (
	"errors"
	"time"
)

type LoginInput struct {
	Username string
	Password string
}

type LoginResult struct {
	User User
	// 两个原始Token即可,不用存数据库持久化的hash
	SessionToken      string
	CSRFToken         string
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
}

// SessionPolicy
//
//	@Description: 定义两层会话过期策略
type SessionPolicy struct {
	// 空闲过期时长
	idle time.Duration
	// 绝对过期时长
	absolute time.Duration
}

// NewSessionPolicy
//
//	@Description: 构造并检验策略参数
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
	// 约束: idle 必须 > 0, absolute 必须 > 0 , 且 idle <= absolute
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

// DefaultSessionPolicy
//
//	@Description: 默认会话过期策略
func DefaultSessionPolicy() SessionPolicy {
	policy, _ := NewSessionPolicy(2*time.Hour, 12*time.Hour)
	return policy
}

type LoginService struct {
	// 按用户名查用户 ==> 用户仓储
	userRepo UserRepository
	// 写入/撤销 Session ==> Session仓储
	sessionRepo SessionRepository
	// 校验密码 ==> 密码哈希器
	passwordHasher PasswordHasher
	// 生成随机Token ==> Token生成器 具体类型走指针,
	tokenGenerator *TokenGenerator
	// 过期时长规则 ==> 策略对象
	policy SessionPolicy
}

func NewLoginService(
	userRepo UserRepository,
	sessionRepo SessionRepository,
	passwordHasher PasswordHasher,
	tokenGenerator *TokenGenerator,
	policy SessionPolicy,
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
	if tokenGenerator == nil {
		return nil, errors.New("token generator is nil")
	}
	if err := policy.validate(); err != nil {
		return nil, err
	}
	return &LoginService{
		userRepo:       userRepo,
		sessionRepo:    sessionRepo,
		passwordHasher: passwordHasher,
		tokenGenerator: tokenGenerator,
		policy:         policy,
	}, nil
}
