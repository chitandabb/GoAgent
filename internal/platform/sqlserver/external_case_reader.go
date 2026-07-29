package sqlserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chitandabb/GoAgent/internal/externalcase"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformlogger "github.com/chitandabb/GoAgent/internal/platform/logger"
	"github.com/chitandabb/GoAgent/internal/repository"

	"go.uber.org/zap"
)

type ExternalCaseReader struct {
	db               *sql.DB
	cfg              config.SQLServerConfig
	log              *zap.Logger
	caseFields       map[string]string
	attachmentFields map[string]string
	attributeKeys    []string
}

var _ externalcase.Reader = (*ExternalCaseReader)(nil)

func NewExternalCaseReader(db *sql.DB, cfg config.SQLServerConfig, log *zap.Logger) (*ExternalCaseReader, error) {
	if db == nil || log == nil {
		return nil, errors.New("sqlserver reader dependencies are nil")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	attributeKeys := make([]string, 0, len(cfg.CaseMapping.Attributes))
	for key := range cfg.CaseMapping.Attributes {
		attributeKeys = append(attributeKeys, key)
	}
	sort.Strings(attributeKeys)
	return &ExternalCaseReader{
		db: db, cfg: cfg, log: log,
		caseFields:       cfg.CaseMapping.Fields,
		attachmentFields: cfg.AttachmentMapping.Fields,
		attributeKeys:    attributeKeys,
	}, nil
}

var caseFieldSpecs = []fieldSpec{
	{"externalCaseKey", "NVARCHAR(128)"}, {"caseType", "NVARCHAR(64)"},
	{"title", "NVARCHAR(4000)"}, {"description", "NVARCHAR(MAX)"},
	{"category", "NVARCHAR(128)"}, {"module", "NVARCHAR(128)"},
	{"status", "NVARCHAR(64)"}, {"priority", "NVARCHAR(64)"},
	{"occurredAt", "DATETIME2"}, {"reportedAt", "DATETIME2"},
	{"sourceUpdatedAt", "DATETIME2"}, {"customerCode", "NVARCHAR(128)"},
	{"customerName", "NVARCHAR(256)"}, {"productCode", "NVARCHAR(128)"},
	{"productName", "NVARCHAR(256)"}, {"productVersion", "NVARCHAR(128)"},
	{"workOrderNo", "NVARCHAR(128)"}, {"workpieceNo", "NVARCHAR(128)"},
	{"materialCode", "NVARCHAR(128)"}, {"batchNo", "NVARCHAR(128)"},
	{"serialNo", "NVARCHAR(256)"}, {"factoryCode", "NVARCHAR(128)"},
	{"workshopCode", "NVARCHAR(128)"}, {"productionLineCode", "NVARCHAR(128)"},
	{"workstationCode", "NVARCHAR(128)"}, {"equipmentCode", "NVARCHAR(128)"},
	{"sourceSystem", "NVARCHAR(128)"}, {"deploymentEnvironment", "NVARCHAR(128)"},
	{"businessDatabaseAlias", "NVARCHAR(256)"},
}

type fieldSpec struct{ canonical, sqlType string }

func (r *ExternalCaseReader) List(ctx context.Context, query externalcase.ListQuery) (externalcase.ListResult, error) {
	startedAt := time.Now()
	queryCtx, cancel := r.queryContext(ctx)
	defer cancel()
	where, args := r.caseWhere(query)

	countSQL := "SELECT COUNT_BIG(1) FROM " + quoteRelation(r.cfg.CaseMapping.Relation) + where
	var total int64
	if err := r.db.QueryRowContext(queryCtx, countSQL, args...).Scan(&total); err != nil {
		r.logFailure(ctx, "list_count", startedAt, err)
		return externalcase.ListResult{}, errors.Join(externalcase.ErrUnavailable, err)
	}
	if total == 0 {
		r.logSuccess(ctx, "list", startedAt, 0)
		return externalcase.ListResult{Items: []externalcase.ExternalCase{}, Total: 0}, nil
	}

	pageArgs := append(append([]any{}, args...),
		sql.Named("offset", (query.Page-1)*query.PageSize), sql.Named("limit", query.PageSize))
	sortField, sortOrder := r.sort(query.SortBy, query.SortOrder)
	orderBy := quoteIdentifier(sortField) + " " + sortOrder
	if sortField != r.caseFields["externalCaseKey"] {
		orderBy += ", " + quoteIdentifier(r.caseFields["externalCaseKey"]) + " ASC"
	}
	statement := "SELECT " + r.caseProjection() + " FROM " + quoteRelation(r.cfg.CaseMapping.Relation) + where +
		" ORDER BY " + orderBy + " OFFSET @offset ROWS FETCH NEXT @limit ROWS ONLY"
	rows, err := r.db.QueryContext(queryCtx, statement, pageArgs...)
	if err != nil {
		r.logFailure(ctx, "list", startedAt, err)
		return externalcase.ListResult{}, errors.Join(externalcase.ErrUnavailable, err)
	}
	items, err := r.scanCases(rows)
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		r.logFailure(ctx, "list_scan", startedAt, err)
		return externalcase.ListResult{}, errors.Join(externalcase.ErrUnavailable, err)
	}
	if err := r.loadAttachments(queryCtx, items); err != nil {
		r.logFailure(ctx, "list_attachments", startedAt, err)
		return externalcase.ListResult{}, errors.Join(externalcase.ErrUnavailable, err)
	}
	if resultSize(items) > r.cfg.MaxResultBytes {
		return externalcase.ListResult{}, externalcase.ErrResultLimit
	}
	r.logSuccess(ctx, "list", startedAt, len(items))
	return externalcase.ListResult{Items: items, Total: int(total)}, nil
}

