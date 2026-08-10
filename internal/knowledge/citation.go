package knowledge

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
)

type CitationPreview struct {
	DocumentID        uuid.UUID
	DocumentVersionID uuid.UUID
	ChunkID           uuid.UUID
	Title             string
	Scope             Scope
	Version           int
	Ordinal           int
	PageNumber        *int
	ElementType       ElementType
	SectionPath       []string
	ContentText       string
	ContentSHA256     string
}

func (p CitationPreview) Validate() error {
	if p.DocumentID == uuid.Nil || p.DocumentVersionID == uuid.Nil || p.ChunkID == uuid.Nil || p.Version < 1 || p.Ordinal < 0 {
		return errors.New("knowledge citation identity is invalid")
	}
	if strings.TrimSpace(p.Title) == "" || p.Title != strings.TrimSpace(p.Title) {
		return errors.New("knowledge citation title is invalid")
	}
	if p.Scope != ScopeGlobal && p.Scope != ScopePersonal {
		return errors.New("knowledge citation scope is invalid")
	}
	if p.PageNumber != nil && *p.PageNumber < 1 {
		return errors.New("knowledge citation page number is invalid")
	}
	switch p.ElementType {
	case ElementText, ElementTable, ElementOCRText, ElementImageDescription:
	default:
		return errors.New("knowledge citation element type is invalid")
	}
	if strings.TrimSpace(p.ContentText) == "" || p.ContentText != strings.TrimSpace(p.ContentText) ||
		p.ContentSHA256 != SHA256Hex(p.ContentText) {
		return errors.New("knowledge citation content is invalid")
	}
	for _, value := range p.SectionPath {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return errors.New("knowledge citation section path is invalid")
		}
	}
	return nil
}

type CitationRepository interface {
	GetCitation(context.Context, uuid.UUID, uuid.UUID) (CitationPreview, error)
}

type CitationService struct {
	repository CitationRepository
}

func NewCitationService(repository CitationRepository) (*CitationService, error) {
	if repository == nil {
		return nil, errors.New("knowledge citation repository is nil")
	}
	return &CitationService{repository: repository}, nil
}

func (s *CitationService) Get(ctx context.Context, actorID, chunkID uuid.UUID) (CitationPreview, error) {
	if s == nil || s.repository == nil {
		return CitationPreview{}, errors.New("knowledge citation service is unavailable")
	}
	if actorID == uuid.Nil || chunkID == uuid.Nil {
		return CitationPreview{}, errors.New("knowledge citation input is invalid")
	}
	preview, err := s.repository.GetCitation(ctx, actorID, chunkID)
	if err != nil {
		return CitationPreview{}, err
	}
	if err := preview.Validate(); err != nil {
		return CitationPreview{}, err
	}
	return preview, nil
}
