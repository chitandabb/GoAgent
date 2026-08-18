package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/webresearch"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type webResearcherStub struct {
	search webresearch.SearchResponse
	page   webresearch.PageSnapshot
	err    error
}

func (s webResearcherStub) Search(context.Context, string, string, int) (webresearch.SearchResponse, error) {
	return s.search, s.err
}

func (s webResearcherStub) Fetch(context.Context, string, string) (webresearch.PageSnapshot, error) {
	return s.page, s.err
}

func TestWebResearchToolsRequireRunAccessWebPermission(t *testing.T) {
	searchTool, err := NewWebSearchTool(webResearcherStub{search: webresearch.SearchResponse{Query: "PostgreSQL timeout", UntrustedContent: true}})
	if err != nil {
		t.Fatal(err)
	}
	// 授权完全来自 RunAccess：web.read 存在时允许执行。
	ctx := agentruntime.WithRunAccess(context.Background(), mustConversationTestRunAccess(
		t, uuid.New(),
		[]agentruntime.Permission{agentruntime.PermissionWebRead},
		agentruntime.ResourceGrantsConfig{},
	))
	if _, err := searchTool.InvokableRun(ctx, `{"query":"PostgreSQL timeout"}`); err != nil {
		t.Fatalf("authorized web_search: %v", err)
	}
	// 没有 web.read：fail-closed。
	ctx = agentruntime.WithRunAccess(context.Background(), mustConversationTestRunAccess(
		t, uuid.New(),
		[]agentruntime.Permission{agentruntime.PermissionCaseRead},
		agentruntime.ResourceGrantsConfig{},
	))
	if _, err := searchTool.InvokableRun(ctx, `{"query":"PostgreSQL timeout"}`); !errors.Is(err, ErrToolNotAllowed) {
		t.Fatalf("denied web_search error=%v", err)
	}
	// 没有 RunAccess：fail-closed。
	if _, err := searchTool.InvokableRun(context.Background(), `{"query":"PostgreSQL timeout"}`); !errors.Is(err, ErrRunAccessRequired) {
		t.Fatalf("missing RunAccess error=%v", err)
	}
}

