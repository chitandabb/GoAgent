package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/resilience"
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
	actorID, documentID, versionID, chunkID, contextChunkID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	page := 4
	contextContent := "同时核对事务日志中的超时错误码。"
	queryPlan, err := knowledge.OriginalQueryPlan("事务超时")
	if err != nil {
		t.Fatal(err)
	}
	searcher := &knowledgeSearcherStub{result: knowledge.HybridSearch{
		Results: []knowledge.SearchResult{{
			DocumentID: documentID, DocumentVersionID: versionID, ChunkID: chunkID,
			Title: "生产手册", Scope: knowledge.ScopeGlobal, Ordinal: 2, PageNumber: &page,
			ElementType: knowledge.ElementText, SectionPath: []string{"网络", "超时"},
			ContentText: "事务超时需要检查连接池。", ContentSHA256: knowledge.SHA256Hex("事务超时需要检查连接池。"),
			Score: 0.92, FTSRank: 1, VectorRank: 2, FusedScore: 0.03,
		}},
		ContextGroups: []knowledge.SearchContextGroup{{
			DocumentID: documentID, DocumentVersionID: versionID, SectionPath: []string{"网络", "超时"},
			HitChunkIDs: []uuid.UUID{chunkID}, Chunks: []knowledge.SearchContextChunk{{
				ChunkID: contextChunkID, Ordinal: 3, PageNumber: &page, ElementType: knowledge.ElementText,
				ContentText: contextContent, ContentSHA256: knowledge.SHA256Hex(contextContent),
			}},
		}},
		QueryPlan: queryPlan, QueryRewriteStatus: knowledge.QueryRewriteDisabled,
		ContextExpanded: true, ContextCompressionEnabled: true, ContextCompressionApplied: true,
		ContextCompression: knowledge.ContextCompressionStats{
			InputChunks: 2, OutputChunks: 1, InputRunes: len([]rune(contextContent)) + 10,
			OutputRunes: len([]rune(contextContent)), OmittedChunks: 1,
		},
		Degraded: true,
		Sources:  []string{"fts"}, MissingChannels: []string{"vector"},
		Degradations: []resilience.DegradationEvent{{
			Operation: "vector_retrieval", Policy: resilience.PolicyBestEffort,
			Fallback: "fts", ReasonCode: "dependency_unavailable", RunID: "retrieval-42",
		}},
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
		Query     string `json:"query"`
		QueryPlan struct {
			OriginalQuery string `json:"originalQuery"`
		} `json:"queryPlan"`
		Results            []map[string]any              `json:"results"`
		Degraded           bool                          `json:"degraded"`
		Sources            []string                      `json:"sources"`
		MissingChannels    []string                      `json:"missingChannels"`
		Degradations       []resilience.DegradationEvent `json:"degradations"`
		ContextExpanded    bool                          `json:"contextExpanded"`
		ContextGroups      []map[string]any              `json:"contextGroups"`
		ContextCompression struct {
			Enabled       bool `json:"enabled"`
			Applied       bool `json:"applied"`
			OmittedChunks int  `json:"omittedChunks"`
		} `json:"contextCompression"`
	}
	if err := json.Unmarshal([]byte(encoded), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Query != "事务超时" || response.QueryPlan.OriginalQuery != "事务超时" ||
		len(response.Results) != 1 || !response.Degraded ||
		len(response.Sources) != 1 || response.Sources[0] != "fts" || len(response.MissingChannels) != 1 ||
		!response.ContextExpanded || len(response.ContextGroups) != 1 ||
		!response.ContextCompression.Enabled || !response.ContextCompression.Applied ||
		response.ContextCompression.OmittedChunks != 1 || len(response.Degradations) != 1 ||
		response.Degradations[0].Operation != "vector_retrieval" {
		t.Fatalf("response = %s", encoded)
	}
	if searcher.actorID != actorID || searcher.query != "事务超时" || searcher.limit != 3 {
		t.Fatalf("search request = actor=%s query=%q limit=%d", searcher.actorID, searcher.query, searcher.limit)
	}
	if _, ok := knowledgeSearchEvidenceLocation(encoded); !ok {
		t.Fatal("valid compressed knowledge response was rejected at the evidence boundary")
	}
	citationSources, ok := conversationCitationSourcesFromTool(ToolSearchKnowledge, encoded)
	if !ok || len(citationSources) != 2 ||
		citationSources[0].SourceRef != "knowledge:"+versionID.String()+"/"+chunkID.String() ||
		citationSources[1].SourceRef != "knowledge:"+versionID.String()+"/"+contextChunkID.String() {
		t.Fatalf("citation sources = %+v, ok=%v", citationSources, ok)
	}
	var tampered searchKnowledgeResponse
	if err := json.Unmarshal([]byte(encoded), &tampered); err != nil {
		t.Fatal(err)
	}
	tampered.ContextCompression.OutputChunks++
	tamperedSnapshot, err := json.Marshal(tampered)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := knowledgeSearchEvidenceLocation(string(tamperedSnapshot)); ok {
		t.Fatal("evidence boundary accepted inconsistent context compression stats")
	}
}

func TestSearchKnowledgeToolRejectsMalformedCitationFields(t *testing.T) {
	result := knowledge.SearchResult{
		DocumentID: uuid.New(), DocumentVersionID: uuid.New(), ChunkID: uuid.New(),
		Title: "生产手册", Scope: knowledge.ScopeGlobal, ElementType: knowledge.ElementText,
		ContentText: "事务超时需要检查连接池。", ContentSHA256: knowledge.SHA256Hex("other"),
	}
	queryPlan, err := knowledge.OriginalQueryPlan("事务超时")
	if err != nil {
		t.Fatal(err)
	}
	tool, err := NewSearchKnowledgeTool(&knowledgeSearcherStub{result: knowledge.HybridSearch{
		Results: []knowledge.SearchResult{result}, QueryPlan: queryPlan,
		QueryRewriteStatus: knowledge.QueryRewriteDisabled,
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

func TestSearchKnowledgeToolPreservesSafeStructuredOperationFailure(t *testing.T) {
	tool, err := NewSearchKnowledgeTool(&knowledgeSearcherStub{err: &resilience.OperationError{
		Operation: "knowledge_retrieval", Policy: resilience.PolicyBestEffort,
		ReasonCode: "all_channels_failed", Err: errors.New("database host is secret"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	scope := mustTaskScopeWithCapabilities(t, auth.RoleAnalyst, TaskTypeKnowledge, nil,
		[]ToolCapability{ToolCapabilityKnowledge}, ToolDependencyKnowledge)
	_, err = tool.InvokableRun(WithTaskScope(context.Background(), scope), `{"query":"连接池超时"}`)
	if err == nil {
		t.Fatal("expected structured Tool failure")
	}
	var payload struct {
		Error      string            `json:"error"`
		Operation  string            `json:"operation"`
		Policy     resilience.Policy `json:"policy"`
		ReasonCode string            `json:"reasonCode"`
	}
	encodedError := err.Error()
	payloadStart := strings.IndexByte(encodedError, '{')
	if payloadStart < 0 {
		t.Fatalf("error has no structured payload: %v", err)
	}
	if decodeErr := json.Unmarshal([]byte(encodedError[payloadStart:]), &payload); decodeErr != nil ||
		payload.Error != "tool_operation_failed" || payload.Operation != "knowledge_retrieval" ||
		payload.Policy != resilience.PolicyBestEffort || payload.ReasonCode != "all_channels_failed" ||
		strings.Contains(err.Error(), "database host is secret") {
		t.Fatalf("error = %v decodeErr=%v payload=%+v", err, decodeErr, payload)
	}
}

func TestQueryRewriteObservationRejectsInconsistentTokenUsage(t *testing.T) {
	plan, err := knowledge.OriginalQueryPlan("connection timeout")
	if err != nil {
		t.Fatal(err)
	}
	if validQueryRewriteObservation(
		plan, knowledge.QueryRewritePolicyRejected, "query-rewrite-v1",
		knowledge.QueryRewriteUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 10},
	) {
		t.Fatal("query rewrite observation accepted inconsistent token usage")
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
