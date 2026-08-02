package githubmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
)

const (
	codeSearchEvaluationMaxCases = 20
	listCommitsToolName          = "list_commits"
	fileContentsToolName         = "get_file_contents"
)

// CodeSearchEvaluationCase 是一条不依赖模型的 GitHub 分层检索评测样本。
// ContentMarker 必须是目标文件内容中的稳定片段，而不能只出现在文件路径中。
type CodeSearchEvaluationCase struct {
	DatasetVersion string `json:"datasetVersion"`
	CaseID         string `json:"caseId"`
	Owner          string `json:"owner"`
	Repo           string `json:"repo"`
	Query          string `json:"query"`
	TreeSHA        string `json:"treeSha,omitempty"`
	PathFilter     string `json:"pathFilter"`
	ExpectedPath   string `json:"expectedPath"`
	ContentMarker  string `json:"contentMarker"`
}

func (c CodeSearchEvaluationCase) Validate() error {
	if strings.TrimSpace(c.DatasetVersion) == "" || strings.TrimSpace(c.CaseID) == "" {
		return errors.New("datasetVersion and caseId are required")
	}
	if !githubRepositoryPart.MatchString(strings.TrimSpace(c.Owner)) ||
		!githubRepositoryPart.MatchString(strings.TrimSpace(c.Repo)) {
		return errors.New("owner and repo must be valid GitHub repository identifiers")
	}
	query := strings.TrimSpace(c.Query)
	if query == "" || strings.ContainsAny(query, "\r\n") || len(query) > 256 {
		return errors.New("query must be a non-empty single line of at most 256 characters")
	}
	pathFilter, err := normalizeRepositoryPathPrefix(c.PathFilter)
	if err != nil {
		return fmt.Errorf("pathFilter: %w", err)
	}
	expectedPath, err := normalizeEvaluationFilePath(c.ExpectedPath)
	if err != nil {
		return fmt.Errorf("expectedPath: %w", err)
	}
	filterPrefix := strings.TrimSuffix(pathFilter, "/")
	if expectedPath != filterPrefix && !strings.HasPrefix(expectedPath, filterPrefix+"/") {
		return errors.New("expectedPath must be inside pathFilter")
	}
	marker := strings.TrimSpace(c.ContentMarker)
	if marker == "" || strings.ContainsAny(marker, "\r\n") || len(marker) > 512 {
		return errors.New("contentMarker must be a non-empty single-line value of at most 512 characters")
	}
	if c.TreeSHA != "" {
		if err := validateGitReference(c.TreeSHA); err != nil {
			return fmt.Errorf("treeSha: %w", err)
		}
	}
	return nil
}

func normalizeEvaluationFilePath(raw string) (string, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if raw == "" || strings.HasPrefix(raw, "/") {
		return "", errors.New("must be a repository-relative file path")
	}
	cleaned := path.Clean(raw)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("path escapes the selected repository")
	}
	return cleaned, nil
}

// CodeSearchEvaluationResult 记录一次样本的搜索、树候选和固定 SHA 文件读取结果。
type CodeSearchEvaluationResult struct {
	DatasetVersion          string   `json:"datasetVersion"`
	CaseID                  string   `json:"caseId"`
	Owner                   string   `json:"owner"`
	Repo                    string   `json:"repo"`
	SearchStatus            string   `json:"searchStatus"`
	SearchAttempts          int      `json:"searchAttempts"`
	SearchTotalCount        int      `json:"searchTotalCount"`
	SearchHitExpectedPath   bool     `json:"searchHitExpectedPath"`
	TreeSHA                 string   `json:"treeSha,omitempty"`
	TreeInvoked             bool     `json:"treeInvoked"`
	TreeCandidateIncomplete bool     `json:"treeCandidateIncomplete"`
	TreeCandidateCount      int      `json:"treeCandidateCount"`
	TreeHitExpectedPath     bool     `json:"treeHitExpectedPath"`
	FileReadInvoked         bool     `json:"fileReadInvoked"`
	FileVerified            bool     `json:"fileVerified"`
	FallbackRecovered       bool     `json:"fallbackRecovered"`
	SearchDurationMillis    int64    `json:"searchDurationMillis"`
	TreeDurationMillis      int64    `json:"treeDurationMillis"`
	FileDurationMillis      int64    `json:"fileDurationMillis"`
	ErrorType               string   `json:"errorType,omitempty"`
	ErrorTypes              []string `json:"errorTypes,omitempty"`
}

