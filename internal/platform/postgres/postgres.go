package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/chitandabb/GoAgent/internal/platform/config"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Open 创建 PostgreSQL 连接，并返回用于优雅关闭连接的函数。
// 数据库密码只从配置指定的环境变量读取，不从 TOML 明文读取。
func Open(ctx context.Context, cfg config.PostgresConfig) (*gorm.DB, func() error, error) {
	password, err := cfg.Password()
	if err != nil {
		return nil, nil, err
	}

	dsn := fmt.Sprintf(
		"host=%s user=%s password=%s dbname=%s port=%d sslmode=%s TimeZone=Asia/Shanghai",
		cfg.Host, cfg.User, password, cfg.Database, cfg.Port, cfg.SSLMode,
	)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Warn)})
	if err != nil {
		return nil, nil, fmt.Errorf("open postgres: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, nil, fmt.Errorf("get postgres sql db: %w", err)
	}
	sqlDB.SetMaxIdleConns(cfg.MaxIdle)
	sqlDB.SetMaxOpenConns(cfg.MaxOpen)
	sqlDB.SetConnMaxLifetime(time.Hour)
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, nil, fmt.Errorf("ping postgres: %w", err)
	}
	return db, sqlDB.Close, nil
}
