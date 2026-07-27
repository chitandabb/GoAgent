// Package migration owns database schema version checks and Goose provider creation.
package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	dbmigrations "github.com/chitandabb/GoAgent/db/migrations"

	"github.com/pressly/goose/v3"
)

// ErrSchemaNotCurrent 表示数据库版本落后或领先于当前代码包含的迁移版本。
var ErrSchemaNotCurrent = errors.New("database schema version is not current")

// NewProvider 使用嵌入二进制的 SQL 文件创建 Goose Provider。
func NewProvider(db *sql.DB) (*goose.Provider, error) {
	provider, err := goose.NewProvider(goose.DialectPostgres, db, dbmigrations.Files)
	if err != nil {
		return nil, fmt.Errorf("create migration provider: %w", err)
	}
	return provider, nil
}

// CheckCurrent 只检查版本，不执行迁移。API 和 Worker 启动时必须使用该函数，
// 真正的 Schema 变更只能由 mesguard-migrate 独立命令完成。
func CheckCurrent(ctx context.Context, db *sql.DB) error {
	provider, err := NewProvider(db)
	if err != nil {
		return err
	}
	sources := provider.ListSources()
	target := sources[len(sources)-1].Version

	// Goose 的迁移操作会创建版本表，但 API 的启动检查必须严格只读。
	var versionTableExists bool
	if err := db.QueryRowContext(
		ctx,
		"SELECT to_regclass($1) IS NOT NULL",
		goose.DefaultTablename,
	).Scan(&versionTableExists); err != nil {
		return fmt.Errorf("check migration version table: %w", err)
	}
	if !versionTableExists {
		return schemaVersionError(0, target)
	}

	current, target, err := provider.GetVersions(ctx)
	if err != nil {
		return fmt.Errorf("read migration versions: %w", err)
	}
	hasPending, err := provider.HasPending(ctx)
	if err != nil {
		return fmt.Errorf("check pending migrations: %w", err)
	}
	if current != target || hasPending {
		return schemaVersionError(current, target)
	}
	return nil
}

func schemaVersionError(current, target int64) error {
	return fmt.Errorf(
		"%w: current=%d target=%d; run mesguard-migrate up",
		ErrSchemaNotCurrent,
		current,
		target,
	)
}