// CodeSearchEvaluationSummary 是分层检索评测的确定性汇总，不包含模型推断指标。
type CodeSearchEvaluationSummary struct {
	DatasetVersion                string                       `json:"datasetVersion"`
	RequestedCases                int                          `json:"requestedCases"`
	Cases                         int                          `json:"cases"`
	SearchCompleteCases           int                          `json:"searchCompleteCases"`
	SearchIncompleteCases         int                          `json:"searchIncompleteCases"`
	SearchExpectedPathHits        int                          `json:"searchExpectedPathHits"`
	TreeEvaluatedCases            int                          `json:"treeEvaluatedCases"`
	TreeExpectedPathHits          int                          `json:"treeExpectedPathHits"`
	TreeCandidateIncompleteCases  int                          `json:"treeCandidateIncompleteCases"`
	FallbackRecoveredCases        int                          `json:"fallbackRecoveredCases"`
	KnownPathVerifiedCases        int                          `json:"knownPathVerifiedCases"`
	SearchCompleteRate            float64                      `json:"searchCompleteRate"`
	SearchExpectedPathRecall      float64                      `json:"searchExpectedPathRecall"`
	TreeExpectedPathRecall        float64                      `json:"treeExpectedPathRecall"`
	FallbackRecoveryRate          float64                      `json:"fallbackRecoveryRate"`
	KnownPathFileVerificationRate float64                      `json:"knownPathFileVerificationRate"`
	FailureTypes                  map[string]int               `json:"failureTypes,omitempty"`
	Results                       []CodeSearchEvaluationResult `json:"results"`
}

type CodeSearchStabilityEvaluator struct {
	tools   map[string]tool.InvokableTool
	rewrite func(context.Context, string, string) (string, error)
}

// NewCodeSearchStabilityEvaluator 接受已连接、已包装的 GitHub MCP Tool。
// 它只调用 search_code、get_repository_tree、get_file_contents 和 list_commits，
// 不启动模型，也不创建本地仓库缓存。
func NewCodeSearchStabilityEvaluator(ctx context.Context, tools []tool.BaseTool) (*CodeSearchStabilityEvaluator, error) {
	available := make(map[string]tool.InvokableTool, len(tools))
	for _, current := range tools {
		if current == nil {
			return nil, errors.New("github evaluation tool is nil")
		}
		info, err := current.Info(ctx)
		if err != nil {
			return nil, fmt.Errorf("read github evaluation tool info: %w", err)
		}
		if info == nil {
			return nil, errors.New("github evaluation tool info is nil")
		}
		if info.Name != codeSearchToolName && info.Name != repositoryTreeToolName &&
			info.Name != fileContentsToolName && info.Name != listCommitsToolName {
			continue
		}
		invokable, ok := current.(tool.InvokableTool)
		if !ok {
			return nil, fmt.Errorf("github evaluation tool %q is not invokable", info.Name)
		}
		available[info.Name] = invokable
	}
	for _, name := range []string{codeSearchToolName, repositoryTreeToolName, fileContentsToolName} {
		if _, ok := available[name]; !ok {
			return nil, fmt.Errorf("github evaluation tool %q is unavailable", name)
		}
	}
	return &CodeSearchStabilityEvaluator{tools: available, rewrite: NewArgumentRewriter()}, nil
}

