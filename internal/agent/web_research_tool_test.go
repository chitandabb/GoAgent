package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/webresearch"
	"github.com/cloudwego/eino/components/tool"
	"github.com/google/uuid"
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