func (r *ExternalCaseReader) sort(sortBy, sortOrder string) (string, string) {
	field := r.caseFields["reportedAt"]
	switch sortBy {
	case "sourceUpdatedAt":
		field = r.caseFields["sourceUpdatedAt"]
	case "externalCaseKey":
		field = r.caseFields["externalCaseKey"]
	}
	order := "DESC"
	if strings.EqualFold(sortOrder, "asc") {
		order = "ASC"
	}
	return field, order
}

func (r *ExternalCaseReader) GetByKey(ctx context.Context, key string) (*externalcase.ExternalCase, error) {
	startedAt := time.Now()
	queryCtx, cancel := r.queryContext(ctx)
	defer cancel()
	statement := "SELECT " + r.caseProjection() + " FROM " + quoteRelation(r.cfg.CaseMapping.Relation) +
		" WHERE " + quoteIdentifier(r.caseFields["externalCaseKey"]) + " = @key"
	rows, err := r.db.QueryContext(queryCtx, statement, sql.Named("key", key))
	if err != nil {
		r.logFailure(ctx, "get", startedAt, err)
		return nil, errors.Join(externalcase.ErrUnavailable, err)
	}
	items, err := r.scanCases(rows)
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, errors.Join(externalcase.ErrUnavailable, err)
	}
	if len(items) == 0 {
		return nil, repository.ErrNotFound
	}
	if err := r.loadAttachments(queryCtx, items); err != nil {
		return nil, errors.Join(externalcase.ErrUnavailable, err)
	}
	if resultSize(items) > r.cfg.MaxResultBytes {
		return nil, externalcase.ErrResultLimit
	}
	r.logSuccess(ctx, "get", startedAt, 1)
	return &items[0], nil
}

func (r *ExternalCaseReader) queryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, time.Duration(r.cfg.QueryTimeoutMillis)*time.Millisecond)
}

func (r *ExternalCaseReader) caseProjection() string {
	parts := make([]string, 0, len(caseFieldSpecs))
	for _, field := range caseFieldSpecs {
		if source := r.caseFields[field.canonical]; source != "" {
			parts = append(parts, quoteIdentifier(source)+" AS "+quoteIdentifier(field.canonical))
		} else {
			parts = append(parts, "CAST(NULL AS "+field.sqlType+") AS "+quoteIdentifier(field.canonical))
		}
	}
	for _, key := range r.attributeKeys {
		parts = append(parts, quoteIdentifier(r.cfg.CaseMapping.Attributes[key])+" AS "+quoteIdentifier(key))
	}
	return strings.Join(parts, ", ")
}

