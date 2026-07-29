// Package externalcase 定义公司 ERP 工单进入 MESGuard 后的统一领域模型。
package externalcase

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	StatusOpen       Status = "open"
	StatusProcessing Status = "processing"
	StatusClosed     Status = "closed"
)

type Priority string

const (
	PriorityHigh   Priority = "high"
	PriorityMedium Priority = "medium"
	PriorityLow    Priority = "low"
)

type DataSource struct {
	ID          uuid.UUID
	Code        string
	Name        string
	Type        string
	Role        string
	Environment string
	SafetyMode  string
	Status      string
}

type ExternalCase struct {
	ID                uuid.UUID
	DataSourceID      uuid.UUID
	ExternalCaseKey   string
	CaseType          string
	Title             string
	Description       string
	Category          string
	Module            string
	Status            Status
	Priority          Priority
	SourceStatus      string
	SourcePriority    string
	OccurredAt        *time.Time
	ReportedAt        time.Time
	SourceUpdatedAt   time.Time
	Customer          CustomerContext
	Product           ProductContext
	Production        ProductionContext
	Environment       EnvironmentContext
	Attributes        map[string]any
	Attachments       []ExternalAttachment
	SourceFingerprint string
	Truncated         bool
}

type CustomerContext struct {
	Code string
	Name string
}

type ProductContext struct {
	Code    string
	Name    string
	Version string
}

type ProductionContext struct {
	WorkOrderNo        string
	WorkpieceNo        string
	MaterialCode       string
	BatchNo            string
	SerialNo           string
	FactoryCode        string
	WorkshopCode       string
	ProductionLineCode string
	WorkstationCode    string
	EquipmentCode      string
}

type EnvironmentContext struct {
	SourceSystem          string
	DeploymentEnvironment string
	BusinessDatabaseAlias string
}

type ExternalAttachment struct {
	ExternalAttachmentKey string
	FileName              string
	MediaType             string
	SizeBytes             int64
	ObjectKey             string
	ContentHash           string
	SourceUpdatedAt       time.Time
}

type ListQuery struct {
	DataSourceID uuid.UUID
	Keyword      string
	Status       Status
	Priority     Priority
	ReportedFrom *time.Time
	ReportedTo   *time.Time
	Page         int
	PageSize     int
	SortBy       string
	SortOrder    string
	CaseType     string
}

type ListResult struct {
	Items []ExternalCase
	Total int
}

type SeenCase struct {
	ExternalCaseKey  string
	ExternalCaseType string
}

type Reference struct {
	ID              uuid.UUID
	DataSourceID    uuid.UUID
	ExternalCaseKey string
}

// ErrUnavailable 表示 ERP 工单库当前不可访问，可安全转换为 503。
var ErrUnavailable = errors.New("external case source unavailable")

// ErrResultLimit 表示外部查询结果超过应用允许的安全大小。
var ErrResultLimit = errors.New("external case result exceeds safety limit")

type Reader interface {
	List(ctx context.Context, query ListQuery) (ListResult, error)
	GetByKey(ctx context.Context, externalCaseKey string) (*ExternalCase, error)
}

// Registry 只保存外部工单稳定身份，不复制 ERP 工单正文。
type Registry interface {
	RegisterSeen(ctx context.Context, dataSourceID uuid.UUID, cases []SeenCase, seenAt time.Time) (map[string]uuid.UUID, error)
	FindReference(ctx context.Context, id uuid.UUID) (*Reference, error)
}
