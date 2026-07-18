package migrate

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"gorm.io/gorm"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func Apply(ctx context.Context, db *gorm.DB) error {
	if err := db.WithContext(ctx).Exec(`
		CREATE TABLE IF NOT EXISTS mesguard_schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`).Error; err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}

	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		if err := applyFile(ctx, db, entry.Name()); err != nil {
			return err
		}
	}
	return nil
}

func applyFile(ctx context.Context, db *gorm.DB, name string) error {
	version := strings.TrimSuffix(name, ".sql")
	var applied bool
	if err := db.WithContext(ctx).
		Raw("SELECT EXISTS (SELECT 1 FROM mesguard_schema_migrations WHERE version = ?)", version).
		Scan(&applied).Error; err != nil {
		return fmt.Errorf("check migration %s: %w", version, err)
	}
	if applied {
		return nil
	}

	sql, err := migrationFiles.ReadFile("migrations/" + name)
	if err != nil {
		return fmt.Errorf("read migration %s: %w", version, err)
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(string(sql)).Error; err != nil {
			return fmt.Errorf("apply migration %s: %w", version, err)
		}
		if err := tx.Exec("INSERT INTO mesguard_schema_migrations (version) VALUES (?)", version).Error; err != nil {
			return fmt.Errorf("record migration %s: %w", version, err)
		}
		return nil
	})
}
