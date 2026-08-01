-- +goose Up
CREATE TABLE schema_catalog_versions (
    id UUID PRIMARY KEY,
    data_source_id UUID NOT NULL REFERENCES data_sources(id) ON DELETE RESTRICT,
    version INTEGER NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'draft',
    scan_status VARCHAR(16) NOT NULL DEFAULT 'pending',
    scan_attempt_count INTEGER NOT NULL DEFAULT 0,
    scan_owner VARCHAR(128),
    scan_lease_until TIMESTAMPTZ,
    scan_started_at TIMESTAMPTZ,
    scan_completed_at TIMESTAMPTZ,
    scan_error TEXT,
    source_introspected_at TIMESTAMPTZ,
    created_by UUID REFERENCES users(id) ON DELETE SET NULL,
    published_by UUID REFERENCES users(id) ON DELETE SET NULL,
    published_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT schema_catalog_versions_version_positive CHECK (version > 0),
    CONSTRAINT schema_catalog_versions_status_check CHECK (status IN ('draft', 'published', 'retired')),
    CONSTRAINT schema_catalog_versions_scan_status_check CHECK (scan_status IN ('pending', 'running', 'succeeded', 'failed')),
    CONSTRAINT schema_catalog_versions_scan_attempts_check CHECK (scan_attempt_count >= 0),
    CONSTRAINT schema_catalog_versions_published_fields_check CHECK (
        status <> 'published' OR (published_by IS NOT NULL AND published_at IS NOT NULL)
    )
);

CREATE UNIQUE INDEX schema_catalog_versions_source_version_unique_idx
    ON schema_catalog_versions (data_source_id, version);
CREATE UNIQUE INDEX schema_catalog_one_published_idx
    ON schema_catalog_versions (data_source_id)
    WHERE status = 'published';
CREATE UNIQUE INDEX schema_catalog_one_active_scan_idx
    ON schema_catalog_versions (data_source_id)
    WHERE scan_status IN ('pending', 'running');

CREATE TABLE schema_catalog_entries (
    id UUID PRIMARY KEY,
    catalog_version_id UUID NOT NULL REFERENCES schema_catalog_versions(id) ON DELETE CASCADE,
    object_schema VARCHAR(128) NOT NULL,
    object_name VARCHAR(256) NOT NULL,
    object_type VARCHAR(32) NOT NULL,
    column_name VARCHAR(256),
    data_type VARCHAR(128),
    nullable BOOLEAN,
    comment TEXT,
    semantic_aliases JSONB NOT NULL DEFAULT '[]'::jsonb,
    queryable BOOLEAN NOT NULL DEFAULT true,
    sensitivity_level VARCHAR(32) NOT NULL DEFAULT 'internal',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT schema_catalog_entries_schema_not_blank CHECK (btrim(object_schema) <> ''),
    CONSTRAINT schema_catalog_entries_name_not_blank CHECK (btrim(object_name) <> ''),
    CONSTRAINT schema_catalog_entries_type_not_blank CHECK (btrim(object_type) <> ''),
    CONSTRAINT schema_catalog_entries_comment_or_alias_check CHECK (
        comment IS NOT NULL OR semantic_aliases <> '[]'::jsonb
    ),
    CONSTRAINT schema_catalog_entries_sensitivity_check CHECK (
        sensitivity_level IN ('public', 'internal', 'sensitive', 'restricted')
    )
);

CREATE UNIQUE INDEX schema_catalog_entries_version_object_column_unique_idx
    ON schema_catalog_entries (catalog_version_id, object_schema, object_name, column_name) NULLS NOT DISTINCT;
CREATE INDEX schema_catalog_entries_queryable_name_idx
    ON schema_catalog_entries (catalog_version_id, object_name, queryable);

-- +goose Down
DROP TABLE IF EXISTS schema_catalog_entries;
DROP TABLE IF EXISTS schema_catalog_versions;
