package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/chitandabb/GoAgent/internal/auth"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"go.uber.org/zap"
)

type DefaultRunnerDependencies struct {
	ChatModel             model.ToolCallingChatModel
	ExternalCases         ExternalCaseGetter
	SkillRoot             string
	GitHubTools           []tool.BaseTool
	GitHubArgumentRewrite ArgumentRewriter
	SQLObjectDefinitions  tool.BaseTool
	SchemaCatalog         tool.BaseTool
	ReadonlyQuery         tool.BaseTool
	Logger                *zap.Logger
}

// NewDefaultRunner 完成单 ADK Agent 的手动依赖装配。
// Skill 只描述调查 SOP；ToolCatalog 才是角色、任务、数据源和依赖权限的事实来源。
func NewDefaultRunner(ctx context.Context, dependencies DefaultRunnerDependencies) (*Runner, error) {
	if dependencies.ChatModel == nil || dependencies.ExternalCases == nil || dependencies.Logger == nil {
		return nil, errors.New("default runner model, external cases, and logger are required")
	}
	skillRuntime, err := NewNativeSkillRuntime(ctx, dependencies.SkillRoot)
	if err != nil {
		return nil, fmt.Errorf("build native Skill runtime: %w", err)
	}
	readExternalCase, err := NewReadExternalCaseTool(dependencies.ExternalCases)
	if err != nil {
		return nil, fmt.Errorf("build external case tool: %w", err)
	}

	roles := []auth.Role{auth.RoleAnalyst, auth.RoleAdmin}
	registrations := []ToolRegistration{
		{
			Tool: readExternalCase, AllowedRoles: roles,
			AllowedTaskTypes:     []TaskType{TaskTypeDiagnosis},
			AllowedDataRoles:     []DataSourceRole{DataSourceRoleCaseSource},
			AllowedSafetyModes:   []DataSourceSafetyMode{DataSourceSafetyReadOnly},
			RequiredDependencies: []ToolDependency{ToolDependencyExternalCase},
		},
		{
			Tool: skillRuntime.ReferenceTool, AllowedRoles: roles,
			AllowedTaskTypes: []TaskType{TaskTypeDiagnosis, TaskTypeKnowledge},
		},
	}
	if dependencies.SQLObjectDefinitions != nil {
		registrations = append(registrations, ToolRegistration{
			Tool: dependencies.SQLObjectDefinitions, AllowedRoles: roles,
			AllowedTaskTypes: []TaskType{TaskTypeDiagnosis},
			AllowedDataRoles: []DataSourceRole{
				DataSourceRoleCaseSource, DataSourceRoleProduction, DataSourceRoleProductReplica,
			},
			AllowedSafetyModes:   []DataSourceSafetyMode{DataSourceSafetyReadOnly},
			RequiredDependencies: []ToolDependency{ToolDependencySQLServer},
		})
	}
	if dependencies.SchemaCatalog != nil {
		registrations = append(registrations, ToolRegistration{
			Tool: dependencies.SchemaCatalog, AllowedRoles: roles,
			AllowedTaskTypes: []TaskType{TaskTypeDiagnosis},
			AllowedDataRoles: []DataSourceRole{
				DataSourceRoleCaseSource, DataSourceRoleProduction, DataSourceRoleProductReplica,
			},
			AllowedSafetyModes:   []DataSourceSafetyMode{DataSourceSafetyReadOnly},
			RequiredDependencies: []ToolDependency{ToolDependencySQLServer},
		})
	}
	if dependencies.ReadonlyQuery != nil {
		registrations = append(registrations, ToolRegistration{
			Tool: dependencies.ReadonlyQuery, AllowedRoles: roles,
			AllowedTaskTypes: []TaskType{TaskTypeDiagnosis},
			AllowedDataRoles: []DataSourceRole{
				DataSourceRoleCaseSource, DataSourceRoleProduction, DataSourceRoleProductReplica,
			},
			AllowedSafetyModes:   []DataSourceSafetyMode{DataSourceSafetyReadOnly},
			RequiredDependencies: []ToolDependency{ToolDependencySQLServer},
		})
	}
	if len(dependencies.GitHubTools) > 0 {
		if dependencies.GitHubArgumentRewrite == nil {
			return nil, errors.New("github argument rewriter is required when github tools are enabled")
		}
		for _, githubTool := range dependencies.GitHubTools {
			if githubTool == nil {
				return nil, errors.New("github tool is nil")
			}
			info, infoErr := githubTool.Info(ctx)
			if infoErr != nil {
				return nil, fmt.Errorf("read github tool info: %w", infoErr)
			}
			if info == nil {
				return nil, errors.New("github tool info is nil")
			}
			if !slices.Contains(GitHubReadOnlyTools, info.Name) {
				return nil, fmt.Errorf("github tool %q is outside the read-only allowlist", info.Name)
			}
			registrations = append(registrations, ToolRegistration{
				Tool: githubTool, AllowedRoles: roles,
				AllowedTaskTypes:     []TaskType{TaskTypeDiagnosis},
				RequiredDependencies: []ToolDependency{ToolDependencyGitHubMCP},
			})
		}
	}
	catalog, err := NewToolCatalog(ctx, registrations...)
	if err != nil {
		return nil, fmt.Errorf("build Tool catalog: %w", err)
	}
	return NewRunner(RunnerConfig{
		ChatModel: dependencies.ChatModel, ToolCatalog: catalog,
		SkillRuntime:          skillRuntime,
		GitHubArgumentRewrite: dependencies.GitHubArgumentRewrite,
		Logger:                dependencies.Logger,
	})
}
