package githubmcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestFilterRepositoryTreeResultReturnsBoundedSourceCandidates(t *testing.T) {
	payload, err := json.Marshal(repositoryTreePayload{
		Owner: "other", Repo: "private", TreeSHA: "main", SHA: "tree-sha",
		Recursive: true, Tree: []repositoryTreeEntry{
			{Path: "src/TicketSearchService.cs", Type: "blob", SHA: "file-sha"},
			{Path: "obj/generated.cs", Type: "blob", SHA: "ignored-sha"},
			{Path: "src", Type: "tree", SHA: "dir-sha"},
			{Path: "README.md", Type: "blob", SHA: "readme-sha"},
		},
	})
	if err != nil {
		t.Fatalf("marshal upstream tree: %v", err)
	}
	envelope, err := json.Marshal(map[string]any{
		"content": []map[string]string{{"type": "text", "text": string(payload)}},
	})
	if err != nil {
		t.Fatalf("marshal MCP envelope: %v", err)
	}

	filtered, err := filterRepositoryTreeResult(string(envelope))
	if err != nil {
		t.Fatalf("filter tree: %v", err)
	}
	var got repositoryTreeCandidateResult
	if err = json.Unmarshal([]byte(filtered), &got); err != nil {
		t.Fatalf("decode filtered tree: %v", err)
	}
	if got.Status != "candidate_paths" || !got.CandidateOnly || got.Owner != "other" || got.TreeSHA != "main" {
		t.Fatalf("metadata = %+v", got)
	}
	if got.CandidateCount != 2 || got.FilteredCount != 2 || got.OmittedCount != 0 || got.UpstreamTruncated || got.CandidateLimitReached {
		t.Fatalf("candidate bounds = %+v", got)
	}
	if got.Candidates[0].Path != "README.md" || got.Candidates[1].Path != "src/TicketSearchService.cs" {
		t.Fatalf("candidates = %+v", got.Candidates)
	}
}

func TestFilterRepositoryTreeResultMarksCandidateLimitSeparately(t *testing.T) {
	entries := make([]repositoryTreeEntry, repositoryTreeCandidateLimit+1)
	for index := range entries {
		entries[index] = repositoryTreeEntry{Path: "src/file-" + string(rune('a'+index%26)) + ".cs", Type: "blob"}
	}
	payload, err := json.Marshal(repositoryTreePayload{
		Owner: "other", Repo: "private", TreeSHA: "main", Recursive: true, Tree: entries,
	})
	if err != nil {
		t.Fatalf("marshal upstream tree: %v", err)
	}
	envelope, err := json.Marshal(map[string]any{
		"content": []map[string]string{{"type": "text", "text": string(payload)}},
	})
	if err != nil {
		t.Fatalf("marshal MCP envelope: %v", err)
	}

	filtered, err := filterRepositoryTreeResult(string(envelope))
	if err != nil {
		t.Fatalf("filter tree: %v", err)
	}
	var got repositoryTreeCandidateResult
	if err = json.Unmarshal([]byte(filtered), &got); err != nil {
		t.Fatalf("decode filtered tree: %v", err)
	}
	if got.CandidateCount != repositoryTreeCandidateLimit || !got.CandidateLimitReached || got.OmittedCount != 1 || got.FilteredCount != 0 || got.UpstreamTruncated {
		t.Fatalf("candidate limit metadata = %+v", got)
	}
}

func TestDecodeRepositoryTreePayloadMergesTextBlocks(t *testing.T) {
	first, err := json.Marshal(repositoryTreePayload{
		Owner: "other", Repo: "private", TreeSHA: "main", Recursive: true,
		Tree: []repositoryTreeEntry{{Path: "src/Foo.cs", Type: "blob"}},
	})
	if err != nil {
		t.Fatalf("marshal first tree block: %v", err)
	}
	second, err := json.Marshal(repositoryTreePayload{
		Owner: "other", Repo: "private", TreeSHA: "main", Recursive: true,
		Tree: []repositoryTreeEntry{{Path: "src/Bar.cs", Type: "blob"}},
	})
	if err != nil {
		t.Fatalf("marshal second tree block: %v", err)
	}
	envelope, err := json.Marshal(map[string]any{
		"content": []map[string]string{
			{"type": "text", "text": string(first)},
			{"type": "text", "text": string(second)},
		},
	})
	if err != nil {
		t.Fatalf("marshal MCP envelope: %v", err)
	}

	decoded, err := decodeRepositoryTreePayload(string(envelope))
	if err != nil {
		t.Fatalf("decode tree: %v", err)
	}
	if len(decoded.Tree) != 2 || decoded.Tree[0].Path != "src/Foo.cs" || decoded.Tree[1].Path != "src/Bar.cs" {
		t.Fatalf("merged tree = %+v", decoded.Tree)
	}
}

func TestRepositoryTreeToolPreservesUnsupportedResponses(t *testing.T) {
	inner := &repositoryTreeTestTool{result: `{"content":[{"type":"text","text":"upstream error"}]}`}
	wrapped, err := wrapRepositoryTreeTool(context.Background(), inner)
	if err != nil {
		t.Fatalf("wrap tree: %v", err)
	}
	result, err := wrapped.(tool.InvokableTool).InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("invoke tree: %v", err)
	}
	if result != inner.result {
		t.Fatalf("unsupported response changed: %s", result)
	}
}

type repositoryTreeTestTool struct {
	result string
}

func (*repositoryTreeTestTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: repositoryTreeToolName}, nil
}

func (t *repositoryTreeTestTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return t.result, nil
}
