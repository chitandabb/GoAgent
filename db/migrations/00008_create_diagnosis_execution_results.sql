-- +goose Up
-- 工单快照本身也是本次诊断实际使用的数据源。早期任务只记录了用户额外选择的
-- evidenceDataSourceIds，这里先回填，再由创建链路持续写入。
INSERT INTO diagnosis_task_data_sources
    (task_id, data_source_id, access_scope, access_scope_schema_version,
     confirmed_by, confirmed_at)
SELECT task.id, external_case.data_source_id, '{}'::jsonb, 1,
       task.created_by, task.created_at
FROM diagnosis_tasks task
JOIN external_cases external_case ON external_case.id = task.external_case_id
ON CONFLICT (task_id, data_source_id) DO NOTHING;

CREATE TABLE diagnosis_steps (
    id UUID PRIMARY KEY,
    task_id UUID NOT NULL REFERENCES diagnosis_tasks(id) ON DELETE RESTRICT,
    attempt_count INTEGER NOT NULL,
    step_no INTEGER NOT NULL,
    step_type VARCHAR(32) NOT NULL,
    display_name VARCHAR(128) NOT NULL,
    status VARCHAR(32) NOT NULL,
    output_summary JSONB NOT NULL DEFAULT '{}'::jsonb,
    duration_ms BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT diagnosis_steps_attempt_positive CHECK (attempt_count > 0),
    CONSTRAINT diagnosis_steps_no_positive CHECK (step_no > 0),
    CONSTRAINT diagnosis_steps_type_not_blank CHECK (btrim(step_type) <> ''),
    CONSTRAINT diagnosis_steps_name_not_blank CHECK (btrim(display_name) <> ''),
    CONSTRAINT diagnosis_steps_status_check CHECK (
        status IN ('completed', 'failed', 'stopped', 'needs_evidence', 'partial', 'skipped')
    ),
    CONSTRAINT diagnosis_steps_duration_non_negative CHECK (duration_ms IS NULL OR duration_ms >= 0),
    CONSTRAINT diagnosis_steps_output_object CHECK (jsonb_typeof(output_summary) = 'object'),
    CONSTRAINT diagnosis_steps_attempt_unique UNIQUE (task_id, attempt_count, step_no)
);

CREATE INDEX diagnosis_steps_task_idx
    ON diagnosis_steps (task_id, attempt_count, step_no);

CREATE TABLE tool_executions (
    id UUID PRIMARY KEY,
    task_id UUID NOT NULL REFERENCES diagnosis_tasks(id) ON DELETE RESTRICT,
    attempt_count INTEGER NOT NULL,
    execution_no INTEGER NOT NULL,
    tool_name VARCHAR(128) NOT NULL,
    status VARCHAR(32) NOT NULL,
    degraded BOOLEAN NOT NULL DEFAULT FALSE,
    error_kind VARCHAR(64),
    error_message TEXT,
    evidence_ref VARCHAR(128),
    duration_ms BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT tool_executions_attempt_positive CHECK (attempt_count > 0),
    CONSTRAINT tool_executions_no_positive CHECK (execution_no > 0),
    CONSTRAINT tool_executions_name_not_blank CHECK (btrim(tool_name) <> ''),
    CONSTRAINT tool_executions_status_check CHECK (status IN ('succeeded', 'failed')),
    CONSTRAINT tool_executions_duration_non_negative CHECK (duration_ms >= 0),
    CONSTRAINT tool_executions_attempt_unique UNIQUE (task_id, attempt_count, execution_no)
);

CREATE INDEX tool_executions_task_idx
    ON tool_executions (task_id, attempt_count, execution_no);

CREATE TABLE evidence_items (
    id UUID PRIMARY KEY,
    task_id UUID NOT NULL REFERENCES diagnosis_tasks(id) ON DELETE RESTRICT,
    source_type VARCHAR(32) NOT NULL,
    source_locator JSONB NOT NULL DEFAULT '{}'::jsonb,
    source_locator_schema_version INTEGER NOT NULL DEFAULT 1,
    content_text TEXT,
    content_hash VARCHAR(128) NOT NULL,
    collected_at TIMESTAMPTZ NOT NULL,
    redaction_status VARCHAR(32) NOT NULL,
    truncated BOOLEAN NOT NULL DEFAULT FALSE,
    validity_status VARCHAR(32) NOT NULL DEFAULT 'valid',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT evidence_items_source_type_check CHECK (
        source_type IN (
            'case_snapshot', 'schema_catalog', 'sql_object_definition',
            'sql_query', 'code_search', 'attachment', 'knowledge_chunk', 'web'
        )
    ),
    CONSTRAINT evidence_items_locator_schema_positive CHECK (source_locator_schema_version > 0),
    CONSTRAINT evidence_items_locator_object CHECK (jsonb_typeof(source_locator) = 'object'),
    CONSTRAINT evidence_items_content_present CHECK (NULLIF(btrim(content_text), '') IS NOT NULL),
    CONSTRAINT evidence_items_hash_not_blank CHECK (btrim(content_hash) <> ''),
    CONSTRAINT evidence_items_redaction_status_check CHECK (
        redaction_status IN ('not_required', 'redacted')
    ),
    CONSTRAINT evidence_items_validity_status_check CHECK (
        validity_status IN ('valid', 'superseded', 'invalid')
    )
);

CREATE INDEX evidence_items_task_collected_idx
    ON evidence_items (task_id, collected_at, id);

CREATE TABLE report_evidence (
    report_id UUID NOT NULL REFERENCES diagnosis_reports(id) ON DELETE RESTRICT,
    evidence_id UUID NOT NULL REFERENCES evidence_items(id) ON DELETE RESTRICT,
    claim_key VARCHAR(64) NOT NULL,
    claim_text TEXT NOT NULL,
    support_type VARCHAR(16) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (report_id, evidence_id, claim_key),
    CONSTRAINT report_evidence_claim_key_not_blank CHECK (btrim(claim_key) <> ''),
    CONSTRAINT report_evidence_claim_text_not_blank CHECK (btrim(claim_text) <> ''),
    CONSTRAINT report_evidence_support_type_check CHECK (
        support_type IN ('supports', 'contradicts', 'context')
    )
);

CREATE INDEX report_evidence_report_idx
    ON report_evidence (report_id, claim_key);

-- +goose Down
DROP TABLE IF EXISTS report_evidence;
DROP TABLE IF EXISTS evidence_items;
DROP TABLE IF EXISTS tool_executions;
DROP TABLE IF EXISTS diagnosis_steps;
