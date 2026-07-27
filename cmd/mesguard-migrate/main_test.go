package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestRunRejectsInvalidArgumentCount(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		// 无参数
		{name: "no args", args: nil},
		// 参数过多
		{name: "too many args", args: []string{"up", "down"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := run(context.Background(), tt.args, io.Discard)
			// 断言1. err不能是nil
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			// 断言2. err.Error() 包含 "usage"
			if !strings.Contains(err.Error(), "usage") {
				t.Fatalf("expected error message to contain 'usage', got %q", err.Error())
			}
		})
	}
}

func TestRunRejectsUnknownCommandBeforeDatabaseAccess(t *testing.T) {
	err := run(context.Background(), []string{"unknown"}, io.Discard)
	if !errors.Is(err, errInvalidCommand) {
		t.Fatalf("run() error = %v, want errInvalidCommand", err)
	}
}
