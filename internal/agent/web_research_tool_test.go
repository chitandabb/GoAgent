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

	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/webresearch"
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

func TestWebResearchToolsRequireScopeCapabilityAndDependency(t *testing.T) {
	searchTool, err := NewWebSearchTool(webResearcherStub{search: webresearch.SearchResponse{Query: "PostgreSQL timeout", UntrustedContent: true}})
	if err != nil {
		t.Fatal(err)
	}
	authorized, err := NewTaskScope(TaskScopeConfig{
		UserID: uuid.New(), Role: auth.RoleAnalyst, TaskType: TaskTypeDiagnosis,
		DataSources:           []ScopedDataSource{{ID: uuid.New(), Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly}},
		AllowedCapabilities:   []ToolCapability{ToolCapabilityCase, ToolCapabilityWebSearch},
		AvailableDependencies: []ToolDependency{ToolDependencyWebSearch},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := searchTool.InvokableRun(WithTaskScope(context.Background(), authorized), `{"query":"PostgreSQL timeout"}`); err != nil {
		t.Fatalf("authorized web_search: %v", err)
	}
	denied, err := NewTaskScope(TaskScopeConfig{
		UserID: uuid.New(), Role: auth.RoleAnalyst, TaskType: TaskTypeDiagnosis,
		DataSources:         []ScopedDataSource{{ID: uuid.New(), Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly}},
		AllowedCapabilities: []ToolCapability{ToolCapabilityCase, ToolCapabilityWebSearch},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := searchTool.InvokableRun(WithTaskScope(context.Background(), denied), `{"query":"PostgreSQL timeout"}`); !errors.Is(err, ErrToolNotAllowed) {
		t.Fatalf("denied web_search error=%v", err)
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

func TestKnowledgeTaskScopeAllowsOnlyKnowledgeAndWebResearch(t *testing.T) {
	if _, err := NewTaskScope(TaskScopeConfig{
		UserID: uuid.New(), Role: auth.RoleAnalyst, TaskType: TaskTypeKnowledge,
		AllowedCapabilities: []ToolCapability{ToolCapabilityKnowledge, ToolCapabilityWebSearch},
	}); err != nil {
		t.Fatalf("knowledge web scope: %v", err)
	}
	if _, err := NewTaskScope(TaskScopeConfig{
		UserID: uuid.New(), Role: auth.RoleAnalyst, TaskType: TaskTypeKnowledge,
		AllowedCapabilities: []ToolCapability{ToolCapabilityKnowledge, ToolCapabilityCode},
	}); err == nil {
		t.Fatal("knowledge scope accepted code capability")
	}
}

func TestDefaultCatalogExposesWebToolsOnlyWhenScopeAndDependencyAllow(t *testing.T) {
	stub := webResearcherStub{}
	searchTool, err := NewWebSearchTool(stub)
	if err != nil {
		t.Fatal(err)
	}
	fetchTool, err := NewFetchPublicPageTool(stub)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
		ExternalCases: runnerTestCaseGetter{}, WebSearch: searchTool, FetchPublicPage: fetchTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := NewTaskScope(TaskScopeConfig{
		UserID: uuid.New(), Role: auth.RoleAnalyst, TaskType: TaskTypeKnowledge,
		AllowedCapabilities:   []ToolCapability{ToolCapabilityKnowledge, ToolCapabilityWebSearch},
		AvailableDependencies: []ToolDependency{ToolDependencyWebSearch},
	})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := catalog.ToolsFor(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	names := toolNamesForTest(t, tools)
	if !slices.Contains(names, ToolWebSearch) || !slices.Contains(names, ToolFetchPublicPage) {
		t.Fatalf("web tool names=%v", names)
	}
	withoutDependency, err := NewTaskScope(TaskScopeConfig{
		UserID: uuid.New(), Role: auth.RoleAnalyst, TaskType: TaskTypeKnowledge,
		AllowedCapabilities: []ToolCapability{ToolCapabilityKnowledge, ToolCapabilityWebSearch},
	})
	if err != nil {
		t.Fatal(err)
	}
	tools, err = catalog.ToolsFor(context.Background(), withoutDependency)
	if err != nil {
		t.Fatal(err)
	}
	if names = toolNamesForTest(t, tools); slices.Contains(names, ToolWebSearch) || slices.Contains(names, ToolFetchPublicPage) {
		t.Fatalf("web tools exposed without dependency: %v", names)
	}
}
