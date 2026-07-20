package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/platform/config"

	"go.uber.org/zap"
)

func TestNewWritesStructuredFileLog(t *testing.T) {
	outputDir := t.TempDir()
	log, closeLog, err := New(config.LogConfig{
		Level:       "info",
		Format:      "json",
		Environment: "test",
		EnableFile:  true,
		OutputDir:   outputDir,
		MaxSize:     1,
		MaxAge:      1,
		MaxBackups:  1,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	log.Info("logger ready", zap.String("component", "test"))
	if err := closeLog(); err != nil {
		t.Fatalf("close logger: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(outputDir, "mesguard.log"))
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	text := string(content)
	for _, expected := range []string{
		`"msg":"logger ready"`,
		`"service":"mesguard-api"`,
		`"environment":"test"`,
		`"component":"test"`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("log = %s, want field %s", text, expected)
		}
	}
}

func TestNewRejectsUnsupportedFormat(t *testing.T) {
	_, _, err := New(config.LogConfig{Level: "info", Format: "xml"})
	if err == nil {
		t.Fatal("New() error = nil, want unsupported format error")
	}
}
