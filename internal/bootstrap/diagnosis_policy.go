package bootstrap

import (
	"fmt"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/chitandabb/GoAgent/internal/platform/config"

	"github.com/google/uuid"
)

// newDiagnosisInvestigationPolicyBuilder 从部署配置生成诊断 Policy Builder 参数：
//   - case.read：始终作为诊断基础上限；
//   - knowledge.read：后端基础上限（实际 Tool 是否存在由 Worker ceiling 收窄）；
//   - web.read：仅 webSearch.enabled；
//   - code.read：仅 githubMCP.enabled；
//   - sql.read：仅 sqlserver.enabled 且 allowedSchemas 非空；
//   - 不授予 task.read、memory.read、diagnosis.create；
//   - 允许调查的数据源当前只有配置的 sqlserver.id。
func newDiagnosisInvestigationPolicyBuilder(cfg config.Config) (diagnosis.InvestigationPolicyBuilder, error) {
	basePermissions := []agentruntime.Permission{
		agentruntime.PermissionCaseRead,
		agentruntime.PermissionKnowledgeRead,
	}
	if cfg.WebSearch.Enabled {
		basePermissions = append(basePermissions, agentruntime.PermissionWebRead)
	}
	if cfg.GitHubMCP.Enabled {
		basePermissions = append(basePermissions, agentruntime.PermissionCodeRead)
	}
	var allowedDataSourceIDs []uuid.UUID
	if cfg.SQLServer.Enabled && len(cfg.SQLServer.Investigation.AllowedSchemas) > 0 {
		basePermissions = append(basePermissions, agentruntime.PermissionSQLRead)
		dataSourceID, err := uuid.Parse(cfg.SQLServer.ID)
		if err != nil {
			return nil, fmt.Errorf("parse SQL Server data source id: %w", err)
		}
		allowedDataSourceIDs = []uuid.UUID{dataSourceID}
	}
	return diagnosis.NewInvestigationPolicyBuilder(diagnosis.InvestigationPolicyConfig{
		BasePermissions:      basePermissions,
		AllowedDataSourceIDs: allowedDataSourceIDs,
	})
}
