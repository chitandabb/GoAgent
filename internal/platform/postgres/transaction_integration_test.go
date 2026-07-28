package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestTxManagerAgainstPostgres 使用真实 PostgreSQL 验证提交、回滚和嵌套复用。
// 默认跳过；本地或 CI 设置 MESGUARD_TEST_POSTGRES_DSN 后执行。
func TestTxManagerAgainstPostgres(t *testing.T) {
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
	table := "tx_manager_probe_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := db.WithContext(ctx).Exec("CREATE TABLE " + table + " (value TEXT PRIMARY KEY)").Error; err != nil {
		t.Fatalf("create probe table: %v", err)
	}
	defer func() { _ = db.WithContext(context.Background()).Exec("DROP TABLE IF EXISTS " + table).Error }()

	manager := NewTxManager(db)
	t.Run("commit", func(t *testing.T) {
		err := manager.WithinTx(ctx, func(txCtx context.Context) error {
			return ResolveDB(txCtx, db).Exec("INSERT INTO "+table+" (value) VALUES (?)", "committed").Error
		})
		if err != nil {
			t.Fatalf("WithinTx() commit: %v", err)
		}
	})

	t.Run("rollback", func(t *testing.T) {
		wantErr := errors.New("force rollback")
		err := manager.WithinTx(ctx, func(txCtx context.Context) error {
			if err := ResolveDB(txCtx, db).Exec("INSERT INTO "+table+" (value) VALUES (?)", "rolled-back").Error; err != nil {
				return err
			}
			return wantErr
		})
		if !errors.Is(err, wantErr) {
			t.Fatalf("WithinTx() error = %v, want %v", err, wantErr)
		}
	})

	t.Run("nested joins outer transaction", func(t *testing.T) {
		err := manager.WithinTx(ctx, func(outerCtx context.Context) error {
			outerDB := ResolveDB(outerCtx, db)
			return manager.WithinTx(outerCtx, func(innerCtx context.Context) error {
				innerDB := ResolveDB(innerCtx, db)
				if outerDB.Statement.ConnPool != innerDB.Statement.ConnPool {
					return errors.New("nested transaction did not reuse outer connection")
				}
				return innerDB.Exec("INSERT INTO "+table+" (value) VALUES (?)", "nested").Error
			})
		})
		if err != nil {
			t.Fatalf("WithinTx() nested: %v", err)
		}
	})

	var values []string
	if err := db.WithContext(ctx).Table(table).Order("value").Pluck("value", &values).Error; err != nil {
		t.Fatalf("read probe rows: %v", err)
	}
	want := fmt.Sprint([]string{"committed", "nested"})
	if got := fmt.Sprint(values); got != want {
		t.Fatalf("persisted values = %s, want %s", got, want)
	}
}
