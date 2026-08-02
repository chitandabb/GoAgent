-- +goose Up
CREATE TABLE diagnosis_reports (
    id UUID PRIMARY KEY,
    task_id UUID NOT NULL UNIQUE REFERENCES diagnosis_tasks(id) ON DELETE RESTRICT,
    conclusion_status VARCHAR(16) NOT NULL,
    business_summary JSONB NOT NULL DEFAULT '{}'::jsonb,
    technical_summary JSONB NOT NULL DEFAULT '{}'::jsonb,
    report_schema_version INTEGER NOT NULL DEFAULT 1,
    risk_level VARCHAR(16) NOT NULL,
    model_name VARCHAR(128) NOT NULL,
    model_version VARCHAR(128) NOT NULL,
    prompt_version VARCHAR(128) NOT NULL,
    generated_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT diagnosis_reports_conclusion_status_check CHECK (
        conclusion_status IN ('conclusive', 'probable', 'inconclusive')
    ),
    CONSTRAINT diagnosis_reports_risk_level_check CHECK (
        risk_level IN ('low', 'medium', 'high')
    ),
    CONSTRAINT diagnosis_reports_schema_version_positive CHECK (report_schema_version > 0),
    CONSTRAINT diagnosis_reports_business_summary_object CHECK (
        jsonb_typeof(business_summary) = 'object'
    ),
    CONSTRAINT diagnosis_reports_technical_summary_object CHECK (
        jsonb_typeof(technical_summary) = 'object'
    )
);

CREATE INDEX diagnosis_reports_generated_idx
    ON diagnosis_reports (generated_at DESC);

CREATE TABLE report_reviews (
    id UUID PRIMARY KEY,
    report_id UUID NOT NULL REFERENCES diagnosis_reports(id) ON DELETE RESTRICT,
    reviewed_by UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    verdict VARCHAR(32) NOT NULL,
    comment TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT report_reviews_verdict_check CHECK (
        verdict IN ('adopted', 'partially_adopted', 'rejected')
    ),
    CONSTRAINT report_reviews_comment_length_check CHECK (
        comment IS NULL OR char_length(comment) <= 2000
    )
);

CREATE INDEX report_reviews_report_created_idx
    ON report_reviews (report_id, created_at DESC, id DESC);

-- +goose Down
DROP TABLE IF EXISTS report_reviews;
DROP TABLE IF EXISTS diagnosis_reports;
