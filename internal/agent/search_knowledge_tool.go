package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/chitandabb/GoAgent/internal/knowledge"
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

type searchKnowledgeResponse struct {
	Query           string                  `json:"query"`
	Results         []searchKnowledgeResult `json:"results"`
	Degraded        bool                    `json:"degraded"`
	Sources         []string                `json:"sources"`
	MissingChannels []string                `json:"missingChannels,omitempty"`
	RerankApplied   bool                    `json:"rerankApplied"`
	RerankTokens    int                     `json:"rerankTotalTokens,omitempty"`
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
				return searchKnowledgeResponse{}, errors.New("knowledge search is unavailable")
			}
			for index, item := range result.Results {
				if err := item.Validate(); err != nil {
					return searchKnowledgeResponse{}, fmt.Errorf("knowledge search result %d is invalid", index)
				}
			}
			response := searchKnowledgeResponse{
				Query: query, Degraded: result.Degraded,
				Sources:         append([]string(nil), result.Sources...),
				MissingChannels: append([]string(nil), result.MissingChannels...),
				RerankApplied:   result.RerankApplied, RerankTokens: result.RerankUsage.TotalTokens,
				Results: make([]searchKnowledgeResult, 0, len(result.Results)),
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
	for _, item := range response.Results {
		if !item.validEvidenceFields() {
			return "", false
		}
	}
	first := response.Results[0]
	return "knowledge:" + first.DocumentVersionID + "/" + first.ChunkID, true
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
