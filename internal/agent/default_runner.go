package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/chitandabb/GoAgent/internal/attachment"
	"github.com/chitandabb/GoAgent/internal/auth"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"go.uber.org/zap"
)

type DefaultRunnerDependencies struct {
	ChatModel             model.ToolCallingChatModel
	ExternalCases         ExternalCaseGetter
	SkillRoot             string
	SystemInstruction     string
	BaselineInstruction   string
	Mode                  RunnerMode
	GitHubTools           []tool.BaseTool
	GitHubArgumentRewrite ArgumentRewriter
	SQLObjectDefinitions  tool.BaseTool
	SchemaCatalog         tool.BaseTool
	ReadonlyQuery         tool.BaseTool
	KnowledgeSearch       tool.BaseTool
	WebSearch             tool.BaseTool
	FetchPublicPage       tool.BaseTool
	CreateDiagnosisTask   DiagnosisTaskCreator
	AttachmentReader      attachment.Reader
	Logger                *zap.Logger
}

type DefaultToolCatalogDependencies struct {
	ExternalCases        ExternalCaseGetter
	SkillReference       tool.BaseTool
	GitHubTools          []tool.BaseTool
	SQLObjectDefinitions tool.BaseTool
	SchemaCatalog        tool.BaseTool
	ReadonlyQuery        tool.BaseTool
	KnowledgeSearch      tool.BaseTool
	WebSearch            tool.BaseTool
	FetchPublicPage      tool.BaseTool
	CreateDiagnosisTask  DiagnosisTaskCreator
	DiagnosisTaskStatus  DiagnosisTaskStatusReader
	AttachmentReader     attachment.Reader
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
	if len(dependencies.GitHubTools) > 0 && dependencies.GitHubArgumentRewrite == nil {
		return nil, errors.New("github argument rewriter is required when github tools are enabled")
	}
	catalog, err := NewDefaultToolCatalog(ctx, DefaultToolCatalogDependencies{
		ExternalCases: dependencies.ExternalCases, SkillReference: skillRuntime.ReferenceTool,
		GitHubTools: dependencies.GitHubTools, SQLObjectDefinitions: dependencies.SQLObjectDefinitions,
		SchemaCatalog: dependencies.SchemaCatalog, ReadonlyQuery: dependencies.ReadonlyQuery,
		KnowledgeSearch: dependencies.KnowledgeSearch, WebSearch: dependencies.WebSearch,
		FetchPublicPage: dependencies.FetchPublicPage, CreateDiagnosisTask: dependencies.CreateDiagnosisTask,
		AttachmentReader: dependencies.AttachmentReader,
	})
	if err != nil {
		return nil, fmt.Errorf("build Tool catalog: %w", err)
	}
	return NewRunner(RunnerConfig{
		ChatModel: dependencies.ChatModel, ToolCatalog: catalog,
		SkillRuntime:          skillRuntime,
		SystemInstruction:     dependencies.SystemInstruction,
		BaselineInstruction:   dependencies.BaselineInstruction,
		Mode:                  dependencies.Mode,
		GitHubArgumentRewrite: dependencies.GitHubArgumentRewrite,
		Logger:                dependencies.Logger,
	})
}