func (e *CodeSearchStabilityEvaluator) Evaluate(ctx context.Context, cases []CodeSearchEvaluationCase) (CodeSearchEvaluationSummary, error) {
	if e == nil {
		return CodeSearchEvaluationSummary{}, errors.New("github code search evaluator is nil")
	}
	if len(cases) == 0 || len(cases) > codeSearchEvaluationMaxCases {
		return CodeSearchEvaluationSummary{}, fmt.Errorf("evaluation cases must contain between 1 and %d items", codeSearchEvaluationMaxCases)
	}
	if err := ctx.Err(); err != nil {
		return CodeSearchEvaluationSummary{RequestedCases: len(cases)}, err
	}
	version := ""
	for index, current := range cases {
		if err := ctx.Err(); err != nil {
			return CodeSearchEvaluationSummary{DatasetVersion: version, RequestedCases: len(cases)}, err
		}
		if err := current.Validate(); err != nil {
			return CodeSearchEvaluationSummary{}, fmt.Errorf("evaluation case %d: %w", index, err)
		}
		if version == "" {
			version = current.DatasetVersion
		} else if version != current.DatasetVersion {
			return CodeSearchEvaluationSummary{}, fmt.Errorf("evaluation cases mix dataset versions %q and %q", version, current.DatasetVersion)
		}
	}

	results := make([]CodeSearchEvaluationResult, 0, len(cases))
	for _, current := range cases {
		if err := ctx.Err(); err != nil {
			return summarizeCodeSearchEvaluation(version, len(cases), results), err
		}
		result, err := e.evaluateCase(ctx, current)
		results = append(results, result)
		if err != nil {
			return summarizeCodeSearchEvaluation(version, len(cases), results), err
		}
	}
	return summarizeCodeSearchEvaluation(version, len(cases), results), nil
}

func (e *CodeSearchStabilityEvaluator) evaluateCase(ctx context.Context, current CodeSearchEvaluationCase) (CodeSearchEvaluationResult, error) {
	result := CodeSearchEvaluationResult{
		DatasetVersion: current.DatasetVersion,
		CaseID:         current.CaseID,
		Owner:          current.Owner,
		Repo:           current.Repo,
		SearchStatus:   "error",
	}
	if err := ctx.Err(); err != nil {
		result.addError(evaluationContextErrorType(err))
		return result, err
	}

	searchStarted := time.Now()
	searchRaw, err := e.invoke(ctx, codeSearchToolName, map[string]any{"query": current.Query})
	result.SearchDurationMillis = time.Since(searchStarted).Milliseconds()
	if err != nil {
		result.addError("search_error")
	} else {
		parsed, parseErr := parseCodeSearchEvaluationResponse(searchRaw)
		if parseErr != nil {
			result.addError("search_response_invalid")
		} else {
			result.SearchStatus = parsed.status
			result.SearchAttempts = parsed.attempts
			result.SearchTotalCount = parsed.totalCount
			result.SearchHitExpectedPath = containsPath(parsed.paths, current.ExpectedPath)
		}
	}
	if err := ctx.Err(); err != nil {
		result.addError(evaluationContextErrorType(err))
		return result, err
	}

	treeSHA := current.TreeSHA
	if treeSHA == "" {
		treeSHA, err = e.resolveTreeSHA(ctx, current.Owner, current.Repo)
		if err != nil {
			result.addError("commit_lookup_error")
			if contextErr := ctx.Err(); contextErr != nil {
				result.addError(evaluationContextErrorType(contextErr))
				return result, contextErr
			}
		}
	}
	if contextErr := ctx.Err(); contextErr != nil {
		result.addError(evaluationContextErrorType(contextErr))
		return result, contextErr
	}
	result.TreeSHA = treeSHA
	if treeSHA == "" {
		return result, nil
	}

	treeStarted := time.Now()
	treeRaw, treeErr := e.invoke(ctx, repositoryTreeToolName, map[string]any{
		"owner": current.Owner, "repo": current.Repo, "tree_sha": treeSHA,
		"path_filter": current.PathFilter, "recursive": true,
	})
	result.TreeDurationMillis = time.Since(treeStarted).Milliseconds()
	result.TreeInvoked = true
	if treeErr != nil {
		result.addError("tree_error")
	} else {
		parsed, parseErr := parseRepositoryTreeEvaluationResponse(treeRaw)
		if parseErr != nil {
			result.addError("tree_response_invalid")
		} else {
			result.TreeCandidateIncomplete = parsed.incomplete
			result.TreeCandidateCount = parsed.count
			result.TreeHitExpectedPath = containsPath(parsed.paths, current.ExpectedPath)
		}
	}
	if err := ctx.Err(); err != nil {
		result.addError(evaluationContextErrorType(err))
		return result, err
	}

	fileStarted := time.Now()
	fileRaw, fileErr := e.invoke(ctx, fileContentsToolName, map[string]any{
		"owner": current.Owner, "repo": current.Repo, "path": current.ExpectedPath, "sha": treeSHA,
	})
	result.FileDurationMillis = time.Since(fileStarted).Milliseconds()
	result.FileReadInvoked = true
	if fileErr != nil {
		result.addError("file_error")
	} else {
		result.FileVerified = responseContainsContentMarker(fileRaw, current.ContentMarker)
		if !result.FileVerified {
			result.addError("content_marker_missing")
		}
	}
	if err := ctx.Err(); err != nil {
		result.addError(evaluationContextErrorType(err))
		return result, err
	}
	result.FallbackRecovered = result.SearchStatus == "incomplete" && result.TreeHitExpectedPath && result.FileVerified
	return result, nil
}

