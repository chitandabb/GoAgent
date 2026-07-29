package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/externalcase"

	"github.com/google/uuid"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestExternalCaseRepositoryAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("MESGUARD_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MESGUARD_TEST_POSTGRES_DSN is not configured")
	}
	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repository := NewExternalCaseRepository(db)
	dataSourceID := uuid.New()
	code := "repo-test-" + uuid.NewString()[:8]
	t.Cleanup(func() {
		cleanup := db.WithContext(context.Background())
		_ = cleanup.Where("data_source_id = ?", dataSourceID).Delete(&externalCaseRecord{}).Error
		_ = cleanup.Where("id = ?", dataSourceID).Delete(&dataSourceRecord{}).Error
	})

	if err := repository.EnsureCaseSource(ctx, dataSourceID, code, "Repository Test", "integration"); err != nil {
		t.Fatalf("ensure case source: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	seen := []externalcase.SeenCase{{ExternalCaseKey: "TKT-TEST", ExternalCaseType: "support_ticket"}}
	first, err := repository.RegisterSeen(ctx, dataSourceID, seen, now)
	if err != nil {
		t.Fatalf("register first: %v", err)
	}
	second, err := repository.RegisterSeen(ctx, dataSourceID, seen, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("register second: %v", err)
	}
	if first["TKT-TEST"] != second["TKT-TEST"] {
		t.Fatalf("stable id changed: %s -> %s", first["TKT-TEST"], second["TKT-TEST"])
	}
	reference, err := repository.FindReference(ctx, first["TKT-TEST"])
	if err != nil {
		t.Fatalf("find reference: %v", err)
	}
	if reference.DataSourceID != dataSourceID || reference.ExternalCaseKey != "TKT-TEST" {
		t.Fatalf("reference = %#v", reference)
	}
}
