package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// SchemaCatalogRepository 读取 PostgreSQL 中已经发布的、管理员审核过的 Catalog。
type SchemaCatalogRepository struct {
	db *gorm.DB
}

func NewSchemaCatalogRepository(db *gorm.DB) *SchemaCatalogRepository {
	return &SchemaCatalogRepository{db: db}
}

var _ repository.SchemaCatalogSearcher = (*SchemaCatalogRepository)(nil)
var _ repository.SchemaCatalogAuthorizer = (*SchemaCatalogRepository)(nil)

// SearchPublished 只查询 published 版本和 queryable 条目。
// keyword 会按对象名、字段名、注释和别名文本做参数化模糊匹配，不接受 SQL 片段。
func (r *SchemaCatalogRepository) SearchPublished(
	ctx context.Context,
	dataSourceID uuid.UUID,
	keyword string,
	limit int,
) ([]repository.SchemaCatalogEntry, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("schema catalog repository is unavailable")
	}
	if dataSourceID == uuid.Nil {
		return nil, errors.New("schema catalog data source id is required")
	}
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, errors.New("schema catalog keyword is required")
	}
	if len([]rune(keyword)) > 128 {
		return nil, errors.New("schema catalog keyword is too long")
	}
	if limit < 1 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	pattern := "%" + escapeLike(strings.ToLower(keyword)) + "%"
	var entries []repository.SchemaCatalogEntry
	query := `
SELECT e.id, e.catalog_version_id, v.data_source_id, v.version AS catalog_version,
       e.object_schema, e.object_name, e.object_type, e.column_name, e.data_type,
       e.nullable, e.comment, e.semantic_aliases, e.queryable, e.sensitivity_level
FROM schema_catalog_entries AS e
JOIN schema_catalog_versions AS v ON v.id = e.catalog_version_id
JOIN data_sources AS ds ON ds.id = v.data_source_id
WHERE v.data_source_id = ?
  AND ds.status = 'active'
  AND v.status = 'published'
  AND e.queryable = true
  AND (
      lower(e.object_name) LIKE ? ESCAPE '\'
      OR lower(coalesce(e.column_name, '')) LIKE ? ESCAPE '\'
      OR lower(coalesce(e.comment, '')) LIKE ? ESCAPE '\'
      OR lower(e.semantic_aliases::text) LIKE ? ESCAPE '\'
  )
ORDER BY e.object_name ASC, e.column_name ASC NULLS FIRST
LIMIT ?`
	if err := ResolveDB(ctx, r.db).Raw(query, dataSourceID, pattern, pattern, pattern, pattern, limit).Scan(&entries).Error; err != nil {
		return nil, fmt.Errorf("search published schema catalog: %w", TranslateError(err))
	}
	return entries, nil
}

// AuthorizePublishedObjects 对 QueryGuard 提取出的对象做精确、全有或全无的授权。
// 字段条目用于检索语义；执行授权只认 column_name IS NULL 的对象级白名单。
func (r *SchemaCatalogRepository) AuthorizePublishedObjects(
	ctx context.Context,
	dataSourceID uuid.UUID,
	objects []repository.SchemaCatalogObjectRef,
) (repository.SchemaCatalogAuthorization, error) {
	if r == nil || r.db == nil {
		return repository.SchemaCatalogAuthorization{}, errors.New("schema catalog repository is unavailable")
	}
	if dataSourceID == uuid.Nil {
		return repository.SchemaCatalogAuthorization{}, errors.New("schema catalog data source id is required")
	}
	objects, err := normalizeCatalogObjectRefs(objects)
	if err != nil {
		return repository.SchemaCatalogAuthorization{}, err
	}

	valueRows := make([]string, 0, len(objects))
	args := make([]any, 0, 1+len(objects)*2)
	args = append(args, dataSourceID)
	for _, object := range objects {
		valueRows = append(valueRows, "(?, ?)")
		args = append(args, object.ObjectSchema, object.ObjectName)
	}
	query := `
WITH requested(object_schema, object_name) AS (
    VALUES ` + strings.Join(valueRows, ", ") + `
)
SELECT v.id AS catalog_version_id, v.version AS catalog_version,
       e.object_schema, e.object_name
FROM schema_catalog_versions AS v
JOIN data_sources AS ds ON ds.id = v.data_source_id
JOIN schema_catalog_entries AS e ON e.catalog_version_id = v.id
JOIN requested AS r
  ON lower(r.object_schema) = lower(e.object_schema)
 AND lower(r.object_name) = lower(e.object_name)
WHERE v.data_source_id = ?
  AND ds.status = 'active'
  AND v.status = 'published'
  AND e.column_name IS NULL
  AND e.queryable = true
ORDER BY lower(e.object_schema), lower(e.object_name)`
	// GORM 按占位符顺序绑定，因此 dataSourceID 要放在 VALUES 参数之后。
	queryArgs := append(args[1:], args[0])
	type authorizedRow struct {
		CatalogVersionID uuid.UUID
		CatalogVersion   int
		ObjectSchema     string
		ObjectName       string
	}
	var rows []authorizedRow
	if err := ResolveDB(ctx, r.db).Raw(query, queryArgs...).Scan(&rows).Error; err != nil {
		return repository.SchemaCatalogAuthorization{}, fmt.Errorf("authorize published schema catalog: %w", TranslateError(err))
	}
	if len(rows) != len(objects) {
		return repository.SchemaCatalogAuthorization{}, repository.ErrSchemaCatalogAuthorizationDenied
	}
	for _, row := range rows {
		if row.CatalogVersionID != rows[0].CatalogVersionID || row.CatalogVersion != rows[0].CatalogVersion {
			return repository.SchemaCatalogAuthorization{}, repository.ErrSchemaCatalogAuthorizationDenied
		}
	}
	authorizedObjects := make([]repository.SchemaCatalogObjectRef, 0, len(rows))
	for _, row := range rows {
		authorizedObjects = append(authorizedObjects, repository.SchemaCatalogObjectRef{
			ObjectSchema: row.ObjectSchema,
			ObjectName:   row.ObjectName,
		})
	}
	return repository.SchemaCatalogAuthorization{
		CatalogVersionID: rows[0].CatalogVersionID,
		CatalogVersion:   rows[0].CatalogVersion,
		Objects:          authorizedObjects,
	}, nil
}

func normalizeCatalogObjectRefs(objects []repository.SchemaCatalogObjectRef) ([]repository.SchemaCatalogObjectRef, error) {
	if len(objects) == 0 {
		return nil, errors.New("schema catalog objects are required")
	}
	if len(objects) > 64 {
		return nil, errors.New("too many schema catalog objects")
	}
	result := make([]repository.SchemaCatalogObjectRef, 0, len(objects))
	seen := make(map[string]struct{}, len(objects))
	for _, object := range objects {
		schemaName := strings.TrimSpace(object.ObjectSchema)
		objectName := strings.TrimSpace(object.ObjectName)
		if schemaName == "" || objectName == "" || schemaName != object.ObjectSchema || objectName != object.ObjectName {
			return nil, errors.New("schema catalog object names must be non-empty and trimmed")
		}
		key := strings.ToLower(schemaName) + "\x00" + strings.ToLower(objectName)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, repository.SchemaCatalogObjectRef{ObjectSchema: schemaName, ObjectName: objectName})
	}
	return result, nil
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}
