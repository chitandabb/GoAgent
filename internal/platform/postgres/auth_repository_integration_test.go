package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/repository"

	"github.com/google/uuid"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestAuthRepositoriesAgainstPostgres 验证认证仓储与真实表约束的协作。
// 默认跳过；本地或 CI 设置 MESGUARD_TEST_POSTGRES_DSN 后执行。
func TestAuthRepositoriesAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("MESGUARD_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MESGUARD_TEST_POSTGRES_DSN is not configured")
	}

	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test postgres: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	defer func() { _ = sqlDB.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	userRepository := NewUserRepository(db)
	sessionRepository := NewSessionRepository(db)
	now := time.Now().UTC().Truncate(time.Microsecond)
	user := &auth.User{
		ID:                 uuid.New(),
		Username:           "repo_test_" + uuid.NewString()[:8],
		DisplayName:        "Repository Test",
		PasswordHash:       "test-hash",
		MustChangePassword: true,
		Role:               auth.RoleAnalyst,
		Status:             auth.UserStatusActive,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	defer func() {
		_ = db.WithContext(context.Background()).Where("user_id = ?", user.ID).Delete(&sessionRecord{}).Error
		_ = db.WithContext(context.Background()).Where("id = ?", user.ID).Delete(&userRecord{}).Error
	}()

	if err := userRepository.Create(ctx, user); err != nil {
		t.Fatalf("Create(user): %v", err)
	}
	foundUser, err := userRepository.FindByNormalizedUsername(ctx, user.Username)
	if err != nil {
		t.Fatalf("FindByNormalizedUsername(): %v", err)
	}
	if foundUser.ID != user.ID || foundUser.PasswordHash != user.PasswordHash {
		t.Fatalf("found user = %+v, want persisted user", foundUser)
	}
	foundByID, err := userRepository.FindByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("FindByID(): %v", err)
	}
	if foundByID.Username != user.Username {
		t.Fatalf("FindByID() username = %q, want %q", foundByID.Username, user.Username)
	}

	duplicate := *user
	duplicate.ID = uuid.New()
	if err := userRepository.Create(ctx, &duplicate); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("Create(duplicate) error = %v, want repository.ErrConflict", err)
	}

	session := &auth.Session{
		ID:                uuid.New(),
		UserID:            user.ID,
		TokenHash:         []byte("session-token-hash"),
		CSRFTokenHash:     []byte("csrf-token-hash"),
		IdleExpiresAt:     now.Add(time.Hour),
		AbsoluteExpiresAt: now.Add(12 * time.Hour),
		LastSeenAt:        now,
		CreatedAt:         now,
	}
	if err := sessionRepository.Create(ctx, session); err != nil {
		t.Fatalf("Create(session): %v", err)
	}
	foundSession, err := sessionRepository.FindActiveByTokenHash(ctx, session.TokenHash, now)
	if err != nil {
		t.Fatalf("FindActiveByTokenHash(): %v", err)
	}
	if foundSession.ID != session.ID || foundSession.UserID != user.ID {
		t.Fatalf("found session = %+v, want persisted session", foundSession)
	}
	refreshedAt := now.Add(6 * time.Minute)
	refreshedIdleExpiry := now.Add(2 * time.Hour)
	if err := sessionRepository.RefreshActivity(
		ctx,
		session.ID,
		refreshedAt.Add(-5*time.Minute),
		refreshedAt,
		refreshedIdleExpiry,
	); err != nil {
		t.Fatalf("RefreshActivity(): %v", err)
	}
	foundSession, err = sessionRepository.FindActiveByTokenHash(ctx, session.TokenHash, refreshedAt)
	if err != nil {
		t.Fatalf("FindActiveByTokenHash(after refresh): %v", err)
	}
	if !foundSession.LastSeenAt.Equal(refreshedAt) || !foundSession.IdleExpiresAt.Equal(refreshedIdleExpiry) {
		t.Fatalf("refreshed times = %v/%v, want %v/%v", foundSession.LastSeenAt, foundSession.IdleExpiresAt, refreshedAt, refreshedIdleExpiry)
	}

	if err := sessionRepository.Revoke(ctx, session.ID, now.Add(time.Minute)); err != nil {
		t.Fatalf("Revoke(): %v", err)
	}
	if err := sessionRepository.Revoke(ctx, session.ID, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("Revoke() should be idempotent: %v", err)
	}
	if _, err := sessionRepository.FindActiveByTokenHash(ctx, session.TokenHash, now); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("FindActiveByTokenHash(revoked) error = %v, want repository.ErrNotFound", err)
	}

	expiredSession := &auth.Session{
		ID:                uuid.New(),
		UserID:            user.ID,
		TokenHash:         []byte("expired-session-token-hash"),
		CSRFTokenHash:     []byte("expired-csrf-token-hash"),
		IdleExpiresAt:     now.Add(-time.Second),
		AbsoluteExpiresAt: now.Add(time.Hour),
		LastSeenAt:        now,
		CreatedAt:         now,
	}
	if err := sessionRepository.Create(ctx, expiredSession); err != nil {
		t.Fatalf("Create(expired session): %v", err)
	}
	if _, err := sessionRepository.FindActiveByTokenHash(ctx, expiredSession.TokenHash, now); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("FindActiveByTokenHash(expired) error = %v, want repository.ErrNotFound", err)
	}

	activeSession := &auth.Session{
		ID:                uuid.New(),
		UserID:            user.ID,
		TokenHash:         []byte("active-session-token-hash"),
		CSRFTokenHash:     []byte("active-csrf-token-hash"),
		IdleExpiresAt:     now.Add(time.Hour),
		AbsoluteExpiresAt: now.Add(12 * time.Hour),
		LastSeenAt:        now,
		CreatedAt:         now,
	}
	if err := sessionRepository.Create(ctx, activeSession); err != nil {
		t.Fatalf("Create(active session): %v", err)
	}
	if err := sessionRepository.RevokeAllByUserID(ctx, user.ID, now.Add(time.Minute)); err != nil {
		t.Fatalf("RevokeAllByUserID(): %v", err)
	}
	if _, err := sessionRepository.FindActiveByTokenHash(ctx, activeSession.TokenHash, now); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("FindActiveByTokenHash(after revoke all) error = %v, want repository.ErrNotFound", err)
	}
}
