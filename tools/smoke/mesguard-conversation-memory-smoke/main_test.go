package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversationmemory"
	modelopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/google/uuid"
)

func TestParseOptionsRequiresExplicitProviderExecution(t *testing.T) {
	opts, err := parseOptions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts.executeProvider || opts.timeout != defaultTimeout {
		t.Fatalf("options = %+v", opts)
	}
	execute, err := parseOptions([]string{"-execute-provider", "-timeout", "45s"})
	if err != nil {
		t.Fatal(err)
	}
	if !execute.executeProvider || execute.timeout != 45*time.Second {
		t.Fatalf("execute options = %+v", execute)
	}
}

func TestProbeInputStaysBounded(t *testing.T) {
	messages := probeMessages(uuid.New())
	if len(messages) != 2 || messages[0].Seq != 1 || messages[1].Seq != 2 {
		t.Fatalf("messages = %+v", messages)
	}
	if _, err := estimateInputTokens("bounded prompt", messages); err != nil {
		t.Fatal(err)
	}
}

func TestParseOptionsRejectsUnsafeTimeout(t *testing.T) {
	for _, args := range [][]string{{"-timeout", "0s"}, {"-timeout", "3m"}, {"extra"}} {
		if _, err := parseOptions(args); err == nil {
			t.Fatalf("parseOptions(%v) succeeded", args)
		}
	}
}

func TestSmokeFailureCodeIsStableAndContentFree(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "provider bad request", err: fmt.Errorf("wrapped: %w", &modelopenai.APIError{HTTPStatusCode: 400, Message: "sensitive provider detail"}), want: "provider_http_400"},
		{name: "provider timeout", err: fmt.Errorf("wrapped: %w", context.DeadlineExceeded), want: "provider_timeout"},
		{name: "entry id", err: &conversationmemory.EntryValidationError{Code: "entry_id"}, want: "entry_entry_id"},
		{name: "entry status", err: conversationmemory.ErrInvalidEntryStatus, want: "entry_status"},
		{name: "unknown", err: errors.New("sensitive unknown detail"), want: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := smokeFailureCode(tt.err); got != tt.want {
				t.Fatalf("smokeFailureCode() = %q, want %q", got, tt.want)
			}
		})
	}
}
