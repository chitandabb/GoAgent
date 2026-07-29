package postgres

import (
	"context"
	"time"

	"github.com/chitandabb/GoAgent/internal/externalcase"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ExternalCaseRepository 保存数据源配置身份和外部工单稳定 ID。
type ExternalCaseRepository struct {
	db *gorm.DB
}

var _ externalcase.Registry = (*ExternalCaseRepository)(nil)

func NewExternalCaseRepository(db *gorm.DB) *ExternalCaseRepository {
	return &ExternalCaseRepository{db: db}
}

// EnsureCaseSource 幂等同步配置文件中的 ERP 工单数据源安全元数据。
func (r *ExternalCaseRepository) EnsureCaseSource(
	ctx context.Context,
	id uuid.UUID,
	code, name, environment string,
) error {
	now := time.Now().UTC()
	record := dataSourceRecord{
		ID: id, Code: code, Name: name, SourceType: "sqlserver", SourceRole: "case_source",
		Environment: environment, SafetyMode: "read_only", Status: "active",
		CreatedAt: now, UpdatedAt: now,
	}
	return TranslateError(ResolveDB(ctx, r.db).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"code": code, "name": name, "environment": environment,
			"source_type": "sqlserver", "source_role": "case_source",
			"safety_mode": "read_only", "updated_at": now,
		}),
	}).Create(&record).Error)
}

func (r *ExternalCaseRepository) RegisterSeen(
	ctx context.Context,
	dataSourceID uuid.UUID,
	cases []externalcase.SeenCase,
	seenAt time.Time,
) (map[string]uuid.UUID, error) {
	ids := make(map[string]uuid.UUID, len(cases))
	if len(cases) == 0 {
		return ids, nil
	}
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		for _, item := range cases {
			id := uuid.New()
			record := externalCaseRecord{
				ID: id, DataSourceID: dataSourceID, ExternalCaseKey: item.ExternalCaseKey,
				ExternalCaseType: item.ExternalCaseType, LastSeenAt: seenAt,
				CreatedAt: seenAt, UpdatedAt: seenAt,
			}
			var stored externalCaseRecord
			err := tx.Raw(`
INSERT INTO external_cases
    (id, data_source_id, external_case_key, external_case_type, last_seen_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (data_source_id, external_case_key) DO UPDATE SET
    external_case_type = EXCLUDED.external_case_type,
    last_seen_at = EXCLUDED.last_seen_at,
    updated_at = EXCLUDED.updated_at
RETURNING id, data_source_id, external_case_key, external_case_type, last_seen_at, created_at, updated_at`,
				record.ID, record.DataSourceID, record.ExternalCaseKey, record.ExternalCaseType,
				record.LastSeenAt, record.CreatedAt, record.UpdatedAt,
			).Scan(&stored).Error
			if err != nil {
				return TranslateError(err)
			}
			ids[item.ExternalCaseKey] = stored.ID
		}
		return nil
	})
	if err != nil {
		return nil, TranslateError(err)
	}
	return ids, nil
}

func (r *ExternalCaseRepository) FindReference(ctx context.Context, id uuid.UUID) (*externalcase.Reference, error) {
	var record externalCaseRecord
	if err := ResolveDB(ctx, r.db).Where("id = ?", id).Take(&record).Error; err != nil {
		return nil, TranslateError(err)
	}
	return &externalcase.Reference{
		ID: record.ID, DataSourceID: record.DataSourceID, ExternalCaseKey: record.ExternalCaseKey,
	}, nil
}

type dataSourceRecord struct {
	ID          uuid.UUID `gorm:"column:id"`
	Code        string    `gorm:"column:code"`
	Name        string    `gorm:"column:name"`
	SourceType  string    `gorm:"column:source_type"`
	SourceRole  string    `gorm:"column:source_role"`
	Environment string    `gorm:"column:environment"`
	SafetyMode  string    `gorm:"column:safety_mode"`
	Status      string    `gorm:"column:status"`
	CreatedAt   time.Time `gorm:"column:created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

func (dataSourceRecord) TableName() string { return "data_sources" }

type externalCaseRecord struct {
	ID               uuid.UUID `gorm:"column:id"`
	DataSourceID     uuid.UUID `gorm:"column:data_source_id"`
	ExternalCaseKey  string    `gorm:"column:external_case_key"`
	ExternalCaseType string    `gorm:"column:external_case_type"`
	LastSeenAt       time.Time `gorm:"column:last_seen_at"`
	CreatedAt        time.Time `gorm:"column:created_at"`
	UpdatedAt        time.Time `gorm:"column:updated_at"`
}

func (externalCaseRecord) TableName() string { return "external_cases" }