func TestFetchPublicPageCreatesTamperEvidentWebEvidence(t *testing.T) {
	content := "Public upstream documentation"
	digest := sha256.Sum256([]byte(content))
	page := webresearch.PageSnapshot{
		ResultID: "web_123", URL: "https://go.dev/doc/", Domain: "go.dev", Title: "Go docs",
		FetchedAt: time.Date(2026, 8, 7, 8, 0, 0, 0, time.UTC), ContentText: content,
		ContentSHA256: hex.EncodeToString(digest[:]), SourceTier: webresearch.SourceTierOfficial,
		UntrustedContent: true,
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	location, ok := webPageEvidenceLocation(string(encoded))
	if !ok || location != page.URL {
		t.Fatalf("webPageEvidenceLocation=%q,%t", location, ok)
	}
	item, ok := newToolEvidenceItem(ToolFetchPublicPage, string(encoded), false)
	if !ok || item.SourceType != EvidenceSourceWebPage || item.Location != page.URL {
		t.Fatalf("web evidence=%+v,%t", item, ok)
	}
	if item.SourceType != EvidenceSourceType("web") {
		t.Fatalf("web evidence source type=%q, database contract requires web", item.SourceType)
	}
	page.ContentText += " tampered"
	tampered, _ := json.Marshal(page)
	if _, ok := webPageEvidenceLocation(string(tampered)); ok {
		t.Fatal("tampered web page became evidence")
	}
	if _, ok := newToolEvidenceItem(ToolWebSearch, `{"results":[]}`, false); ok {
		t.Fatal("search discovery output became evidence")
	}
}

// TestConversationProfileExposesWebToolsAndExecutionRequiresWebPermission
// 证明 Conversation 固定 Profile 总是包含 web 工具（Schema 与 RunAccess
// 解耦），但执行期仍按 RunAccess.PermissionWebRead fail-closed。
func TestConversationProfileExposesWebToolsAndExecutionRequiresWebPermission(t *testing.T) {
	stub := webResearcherStub{}
	searchTool, err := NewWebSearchTool(stub)
	if err != nil {
		t.Fatal(err)
	}
	fetchTool, err := NewFetchPublicPageTool(stub)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewConversationDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
		ExternalCases: runnerTestCaseGetter{}, WebSearch: searchTool, FetchPublicPage: fetchTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := catalog.ResolveProfile(context.Background(), agentruntime.ToolProfileConversation)
	if err != nil {
		t.Fatal(err)
	}
	names := toolNamesForTest(t, resolved.Tools)
	if !slices.Contains(names, ToolWebSearch) || !slices.Contains(names, ToolFetchPublicPage) {
		t.Fatalf("conversation profile web tool names=%v", names)
	}
	var guarded tool.InvokableTool
	for _, current := range resolved.Tools {
		info, infoErr := current.Info(context.Background())
		if infoErr != nil {
			t.Fatalf("Tool.Info: %v", infoErr)
		}
		if info.Name == ToolWebSearch {
			guarded = current.(tool.InvokableTool)
			break
		}
	}
	if guarded == nil {
		t.Fatal("web_search missing from resolved profile")
	}
	// 无 web.read 的执行必须拒绝（Profile 可见不等于可执行）。
	denied := agentruntime.WithRunAccess(context.Background(), mustConversationTestRunAccess(
		t, uuid.New(),
		[]agentruntime.Permission{agentruntime.PermissionCaseRead},
		agentruntime.ResourceGrantsConfig{},
	))
	if _, err := guarded.InvokableRun(denied, `{"query":"PostgreSQL timeout"}`); !errors.Is(err, ErrToolNotAllowed) {
		t.Fatalf("denied web_search error=%v, want ErrToolNotAllowed", err)
	}
}

// TestConversationRunnerInjectsWebResearchRunState 是 Conversation web 工具
// 全链路的回归测试：此前 Conversation Runner 从未调用 WithRunContext 注入
// RunState，web_search 在真实 Service 上总是失败为
// ErrRunStateRequired（safeWebResearchError 兜底为 "public web research is
// unavailable" 并被标成 tool_call_rejected）。现在 Runner 注入 RunState 后，
// 工具必须通过真实 QueryPolicy/URLPolicy 完成一次搜索。
func TestConversationRunnerInjectsWebResearchRunState(t *testing.T) {
	queryPolicy, err := webresearch.NewQueryPolicy(webresearch.QueryPolicyConfig{})
	if err != nil {
		t.Fatal(err)
	}
	urlPolicy := webresearch.NewURLPolicy(nil)
	service, err := webresearch.NewService(webresearch.ServiceConfig{
		SearchProvider:  staticSearchProvider{},
		ContentProvider: staticContentProvider{urlPolicy: urlPolicy},
		QueryPolicy:     queryPolicy, URLPolicy: urlPolicy,
		MaxResults: 5, MaxFetchedPages: 1, MaxPageChars: 4000, MaxRounds: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	searchTool, err := NewWebSearchTool(service)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewConversationDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
		ExternalCases: runnerTestCaseGetter{}, WebSearch: searchTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	state := &conversationRunnerModelState{searchWebIfAvailable: true}
	runner, err := NewConversationRunner(ConversationRunnerConfig{
		ChatModel:         &conversationRunnerTestModel{state: state},
		ToolCatalog:       catalog,
		SystemInstruction: "conversation web research test",
		ModelProvider:     "fixture", ModelID: "fixture-v1",
		PromptVersion: "conversation-test-v1", Logger: zap.NewNop(),
		MaxContextRunes: conversation.MaxContentRunes,
		WebResearch:     service,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, ctx := conversationRunnerRequest(nil)
	response, err := runner.Respond(ctx, request)
	if err != nil {
		t.Fatalf("Respond(): %v", err)
	}
	if response.Content == "" {
		t.Fatal("response content is empty after web search turn")
	}
	// 模型必须真正看到 web_search 的执行结果（而不是被拒绝的失败编码）。
	found := false
	for _, input := range state.inputs {
		for _, entry := range input {
			if strings.Contains(entry, string(schema.Tool)+"\x00"+ToolWebSearch) {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("model never received a web_search tool result; schemas=%v inputs=%v", state.schemas, state.inputs)
	}
}

// staticSearchProvider 固定返回一条真实公网候选（不发起网络调用）。
type staticSearchProvider struct{}

func (staticSearchProvider) Search(context.Context, webresearch.PublicQuery, int) ([]webresearch.ProviderSearchResult, error) {
	return []webresearch.ProviderSearchResult{{
		URL: "https://www.postgresql.org/docs/current/logical-replication.html",
		Title: "PostgreSQL: Documentation: 17: Chapter 31. Logical Replication",
		Description: "Logical replication is a method of replicating data objects and their changes, based on their replication identity.",
	}}, nil
}

// staticContentProvider 直接返回页面内容（URLPolicy 已由 Service 校验）。
type staticContentProvider struct{ urlPolicy *webresearch.URLPolicy }

func (p staticContentProvider) Fetch(ctx context.Context, target webresearch.PublicURL) (webresearch.ProviderPage, error) {
	return webresearch.ProviderPage{
		URL: target.String(), Title: "Logical Replication", Markdown: "# Logical Replication\n\nLogical replication is a method of replicating data.",
	}, nil
}
