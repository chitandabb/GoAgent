-- +goose Up
CREATE TABLE case_snapshots (
    id UUID PRIMARY KEY,
    external_case_id UUID NOT NULL REFERENCES external_cases(id) ON DELETE RESTRICT,
    snapshot_no INTEGER NOT NULL,
    payload JSONB NOT NULL,
    payload_schema_version INTEGER NOT NULL DEFAULT 1,
    content_hash VARCHAR(128) NOT NULL,
    source_read_at TIMESTAMPTZ NOT NULL,
    redaction_status VARCHAR(32) NOT NULL DEFAULT 'not_required',
    truncation_status VARCHAR(32) NOT NULL DEFAULT 'complete',
    created_by UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT case_snapshots_no_positive CHECK (snapshot_no > 0),
    CONSTRAINT case_snapshots_schema_version_positive CHECK (payload_schema_version > 0),
    CONSTRAINT case_snapshots_hash_not_blank CHECK (btrim(content_hash) <> ''),
    CONSTRAINT case_snapshots_payload_object_or_array CHECK (
        jsonb_typeof(payload) IN ('object', 'array')
    ),
    CONSTRAINT case_snapshots_redaction_status_check CHECK (
        redaction_status IN ('not_required', 'pending', 'redacted', 'failed')
    ),
    CONSTRAINT case_snapshots_truncation_status_check CHECK (
        truncation_status IN ('complete', 'truncated', 'unknown')
    ),
    CONSTRAINT case_snapshots_source_unique UNIQUE (external_case_id, snapshot_no)
);

CREATE INDEX case_snapshots_external_case_idx
    ON case_snapshots (external_case_id, snapshot_no DESC);

CREATE TABLE diagnosis_tasks (
    id UUID PRIMARY KEY,
    created_by UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    external_case_id UUID NOT NULL REFERENCES external_cases(id) ON DELETE RESTRICT,
    case_snapshot_id UUID NOT NULL REFERENCES case_snapshots(id) ON DELETE RESTRICT,
    retry_of UUID REFERENCES diagnosis_tasks(id) ON DELETE RESTRICT,
    idempotency_key VARCHAR(128) NOT NULL,
    request_fingerprint VARCHAR(128) NOT NULL,
    request_text TEXT NOT NULL,
    request_scope JSONB NOT NULL DEFAULT '{}'::jsonb,
    request_scope_schema_version INTEGER NOT NULL DEFAULT 1,
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    attempt_count INTEGER NOT NULL DEFAULT 0,
    claim_owner VARCHAR(128),
    claimed_at TIMESTAMPTZ,
    lease_until TIMESTAMPTZ,
    cancel_requested_at TIMESTAMPTZ,
    last_error_code VARCHAR(64),
    last_error_message TEXT,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT diagnosis_tasks_idempotency_not_blank CHECK (btrim(idempotency_key) <> ''),
    CONSTRAINT diagnosis_tasks_fingerprint_not_blank CHECK (btrim(request_fingerprint) <> ''),
    CONSTRAINT diagnosis_tasks_request_not_blank CHECK (btrim(request_text) <> ''),
    CONSTRAINT diagnosis_tasks_scope_schema_positive CHECK (request_scope_schema_version > 0),
    CONSTRAINT diagnosis_tasks_attempt_non_negative CHECK (attempt_count >= 0),
    CONSTRAINT diagnosis_tasks_status_check CHECK (
        status IN ('pending', 'running', 'cancel_requested', 'succeeded', 'failed', 'cancelled')
    ),
    CONSTRAINT diagnosis_tasks_scope_object CHECK (jsonb_typeof(request_scope) = 'object'),
    CONSTRAINT diagnosis_tasks_cancel_state_check CHECK (
        status <> 'cancel_requested' OR cancel_requested_at IS NOT NULL
    )
);

CREATE UNIQUE INDEX diagnosis_tasks_creator_idempotency_unique_idx
    ON diagnosis_tasks (created_by, idempotency_key);
CREATE INDEX diagnosis_tasks_status_claim_idx
    ON diagnosis_tasks (status, lease_until, created_at);
CREATE INDEX diagnosis_tasks_external_case_idx
    ON diagnosis_tasks (external_case_id, created_at DESC);

CREATE TABLE diagnosis_task_data_sources (
    task_id UUID NOT NULL REFERENCES diagnosis_tasks(id) ON DELETE CASCADE,
    data_source_id UUID NOT NULL REFERENCES data_sources(id) ON DELETE RESTRICT,
    catalog_version_id UUID REFERENCES schema_catalog_versions(id) ON DELETE RESTRICT,
    access_scope JSONB NOT NULL DEFAULT '{}'::jsonb,
    access_scope_schema_version INTEGER NOT NULL DEFAULT 1,
    confirmed_by UUID REFERENCES users(id) ON DELETE SET NULL,
    confirmed_at TIMESTAMPTZ,
    PRIMARY KEY (task_id, data_source_id),
    CONSTRAINT diagnosis_task_sources_scope_schema_positive CHECK (access_scope_schema_version > 0),
    CONSTRAINT diagnosis_task_sources_scope_object CHECK (jsonb_typeof(access_scope) = 'object')
);

-- +goose Down
DROP TABLE IF EXISTS diagnosis_task_data_sources;
DROP TABLE IF EXISTS diagnosis_tasks;
DROP TABLE IF EXISTS case_snapshots;
