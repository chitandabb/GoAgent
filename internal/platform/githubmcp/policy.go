package githubmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/chitandabb/GoAgent/internal/platform/config"
)

var (
	repositoryQualifier = regexp.MustCompile(`(?i)(?:^|\s)repo:([^\s]+)`)
	globalQualifier     = regexp.MustCompile(`(?i)(?:^|\s)(org|user):[^\s]+`)
	commitSHA           = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)
)

// NewArgumentRewriter 返回 Eino ToolsNode 可直接使用的参数策略。
// 模型即使生成了其他 owner/repo/ref，也会在真正发往 GitHub 前被覆盖或拒绝。
func NewArgumentRewriter(cfg config.GitHubMCPConfig) func(context.Context, string, string) (string, error) {
	return func(_ context.Context, toolName, raw string) (string, error) {
		arguments := make(map[string]any)
		if err := json.Unmarshal([]byte(raw), &arguments); err != nil {
			return "", fmt.Errorf("github MCP tool arguments are not valid JSON: %w", err)
		}
		switch toolName {
		case "search_code":
			// search_code 的官方 schema 没有 owner/repo 字段，仓库范围只能写入 query qualifier。
			delete(arguments, "owner")
			delete(arguments, "repo")
			if err := rewriteCodeSearch(arguments, cfg); err != nil {
				return "", err
			}
		case "get_file_contents":
			forceRepository(arguments, cfg)
			if err := rewriteFileRead(arguments, cfg); err != nil {
				return "", err
			}
		case "list_commits":
			forceRepository(arguments, cfg)
			arguments["sha"] = cfg.Ref
			clampPerPage(arguments, 20)
			arguments["fields"] = []string{"sha", "html_url", "commit"}
		case "get_commit":
			forceRepository(arguments, cfg)
			sha, _ := arguments["sha"].(string)
			if !commitSHA.MatchString(strings.TrimSpace(sha)) {
				return "", errors.New("get_commit sha must be a 7 to 40 character hexadecimal commit id")
			}
			arguments["detail"] = "stats"
			clampPerPage(arguments, 20)
		default:
			return "", fmt.Errorf("github MCP tool %q is outside the application whitelist", toolName)
		}
		rewritten, err := json.Marshal(arguments)
		if err != nil {
			return "", fmt.Errorf("marshal github MCP arguments: %w", err)
		}
		return string(rewritten), nil
	}
}

func forceRepository(arguments map[string]any, cfg config.GitHubMCPConfig) {
	arguments["owner"] = cfg.Owner
	arguments["repo"] = cfg.Repository
}

func rewriteCodeSearch(arguments map[string]any, cfg config.GitHubMCPConfig) error {
	query, _ := arguments["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" || strings.ContainsAny(query, "\r\n") {
		return errors.New("search_code query is required and must be a single line")
	}
	if globalQualifier.MatchString(query) {
		return errors.New("search_code cannot use org or user qualifiers")
	}
	expectedRepository := strings.ToLower(cfg.Owner + "/" + cfg.Repository)
	matches := repositoryQualifier.FindAllStringSubmatch(query, -1)
	for _, match := range matches {
		if len(match) != 2 || strings.ToLower(match[1]) != expectedRepository {
			return errors.New("search_code query targets a repository outside the configured scope")
		}
	}
	if len(matches) == 0 {
		query += " repo:" + cfg.Owner + "/" + cfg.Repository
	}
	if len(query) > 256 {
		return errors.New("search_code query exceeds GitHub's 256 character limit after repository scoping")
	}
	arguments["query"] = query
	clampPerPage(arguments, 20)
	arguments["fields"] = []string{"path", "sha", "html_url", "text_matches"}
	return nil
}

func rewriteFileRead(arguments map[string]any, cfg config.GitHubMCPConfig) error {
	rawPath, _ := arguments["path"].(string)
	rawPath = strings.TrimSpace(strings.ReplaceAll(rawPath, "\\", "/"))
	if rawPath == "" || strings.HasPrefix(rawPath, "/") {
		return errors.New("get_file_contents path must be a repository-relative file path")
	}
	cleaned := path.Clean(rawPath)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return errors.New("get_file_contents path escapes the configured repository")
	}
	arguments["path"] = cleaned
	arguments["ref"] = cfg.Ref
	delete(arguments, "sha")
	delete(arguments, "fields")
	return nil
}

func clampPerPage(arguments map[string]any, maximum int) {
	value, ok := arguments["perPage"].(float64)
	if !ok || value < 1 || value > float64(maximum) {
		arguments["perPage"] = maximum
	}
}
