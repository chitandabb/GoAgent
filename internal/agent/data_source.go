package agent

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// DataSourceRole 与 DataSourceSafetyMode 是数据源静态身份描述，不再承载任何
// 授权含义：Tool 执行授权只来自 RunAccess.Permission 与 RunAccess.Grants。
// 它们仅用于诊断执行上限（AccessCeiling）的只读源筛选与 task_context 安全投影。
type DataSourceRole string

const (
	DataSourceRoleCaseSource     DataSourceRole = "case_source"
	DataSourceRoleProduction     DataSourceRole = "production"
	DataSourceRoleProductReplica DataSourceRole = "product_replica"
)

func (r DataSourceRole) Valid() bool {
	switch r {
	case DataSourceRoleCaseSource, DataSourceRoleProduction, DataSourceRoleProductReplica:
		return true
	}
	return false
}

type DataSourceSafetyMode string

const (
	DataSourceSafetyReadOnly   DataSourceSafetyMode = "read_only"
	DataSourceSafetyBoundedLab DataSourceSafetyMode = "bounded_lab"
)

func (m DataSourceSafetyMode) Valid() bool {
	return m == DataSourceSafetyReadOnly || m == DataSourceSafetyBoundedLab
}

// ScopedDataSource 描述一个任务绑定数据源的身份；bounded_lab 只允许出现在
// product_replica 上，且永不进入 RunAccess 数据源 Grant。
type ScopedDataSource struct {
	ID         uuid.UUID
	Role       DataSourceRole
	SafetyMode DataSourceSafetyMode
}

func (s ScopedDataSource) Validate() error {
	if s.ID == uuid.Nil {
		return errors.New("data source id is required")
	}
	if !s.Role.Valid() {
		return fmt.Errorf("invalid data source role %q", s.Role)
	}
	if !s.SafetyMode.Valid() {
		return fmt.Errorf("invalid data source safety mode %q", s.SafetyMode)
	}
	if s.Role != DataSourceRoleProductReplica && s.SafetyMode == DataSourceSafetyBoundedLab {
		return fmt.Errorf("data source role %q cannot use bounded_lab", s.Role)
	}
	return nil
}