func (e *CodeSearchStabilityEvaluator) resolveTreeSHA(ctx context.Context, owner, repo string) (string, error) {
	raw, err := e.invoke(ctx, listCommitsToolName, map[string]any{"owner": owner, "repo": repo})
	if err != nil {
		return "", err
	}
	data, err := unwrapGitHubMCPText(raw)
	if err != nil {
		return "", err
	}
	var commits []struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(data, &commits); err != nil || len(commits) == 0 || !commitSHA.MatchString(commits[0].SHA) {
		return "", errors.New("list_commits did not return a usable commit SHA")
	}
	return commits[0].SHA, nil
}

func (e *CodeSearchStabilityEvaluator) invoke(ctx context.Context, name string, arguments map[string]any) (string, error) {
	target, ok := e.tools[name]
	if !ok {
		return "", fmt.Errorf("github evaluation tool %q is unavailable", name)
	}
	raw, err := json.Marshal(arguments)
	if err != nil {
		return "", err
	}
	rewritten, err := e.rewrite(ctx, name, string(raw))
	if err != nil {
		return "", err
	}
	return target.InvokableRun(ctx, rewritten)
}

type parsedCodeSearchEvaluationResponse struct {
	status     string
	attempts   int
	totalCount int
	paths      []string
}

func parseCodeSearchEvaluationResponse(raw string) (parsedCodeSearchEvaluationResponse, error) {
	result := parsedCodeSearchEvaluationResponse{status: "complete", attempts: 1}
	data := []byte(raw)
	var status struct {
		Status            string          `json:"status"`
		Attempts          int             `json:"attempts"`
		IncompleteResults bool            `json:"incomplete_results"`
		GitHubResponse    json.RawMessage `json:"github_response"`
	}
	if json.Unmarshal(data, &status) == nil && status.Status != "" {
		if status.Attempts > 0 {
			result.attempts = status.Attempts
		}
		if status.Status == codeSearchIndexPendingResultStatus || status.IncompleteResults {
			result.status = "incomplete"
		}
		if len(status.GitHubResponse) > 0 && json.Valid(status.GitHubResponse) {
			data = status.GitHubResponse
		}
	}
	data, err := unwrapGitHubMCPText(string(data))
	if err != nil {
		return parsedCodeSearchEvaluationResponse{}, err
	}
	var payload struct {
		TotalCount        int  `json:"total_count"`
		IncompleteResults bool `json:"incomplete_results"`
		Items             []struct {
			Path string `json:"path"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return parsedCodeSearchEvaluationResponse{}, err
	}
	if payload.IncompleteResults {
		result.status = "incomplete"
	}
	result.totalCount = payload.TotalCount
	for _, item := range payload.Items {
		result.paths = append(result.paths, item.Path)
	}
	return result, nil
}

type parsedRepositoryTreeEvaluationResponse struct {
	incomplete bool
	count      int
	paths      []string
}

func parseRepositoryTreeEvaluationResponse(raw string) (parsedRepositoryTreeEvaluationResponse, error) {
	var candidate repositoryTreeCandidateResult
	if json.Unmarshal([]byte(raw), &candidate) == nil && candidate.Status == "candidate_paths" {
		result := parsedRepositoryTreeEvaluationResponse{
			incomplete: candidate.UpstreamTruncated || candidate.CandidateLimitReached,
			count:      candidate.CandidateCount,
		}
		for _, item := range candidate.Candidates {
			result.paths = append(result.paths, item.Path)
		}
		return result, nil
	}
	data, err := unwrapGitHubMCPText(raw)
	if err != nil {
		return parsedRepositoryTreeEvaluationResponse{}, err
	}
	var payload repositoryTreePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return parsedRepositoryTreeEvaluationResponse{}, err
	}
	result := parsedRepositoryTreeEvaluationResponse{incomplete: payload.Truncated}
	for _, item := range payload.Tree {
		if item.Type == "blob" {
			result.paths = append(result.paths, item.Path)
		}
	}
	result.count = len(result.paths)
	return result, nil
}

func unwrapGitHubMCPText(raw string) ([]byte, error) {
	var envelope struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Resource *struct {
				Text string `json:"text"`
			} `json:"resource,omitempty"`
		} `json:"content"`
	}
	if json.Unmarshal([]byte(raw), &envelope) == nil && len(envelope.Content) > 0 {
		var fallbackText string
		for _, block := range envelope.Content {
			if block.Type == "resource" && block.Resource != nil && block.Resource.Text != "" {
				return []byte(block.Resource.Text), nil
			}
			if block.Type == "text" && strings.TrimSpace(block.Text) != "" && fallbackText == "" {
				fallbackText = block.Text
			}
		}
		if fallbackText != "" {
			return []byte(fallbackText), nil
		}
		return nil, errors.New("GitHub MCP response has no text content")
	}
	return []byte(raw), nil
}

func responseContainsContentMarker(raw, marker string) bool {
	data, err := unwrapGitHubMCPText(raw)
	if err != nil {
		return false
	}
	var payload struct {
		Content string `json:"content"`
	}
	if json.Unmarshal(data, &payload) == nil && payload.Content != "" {
		return strings.Contains(payload.Content, marker)
	}
	return strings.Contains(string(data), marker)
}

func containsPath(paths []string, expected string) bool {
	for _, current := range paths {
		if current == expected {
			return true
		}
	}
	return false
}

func (r *CodeSearchEvaluationResult) addError(errorType string) {
	if r == nil || errorType == "" {
		return
	}
	if r.ErrorType == "" {
		r.ErrorType = errorType
	}
	for _, current := range r.ErrorTypes {
		if current == errorType {
			return
		}
	}
	r.ErrorTypes = append(r.ErrorTypes, errorType)
}

func evaluationContextErrorType(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "context_deadline_exceeded"
	}
	return "context_canceled"
}

func summarizeCodeSearchEvaluation(version string, requestedCases int, results []CodeSearchEvaluationResult) CodeSearchEvaluationSummary {
	summary := CodeSearchEvaluationSummary{
		DatasetVersion: version, RequestedCases: requestedCases, Cases: len(results), Results: results,
		FailureTypes: make(map[string]int),
	}
	for _, result := range results {
		switch result.SearchStatus {
		case "complete":
			summary.SearchCompleteCases++
		case "incomplete":
			summary.SearchIncompleteCases++
		}
		if result.SearchHitExpectedPath {
			summary.SearchExpectedPathHits++
		}
		if result.TreeInvoked {
			summary.TreeEvaluatedCases++
		}
		if result.TreeHitExpectedPath {
			summary.TreeExpectedPathHits++
		}
		if result.TreeCandidateIncomplete {
			summary.TreeCandidateIncompleteCases++
		}
		if result.FallbackRecovered {
			summary.FallbackRecoveredCases++
		}
		if result.FileVerified {
			summary.KnownPathVerifiedCases++
		}
		for _, errorType := range result.ErrorTypes {
			summary.FailureTypes[errorType]++
		}
	}
	summary.SearchCompleteRate = safeEvaluationRate(summary.SearchCompleteCases, summary.Cases)
	summary.SearchExpectedPathRecall = safeEvaluationRate(summary.SearchExpectedPathHits, summary.Cases)
	summary.TreeExpectedPathRecall = safeEvaluationRate(summary.TreeExpectedPathHits, summary.TreeEvaluatedCases)
	summary.FallbackRecoveryRate = safeEvaluationRate(summary.FallbackRecoveredCases, summary.SearchIncompleteCases)
	summary.KnownPathFileVerificationRate = safeEvaluationRate(summary.KnownPathVerifiedCases, countFileReads(results))
	if len(summary.FailureTypes) == 0 {
		summary.FailureTypes = nil
	}
	return summary
}

func countFileReads(results []CodeSearchEvaluationResult) int {
	count := 0
	for _, result := range results {
		if result.FileReadInvoked {
			count++
		}
	}
	return count
}

func safeEvaluationRate(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}
