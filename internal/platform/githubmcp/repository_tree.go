package githubmcp

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

const (
	repositoryTreeToolName       = "get_repository_tree"
	repositoryTreeCandidateLimit = 200
)

var ignoredRepositoryTreeDirectories = map[string]struct{}{
	".git": {}, "bin": {}, "obj": {}, "node_modules": {}, "vendor": {},
	"dist": {}, "build": {}, "coverage": {}, ".next": {},
}

var preferredRepositoryTreeExtensions = map[string]struct{}{
	".bat": {}, ".c": {}, ".cc": {}, ".config": {}, ".cpp": {}, ".cs": {},
	".csproj": {}, ".fs": {}, ".fsproj": {}, ".go": {}, ".graphql": {},
	".gql": {}, ".h": {}, ".hpp": {}, ".java": {}, ".js": {}, ".json": {},
	".jsx": {}, ".kt": {}, ".kts": {}, ".md": {}, ".php": {}, ".proto": {},
	".properties": {}, ".ps1": {}, ".py": {}, ".rs": {}, ".sh": {}, ".sln": {},
	".sql": {}, ".targets": {}, ".toml": {}, ".ts": {}, ".tsx": {}, ".xml": {},
	".yaml": {}, ".yml": {},
}

var preferredRepositoryTreeBasenames = map[string]struct{}{
	"dockerfile": {}, "makefile": {}, "jenkinsfile": {},
}

type repositoryTreeTool struct {
	inner tool.InvokableTool
}

func (t *repositoryTreeTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	if t == nil || t.inner == nil {
		return nil, errors.New("github repository tree tool is nil")
	}
	return t.inner.Info(ctx)
}

func (t *repositoryTreeTool) InvokableRun(ctx context.Context, arguments string, opts ...tool.Option) (string, error) {
	if t == nil || t.inner == nil {
		return "", errors.New("github repository tree tool is nil")
	}
	raw, err := t.inner.InvokableRun(ctx, arguments, opts...)
	if err != nil {
		return raw, err
	}
	filtered, filterErr := filterRepositoryTreeResult(raw)
	if filterErr != nil {
		// A server response that changes shape must remain observable to the
		// caller; candidate filtering is an optimization, not an authorization
		// or availability boundary.
		return raw, nil
	}
	return filtered, nil
}

func wrapRepositoryTreeTool(ctx context.Context, current tool.BaseTool) (tool.BaseTool, error) {
	if current == nil {
		return nil, errors.New("github get_repository_tree tool is nil")
	}
	info, err := current.Info(ctx)
	if err != nil {
		return nil, err
	}
	if info == nil || info.Name != repositoryTreeToolName {
		return current, nil
	}
	invokable, ok := current.(tool.InvokableTool)
	if !ok {
		return nil, errors.New("github get_repository_tree tool is not invokable")
	}
	return &repositoryTreeTool{inner: invokable}, nil
}

type repositoryTreeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size *int   `json:"size,omitempty"`
	SHA  string `json:"sha"`
}

type repositoryTreePayload struct {
	SHA       string                `json:"sha"`
	Truncated bool                  `json:"truncated"`
	Tree      []repositoryTreeEntry `json:"tree"`
	TreeSHA   string                `json:"tree_sha"`
	Owner     string                `json:"owner"`
	Repo      string                `json:"repo"`
	Recursive bool                  `json:"recursive"`
}

type repositoryTreeMCPEnvelope struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

type repositoryTreeCandidateResult struct {
	Status                string                `json:"status"`
	CandidateOnly         bool                  `json:"candidate_only"`
	Owner                 string                `json:"owner"`
	Repo                  string                `json:"repo"`
	TreeSHA               string                `json:"tree_sha"`
	SHA                   string                `json:"sha"`
	Recursive             bool                  `json:"recursive"`
	UpstreamTruncated     bool                  `json:"upstream_truncated"`
	CandidateLimitReached bool                  `json:"candidate_limit_reached"`
	FilteredCount         int                   `json:"filtered_count"`
	CandidateCount        int                   `json:"candidate_count"`
	OmittedCount          int                   `json:"omitted_count"`
	Candidates            []repositoryTreeEntry `json:"candidates"`
}

