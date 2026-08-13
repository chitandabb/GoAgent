package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/resilience"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/google/uuid"
)

const ToolSearchKnowledge = "search_knowledge"

type KnowledgeSearcher interface {
	Search(context.Context, uuid.UUID, string, int) (knowledge.HybridSearch, error)
}

type searchKnowledgeInput struct {
	Query      string `json:"query" jsonschema:"required,description=要检索的企业知识问题；保留错误码、编号、数值和否定条件"`
	MaxResults int    `json:"maxResults,omitempty" jsonschema:"description=最多返回的证据片段数，默认 8，最大 20"`
}

type searchKnowledgeResult struct {
	DocumentID        string                `json:"documentId"`
	DocumentVersionID string                `json:"documentVersionId"`
	ChunkID           string                `json:"chunkId"`
	Title             string                `json:"title"`
	Scope             knowledge.Scope       `json:"scope"`
	Ordinal           int                   `json:"ordinal"`
	PageNumber        *int                  `json:"pageNumber,omitempty"`
	ElementType       knowledge.ElementType `json:"elementType"`
	SectionPath       []string              `json:"sectionPath,omitempty"`
	ContentText       string                `json:"contentText"`
	ContentSHA256     string                `json:"contentSha256"`
	Score             float64               `json:"score"`
	FTSRank           int                   `json:"ftsRank,omitempty"`
	VectorRank        int                   `json:"vectorRank,omitempty"`
	FusedScore        float64               `json:"fusedScore,omitempty"`
}

type searchKnowledgeContextChunk struct {
	ChunkID       string                `json:"chunkId"`
	Ordinal       int                   `json:"ordinal"`
	PageNumber    *int                  `json:"pageNumber,omitempty"`
	ElementType   knowledge.ElementType `json:"elementType"`
	ContentText   string                `json:"contentText"`
	ContentSHA256 string                `json:"contentSha256"`
}

type searchKnowledgeContextGroup struct {
	DocumentID        string                        `json:"documentId"`
	DocumentVersionID string                        `json:"documentVersionId"`
	SectionPath       []string                      `json:"sectionPath,omitempty"`
	HitChunkIDs       []string                      `json:"hitChunkIds"`
	Chunks            []searchKnowledgeContextChunk `json:"chunks"`
	Truncated         bool                          `json:"truncated"`
}

type searchKnowledgeQueryPlan struct {
	OriginalQuery    string                       `json:"originalQuery"`
	LexicalQuery     string                       `json:"lexicalQuery"`
	SemanticQuery    string                       `json:"semanticQuery"`
	Subqueries       []string                     `json:"subqueries,omitempty"`
	RewriteAttempted bool                         `json:"rewriteAttempted"`
	RewriteStatus    knowledge.QueryRewriteStatus `json:"rewriteStatus"`
	RewriteApplied   bool                         `json:"rewriteApplied"`
	PromptVersion    string                       `json:"promptVersion,omitempty"`
	PromptTokens     int                          `json:"promptTokens,omitempty"`
	CompletionTokens int                          `json:"completionTokens,omitempty"`
	TotalTokens      int                          `json:"totalTokens,omitempty"`
}

type searchKnowledgeContextCompression struct {
	Enabled       bool `json:"enabled"`
	Applied       bool `json:"applied"`
	InputChunks   int  `json:"inputChunks"`
	OutputChunks  int  `json:"outputChunks"`
	InputRunes    int  `json:"inputRunes"`
	OutputRunes   int  `json:"outputRunes"`
	OmittedChunks int  `json:"omittedChunks"`
}

type searchKnowledgeResponse struct {
	Query              string                            `json:"query"`
	QueryPlan          searchKnowledgeQueryPlan          `json:"queryPlan"`
	Results            []searchKnowledgeResult           `json:"results"`
	ContextGroups      []searchKnowledgeContextGroup     `json:"contextGroups,omitempty"`
	Degraded           bool                              `json:"degraded"`
	Sources            []string                          `json:"sources"`
	MissingChannels    []string                          `json:"missingChannels,omitempty"`
	Degradations       []resilience.DegradationEvent     `json:"degradations,omitempty"`
	RerankApplied      bool                              `json:"rerankApplied"`
	RerankTokens       int                               `json:"rerankTotalTokens,omitempty"`
	EmbeddingTokens    int                               `json:"embeddingTotalTokens,omitempty"`
	ContextExpanded    bool                              `json:"contextExpanded"`
	ContextCompression searchKnowledgeContextCompression `json:"contextCompression"`
}

