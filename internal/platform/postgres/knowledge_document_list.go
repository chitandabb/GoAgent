package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/google/uuid"
)

// ListDocuments 返回 global 范围（企业知识库）文档的分页列表，
// 每行附带最新版本号与最新解析任务状态，按文档创建时间倒序。
// 实现 knowledge.IngestionTaskControlRepository。
func (r *KnowledgeRepository) ListDocuments(
	ctx context.Context,
	page, pageSize int,
) (knowledge.DocumentListPage, error) {
	if r == nil || r.db == nil {
		return knowledge.DocumentListPage{}, errors.New("knowledge document repository is unavailable")
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = knowledge.DefaultDocumentListPageSize
	} else if pageSize > knowledge.MaxDocumentListPageSize {
		pageSize = knowledge.MaxDocumentListPageSize
	}

	db := ResolveDB(ctx, r.db)
	where := " WHERE doc.scope = 'global' AND doc.deleted_at IS NULL"

	var total int64
	if err := db.Raw("SELECT count(*) FROM knowledge_documents doc" + where).Scan(&total).Error; err != nil {
		return knowledge.DocumentListPage{}, TranslateError(err)
	}

	type documentListRow struct {
		DocumentID      uuid.UUID                     `gorm:"column:document_id"`
		Title           string                        `gorm:"column:title"`
		Scope           knowledge.Scope               `gorm:"column:scope"`
		Version         int                           `gorm:"column:version"`
		TaskID          uuid.UUID                     `gorm:"column:task_id"`
		Status          knowledge.IngestionTaskStatus `gorm:"column:status"`
		Stage           knowledge.IngestionStage      `gorm:"column:stage"`
		ProgressPercent int                           `gorm:"column:progress_percent"`
		AttemptCount    int                           `gorm:"column:attempt_count"`
		MaxAttempts     int                           `gorm:"column:max_attempts"`
		CreatedAt       time.Time                     `gorm:"column:created_at"`
	}

	var rows []documentListRow
	query := `
SELECT doc.id AS document_id, doc.title, doc.scope,
       version.version,
       task.id AS task_id, task.status, task.stage, task.progress_percent,
       task.attempt_count, task.max_attempts, doc.created_at
FROM knowledge_documents doc
LEFT JOIN LATERAL (
  SELECT version.id, version.document_id, version.version
  FROM knowledge_document_versions version
  WHERE version.document_id = doc.id
  ORDER BY version.version DESC
  LIMIT 1
) version ON version.document_id = doc.id
LEFT JOIN knowledge_ingestion_tasks task ON task.document_version_id = version.id` + where +
		" ORDER BY doc.created_at DESC LIMIT ? OFFSET ?"
	if err := db.Raw(query, pageSize, (page-1)*pageSize).Scan(&rows).Error; err != nil {
		return knowledge.DocumentListPage{}, TranslateError(err)
	}

	items := make([]knowledge.DocumentListItem, 0, len(rows))
	for index := range rows {
		row := rows[index]
		items = append(items, knowledge.DocumentListItem{
			DocumentID:      row.DocumentID,
			Title:           row.Title,
			Scope:           row.Scope,
			Version:         row.Version,
			TaskID:          row.TaskID,
			Status:          row.Status,
			Stage:           row.Stage,
			ProgressPercent: row.ProgressPercent,
			AttemptCount:    row.AttemptCount,
			MaxAttempts:     row.MaxAttempts,
			CreatedAt:       row.CreatedAt,
		})
	}
	return knowledge.DocumentListPage{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}