// NewDefaultToolCatalog 复用生产 Runner 的 Tool 注册与 TaskScope 筛选规则。
// SkillReference 可为空，供只评测业务 Tool Schema 的受控实验使用。
func NewDefaultToolCatalog(ctx context.Context, dependencies DefaultToolCatalogDependencies) (*ToolCatalog, error) {
	if dependencies.ExternalCases == nil {
		return nil, errors.New("default tool catalog external cases are required")
	}
	readExternalCase, err := NewReadExternalCaseTool(dependencies.ExternalCases)
	if err != nil {
		return nil, fmt.Errorf("build external case tool: %w", err)
	}
	roles := []auth.Role{auth.RoleAnalyst, auth.RoleAdmin}
	registrations := []ToolRegistration{{
		Tool: readExternalCase, AllowedRoles: roles,
		AllowedTaskTypes:     []TaskType{TaskTypeDiagnosis, TaskTypeConversation},
		RequiredCapabilities: []ToolCapability{ToolCapabilityCase},
		RequiredDependencies: []ToolDependency{ToolDependencyExternalCase},
	}}
	if dependencies.SkillReference != nil {
		registrations = append(registrations, ToolRegistration{
			Tool: dependencies.SkillReference, AllowedRoles: roles,
			AllowedTaskTypes: []TaskType{TaskTypeDiagnosis, TaskTypeKnowledge},
		})
	}
	if dependencies.KnowledgeSearch != nil {
		registrations = append(registrations, ToolRegistration{
			Tool: dependencies.KnowledgeSearch, AllowedRoles: roles,
			AllowedTaskTypes:     []TaskType{TaskTypeDiagnosis, TaskTypeKnowledge, TaskTypeConversation},
			RequiredCapabilities: []ToolCapability{ToolCapabilityKnowledge},
			RequiredDependencies: []ToolDependency{ToolDependencyKnowledge},
		})
	}
	for _, webTool := range []tool.BaseTool{dependencies.WebSearch, dependencies.FetchPublicPage} {
		if webTool == nil {
			continue
		}
		registrations = append(registrations, ToolRegistration{
			Tool: webTool, AllowedRoles: roles,
			AllowedTaskTypes:     []TaskType{TaskTypeDiagnosis, TaskTypeKnowledge, TaskTypeConversation},
			RequiredCapabilities: []ToolCapability{ToolCapabilityWebSearch},
			RequiredDependencies: []ToolDependency{ToolDependencyWebSearch},
		})
	}
	if dependencies.CreateDiagnosisTask != nil {
		createDiagnosisTask, err := NewCreateDiagnosisTaskTool(dependencies.CreateDiagnosisTask)
		if err != nil {
			return nil, fmt.Errorf("build create diagnosis task Tool: %w", err)
		}
		registrations = append(registrations, ToolRegistration{
			Tool: createDiagnosisTask, AllowedRoles: roles,
			AllowedTaskTypes:     []TaskType{TaskTypeDiagnosis, TaskTypeConversation},
			RequiredCapabilities: []ToolCapability{ToolCapabilityCase},
			RequiredDependencies: []ToolDependency{ToolDependencyExternalCase},
		})
	}
	if dependencies.DiagnosisTaskStatus != nil {
		getDiagnosisTaskStatus, err := NewGetDiagnosisTaskStatusTool(dependencies.DiagnosisTaskStatus)
		if err != nil {
			return nil, fmt.Errorf("build diagnosis task status Tool: %w", err)
		}
		registrations = append(registrations, ToolRegistration{
			Tool: getDiagnosisTaskStatus, AllowedRoles: roles,
			AllowedTaskTypes:     []TaskType{TaskTypeConversation},
			RequiredCapabilities: []ToolCapability{ToolCapabilityTask},
		})
	}
	if dependencies.AttachmentReader != nil {
		readAttachment, err := NewReadAttachmentTool(dependencies.AttachmentReader)
		if err != nil {
			return nil, fmt.Errorf("build read attachment Tool: %w", err)
		}
		registrations = append(registrations, ToolRegistration{
			Tool: readAttachment, AllowedRoles: roles,
			AllowedTaskTypes:     []TaskType{TaskTypeConversation},
			RequiredCapabilities: []ToolCapability{ToolCapabilityAttachment},
			RequiredDependencies: []ToolDependency{ToolDependencyAttachment},
		})
	}
	for _, sqlTool := range []tool.BaseTool{
		dependencies.SQLObjectDefinitions, dependencies.SchemaCatalog, dependencies.ReadonlyQuery,
	} {
		if sqlTool == nil {
			continue
		}
		registrations = append(registrations, ToolRegistration{
			Tool: sqlTool, AllowedRoles: roles,
			AllowedTaskTypes: []TaskType{TaskTypeDiagnosis},
			AllowedDataRoles: []DataSourceRole{
				DataSourceRoleCaseSource, DataSourceRoleProduction, DataSourceRoleProductReplica,
			},
			AllowedSafetyModes:   []DataSourceSafetyMode{DataSourceSafetyReadOnly},
			RequiredCapabilities: []ToolCapability{ToolCapabilitySQL},
			RequiredDependencies: []ToolDependency{ToolDependencySQLServer},
		})
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
			RequiredCapabilities: []ToolCapability{ToolCapabilityCode},
			RequiredDependencies: []ToolDependency{ToolDependencyGitHubMCP},
		})
	}
	return NewToolCatalog(ctx, registrations...)
}
