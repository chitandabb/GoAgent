package githubmcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/platform/config"
)

func TestArgumentRewriterScopesGitHubTools(t *testing.T) {
	cfg := config.GitHubMCPConfig{Owner: "chitandabb", Repository: "GoAgent", Ref: "fixed-ref"}
	rewrite := NewArgumentRewriter(cfg)

	t.Run("search appends repository and clamps output", func(t *testing.T) {
		raw, err := rewrite(context.Background(), "search_code", `{"query":"LoginService","perPage":100}`)
		if err != nil {
			t.Fatalf("rewrite: %v", err)
		}
		var got map[string]any
		if err = json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got["query"] != "LoginService repo:chitandabb/GoAgent" {
			t.Fatalf("query = %v", got["query"])
		}
		if got["perPage"] != float64(20) {
			t.Fatalf("perPage = %v", got["perPage"])
		}
		if _, exists := got["owner"]; exists {
			t.Fatalf("search_code received unsupported owner field: %v", got)
		}
	})

	t.Run("search rejects another repository", func(t *testing.T) {
		_, err := rewrite(context.Background(), "search_code", `{"query":"password repo:other/private"}`)
		if err == nil {
			t.Fatal("rewrite accepted another repository")
		}
	})

	t.Run("file read forces configured repository and ref", func(t *testing.T) {
		raw, err := rewrite(context.Background(), "get_file_contents", `{"owner":"other","repo":"other","path":"internal/auth/login.go","ref":"main"}`)
		if err != nil {
			t.Fatalf("rewrite: %v", err)
		}
		if !strings.Contains(raw, `"owner":"chitandabb"`) || !strings.Contains(raw, `"ref":"fixed-ref"`) {
			t.Fatalf("unexpected rewritten arguments: %s", raw)
		}
	})

	t.Run("file read rejects path escape", func(t *testing.T) {
		_, err := rewrite(context.Background(), "get_file_contents", `{"path":"../secret.txt"}`)
		if err == nil {
			t.Fatal("rewrite accepted path escape")
		}
	})

	t.Run("commit detail rejects non commit id", func(t *testing.T) {
		_, err := rewrite(context.Background(), "get_commit", `{"sha":"main"}`)
		if err == nil {
			t.Fatal("rewrite accepted branch as commit evidence")
		}
	})
}