func filterRepositoryTreeResult(raw string) (string, error) {
	payload, err := decodeRepositoryTreePayload(raw)
	if err != nil {
		return "", err
	}

	preferred := make([]repositoryTreeEntry, 0, len(payload.Tree))
	other := make([]repositoryTreeEntry, 0, len(payload.Tree))
	filteredCount := 0
	for _, entry := range payload.Tree {
		if entry.Type != "blob" || shouldIgnoreRepositoryTreePath(entry.Path) {
			filteredCount++
			continue
		}
		if isPreferredRepositoryTreePath(entry.Path) {
			preferred = append(preferred, entry)
		} else {
			other = append(other, entry)
		}
	}
	sortRepositoryTreeEntries(preferred)
	sortRepositoryTreeEntries(other)
	ordered := append(preferred, other...)
	candidateLimitReached := len(ordered) > repositoryTreeCandidateLimit
	omittedCount := 0
	if candidateLimitReached {
		omittedCount = len(ordered) - repositoryTreeCandidateLimit
		ordered = ordered[:repositoryTreeCandidateLimit]
	}
	result := repositoryTreeCandidateResult{
		Status:                "candidate_paths",
		CandidateOnly:         true,
		Owner:                 payload.Owner,
		Repo:                  payload.Repo,
		TreeSHA:               payload.TreeSHA,
		SHA:                   payload.SHA,
		Recursive:             payload.Recursive,
		UpstreamTruncated:     payload.Truncated,
		CandidateLimitReached: candidateLimitReached,
		FilteredCount:         filteredCount,
		CandidateCount:        len(ordered),
		OmittedCount:          omittedCount,
		Candidates:            ordered,
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func decodeRepositoryTreePayload(raw string) (repositoryTreePayload, error) {
	var direct repositoryTreePayload
	if json.Unmarshal([]byte(raw), &direct) == nil && direct.Tree != nil {
		return direct, nil
	}
	var envelope repositoryTreeMCPEnvelope
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return repositoryTreePayload{}, err
	}
	var merged *repositoryTreePayload
	for _, block := range envelope.Content {
		if block.Type != "text" || strings.TrimSpace(block.Text) == "" {
			continue
		}
		var payload repositoryTreePayload
		if json.Unmarshal([]byte(block.Text), &payload) == nil && payload.Tree != nil {
			if merged == nil {
				merged = &payload
				continue
			}
			merged.Truncated = merged.Truncated || payload.Truncated
			merged.Tree = append(merged.Tree, payload.Tree...)
			if merged.SHA == "" {
				merged.SHA = payload.SHA
			}
			if merged.TreeSHA == "" {
				merged.TreeSHA = payload.TreeSHA
			}
			if merged.Owner == "" {
				merged.Owner = payload.Owner
			}
			if merged.Repo == "" {
				merged.Repo = payload.Repo
			}
			merged.Recursive = merged.Recursive || payload.Recursive
		}
	}
	if merged != nil {
		return *merged, nil
	}
	return repositoryTreePayload{}, errors.New("github repository tree response has unsupported shape")
}

func sortRepositoryTreeEntries(entries []repositoryTreeEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Path < entries[j].Path
	})
}

func shouldIgnoreRepositoryTreePath(value string) bool {
	for _, segment := range strings.Split(path.Clean(strings.ReplaceAll(value, "\\", "/")), "/") {
		if _, ignored := ignoredRepositoryTreeDirectories[strings.ToLower(segment)]; ignored {
			return true
		}
	}
	return false
}

func isPreferredRepositoryTreePath(value string) bool {
	base := strings.ToLower(path.Base(value))
	if _, preferred := preferredRepositoryTreeBasenames[base]; preferred {
		return true
	}
	extension := strings.ToLower(path.Ext(base))
	_, preferred := preferredRepositoryTreeExtensions[extension]
	return preferred
}
