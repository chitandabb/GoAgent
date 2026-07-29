package postgres

import (
	"context"
	"time"

	"github.com/chitandabb/GoAgent/internal/auth"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// UserRepository 是 auth.UserRepository 的 PostgreSQL 适配器。
type UserRepository struct {
	db *gorm.DB
}

var _ auth.UserRepository = (*UserRepository)(nil)

// NewUserRepository 创建用户仓储。调用方不应直接把 GORM DB 传入 Service。
func NewUserRepository(db *gorm.DB) *UserRepository {
	return &UserRepository{db: db}
}

// Create 保存一个新用户。唯一键冲突会被转换为 repository.ErrConflict。
func (r *UserRepository) Create(ctx context.Context, user *auth.User) error {
	if err := ResolveDB(ctx, r.db).Create(userRecordFromDomain(user)).Error; err != nil {
		return TranslateError(err)
	}
	return nil
}

// FindByID 根据主键查询用户，用于从 Session 恢复当前身份。
func (r *UserRepository) FindByID(ctx context.Context, userID uuid.UUID) (*auth.User, error) {
	var record userRecord
	if err := ResolveDB(ctx, r.db).Where("id = ?", userID).Take(&record).Error; err != nil {
		return nil, TranslateError(err)
	}
	return record.toDomain(), nil
}

// FindByNormalizedUsername 根据已规范化的用户名查询用户。
// 调用方负责先调用 auth.NormalizeUsername，避免仓储层悄悄改变查询语义。
func (r *UserRepository) FindByNormalizedUsername(ctx context.Context, username string) (*auth.User, error) {
	var record userRecord
	err := ResolveDB(ctx, r.db).
		Where("username = ?", username).
		Take(&record).Error
	if err != nil {
		return nil, TranslateError(err)
	}
	return record.toDomain(), nil
}

type userRecord struct {
	ID                 uuid.UUID       `gorm:"column:id"`
	Username           string          `gorm:"column:username"`
	DisplayName        string          `gorm:"column:display_name"`
	PasswordHash       string          `gorm:"column:password_hash"`
	MustChangePassword bool            `gorm:"column:must_change_password"`
	PasswordChangedAt  *time.Time      `gorm:"column:password_changed_at"`
	Role               auth.Role       `gorm:"column:role"`
	Status             auth.UserStatus `gorm:"column:status"`
	LastLoginAt        *time.Time      `gorm:"column:last_login_at"`
	CreatedAt          time.Time       `gorm:"column:created_at"`
	UpdatedAt          time.Time       `gorm:"column:updated_at"`
}

func (userRecord) TableName() string {
	return "users"
}

func userRecordFromDomain(user *auth.User) *userRecord {
	return &userRecord{
		ID:                 user.ID,
		Username:           user.Username,
		DisplayName:        user.DisplayName,
		PasswordHash:       user.PasswordHash,
		MustChangePassword: user.MustChangePassword,
		PasswordChangedAt:  user.PasswordChangedAt,
		Role:               user.Role,
		Status:             user.Status,
		LastLoginAt:        user.LastLoginAt,
		CreatedAt:          user.CreatedAt,
		UpdatedAt:          user.UpdatedAt,
	}
}

func (r userRecord) toDomain() *auth.User {
	return &auth.User{
		ID:                 r.ID,
		Username:           r.Username,
		DisplayName:        r.DisplayName,
		PasswordHash:       r.PasswordHash,
		MustChangePassword: r.MustChangePassword,
		PasswordChangedAt:  r.PasswordChangedAt,
		Role:               r.Role,
		Status:             r.Status,
		LastLoginAt:        r.LastLoginAt,
		CreatedAt:          r.CreatedAt,
		UpdatedAt:          r.UpdatedAt,
	}
}

// SessionRepository 是 auth.SessionRepository 的 PostgreSQL 适配器。
type SessionRepository struct {
	db *gorm.DB
}

var _ auth.SessionRepository = (*SessionRepository)(nil)

// NewSessionRepository 创建会话仓储。
func NewSessionRepository(db *gorm.DB) *SessionRepository {
	return &SessionRepository{db: db}
}

// Create 保存新会话。TokenHash 的唯一约束是随机令牌碰撞时的最终防线。
func (r *SessionRepository) Create(ctx context.Context, session *auth.Session) error {
	if err := ResolveDB(ctx, r.db).Create(sessionRecordFromDomain(session)).Error; err != nil {
		return TranslateError(err)
	}
	return nil
}

// FindActiveByTokenHash 只返回未撤销且两个过期时间都尚未到达的会话。
func (r *SessionRepository) FindActiveByTokenHash(ctx context.Context, tokenHash []byte, now time.Time) (*auth.Session, error) {
	var record sessionRecord
	err := ResolveDB(ctx, r.db).
		Where("token_hash = ?", tokenHash).
		Where("revoked_at IS NULL").
		Where("idle_expires_at > ?", now).
		Where("absolute_expires_at > ?", now).
		Take(&record).Error
	if err != nil {
		return nil, TranslateError(err)
	}
	return record.toDomain(), nil
}

// RefreshActivity 延长空闲有效期。条件更新限制同一 Session 最多每个刷新窗口写一次。
func (r *SessionRepository) RefreshActivity(
	ctx context.Context,
	sessionID uuid.UUID,
	lastSeenBefore, lastSeenAt, idleExpiresAt time.Time,
) error {
	err := ResolveDB(ctx, r.db).
		Model(&sessionRecord{}).
		Where("id = ?", sessionID).
		Where("revoked_at IS NULL").
		Where("last_seen_at <= ?", lastSeenBefore).
		Where("absolute_expires_at > ?", lastSeenAt).
		Updates(map[string]any{
			"last_seen_at":    lastSeenAt,
			"idle_expires_at": idleExpiresAt,
		}).Error
	return TranslateError(err)
}

// Revoke 撤销单个会话。已经撤销或不存在时保持幂等，不泄露会话是否存在。
func (r *SessionRepository) Revoke(ctx context.Context, sessionID uuid.UUID, revokedAt time.Time) error {
	err := ResolveDB(ctx, r.db).
		Model(&sessionRecord{}).
		Where("id = ?", sessionID).
		Where("revoked_at IS NULL").
		Update("revoked_at", revokedAt).Error
	return TranslateError(err)
}

// RevokeAllByUserID 用于改密、禁用账号和角色变更后撤销该用户的全部有效会话。
func (r *SessionRepository) RevokeAllByUserID(ctx context.Context, userID uuid.UUID, revokedAt time.Time) error {
	err := ResolveDB(ctx, r.db).
		Model(&sessionRecord{}).
		Where("user_id = ?", userID).
		Where("revoked_at IS NULL").
		Update("revoked_at", revokedAt).Error
	return TranslateError(err)
}

type sessionRecord struct {
	ID                uuid.UUID  `gorm:"column:id"`
	UserID            uuid.UUID  `gorm:"column:user_id"`
	TokenHash         []byte     `gorm:"column:token_hash"`
	CSRFTokenHash     []byte     `gorm:"column:csrf_token_hash"`
	IdleExpiresAt     time.Time  `gorm:"column:idle_expires_at"`
	AbsoluteExpiresAt time.Time  `gorm:"column:absolute_expires_at"`
	RevokedAt         *time.Time `gorm:"column:revoked_at"`
	LastSeenAt        time.Time  `gorm:"column:last_seen_at"`
	CreatedAt         time.Time  `gorm:"column:created_at"`
}

func (sessionRecord) TableName() string {
	return "sessions"
}

func sessionRecordFromDomain(session *auth.Session) *sessionRecord {
	return &sessionRecord{
		ID:                session.ID,
		UserID:            session.UserID,
		TokenHash:         session.TokenHash,
		CSRFTokenHash:     session.CSRFTokenHash,
		IdleExpiresAt:     session.IdleExpiresAt,
		AbsoluteExpiresAt: session.AbsoluteExpiresAt,
		RevokedAt:         session.RevokedAt,
		LastSeenAt:        session.LastSeenAt,
		CreatedAt:         session.CreatedAt,
	}
}

func (r sessionRecord) toDomain() *auth.Session {
	return &auth.Session{
		ID:                r.ID,
		UserID:            r.UserID,
		TokenHash:         r.TokenHash,
		CSRFTokenHash:     r.CSRFTokenHash,
		IdleExpiresAt:     r.IdleExpiresAt,
		AbsoluteExpiresAt: r.AbsoluteExpiresAt,
		RevokedAt:         r.RevokedAt,
		LastSeenAt:        r.LastSeenAt,
		CreatedAt:         r.CreatedAt,
	}
}
