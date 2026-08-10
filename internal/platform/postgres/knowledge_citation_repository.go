package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type KnowledgeCitationRepository struct {
	db *gorm.DB
}

var _ knowledge.CitationRepository = (*KnowledgeCitationRepository)(nil)

func NewKnowledgeCitationRepository(db *gorm.DB) *KnowledgeCitationRepository {
	return &KnowledgeCitationRepository{db: db}
}

func (r *KnowledgeCitationRepository) GetCitation(ctx context.Context, actorID, chunkID uuid.UUID) (knowledge.CitationPreview, error) {
	if r == nil || r.db == nil {
		return knowledge.CitationPreview{}, errors.New("knowledge citation repository is unavailable")
	}
	if actorID == uuid.Nil || chunkID == uuid.Nil {
		return knowledge.CitationPreview{}, errors.New("knowledge citation input is invalid")
	}
	var record knowledgeCitationRecord
	result := ResolveDB(ctx, r.db).Raw(`
SELECT document.id AS document_id, version.id AS document_version_id, chunk.id AS chunk_id,
       document.title, document.scope, version.version, chunk.ordinal, chunk.page_number,
       chunk.element_type, chunk.section_path, chunk.content_text, chunk.content_sha256
FROM knowledge_chunks chunk
JOIN knowledge_document_versions version ON version.id = chunk.document_version_id
JOIN knowledge_documents document ON document.id = version.document_id
WHERE chunk.id = ? AND document.deleted_at IS NULL
  AND version.status IN ('ready', 'retired')
  AND (document.scope = 'global' OR (document.scope = 'personal' AND document.owner_user_id = ?))`,
		chunkID, actorID).Scan(&record)
	if result.Error != nil {
		return knowledge.CitationPreview{}, TranslateError(result.Error)
	}
	if result.RowsAffected == 0 {
		return knowledge.CitationPreview{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
	}
	var sectionPath []string
	if err := json.Unmarshal(record.SectionPath, &sectionPath); err != nil {
		return knowledge.CitationPreview{}, err
	}
	return knowledge.CitationPreview{
		DocumentID: record.DocumentID, DocumentVersionID: record.DocumentVersionID,
		ChunkID: record.ChunkID, Title: record.Title, Scope: record.Scope, Version: record.Version,
		Ordinal: record.Ordinal, PageNumber: record.PageNumber, ElementType: record.ElementType,
		SectionPath: sectionPath, ContentText: record.ContentText, ContentSHA256: record.ContentSHA256,
	}, nil
}

type knowledgeCitationRecord struct {
	DocumentID        uuid.UUID             `gorm:"column:document_id"`
	DocumentVersionID uuid.UUID             `gorm:"column:document_version_id"`
	ChunkID           uuid.UUID             `gorm:"column:chunk_id"`
	Title             string                `gorm:"column:title"`
	Scope             knowledge.Scope       `gorm:"column:scope"`
	Version           int                   `gorm:"column:version"`
	Ordinal           int                   `gorm:"column:ordinal"`
	PageNumber        *int                  `gorm:"column:page_number"`
	ElementType       knowledge.ElementType `gorm:"column:element_type"`
	SectionPath       []byte                `gorm:"column:section_path"`
	ContentText       string                `gorm:"column:content_text"`
	ContentSHA256     string                `gorm:"column:content_sha256"`
}
