package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/google/uuid"
)

type knowledgeSearcherStub struct {
	actorID uuid.UUID
	query   string
	limit   int
	result  knowledge.HybridSearch
	err     error
}

func (s *knowledgeSearcherStub) Search(_ context.Context, actorID uuid.UUID, query string, limit int) (knowledge.HybridSearch, error) {
	s.actorID, s.query, s.limit = actorID, query, limit
	return s.result, s.err
}

func TestSearchKnowledgeToolReturnsBoundedEvidenceAndDegradedChannels(t *testing.T) {
	actorID, documentID, versionID, chunkID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	page := 4
	searcher := &knowledgeSearcherStub{result: knowledge.HybridSearch{
		Results: []knowledge.SearchResult{{
			DocumentID: documentID, DocumentVersionID: versionID, ChunkID: chunkID,
			Title: "生产手册", Scope: knowledge.ScopeGlobal, Ordinal: 2, PageNumber: &page,
			ElementType: knowledge.ElementText, SectionPath: []string{"网络", "超时"},
			ContentText: "事务超时需要检查连接池。", ContentSHA256: knowledge.SHA256Hex("事务超时需要检查连接池。"),
			Score: 0.92, FTSRank: 1, VectorRank: 2, FusedScore: 0.03,
		}}, Degraded: true, Sources: []string{"fts"}, MissingChannels: []string{"vector"},
	}}
	tool, err := NewSearchKnowledgeTool(searcher)
	if err != nil {
		t.Fatalf("NewSearchKnowledgeTool: %v", err)
	}
	scope := mustTaskScopeWithCapabilities(t, auth.RoleAnalyst, TaskTypeKnowledge, nil,
		[]ToolCapability{ToolCapabilityKnowledge}, ToolDependencyKnowledge)
	// Use a stable actor assertion independent of the helper's generated user.
	scope = scopeWithUserID(t, scope, actorID)
	encoded, err := tool.InvokableRun(WithTaskScope(context.Background(), scope), `{"query":"  事务超时  ","maxResults":3}`)
	if err == nil {
		t.Fatal("InvokableRun accepted an untrimmed query")
	}
	encoded, err = tool.InvokableRun(WithTaskScope(context.Background(), scope), `{"query":"事务超时","maxResults":3}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var response struct {
		Query           string           `json:"query"`
		Results         []map[string]any `json:"results"`
		Degraded        bool             `json:"degraded"`
		Sources         []string         `json:"sources"`
		MissingChannels []string         `json:"missingChannels"`
	}
	if err := json.Unmarshal([]byte(encoded), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Query != "事务超时" || len(response.Results) != 1 || !response.Degraded ||
		len(response.Sources) != 1 || response.Sources[0] != "fts" || len(response.MissingChannels) != 1 {
		t.Fatalf("response = %s", encoded)
	}
	if searcher.actorID != actorID || searcher.query != "事务超时" || searcher.limit != 3 {
		t.Fatalf("search request = actor=%s query=%q limit=%d", searcher.actorID, searcher.query, searcher.limit)
	}
}

func TestSearchKnowledgeToolRejectsMalformedCitationFields(t *testing.T) {
	result := knowledge.SearchResult{
		DocumentID: uuid.New(), DocumentVersionID: uuid.New(), ChunkID: uuid.New(),
		Title: "生产手册", Scope: knowledge.ScopeGlobal, ElementType: knowledge.ElementText,
		ContentText: "事务超时需要检查连接池。", ContentSHA256: knowledge.SHA256Hex("other"),
	}
	tool, err := NewSearchKnowledgeTool(&knowledgeSearcherStub{result: knowledge.HybridSearch{
		Results: []knowledge.SearchResult{result},
	}})
	if err != nil {
		t.Fatalf("NewSearchKnowledgeTool: %v", err)
	}
	scope := mustTaskScopeWithCapabilities(t, auth.RoleAnalyst, TaskTypeKnowledge, nil,
		[]ToolCapability{ToolCapabilityKnowledge}, ToolDependencyKnowledge)
	if _, err := tool.InvokableRun(WithTaskScope(context.Background(), scope), `{"query":"事务超时"}`); err == nil ||
		!strings.Contains(err.Error(), "knowledge search result 0 is invalid") {
		t.Fatalf("malformed result error = %v", err)
	}
}

func TestSearchKnowledgeToolRequiresKnowledgeDependency(t *testing.T) {
	searcher := &knowledgeSearcherStub{}
	tool, err := NewSearchKnowledgeTool(searcher)
	if err != nil {
		t.Fatalf("NewSearchKnowledgeTool: %v", err)
	}
	scope := mustTaskScopeWithCapabilities(t, auth.RoleAnalyst, TaskTypeKnowledge, nil,
		[]ToolCapability{ToolCapabilityKnowledge})
	if _, err := tool.InvokableRun(WithTaskScope(context.Background(), scope), `{"query":"问题"}`); !errors.Is(err, ErrToolNotAllowed) {
		t.Fatalf("InvokableRun error = %v, want ErrToolNotAllowed", err)
	}
	if _, err := tool.InvokableRun(context.Background(), `{"query":"问题"}`); !errors.Is(err, ErrTaskScopeRequired) {
		t.Fatalf("unscoped InvokableRun error = %v, want ErrTaskScopeRequired", err)
	}
}

func scopeWithUserID(t *testing.T, original TaskScope, userID uuid.UUID) TaskScope {
	t.Helper()
	scope, err := NewTaskScope(TaskScopeConfig{
		UserID: userID, Role: original.Role(), TaskType: original.TaskType(),
		DataSources: original.DataSources(), AllowedCapabilities: original.AllowedCapabilities(),
		AvailableDependencies: []ToolDependency{ToolDependencyKnowledge},
	})
	if err != nil {
		t.Fatalf("scopeWithUserID: %v", err)
	}
	return scope
}
