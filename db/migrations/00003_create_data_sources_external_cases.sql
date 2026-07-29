-- +goose Up
CREATE TABLE data_sources (
    id UUID PRIMARY KEY,
    code VARCHAR(64) NOT NULL,
    name VARCHAR(128) NOT NULL,
    source_type VARCHAR(32) NOT NULL,
    source_role VARCHAR(32) NOT NULL,
    environment VARCHAR(32) NOT NULL,
    safety_mode VARCHAR(32) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT data_sources_code_not_blank CHECK (btrim(code) <> ''),
    CONSTRAINT data_sources_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT data_sources_type_check CHECK (source_type IN ('sqlserver')),
    CONSTRAINT data_sources_role_check CHECK (source_role IN ('case_source', 'production', 'product_replica')),
    CONSTRAINT data_sources_safety_check CHECK (safety_mode IN ('read_only', 'bounded_lab')),
    CONSTRAINT data_sources_status_check CHECK (status IN ('active', 'disabled'))
);

CREATE UNIQUE INDEX data_sources_code_unique_idx ON data_sources (code);
CREATE INDEX data_sources_status_role_idx ON data_sources (status, source_role);

CREATE TABLE external_cases (
    id UUID PRIMARY KEY,
    data_source_id UUID NOT NULL REFERENCES data_sources(id) ON DELETE RESTRICT,
    external_case_key VARCHAR(128) NOT NULL,
    external_case_type VARCHAR(64) NOT NULL DEFAULT 'support_ticket',
    last_seen_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT external_cases_key_not_blank CHECK (btrim(external_case_key) <> ''),
    CONSTRAINT external_cases_type_not_blank CHECK (btrim(external_case_type) <> '')
);

CREATE UNIQUE INDEX external_cases_source_key_unique_idx
    ON external_cases (data_source_id, external_case_key);
CREATE INDEX external_cases_last_seen_idx ON external_cases (data_source_id, last_seen_at DESC);

-- +goose Down
DROP TABLE IF EXISTS external_cases;
DROP TABLE IF EXISTS data_sources;
