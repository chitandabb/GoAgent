package postgres

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/chitandabb/GoAgent/internal/platform/config"

	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// ConnectionString 生成 PostgreSQL URI，并由 net/url 正确转义密码等特殊字符。
// API、迁移命令和后续 Worker 必须复用这里，避免各自拼接出不同的连接参数。
func ConnectionString(cfg config.PostgresConfig) (string, error) {
	password, err := cfg.Password()
	if err != nil {
		return "", err
	}

	dsn := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(cfg.User, password),
		Host:   net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Path:   cfg.Database,
	}
	query := dsn.Query()
	query.Set("sslmode", cfg.SSLMode)
	query.Set("TimeZone", "Asia/Shanghai")
	dsn.RawQuery = query.Encode()
	return dsn.String(), nil
}

// Open 创建 PostgreSQL 连接，并返回用于优雅关闭连接的函数。
// 数据库密码只从配置指定的环境变量读取，不从 TOML 明文读取。
func Open(ctx context.Context, cfg config.PostgresConfig, log *zap.Logger) (*gorm.DB, func() error, error) {
	dsn, err := ConnectionString(cfg)
	if err != nil {
		return nil, nil, err
	}
	// GORM 仍使用自己的 Logger 接口，这里把它桥接到项目统一的 Zap Logger。
	gormWriter, err := zap.NewStdLogAt(log.Named("gorm"), zap.WarnLevel)
	if err != nil {
		return nil, nil, fmt.Errorf("build gorm logger: %w", err)
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: gormlogger.New(gormWriter, gormlogger.Config{
			SlowThreshold:             500 * time.Millisecond,
			LogLevel:                  gormlogger.Warn,
			IgnoreRecordNotFoundError: true,
			ParameterizedQueries:      true,
			Colorful:                  false,
		}),
	})
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
