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

func TestStepFunQueryRewritePreservesProtectedSignals(t *testing.T) {
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
	if _, err := cfg.Models.Chat.APIKey(); err != nil {
		t.Skip("StepFun API key is not configured")
	}
	rewriteConfig := cfg.Knowledge.Retrieval.QueryRewrite
	rewriteConfig.Enabled = true
	prompt, err := rewriteConfig.LoadPrompt()
	if err != nil {
		t.Fatalf("load query rewrite prompt: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	chatModel, err := chatmodel.NewStepFun(ctx, cfg.Models.Chat)
	if err != nil {
		t.Fatalf("build StepFun chat model: %v", err)
	}
	rewriter, err := queryrewrite.New(
		chatModel, prompt, rewriteConfig.PromptVersion,
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
