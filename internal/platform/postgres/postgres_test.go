package postgres

import (
	"net/url"
	"testing"

	"github.com/chitandabb/GoAgent/internal/platform/config"
)

func TestConnectionStringEscapesCredentials(t *testing.T) {
	t.Setenv("TEST_POSTGRES_PASSWORD", "p@ss word:/?#[]")
	cfg := config.PostgresConfig{
		Host:        "127.0.0.1",
		Port:        5432,
		User:        "mesguard-user",
		Database:    "mesguard",
		PasswordEnv: "TEST_POSTGRES_PASSWORD",
		SSLMode:     "disable",
	}

	dsn, err := ConnectionString(cfg)
	if err != nil {
		t.Fatalf("ConnectionString() error = %v", err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	password, ok := parsed.User.Password()
	if !ok || password != "p@ss word:/?#[]" {
		t.Fatal("password was not preserved after URI escaping")
	}
	if got := parsed.Query().Get("sslmode"); got != "disable" {
		t.Fatalf("sslmode = %q, want disable", got)
	}
	if got := parsed.Query().Get("TimeZone"); got != "" {
		t.Fatalf("TimeZone = %q, want application-managed timezone", got)
	}
}
