package postgres

import (
	"os"
	"testing"
)

func TestInitPostgresMigratesLegacyPersistenceTables(t *testing.T) {
	if testing.Short() {
		t.Skip("requires the local PostgreSQL development service")
	}

	t.Setenv("CONFIG_FILE", "../../config/config.toml")
	if os.Getenv("MESGUARD_POSTGRES_PASSWORD") == "" {
		t.Setenv("MESGUARD_POSTGRES_PASSWORD", "mesguard_dev_password")
	}

	if err := InitPostgres(); err != nil {
		t.Fatalf("initialize PostgreSQL: %v", err)
	}

	for _, table := range []string{"users", "sessions", "messages"} {
		if !DB.Migrator().HasTable(table) {
			t.Fatalf("expected migrated table %q", table)
		}
	}
}
