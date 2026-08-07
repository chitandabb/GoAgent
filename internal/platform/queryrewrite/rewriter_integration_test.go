//go:build integration

package queryrewrite_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/platform/chatmodel"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/platform/queryrewrite"
)

func TestConfiguredQueryRewriteProfilePreservesProtectedSignals(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate integration test source")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", ".."))
	previousDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repositoryRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousDirectory) })

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	rewriteConfig := cfg.Knowledge.Retrieval.QueryRewrite
	rewriteConfig.Enabled = true
	profile, err := cfg.Models.Chat.Profile(rewriteConfig.ModelProfile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := profile.APIKey(); err != nil {
		t.Skip("query rewrite profile API key is not configured")
	}
	prompt, err := rewriteConfig.LoadPrompt()
	if err != nil {
		t.Fatalf("load query rewrite prompt: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	instance, err := chatmodel.NewProfile(ctx, cfg.Models.Chat, rewriteConfig.ModelProfile)
	if err != nil {
		t.Fatalf("build query rewrite chat model: %v", err)
	}
	rewriter, err := queryrewrite.New(
		instance.Model, prompt, rewriteConfig.PromptVersion,
		30*time.Second,
		rewriteConfig.MaxSubqueries, rewriteConfig.MaxOutputRunes,
	)
	if err != nil {
		t.Fatal(err)
	}
	original := "SQL Server 2022 error 258 最近不能建立连接"
	rewrite, err := rewriter.Rewrite(ctx, original)
	if err != nil {
		t.Fatalf("rewrite query: %v", err)
	}
	plan, err := knowledge.BuildQueryPlan(original, rewrite, rewriteConfig.MaxSubqueries)
	if err != nil {
		t.Fatalf("validate query plan: %v", err)
	}
	if !plan.RewriteAttempted || plan.OriginalQuery != original {
		t.Fatalf("query plan metadata is invalid")
	}
}