type searchKnowledgeOperationFailure struct {
	Error      string            `json:"error"`
	Operation  string            `json:"operation"`
	Policy     resilience.Policy `json:"policy"`
	ReasonCode string            `json:"reasonCode"`
}

func NewSearchKnowledgeTool(searcher KnowledgeSearcher) (tool.InvokableTool, error) {
	if searcher == nil {
		return nil, errors.New("knowledge searcher is required")
	}
	return toolutils.InferTool(
		ToolSearchKnowledge,
		"检索当前任务授权范围内的企业知识证据；服务端自动完成关键词/语义混合召回和降级，不接受检索后端、向量或对象存储地址参数",
		func(ctx context.Context, input searchKnowledgeInput) (searchKnowledgeResponse, error) {
			scope, ok := TaskScopeFromContext(ctx)
			if !ok {
				return searchKnowledgeResponse{}, ErrTaskScopeRequired
			}
			if !scope.CapabilityAllowed(ToolCapabilityKnowledge) || !scope.DependencyAvailable(ToolDependencyKnowledge) {
				return searchKnowledgeResponse{}, ErrToolNotAllowed
			}
			query := strings.TrimSpace(input.Query)
			if query == "" || query != input.Query || len([]rune(query)) > knowledge.MaxKnowledgeSearchQueryRunes {
				return searchKnowledgeResponse{}, errors.New("query must be non-empty, trimmed, and within the size limit")
			}
			result, err := searcher.Search(ctx, scope.UserID(), query, input.MaxResults)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return searchKnowledgeResponse{}, err
				}
				var operationErr *resilience.OperationError
				if errors.As(err, &operationErr) {
					payload, encodeErr := json.Marshal(searchKnowledgeOperationFailure{
						Error: "tool_operation_failed", Operation: operationErr.Operation,
						Policy: operationErr.Policy, ReasonCode: operationErr.ReasonCode,
					})
					if encodeErr != nil {
						return searchKnowledgeResponse{}, errors.New("knowledge search failure encoding failed")
					}
					return searchKnowledgeResponse{}, errors.New(string(payload))
				}
				return searchKnowledgeResponse{}, errors.New("knowledge search is unavailable")
			}
			for index, item := range result.Results {
				if err := item.Validate(); err != nil {
					return searchKnowledgeResponse{}, fmt.Errorf("knowledge search result %d is invalid", index)
				}
			}
			if result.ContextExpanded != (len(result.ContextGroups) > 0) {
				return searchKnowledgeResponse{}, errors.New("knowledge search context state is invalid")
			}
			contextChunks, contextRunes := contextGroupDimensions(result.ContextGroups)
			if !validContextCompressionObservation(
				result.ContextCompressionEnabled, result.ContextCompressionApplied,
				result.ContextCompression, contextChunks, contextRunes,
			) {
				return searchKnowledgeResponse{}, errors.New("knowledge search context compression state is invalid")
			}
			for index, group := range result.ContextGroups {
				if err := group.Validate(result.Results); err != nil {
					return searchKnowledgeResponse{}, fmt.Errorf("knowledge search context group %d is invalid", index)
				}
			}
			for index, event := range result.Degradations {
				if err := event.Validate(); err != nil {
					return searchKnowledgeResponse{}, fmt.Errorf("knowledge degradation event %d is invalid", index)
				}
			}
			if err := result.QueryPlan.Validate(); err != nil || result.QueryPlan.OriginalQuery != query ||
				!validQueryRewriteObservation(
					result.QueryPlan, result.QueryRewriteStatus,
					result.QueryRewritePromptVersion, result.QueryRewriteUsage,
				) {
				return searchKnowledgeResponse{}, errors.New("knowledge search query plan is invalid")
			}
			response := searchKnowledgeResponse{
				Query: query, Degraded: result.Degraded,
				QueryPlan: searchKnowledgeQueryPlan{
					OriginalQuery: result.QueryPlan.OriginalQuery, LexicalQuery: result.QueryPlan.LexicalQuery,
					SemanticQuery:    result.QueryPlan.SemanticQuery,
					Subqueries:       append([]string(nil), result.QueryPlan.Subqueries...),
					RewriteAttempted: result.QueryRewriteStatus != knowledge.QueryRewriteDisabled,
					RewriteStatus:    result.QueryRewriteStatus,
					RewriteApplied:   result.QueryPlan.RewriteApplied,
					PromptVersion:    result.QueryRewritePromptVersion,
					PromptTokens:     result.QueryRewriteUsage.PromptTokens,
					CompletionTokens: result.QueryRewriteUsage.CompletionTokens,
					TotalTokens:      result.QueryRewriteUsage.TotalTokens,
				},
				Sources:         append([]string(nil), result.Sources...),
				MissingChannels: append([]string(nil), result.MissingChannels...),
				Degradations:    append([]resilience.DegradationEvent(nil), result.Degradations...),
				RerankApplied:   result.RerankApplied, RerankTokens: result.RerankUsage.TotalTokens,
				EmbeddingTokens: result.EmbeddingUsage.TotalTokens,
				ContextExpanded: result.ContextExpanded,
				ContextCompression: searchKnowledgeContextCompression{
					Enabled: result.ContextCompressionEnabled, Applied: result.ContextCompressionApplied,
					InputChunks:   result.ContextCompression.InputChunks,
					OutputChunks:  result.ContextCompression.OutputChunks,
					InputRunes:    result.ContextCompression.InputRunes,
					OutputRunes:   result.ContextCompression.OutputRunes,
					OmittedChunks: result.ContextCompression.OmittedChunks,
				},
				Results:       make([]searchKnowledgeResult, 0, len(result.Results)),
				ContextGroups: make([]searchKnowledgeContextGroup, 0, len(result.ContextGroups)),
			}
			for _, item := range result.Results {
				response.Results = append(response.Results, searchKnowledgeResult{
					DocumentID: item.DocumentID.String(), DocumentVersionID: item.DocumentVersionID.String(),
					ChunkID: item.ChunkID.String(), Title: item.Title, Scope: item.Scope, Ordinal: item.Ordinal,
					PageNumber: item.PageNumber, ElementType: item.ElementType,
					SectionPath: append([]string(nil), item.SectionPath...), ContentText: item.ContentText,
					ContentSHA256: item.ContentSHA256, Score: item.Score, FTSRank: item.FTSRank,
					VectorRank: item.VectorRank, FusedScore: item.FusedScore,
				})
			}
			for _, group := range result.ContextGroups {
				mapped := searchKnowledgeContextGroup{
					DocumentID: group.DocumentID.String(), DocumentVersionID: group.DocumentVersionID.String(),
					SectionPath: append([]string(nil), group.SectionPath...), Truncated: group.Truncated,
					HitChunkIDs: make([]string, 0, len(group.HitChunkIDs)),
					Chunks:      make([]searchKnowledgeContextChunk, 0, len(group.Chunks)),
				}
				for _, chunkID := range group.HitChunkIDs {
					mapped.HitChunkIDs = append(mapped.HitChunkIDs, chunkID.String())
				}
				for _, chunk := range group.Chunks {
					mapped.Chunks = append(mapped.Chunks, searchKnowledgeContextChunk{
						ChunkID: chunk.ChunkID.String(), Ordinal: chunk.Ordinal, PageNumber: chunk.PageNumber,
						ElementType: chunk.ElementType, ContentText: chunk.ContentText, ContentSHA256: chunk.ContentSHA256,
					})
				}
				response.ContextGroups = append(response.ContextGroups, mapped)
			}
			return response, nil
		},
	)
}

