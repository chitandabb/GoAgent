package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type KnowledgeRepository struct {
	db *gorm.DB
}

func NewKnowledgeRepository(db *gorm.DB) *KnowledgeRepository {
	return &KnowledgeRepository{db: db}
}

var _ knowledge.Repository = (*KnowledgeRepository)(nil)

func (r *KnowledgeRepository) CreateDocument(
	ctx context.Context,
	input knowledge.CreateDocumentInput,
) (knowledge.Document, error) {
	if r == nil || r.db == nil {
		return knowledge.Document{}, errors.New("knowledge repository is unavailable")
	}
	if err := input.Validate(); err != nil {
		return knowledge.Document{}, err
	}
	createdAt := time.Now().UTC()
	query := `
INSERT INTO knowledge_documents
    (id, scope, owner_user_id, title, created_by, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`
	if err := ResolveDB(ctx, r.db).Exec(
		query, input.ID, input.Scope, input.OwnerUserID, input.Title, input.CreatedBy, createdAt, createdAt,
	).Error; err != nil {
		return knowledge.Document{}, fmt.Errorf("create knowledge document: %w", TranslateError(err))
	}
	return knowledge.Document{
		ID: input.ID, Scope: input.Scope, OwnerUserID: input.OwnerUserID,
		Title: input.Title, CreatedBy: input.CreatedBy, CreatedAt: createdAt,
	}, nil
}

func (r *KnowledgeRepository) PublishVersion(
	ctx context.Context,
	input knowledge.PublishVersionInput,
) (knowledge.DocumentVersion, error) {
	if r == nil || r.db == nil {
		return knowledge.DocumentVersion{}, errors.New("knowledge repository is unavailable")
	}
	if err := input.Validate(); err != nil {
		return knowledge.DocumentVersion{}, err
	}
	parserMetadata := input.ParserMetadata
	if len(parserMetadata) == 0 {
		parserMetadata = json.RawMessage(`{}`)
	}
	completedAt := time.Now().UTC()
	var published knowledge.DocumentVersion
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		var documentExists bool
		if err := tx.Raw(
			"SELECT true FROM knowledge_documents WHERE id = ? AND deleted_at IS NULL FOR UPDATE",
			input.DocumentID,
		).Scan(&documentExists).Error; err != nil {
			return err
		}
		if !documentExists {
			return gorm.ErrRecordNotFound
		}

		var version int
		if err := tx.Raw(
			"SELECT COALESCE(MAX(version), 0) + 1 FROM knowledge_document_versions WHERE document_id = ?",
			input.DocumentID,
		).Scan(&version).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
UPDATE knowledge_document_versions
SET is_current = false, status = 'retired'
WHERE document_id = ? AND is_current = true`, input.DocumentID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
INSERT INTO knowledge_document_versions
    (id, document_id, version, status, is_current, source_media_type,
     source_size_bytes, source_sha256, parser_version, parser_metadata,
     created_by, completed_at, created_at)
VALUES (?, ?, ?, 'ready', true, ?, ?, ?, ?, ?::jsonb, ?, ?, ?)`,
			input.ID, input.DocumentID, version, input.SourceMediaType,
			input.SourceSizeBytes, input.SourceSHA256, input.ParserVersion,
			string(parserMetadata), input.CreatedBy, completedAt, completedAt,
		).Error; err != nil {
			return err
		}
		for ordinal, chunk := range input.Chunks {
			sectionValues := chunk.SectionPath
			if sectionValues == nil {
				sectionValues = []string{}
			}
			sectionPath, err := json.Marshal(sectionValues)
			if err != nil {
				return err
			}
			metadata := chunk.Metadata
			if len(metadata) == 0 {
				metadata = json.RawMessage(`{}`)
			}
			if err := tx.Exec(`
INSERT INTO knowledge_chunks
    (id, document_version_id, ordinal, page_number, element_index, element_type,
     section_path, content_text, search_text, content_sha256, metadata)
VALUES (?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?, ?::jsonb)`,
				uuid.New(), input.ID, ordinal, chunk.PageNumber, chunk.ElementIndex,
				chunk.ElementType, string(sectionPath), chunk.ContentText, chunk.SearchText,
				chunk.ContentSHA256, string(metadata),
			).Error; err != nil {
				return err
			}
		}
		published = knowledge.DocumentVersion{
			ID: input.ID, DocumentID: input.DocumentID, Version: version, CreatedAt: completedAt,
		}
		return nil
	})
	if err != nil {
		return knowledge.DocumentVersion{}, fmt.Errorf("publish knowledge document version: %w", TranslateError(err))
	}
	return published, nil
}

func (r *KnowledgeRepository) SearchFTS(
	ctx context.Context,
	actorID uuid.UUID,
	queryText string,
	limit int,
) ([]knowledge.SearchResult, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("knowledge repository is unavailable")
	}
	if actorID == uuid.Nil {
		return nil, errors.New("knowledge search actor is required")
	}
	if len([]rune(queryText)) > 512 {
		return nil, errors.New("knowledge search query is too long")
	}
	tsQuery, err := knowledge.BuildTSQuery(strings.TrimSpace(queryText))
	if err != nil {
		return nil, err
	}
	if limit < 1 {
		limit = 5
	}
	if limit > 50 {
		limit = 50
	}
	type searchRow struct {
		DocumentID        uuid.UUID
		DocumentVersionID uuid.UUID
		ChunkID           uuid.UUID
		Title             string
		Scope             knowledge.Scope
		Ordinal           int
		PageNumber        *int
		ElementType       knowledge.ElementType
		SectionPathJSON   string
		ContentText       string
		Score             float64
	}
	var rows []searchRow
	searchSQL := `
WITH search_query AS (
    SELECT to_tsquery('simple', ?) AS query
)
SELECT d.id AS document_id, v.id AS document_version_id, c.id AS chunk_id,
       d.title, d.scope, c.ordinal, c.page_number, c.element_type,
       c.section_path::text AS section_path_json, c.content_text,
       ts_rank_cd(c.search_vector, q.query) AS score
FROM knowledge_chunks AS c
JOIN knowledge_document_versions AS v ON v.id = c.document_version_id
JOIN knowledge_documents AS d ON d.id = v.document_id
CROSS JOIN search_query AS q
WHERE d.deleted_at IS NULL
  AND v.status = 'ready'
  AND v.is_current = true
  AND (d.scope = 'global' OR (d.scope = 'personal' AND d.owner_user_id = ?))
  AND c.search_vector @@ q.query
ORDER BY score DESC, d.id, c.ordinal
LIMIT ?`
	if err := ResolveDB(ctx, r.db).Raw(searchSQL, tsQuery, actorID, limit).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("search knowledge chunks: %w", TranslateError(err))
	}
	results := make([]knowledge.SearchResult, 0, len(rows))
	for _, row := range rows {
		var sectionPath []string
		if err := json.Unmarshal([]byte(row.SectionPathJSON), &sectionPath); err != nil {
			return nil, fmt.Errorf("decode knowledge section path: %w", err)
		}
		results = append(results, knowledge.SearchResult{
			DocumentID: row.DocumentID, DocumentVersionID: row.DocumentVersionID,
			ChunkID: row.ChunkID, Title: row.Title, Scope: row.Scope,
			Ordinal: row.Ordinal, PageNumber: row.PageNumber, ElementType: row.ElementType,
			SectionPath: sectionPath, ContentText: row.ContentText, Score: row.Score,
		})
	}
	return results, nil
}
