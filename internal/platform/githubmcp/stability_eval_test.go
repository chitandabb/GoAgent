package githubmcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestCodeSearchStabilityEvaluatorMeasuresFallbackRecovery(t *testing.T) {
	search := &evaluationFakeTool{
		name:   codeSearchToolName,
		result: `{"status":"index_pending","attempts":3,"incomplete_results":true,"github_response":{"content":[{"type":"text","text":"{\"total_count\":0,\"incomplete_results\":true,\"items\":[]}"}]}}`,
	}
	tree := &evaluationFakeTool{
		name:   repositoryTreeToolName,
		result: `{"status":"candidate_paths","candidate_only":true,"tree_sha":"tree-sha","upstream_truncated":false,"candidate_limit_reached":false,"candidate_count":1,"omitted_count":0,"candidates":[{"path":"src/Foo.cs","type":"blob","sha":"file-sha"}]}`,
	}
	file := &evaluationFakeTool{name: fileContentsToolName, result: `{"content":[{"type":"resource","resource":{"text":"public class Foo { }"}}]}`}
	evaluator, err := NewCodeSearchStabilityEvaluator(context.Background(), []tool.BaseTool{search, tree, file})
	if err != nil {
		t.Fatalf("new evaluator: %v", err)
	}
	summary, err := evaluator.Evaluate(context.Background(), []CodeSearchEvaluationCase{{
		DatasetVersion: "github-search-v1", CaseID: "fallback", Owner: "other", Repo: "private",
		Query: "Foo repo:other/private", TreeSHA: "19a91acd6edcd47f35dc9278b3cf886fb09e3fb3",
		PathFilter: "src/", ExpectedPath: "src/Foo.cs", ContentMarker: "class Foo",
	}})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if summary.SearchIncompleteCases != 1 || summary.TreeExpectedPathHits != 1 || summary.FallbackRecoveredCases != 1 || summary.KnownPathVerifiedCases != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.FallbackRecoveryRate != 1 || summary.TreeExpectedPathRecall != 1 || summary.KnownPathFileVerificationRate != 1 {
		t.Fatalf("rates = %+v", summary)
	}
	if !strings.Contains(search.arguments, "Foo repo:other/private") || !strings.Contains(tree.arguments, `"tree_sha":"19a91acd6edcd47f35dc9278b3cf886fb09e3fb3"`) {
		t.Fatalf("rewritten arguments search=%s tree=%s", search.arguments, tree.arguments)
	}
}

func TestCodeSearchStabilityEvaluatorPreservesMultipleStageErrors(t *testing.T) {
	search := &evaluationFakeTool{name: codeSearchToolName, err: errors.New("search unavailable")}
	tree := &evaluationFakeTool{name: repositoryTreeToolName, err: errors.New("tree unavailable")}
	file := &evaluationFakeTool{name: fileContentsToolName, err: errors.New("file unavailable")}
	evaluator, err := NewCodeSearchStabilityEvaluator(context.Background(), []tool.BaseTool{search, tree, file})
	if err != nil {
		t.Fatalf("new evaluator: %v", err)
	}
	summary, err := evaluator.Evaluate(context.Background(), []CodeSearchEvaluationCase{{
		DatasetVersion: "github-search-v1", CaseID: "multiple-errors", Owner: "other", Repo: "private",
		Query: "Foo repo:other/private", TreeSHA: "19a91acd6edcd47f35dc9278b3cf886fb09e3fb3",
		PathFilter: "src/", ExpectedPath: "src/Foo.cs", ContentMarker: "class Foo",
	}})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(summary.Results) != 1 || len(summary.Results[0].ErrorTypes) != 3 || len(summary.Results[0].ErrorMessages) != 3 {
		t.Fatalf("result errors = %+v", summary.Results)
	}
	if !strings.Contains(strings.Join(summary.Results[0].ErrorMessages, "\n"), "search unavailable") ||
		!strings.Contains(strings.Join(summary.Results[0].ErrorMessages, "\n"), "tree unavailable") ||
		!strings.Contains(strings.Join(summary.Results[0].ErrorMessages, "\n"), "file unavailable") {
		t.Fatalf("error messages = %+v", summary.Results[0].ErrorMessages)
	}
	for _, errorType := range []string{"search_error", "tree_error", "file_error"} {
		if summary.FailureTypes[errorType] != 1 {
			t.Fatalf("failure type %q = %d, want 1", errorType, summary.FailureTypes[errorType])
		}
	}
}

func TestCodeSearchStabilityEvaluatorReturnsPartialSummaryOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	search := &evaluationFakeTool{
		name:   codeSearchToolName,
		result: `{"content":[{"type":"text","text":"{\"incomplete_results\":false,\"items\":[],\"total_count\":0}"}]}`,
		cancel: cancel,
	}
	tree := &evaluationFakeTool{name: repositoryTreeToolName, result: `{}`}
	file := &evaluationFakeTool{name: fileContentsToolName, result: `{}`}
	evaluator, err := NewCodeSearchStabilityEvaluator(context.Background(), []tool.BaseTool{search, tree, file})
	if err != nil {
		t.Fatalf("new evaluator: %v", err)
	}
	current := CodeSearchEvaluationCase{
		DatasetVersion: "github-search-v1", CaseID: "cancelled", Owner: "other", Repo: "private",
		Query: "Foo repo:other/private", TreeSHA: "19a91acd6edcd47f35dc9278b3cf886fb09e3fb3",
		PathFilter: "src/", ExpectedPath: "src/Foo.cs", ContentMarker: "class Foo",
	}
	summary, err := evaluator.Evaluate(ctx, []CodeSearchEvaluationCase{current, current})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("evaluate error = %v, want context.Canceled", err)
	}
	if summary.RequestedCases != 2 || summary.Cases != 1 || len(summary.Results) != 1 {
		t.Fatalf("partial summary = %+v", summary)
	}
	if summary.Results[0].ErrorType != "context_canceled" || summary.Results[0].TreeInvoked {
		t.Fatalf("cancelled result = %+v", summary.Results[0])
	}
}

func TestCodeSearchEvaluationCaseRejectsBroadOrMismatchedPath(t *testing.T) {
	for _, current := range []CodeSearchEvaluationCase{
		{DatasetVersion: "v1", CaseID: "empty-filter", Owner: "owner", Repo: "repo", Query: "x", ExpectedPath: "src/Foo.cs", ContentMarker: "class Foo"},
		{DatasetVersion: "v1", CaseID: "outside-filter", Owner: "owner", Repo: "repo", Query: "x", PathFilter: "tests/", ExpectedPath: "src/Foo.cs", ContentMarker: "class Foo"},
	} {
		if err := current.Validate(); err == nil {
			t.Fatalf("case was accepted: %+v", current)
		}
	}
}

type evaluationFakeTool struct {
	name      string
	result    string
	arguments string
	err       error
	cancel    context.CancelFunc
}

func (t *evaluationFakeTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name}, nil
}

func (t *evaluationFakeTool) InvokableRun(_ context.Context, arguments string, _ ...tool.Option) (string, error) {
	t.arguments = arguments
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
	return t.result, t.err
}
