package httptransport

import (
	"context"
	"errors"

	"github.com/chitandabb/GoAgent/internal/apperror"
	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type knowledgeCitationUseCase interface {
	Get(context.Context, uuid.UUID, uuid.UUID) (knowledge.CitationPreview, error)
}

type KnowledgeCitationRoutes struct {
	useCase knowledgeCitationUseCase
	auth    gin.HandlerFunc
}

func NewKnowledgeCitationRoutes(useCase knowledgeCitationUseCase, authMiddleware gin.HandlerFunc) (*KnowledgeCitationRoutes, error) {
	if useCase == nil || authMiddleware == nil {
		return nil, errors.New("knowledge citation route dependencies are nil")
	}
	return &KnowledgeCitationRoutes{useCase: useCase, auth: authMiddleware}, nil
}

func (r *KnowledgeCitationRoutes) Register(api *gin.RouterGroup) {
	routes := api.Group("/knowledge-citations")
	routes.Use(r.auth)
	routes.GET("/:chunkId", r.get)
}

type knowledgeCitationResponse struct {
	SourceType        string                `json:"sourceType"`
	SourceRef         string                `json:"sourceRef"`
	DocumentID        string                `json:"documentId"`
	DocumentVersionID string                `json:"documentVersionId"`
	ChunkID           string                `json:"chunkId"`
	Title             string                `json:"title"`
	Scope             knowledge.Scope       `json:"scope"`
	Version           int                   `json:"version"`
	Ordinal           int                   `json:"ordinal"`
	PageNumber        *int                  `json:"pageNumber,omitempty"`
	ElementType       knowledge.ElementType `json:"elementType"`
	SectionPath       []string              `json:"sectionPath"`
	ContentText       string                `json:"contentText"`
	ContentSHA256     string                `json:"contentSha256"`
}

func (r *KnowledgeCitationRoutes) get(c *gin.Context) {
	chunkID, err := uuid.Parse(c.Param("chunkId"))
	if err != nil {
		AbortWithError(c, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
			Field: "chunkId", Reason: "必须是合法的 UUID",
		}}))
		return
	}
	identity, ok := identityFromContext(c)
	if !ok {
		AbortWithError(c, apperror.New(apperror.CodeUnauthorized))
		return
	}
	item, err := r.useCase.Get(c.Request.Context(), identity.User.ID, chunkID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			AbortWithError(c, apperror.New(apperror.CodeNotFound))
			return
		}
		AbortWithError(c, apperror.Wrap(apperror.CodeInternal, errors.New("read knowledge citation failed")))
		return
	}
	WriteSuccess(c, knowledgeCitationResponse{
		SourceType: "knowledge_chunk", SourceRef: "knowledge_chunk:" + item.ChunkID.String(),
		DocumentID: item.DocumentID.String(), DocumentVersionID: item.DocumentVersionID.String(),
		ChunkID: item.ChunkID.String(), Title: item.Title, Scope: item.Scope, Version: item.Version,
		Ordinal: item.Ordinal, PageNumber: item.PageNumber, ElementType: item.ElementType,
		SectionPath: item.SectionPath, ContentText: item.ContentText, ContentSHA256: item.ContentSHA256,
	})
}

var _ knowledgeCitationUseCase = (*knowledge.CitationService)(nil)