func (r *ExternalCaseReader) caseWhere(query externalcase.ListQuery) (string, []any) {
	conditions := []string{"1 = 1"}
	args := make([]any, 0, 8)
	if keyword := strings.TrimSpace(query.Keyword); keyword != "" {
		columns := []string{r.caseFields["externalCaseKey"], r.caseFields["title"], r.caseFields["customerName"]}
		likes := make([]string, 0, len(columns))
		for _, column := range columns {
			if column != "" {
				likes = append(likes, quoteIdentifier(column)+" LIKE @keyword ESCAPE '\\'")
			}
		}
		conditions = append(conditions, "("+strings.Join(likes, " OR ")+")")
		args = append(args, sql.Named("keyword", "%"+escapeLike(keyword)+"%"))
	}
	conditions, args = appendMappedFilter(conditions, args, r.caseFields["status"], "status", string(query.Status), r.cfg.CaseMapping.StatusValues)
	conditions, args = appendMappedFilter(conditions, args, r.caseFields["priority"], "priority", string(query.Priority), r.cfg.CaseMapping.PriorityValues)
	if query.ReportedFrom != nil {
		conditions = append(conditions, quoteIdentifier(r.caseFields["reportedAt"])+" >= @reportedFrom")
		args = append(args, sql.Named("reportedFrom", query.ReportedFrom.UTC()))
	}
	if query.ReportedTo != nil {
		conditions = append(conditions, quoteIdentifier(r.caseFields["reportedAt"])+" <= @reportedTo")
		args = append(args, sql.Named("reportedTo", query.ReportedTo.UTC()))
	}
	if caseType := strings.TrimSpace(query.CaseType); caseType != "" {
		conditions = append(conditions, quoteIdentifier(r.caseFields["caseType"])+" = @caseType")
		args = append(args, sql.Named("caseType", caseType))
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

func appendMappedFilter(conditions []string, args []any, column, prefix, normalized string, mappings map[string]string) ([]string, []any) {
	if normalized == "" {
		return conditions, args
	}
	if column == "" {
		return append(conditions, "1 = 0"), args
	}
	rawValues := make([]string, 0)
	for raw, value := range mappings {
		if value == normalized {
			rawValues = append(rawValues, raw)
		}
	}
	sort.Strings(rawValues)
	if len(rawValues) == 0 {
		return append(conditions, "1 = 0"), args
	}
	placeholders := make([]string, 0, len(rawValues))
	for i, raw := range rawValues {
		name := fmt.Sprintf("%s%d", prefix, i)
		placeholders = append(placeholders, "@"+name)
		args = append(args, sql.Named(name, raw))
	}
	return append(conditions, quoteIdentifier(column)+" IN ("+strings.Join(placeholders, ", ")+")"), args
}

func (r *ExternalCaseReader) scanCases(rows *sql.Rows) ([]externalcase.ExternalCase, error) {
	items := make([]externalcase.ExternalCase, 0)
	for rows.Next() {
		var values [29]sql.NullString
		var occurred, reported, updated sql.NullTime
		destinations := []any{
			&values[0], &values[1], &values[2], &values[3], &values[4], &values[5], &values[6], &values[7],
			&occurred, &reported, &updated,
			&values[8], &values[9], &values[10], &values[11], &values[12], &values[13], &values[14],
			&values[15], &values[16], &values[17], &values[18], &values[19], &values[20], &values[21],
			&values[22], &values[23], &values[24], &values[25],
		}
		attributeValues := make([]sql.NullString, len(r.attributeKeys))
		for i := range attributeValues {
			destinations = append(destinations, &attributeValues[i])
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, err
		}
		item := externalcase.ExternalCase{
			ExternalCaseKey: values[0].String, CaseType: values[1].String, Title: values[2].String,
			Description: values[3].String, Category: values[4].String, Module: values[5].String,
			Status:       normalizeStatus(values[6].String, r.cfg.CaseMapping.StatusValues),
			Priority:     normalizePriority(values[7].String, r.cfg.CaseMapping.PriorityValues),
			SourceStatus: values[6].String, SourcePriority: values[7].String,
			ReportedAt: reported.Time, SourceUpdatedAt: updated.Time,
			Customer: externalcase.CustomerContext{Code: values[8].String, Name: values[9].String},
			Product:  externalcase.ProductContext{Code: values[10].String, Name: values[11].String, Version: values[12].String},
			Production: externalcase.ProductionContext{
				WorkOrderNo: values[13].String, WorkpieceNo: values[14].String, MaterialCode: values[15].String,
				BatchNo: values[16].String, SerialNo: values[17].String, FactoryCode: values[18].String,
				WorkshopCode: values[19].String, ProductionLineCode: values[20].String,
				WorkstationCode: values[21].String, EquipmentCode: values[22].String,
			},
			Environment: externalcase.EnvironmentContext{
				SourceSystem: values[23].String, DeploymentEnvironment: values[24].String,
				BusinessDatabaseAlias: values[25].String,
			},
			Attributes: map[string]any{}, Attachments: []externalcase.ExternalAttachment{},
		}
		item.Description, item.Truncated = truncateUTF8(item.Description, r.cfg.MaxTextBytes)
		if item.Status == "" || (item.SourcePriority != "" && item.Priority == "") {
			return nil, fmt.Errorf("ERP status or priority value is not mapped")
		}
		for i, key := range r.attributeKeys {
			if attributeValues[i].Valid {
				value, truncated := truncateUTF8(attributeValues[i].String, r.cfg.MaxTextBytes)
				item.Attributes[key] = value
				item.Truncated = item.Truncated || truncated
			}
		}
		if occurred.Valid {
			value := occurred.Time
			item.OccurredAt = &value
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *ExternalCaseReader) loadAttachments(ctx context.Context, items []externalcase.ExternalCase) error {
	if len(items) == 0 {
		return nil
	}
	args := make([]any, 0, len(items))
	placeholders := make([]string, 0, len(items))
	byKey := make(map[string]int, len(items))
	for i := range items {
		name := fmt.Sprintf("case%d", i)
		placeholders = append(placeholders, "@"+name)
		args = append(args, sql.Named(name, items[i].ExternalCaseKey))
		byKey[items[i].ExternalCaseKey] = i
	}
	fields := []string{"externalCaseKey", "externalAttachmentKey", "fileName", "mediaType", "sizeBytes", "objectKey", "contentHash", "sourceUpdatedAt"}
	projection := make([]string, 0, len(fields))
	for _, field := range fields {
		projection = append(projection, quoteIdentifier(r.attachmentFields[field]))
	}
	statement := "SELECT " + strings.Join(projection, ", ") + " FROM " + quoteRelation(r.cfg.AttachmentMapping.Relation) +
		" WHERE " + quoteIdentifier(r.attachmentFields["externalCaseKey"]) + " IN (" + strings.Join(placeholders, ", ") + ")" +
		" ORDER BY " + quoteIdentifier(r.attachmentFields["externalCaseKey"]) + ", " + quoteIdentifier(r.attachmentFields["externalAttachmentKey"])
	rows, err := r.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var caseKey, attachmentKey, fileName, mediaType, objectKey, contentHash string
		var sizeBytes int64
		var updated time.Time
		if err := rows.Scan(&caseKey, &attachmentKey, &fileName, &mediaType, &sizeBytes, &objectKey, &contentHash, &updated); err != nil {
			return err
		}
		if index, ok := byKey[caseKey]; ok {
			items[index].Attachments = append(items[index].Attachments, externalcase.ExternalAttachment{
				ExternalAttachmentKey: attachmentKey, FileName: fileName, MediaType: mediaType,
				SizeBytes: sizeBytes, ObjectKey: objectKey, ContentHash: contentHash, SourceUpdatedAt: updated,
			})
		}
	}
	return rows.Err()
}

func normalizeStatus(raw string, values map[string]string) externalcase.Status {
	return externalcase.Status(values[raw])
}

func normalizePriority(raw string, values map[string]string) externalcase.Priority {
	return externalcase.Priority(values[raw])
}

func quoteRelation(relation string) string {
	parts := strings.Split(relation, ".")
	return quoteIdentifier(parts[0]) + "." + quoteIdentifier(parts[1])
}

func quoteIdentifier(identifier string) string { return "[" + identifier + "]" }

func escapeLike(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_", "[", "\\[")
	return replacer.Replace(value)
}

func truncateUTF8(value string, maxBytes int) (string, bool) {
	if len(value) <= maxBytes {
		return value, false
	}
	bytes := []byte(value)
	cut := maxBytes
	for cut > 0 && !utf8.Valid(bytes[:cut]) {
		cut--
	}
	return string(bytes[:cut]), true
}

func resultSize(items []externalcase.ExternalCase) int {
	total := 0
	for _, item := range items {
		total += len(item.ExternalCaseKey + item.Title + item.Description + item.Category + item.Module)
		for _, attachment := range item.Attachments {
			total += len(attachment.ExternalAttachmentKey + attachment.FileName + attachment.MediaType + attachment.ContentHash)
		}
	}
	return total
}

func (r *ExternalCaseReader) logSuccess(ctx context.Context, operation string, started time.Time, rows int) {
	platformlogger.FromContext(ctx, r.log).Info("SQL Server query completed",
		zap.String("operation", operation), zap.Duration("duration", time.Since(started)), zap.Int("row_count", rows))
}

func (r *ExternalCaseReader) logFailure(ctx context.Context, operation string, started time.Time, err error) {
	platformlogger.FromContext(ctx, r.log).Warn("SQL Server query failed",
		zap.String("operation", operation), zap.Duration("duration", time.Since(started)), zap.Error(err))
}