// knowledgeSearchEvidenceLocation validates the serialized Tool response a
// second time at the Evidence boundary. This keeps malformed or empty search
// responses from becoming reportable evidence even if the Tool implementation
// changes independently of the trace middleware.
func knowledgeSearchEvidenceLocation(snapshot string) (string, bool) {
	var response searchKnowledgeResponse
	if err := json.Unmarshal([]byte(snapshot), &response); err != nil || len(response.Results) == 0 {
		return "", false
	}
	if !response.QueryPlan.validEvidenceFields(response.Query) {
		return "", false
	}
	for _, item := range response.Results {
		if !item.validEvidenceFields() {
			return "", false
		}
	}
	resultByChunkID := make(map[string]searchKnowledgeResult, len(response.Results))
	for _, item := range response.Results {
		resultByChunkID[item.ChunkID] = item
	}
	if response.ContextExpanded != (len(response.ContextGroups) > 0) {
		return "", false
	}
	compressionStats := knowledge.ContextCompressionStats{
		InputChunks:   response.ContextCompression.InputChunks,
		OutputChunks:  response.ContextCompression.OutputChunks,
		InputRunes:    response.ContextCompression.InputRunes,
		OutputRunes:   response.ContextCompression.OutputRunes,
		OmittedChunks: response.ContextCompression.OmittedChunks,
	}
	contextChunks, contextRunes := serializedContextGroupDimensions(response.ContextGroups)
	if !validContextCompressionObservation(
		response.ContextCompression.Enabled, response.ContextCompression.Applied,
		compressionStats, contextChunks, contextRunes,
	) {
		return "", false
	}
	for _, group := range response.ContextGroups {
		if !group.validEvidenceFields(resultByChunkID) {
			return "", false
		}
	}
	first := response.Results[0]
	return "knowledge:" + first.DocumentVersionID + "/" + first.ChunkID, true
}

