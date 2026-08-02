package githubmcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestArgumentRewriterKeepsGitHubTokenRepositoryScope(t *testing.T) {
	rewrite := NewArgumentRewriter()

	t.Run("search keeps repository qualifier and clamps output", func(t *testing.T) {
		raw, err := rewrite(context.Background(), "search_code", `{"query":"LoginService repo:other/private","perPage":100}`)
		if err != nil {
			t.Fatalf("rewrite: %v", err)
		}
		var got map[string]any
		if err = json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got["query"] != "LoginService repo:other/private" {
			t.Fatalf("query = %v", got["query"])
		}
		if got["perPage"] != float64(20) {
			t.Fatalf("perPage = %v", got["perPage"])
		}
		if _, exists := got["owner"]; exists {
			t.Fatalf("search_code received unsupported owner field: %v", got)
		}
		fields := got["fields"].([]any)
		if !containsAny(fields, "repository") {
			t.Fatalf("search_code fields do not preserve repository provenance: %v", fields)
		}
	})

	t.Run("search removes unsupported repository fields", func(t *testing.T) {
		raw, err := rewrite(context.Background(), "search_code", `{"query":"LoginService","owner":"other","repo":"private","ref":"feature/csharp"}`)
		if err != nil {
			t.Fatalf("rewrite: %v", err)
		}
		if strings.Contains(raw, `"owner"`) || strings.Contains(raw, `"repo"`) || strings.Contains(raw, `"ref"`) {
			t.Fatalf("search_code retained unsupported repository fields: %s", raw)
		}
	})

	t.Run("repository discovery stays read only", func(t *testing.T) {
		raw, err := rewrite(context.Background(), "search_repositories", `{"query":"language:csharp","perPage":100,"owner":"ignored"}`)
		if err != nil {
			t.Fatalf("rewrite: %v", err)
		}
		var got map[string]any
		if err = json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got["minimal_output"] != true || got["perPage"] != float64(20) || got["owner"] != nil {
			t.Fatalf("unexpected repository search arguments: %v", got)
		}
	})

	t.Run("repository tree preserves revision and narrows path prefix", func(t *testing.T) {
		raw, err := rewrite(context.Background(), "get_repository_tree", `{"owner":"other","repo":"private","tree_sha":"feature/csharp","path_filter":"src\\MesGuard\\","recursive":true,"fields":["url"]}`)
		if err != nil {
			t.Fatalf("rewrite: %v", err)
		}
		var got map[string]any
		if err = json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got["owner"] != "other" || got["repo"] != "private" || got["tree_sha"] != "feature/csharp" || got["path_filter"] != "src/MesGuard/" || got["recursive"] != true {
			t.Fatalf("unexpected repository tree arguments: %v", got)
		}
		if _, exists := got["fields"]; exists {
			t.Fatalf("get_repository_tree retained unsupported fields: %v", got)
		}
	})

	t.Run("repository tree defaults recursive to false", func(t *testing.T) {
		raw, err := rewrite(context.Background(), "get_repository_tree", `{"owner":"other","repo":"private"}`)
		if err != nil {
			t.Fatalf("rewrite: %v", err)
		}
		var got map[string]any
		if err = json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got["recursive"] != false {
			t.Fatalf("recursive = %v, want false", got["recursive"])
		}
	})

	t.Run("repository tree rejects path escape and alternate revision fields", func(t *testing.T) {
		for _, raw := range []string{
			`{"owner":"other","repo":"private","path_filter":"../secret"}`,
			`{"owner":"other","repo":"private","ref":"feature/csharp"}`,
			`{"owner":"other","repo":"private","sha":"19a91acd6edcd47f35dc9278b3cf886fb09e3fb3"}`,
			`{"owner":"other","repo":"private","recursive":"true"}`,
		} {
			if _, err := rewrite(context.Background(), "get_repository_tree", raw); err == nil {
				t.Fatalf("rewrite accepted unsafe repository tree arguments: %s", raw)
			}
		}
	})

	t.Run("file read preserves selected repository and ref", func(t *testing.T) {
		raw, err := rewrite(context.Background(), "get_file_contents", `{"owner":"other","repo":"private","path":"internal/auth/login.go","ref":"feature/csharp"}`)
		if err != nil {
			t.Fatalf("rewrite: %v", err)
		}
		if !strings.Contains(raw, `"owner":"other"`) || !strings.Contains(raw, `"repo":"private"`) || !strings.Contains(raw, `"ref":"feature/csharp"`) {
			t.Fatalf("unexpected rewritten arguments: %s", raw)
		}
	})

	t.Run("file read rejects path escape", func(t *testing.T) {
		_, err := rewrite(context.Background(), "get_file_contents", `{"owner":"other","repo":"private","path":"../secret.txt"}`)
		if err == nil {
			t.Fatal("rewrite accepted path escape")
		}
	})

	t.Run("file read rejects ambiguous revisions and oversized paths", func(t *testing.T) {
		for _, raw := range []string{
			`{"owner":"other","repo":"private","path":"src/file.cs","sha":"main","ref":"feature/csharp"}`,
			`{"owner":"other","repo":"private","path":"` + strings.Repeat("a", 513) + `.cs"}`,
		} {
			if _, err := rewrite(context.Background(), "get_file_contents", raw); err == nil {
				t.Fatalf("rewrite accepted ambiguous or oversized file read: %s", raw)
			}
		}
	})

	t.Run("file read requires selected repository", func(t *testing.T) {
		_, err := rewrite(context.Background(), "get_file_contents", `{"path":"internal/auth/login.go"}`)
		if err == nil {
			t.Fatal("rewrite accepted missing repository")
		}
	})

	t.Run("file read rejects non-string ref", func(t *testing.T) {
		_, err := rewrite(context.Background(), "get_file_contents", `{"owner":"other","repo":"private","path":"internal/auth/login.go","ref":123}`)
		if err == nil {
			t.Fatal("rewrite accepted non-string ref")
		}
	})

	t.Run("commit history does not inject a branch", func(t *testing.T) {
		raw, err := rewrite(context.Background(), "list_commits", `{"owner":"other","repo":"private"}`)
		if err != nil {
			t.Fatalf("rewrite: %v", err)
		}
		var got map[string]any
		if err = json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, exists := got["sha"]; exists {
			t.Fatalf("list_commits unexpectedly fixed a sha: %s", raw)
		}
	})

	t.Run("commit history preserves selected branch", func(t *testing.T) {
		raw, err := rewrite(context.Background(), "list_commits", `{"owner":"other","repo":"private","sha":"feature/csharp","path":"internal"}`)
		if err != nil {
			t.Fatalf("rewrite: %v", err)
		}
		if !strings.Contains(raw, `"sha":"feature/csharp"`) || !strings.Contains(raw, `"path":"internal"`) {
			t.Fatalf("selected commit history scope was not preserved: %s", raw)
		}
	})

	t.Run("commit history rejects oversized path", func(t *testing.T) {
		_, err := rewrite(context.Background(), "list_commits", `{"owner":"other","repo":"private","path":"`+strings.Repeat("a", 513)+`"}`)
		if err == nil {
			t.Fatal("rewrite accepted oversized commit history path")
		}
	})

	t.Run("commit detail rejects non commit id", func(t *testing.T) {
		_, err := rewrite(context.Background(), "get_commit", `{"owner":"other","repo":"private","sha":"main"}`)
		if err == nil {
			t.Fatal("rewrite accepted branch as commit evidence")
		}
	})
}

func containsAny(values []any, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
