package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnvAddsMissingValuesWithoutOverridingExistingValues(t *testing.T) {
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("CONFIG_TEST_ONLY=from-dotenv\nCONFIG_TEST_OVERRIDE=from-dotenv\n"), 0600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("change directory: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalDir)
	})

	t.Setenv("CONFIG_TEST_OVERRIDE", "from-environment")
	if err := loadDotEnv(); err != nil {
		t.Fatalf("load .env: %v", err)
	}

	if got := os.Getenv("CONFIG_TEST_ONLY"); got != "from-dotenv" {
		t.Fatalf("missing .env value: got %q", got)
	}
	if got := os.Getenv("CONFIG_TEST_OVERRIDE"); got != "from-environment" {
		t.Fatalf("existing environment variable was overridden: got %q", got)
	}
}
