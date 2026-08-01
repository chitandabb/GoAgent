package sqlserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformlogger "github.com/chitandabb/GoAgent/internal/platform/logger"
	"github.com/chitandabb/GoAgent/internal/repository"
	"go.uber.org/zap"
)

var objectIdentifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ObjectDefinitionReader 是 SQL 调查 Tool 所需的窄数据库能力，不暴露 *sql.DB。
type ObjectDefinitionReader struct {
	db             *sql.DB
	queryTimeout   time.Duration
	maxTextBytes   int
	allowedSchemas []string
	log            *zap.Logger
}

func NewObjectDefinitionReader(db *sql.DB, cfg config.SQLServerConfig, log *zap.Logger) (*ObjectDefinitionReader, error) {
	if db == nil || log == nil {
		return nil, errors.New("object definition reader dependencies are nil")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if len(cfg.Investigation.AllowedSchemas) == 0 {
		return nil, errors.New("sqlserver investigation allowedSchemas is empty")
	}
	allowedSchemas := append([]string(nil), cfg.Investigation.AllowedSchemas...)
	return &ObjectDefinitionReader{
		db: db, queryTimeout: time.Duration(cfg.QueryTimeoutMillis) * time.Millisecond,
		maxTextBytes: cfg.MaxTextBytes, allowedSchemas: allowedSchemas, log: log,
	}, nil
}

func (r *ObjectDefinitionReader) GetObjectDefinition(
	ctx context.Context,
	schemaName string,
	objectName string,
) (definition string, objectType string, truncated bool, err error) {
	if r == nil || r.db == nil {
		return "", "", false, errors.New("object definition reader is nil")
	}
	schemaName = strings.TrimSpace(schemaName)
	objectName = strings.TrimSpace(objectName)
	if !objectIdentifierPattern.MatchString(schemaName) || !objectIdentifierPattern.MatchString(objectName) {
		return "", "", false, errors.New("schema and objectName must be simple SQL identifiers")
	}
	if !slices.Contains(r.allowedSchemas, schemaName) {
		return "", "", false, errors.New("schema is not allowed by the data source policy")
	}
	startedAt := time.Now()
	queryCtx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()
	const statement = `
SELECT o.type_desc, OBJECT_DEFINITION(o.object_id)
FROM sys.objects AS o
JOIN sys.schemas AS s ON s.schema_id = o.schema_id
WHERE s.name = @schema AND o.name = @objectName
  AND o.is_ms_shipped = 0
  AND o.type IN ('P', 'V', 'FN', 'IF', 'TF')`
	var rawDefinition sql.NullString
	if err := r.db.QueryRowContext(
		queryCtx, statement,
		sql.Named("schema", schemaName), sql.Named("objectName", objectName),
	).Scan(&objectType, &rawDefinition); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			r.logFailure(ctx, startedAt, schemaName, objectName, err)
			return "", "", false, repository.ErrNotFound
		}
		r.logFailure(ctx, startedAt, schemaName, objectName, err)
		return "", "", false, err
	}
	if !rawDefinition.Valid || strings.TrimSpace(rawDefinition.String) == "" {
		return "", "", false, fmt.Errorf("database object %s.%s has no readable definition", schemaName, objectName)
	}
	definition, truncated = truncateUTF8(rawDefinition.String, r.maxTextBytes)
	r.logSuccess(ctx, startedAt, schemaName, objectName, objectType, len(definition), truncated)
	return definition, objectType, truncated, nil
}

func (r *ObjectDefinitionReader) logSuccess(
	ctx context.Context,
	started time.Time,
	schemaName, objectName, objectType string,
	bytes int,
	truncated bool,
) {
	platformlogger.FromContext(ctx, r.log).Info("SQL object definition read",
		zap.String("schema", schemaName), zap.String("object_name", objectName), zap.String("object_type", objectType),
		zap.Duration("duration", time.Since(started)), zap.Int("result_bytes", bytes), zap.Bool("truncated", truncated))
}

func (r *ObjectDefinitionReader) logFailure(
	ctx context.Context,
	started time.Time,
	schemaName, objectName string,
	err error,
) {
	platformlogger.FromContext(ctx, r.log).Warn("SQL object definition read failed",
		zap.String("schema", schemaName), zap.String("object_name", objectName),
		zap.Duration("duration", time.Since(started)), zap.Error(err))
}