func validContextCompressionObservation(
	enabled bool,
	applied bool,
	stats knowledge.ContextCompressionStats,
	outputChunks int,
	outputRunes int,
) bool {
	if stats.Validate() != nil {
		return false
	}
	if !enabled {
		return !applied && stats == (knowledge.ContextCompressionStats{})
	}
	if !applied {
		return stats == (knowledge.ContextCompressionStats{})
	}
	if stats.InputChunks < 1 {
		return false
	}
	return outputChunks == stats.OutputChunks && outputRunes == stats.OutputRunes
}

func contextGroupDimensions(groups []knowledge.SearchContextGroup) (int, int) {
	chunks := 0
	runes := 0
	for _, group := range groups {
		for _, chunk := range group.Chunks {
			chunks++
			runes += len([]rune(chunk.ContentText))
		}
	}
	return chunks, runes
}

func serializedContextGroupDimensions(groups []searchKnowledgeContextGroup) (int, int) {
	chunks := 0
	runes := 0
	for _, group := range groups {
		for _, chunk := range group.Chunks {
			chunks++
			runes += len([]rune(chunk.ContentText))
		}
	}
	return chunks, runes
}

func (p searchKnowledgeQueryPlan) validEvidenceFields(query string) bool {
	planRewriteAttempted := p.RewriteStatus == knowledge.QueryRewriteAccepted
	plan := knowledge.QueryPlan{
		OriginalQuery: p.OriginalQuery, LexicalQuery: p.LexicalQuery, SemanticQuery: p.SemanticQuery,
		Subqueries: append([]string(nil), p.Subqueries...), RewriteAttempted: planRewriteAttempted,
		RewriteApplied: p.RewriteApplied,
	}
	if planRewriteAttempted {
		plan.PromptVersion = p.PromptVersion
		plan.Usage = knowledge.QueryRewriteUsage{
			PromptTokens: p.PromptTokens, CompletionTokens: p.CompletionTokens, TotalTokens: p.TotalTokens,
		}
	}
	usage := knowledge.QueryRewriteUsage{
		PromptTokens: p.PromptTokens, CompletionTokens: p.CompletionTokens, TotalTokens: p.TotalTokens,
	}
	if plan.OriginalQuery != query || plan.Validate() != nil ||
		p.RewriteAttempted != (p.RewriteStatus != knowledge.QueryRewriteDisabled) ||
		!validQueryRewriteObservation(plan, p.RewriteStatus, p.PromptVersion, usage) {
		return false
	}
	return true
}

func validQueryRewriteObservation(
	plan knowledge.QueryPlan,
	status knowledge.QueryRewriteStatus,
	promptVersion string,
	usage knowledge.QueryRewriteUsage,
) bool {
	usageValid := usage.Validate() == nil
	switch status {
	case knowledge.QueryRewriteDisabled:
		return !plan.RewriteAttempted && promptVersion == "" && usage == (knowledge.QueryRewriteUsage{})
	case knowledge.QueryRewriteAccepted:
		return plan.RewriteAttempted && promptVersion == plan.PromptVersion && usage == plan.Usage && usageValid
	case knowledge.QueryRewriteProviderFailed:
		return !plan.RewriteAttempted && promptVersion == "" && usage == (knowledge.QueryRewriteUsage{})
	case knowledge.QueryRewritePolicyRejected:
		return !plan.RewriteAttempted && strings.TrimSpace(promptVersion) != "" &&
			promptVersion == strings.TrimSpace(promptVersion) && len(promptVersion) <= 128 && usageValid
	default:
		return false
	}
}

