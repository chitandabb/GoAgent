package githubmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

var (
	githubRepositoryPart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)
	githubRef            = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
	commitSHA            = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)
)

// NewArgumentRewriter 返回 Eino ToolsNode 可直接使用的参数策略。
// 仓库和分支范围由 GitHub Token/App 决定；这里仅校验 GitHub 参数形状、
// 路径边界和只读结果规模，不把调用重写到某个应用配置仓库。
func NewArgumentRewriter() func(context.Context, string, string) (string, error) {
	return func(_ context.Context, toolName, raw string) (string, error) {
		arguments := make(map[string]any)
		if err := json.Unmarshal([]byte(raw), &arguments); err != nil {
			return "", fmt.Errorf("github MCP tool arguments are not valid JSON: %w", err)
		}
		switch toolName {
		case "search_code":
			if err := rewriteCodeSearch(arguments); err != nil {
				return "", err
			}
		case "search_repositories":
			if err := rewriteRepositorySearch(arguments); err != nil {
				return "", err
			}
		case "get_repository_tree":
			if err := requireRepository(arguments); err != nil {
				return "", err
			}
			if err := rewriteRepositoryTree(arguments); err != nil {
				return "", err
			}
		case "get_file_contents":
			if err := requireRepository(arguments); err != nil {
				return "", err
			}
			if err := rewriteFileRead(arguments); err != nil {
				return "", err
			}
		case "list_commits":
			if err := requireRepository(arguments); err != nil {
				return "", err
			}
			if err := rewriteCommitHistory(arguments); err != nil {
				return "", err
			}
			clampPerPage(arguments, 20)
		case "get_commit":
			if err := requireRepository(arguments); err != nil {
				return "", err
			}
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

func requireRepository(arguments map[string]any) error {
	for _, key := range []string{"owner", "repo"} {
		value, ok := arguments[key].(string)
		value = strings.TrimSpace(value)
		if !ok || value == "" || !githubRepositoryPart.MatchString(value) {
			return fmt.Errorf("github %s must be a valid repository identifier", key)
		}
		arguments[key] = value
	}
	return nil
}

func rewriteCodeSearch(arguments map[string]any) error {
	query, _ := arguments["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" || strings.ContainsAny(query, "\r\n") {
		return errors.New("search_code query is required and must be a single line")
	}
	if len(query) > 256 {
		return errors.New("search_code query exceeds GitHub's 256 character limit")
	}
	arguments["query"] = query
	// search_code 通过 query qualifier 表达仓库范围；owner/repo/ref 不是该
	// Tool 的参数，不能让模型把其他 Tool 的参数形态带进来。
	delete(arguments, "owner")
	delete(arguments, "repo")
	delete(arguments, "ref")
	clampPerPage(arguments, 20)
	arguments["fields"] = []string{"path", "sha", "repository", "html_url", "text_matches"}
	return nil
}

func rewriteRepositorySearch(arguments map[string]any) error {
	query, _ := arguments["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" || strings.ContainsAny(query, "\r\n") {
		return errors.New("search_repositories query is required and must be a single line")
	}
	if len(query) > 256 {
		return errors.New("search_repositories query exceeds the 256 character limit")
	}
	arguments["query"] = query
	arguments["minimal_output"] = true
	clampPerPage(arguments, 20)
	delete(arguments, "owner")
	delete(arguments, "repo")
	delete(arguments, "ref")
	return nil
}

func rewriteRepositoryTree(arguments map[string]any) error {
	treeSHA, hasTreeSHA, err := optionalStringArgument(arguments, "tree_sha")
	if err != nil {
		return err
	}
	if hasTreeSHA {
		if err := validateGitReference(treeSHA); err != nil {
			return fmt.Errorf("get_repository_tree tree_sha: %w", err)
		}
		arguments["tree_sha"] = treeSHA
	} else {
		delete(arguments, "tree_sha")
	}

	pathFilter, hasPathFilter, err := optionalStringArgument(arguments, "path_filter")
	if err != nil {
		return err
	}
	if hasPathFilter {
		pathFilter, err = normalizeRepositoryPathPrefix(pathFilter)
		if err != nil {
			return fmt.Errorf("get_repository_tree path_filter: %w", err)
		}
		arguments["path_filter"] = pathFilter
	} else {
		delete(arguments, "path_filter")
	}

	if rawRecursive, exists := arguments["recursive"]; exists {
		recursive, ok := rawRecursive.(bool)
		if !ok {
			return errors.New("get_repository_tree recursive must be a boolean")
		}
		arguments["recursive"] = recursive
	} else {
		// Make the server default explicit so a broad tree query cannot become
		// recursive merely because an upstream default changes.
		arguments["recursive"] = false
	}
	// The official Tool names the revision tree_sha. Silently accepting ref or
	// sha would make a caller believe it inspected a different revision.
	if _, exists := arguments["ref"]; exists {
		return errors.New("get_repository_tree uses tree_sha; ref is unsupported")
	}
	if _, exists := arguments["sha"]; exists {
		return errors.New("get_repository_tree uses tree_sha; sha is unsupported")
	}
	delete(arguments, "fields")
	return nil
}

func normalizeRepositoryPathPrefix(raw string) (string, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if raw == "" {
		return "", errors.New("path_filter must not be empty when provided")
	}
	if len(raw) > 512 || strings.ContainsAny(raw, "\r\n\t") || strings.HasPrefix(raw, "/") {
		return "", errors.New("path_filter must be a repository-relative prefix of at most 512 characters")
	}
	trailingSlash := strings.HasSuffix(raw, "/")
	cleaned := path.Clean(raw)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("path_filter escapes the selected repository")
	}
	if trailingSlash {
		cleaned += "/"
	}
	return cleaned, nil
}

func rewriteFileRead(arguments map[string]any) error {
	rawPath, _ := arguments["path"].(string)
	cleaned, err := normalizeRepositoryFilePath(rawPath)
	if err != nil {
		return fmt.Errorf("get_file_contents path: %w", err)
	}
	arguments["path"] = cleaned
	delete(arguments, "fields")
	sha, hasSHA, err := optionalStringArgument(arguments, "sha")
	if err != nil {
		return err
	}
	ref, hasRef, err := optionalStringArgument(arguments, "ref")
	if err != nil {
		return err
	}
	if hasSHA && hasRef {
		return errors.New("get_file_contents sha and ref are mutually exclusive")
	}
	if hasSHA {
		if err := validateGitReference(sha); err != nil {
			return fmt.Errorf("get_file_contents sha: %w", err)
		}
		arguments["sha"] = sha
		delete(arguments, "ref")
	} else if hasRef {
		if err := validateGitReference(ref); err != nil {
			return fmt.Errorf("get_file_contents ref: %w", err)
		}
		arguments["ref"] = ref
		delete(arguments, "sha")
	} else {
		delete(arguments, "sha")
		delete(arguments, "ref")
	}
	return nil
}

func rewriteCommitHistory(arguments map[string]any) error {
	sha, hasSHA, err := optionalStringArgument(arguments, "sha")
	if err != nil {
		return err
	}
	if hasSHA {
		if err := validateGitReference(sha); err != nil {
			return fmt.Errorf("list_commits sha: %w", err)
		}
		arguments["sha"] = sha
	}
	pathValue, hasPath, err := optionalStringArgument(arguments, "path")
	if err != nil {
		return err
	}
	if hasPath {
		cleaned, err := normalizeRepositoryFilePath(pathValue)
		if err != nil {
			return fmt.Errorf("list_commits path: %w", err)
		}
		arguments["path"] = cleaned
	}
	arguments["fields"] = []string{"sha", "html_url", "commit", "author", "committer"}
	return nil
}

func optionalStringArgument(arguments map[string]any, key string) (string, bool, error) {
	raw, exists := arguments[key]
	if !exists {
		return "", false, nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", false, fmt.Errorf("github %s must be a string", key)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		delete(arguments, key)
		return "", false, nil
	}
	return value, true, nil
}

func normalizeRepositoryFilePath(raw string) (string, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if raw == "" || len(raw) > 512 || strings.ContainsAny(raw, "\r\n\t") || strings.HasPrefix(raw, "/") {
		return "", errors.New("must be a repository-relative file path of at most 512 characters")
	}
	cleaned := path.Clean(raw)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("path escapes the selected repository")
	}
	return cleaned, nil
}

func validateGitReference(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || !githubRef.MatchString(value) || strings.Contains(value, "..") || strings.Contains(value, "@{") {
		return errors.New("git reference has unsupported characters")
	}
	return nil
}

func clampPerPage(arguments map[string]any, maximum int) {
	value, ok := arguments["perPage"].(float64)
	if !ok || value < 1 || value > float64(maximum) {
		arguments["perPage"] = maximum
	}
}
