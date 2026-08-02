-- +goose Up
ALTER TABLE diagnosis_reports
    ADD COLUMN model_provider VARCHAR(64);

-- 当前正式运行时只允许 StepFun；00006 到本迁移之间产生的报告均来自该 Provider。
UPDATE diagnosis_reports
SET model_provider = 'stepfun'
WHERE model_provider IS NULL;

ALTER TABLE diagnosis_reports
    ALTER COLUMN model_provider SET NOT NULL;

ALTER TABLE diagnosis_reports
    RENAME COLUMN model_name TO model_id;

ALTER TABLE diagnosis_reports
    DROP COLUMN model_version,
    ADD CONSTRAINT diagnosis_reports_model_provider_not_blank CHECK (btrim(model_provider) <> ''),
    ADD CONSTRAINT diagnosis_reports_model_id_not_blank CHECK (btrim(model_id) <> ''),
    ADD CONSTRAINT diagnosis_reports_prompt_version_not_blank CHECK (btrim(prompt_version) <> '');

-- +goose Down
ALTER TABLE diagnosis_reports
    DROP CONSTRAINT diagnosis_reports_prompt_version_not_blank,
    DROP CONSTRAINT diagnosis_reports_model_id_not_blank,
    DROP CONSTRAINT diagnosis_reports_model_provider_not_blank,
    ADD COLUMN model_version VARCHAR(128);

UPDATE diagnosis_reports
SET model_version = model_id;

ALTER TABLE diagnosis_reports
    ALTER COLUMN model_version SET NOT NULL;

ALTER TABLE diagnosis_reports
    RENAME COLUMN model_id TO model_name;

ALTER TABLE diagnosis_reports
    DROP COLUMN model_provider;