func (g searchKnowledgeContextGroup) validEvidenceFields(results map[string]searchKnowledgeResult) bool {
	documentID, err := uuid.Parse(g.DocumentID)
	if err != nil {
		return false
	}
	versionID, err := uuid.Parse(g.DocumentVersionID)
	if err != nil || len(g.HitChunkIDs) == 0 || len(g.Chunks) == 0 {
		return false
	}
	hitIDs := make([]uuid.UUID, 0, len(g.HitChunkIDs))
	domainResults := make([]knowledge.SearchResult, 0, len(g.HitChunkIDs))
	for _, chunkID := range g.HitChunkIDs {
		item, exists := results[chunkID]
		if !exists || item.DocumentID != g.DocumentID || item.DocumentVersionID != g.DocumentVersionID {
			return false
		}
		parsedChunkID, err := uuid.Parse(chunkID)
		if err != nil {
			return false
		}
		hitIDs = append(hitIDs, parsedChunkID)
		domainResults = append(domainResults, knowledge.SearchResult{
			DocumentID: documentID, DocumentVersionID: versionID, ChunkID: parsedChunkID,
			Title: item.Title, Scope: item.Scope, Ordinal: item.Ordinal, PageNumber: item.PageNumber,
			ElementType: item.ElementType, SectionPath: append([]string(nil), item.SectionPath...),
			ContentText: item.ContentText, ContentSHA256: item.ContentSHA256,
			Score: item.Score, FTSRank: item.FTSRank, VectorRank: item.VectorRank, FusedScore: item.FusedScore,
		})
	}
	domainGroup := knowledge.SearchContextGroup{
		DocumentID: documentID, DocumentVersionID: versionID,
		SectionPath: append([]string(nil), g.SectionPath...), HitChunkIDs: hitIDs,
		Truncated: g.Truncated, Chunks: make([]knowledge.SearchContextChunk, 0, len(g.Chunks)),
	}
	for _, chunk := range g.Chunks {
		chunkID, err := uuid.Parse(chunk.ChunkID)
		if err != nil {
			return false
		}
		domainGroup.Chunks = append(domainGroup.Chunks, knowledge.SearchContextChunk{
			ChunkID: chunkID, Ordinal: chunk.Ordinal, PageNumber: chunk.PageNumber,
			ElementType: chunk.ElementType, ContentText: chunk.ContentText, ContentSHA256: chunk.ContentSHA256,
		})
	}
	return domainGroup.Validate(domainResults) == nil
}

func (r searchKnowledgeResult) validEvidenceFields() bool {
	if _, err := uuid.Parse(r.DocumentID); err != nil {
		return false
	}
	if _, err := uuid.Parse(r.DocumentVersionID); err != nil {
		return false
	}
	if _, err := uuid.Parse(r.ChunkID); err != nil {
		return false
	}
	if strings.TrimSpace(r.Title) == "" || r.Title != strings.TrimSpace(r.Title) ||
		strings.TrimSpace(r.ContentText) == "" || r.ContentText != strings.TrimSpace(r.ContentText) ||
		r.ContentSHA256 != knowledge.SHA256Hex(r.ContentText) {
		return false
	}
	switch r.Scope {
	case knowledge.ScopeGlobal, knowledge.ScopePersonal:
	default:
		return false
	}
	switch r.ElementType {
	case knowledge.ElementText, knowledge.ElementTable, knowledge.ElementOCRText, knowledge.ElementImageDescription:
	default:
		return false
	}
	if r.Ordinal < 0 || (r.PageNumber != nil && *r.PageNumber < 1) || r.Score != r.Score ||
		r.FTSRank < 0 || r.VectorRank < 0 || r.FusedScore != r.FusedScore {
		return false
	}
	for _, value := range r.SectionPath {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return false
		}
	}
	return true
}
