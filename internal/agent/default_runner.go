package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/attachment"
	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/resilience"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"go.uber.org/zap"
)

type DefaultRunnerDependencies struct {
	ChatModel                 model.ToolCallingChatModel
	ExternalCases             ExternalCaseGetter
	SkillRoot                 string
	SystemInstruction         string
	BaselineInstruction       string
	Mode                      RunnerMode
	GitHubTools               []tool.BaseTool
	GitHubArgumentRewrite     ArgumentRewriter
	SQLObjectDefinitions      tool.BaseTool
	SchemaCatalog             tool.BaseTool
	ReadonlyQuery             tool.BaseTool
	KnowledgeSearch           tool.BaseTool
	WebSearch                 tool.BaseTool
	FetchPublicPage           tool.BaseTool
	CreateDiagnosisTask       DiagnosisTaskCreator
	AttachmentReader          attachment.Reader
	ConversationMemorySources ConversationMemorySourceReader
	ContextPreflight          DiagnosisContextPreflightConfig
	Logger                    *zap.Logger
	ModelProvider             string
	ModelID                   string
}

type DefaultToolCatalogDependencies struct {
	ExternalCases             ExternalCaseGetter
	SkillReference            tool.BaseTool
	GitHubTools               []tool.BaseTool
	SQLObjectDefinitions      tool.BaseTool
	SchemaCatalog             tool.BaseTool
	ReadonlyQuery             tool.BaseTool
	KnowledgeSearch           tool.BaseTool
	WebSearch                 tool.BaseTool
	FetchPublicPage           tool.BaseTool
	CreateDiagnosisTask       DiagnosisTaskCreator
	DiagnosisTaskStatus       DiagnosisTaskStatusReader
	AttachmentReader          attachment.Reader
	ConversationMemorySources ConversationMemorySourceReader
	DegradationObserver       resilience.Observer
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
	catalog, err := NewDiagnosisDefaultToolCatalog(ctx, DefaultToolCatalogDependencies{
		ExternalCases: dependencies.ExternalCases, SkillReference: skillRuntime.ReferenceTool,
		GitHubTools: dependencies.GitHubTools, SQLObjectDefinitions: dependencies.SQLObjectDefinitions,
		SchemaCatalog: dependencies.SchemaCatalog, ReadonlyQuery: dependencies.ReadonlyQuery,
		KnowledgeSearch: dependencies.KnowledgeSearch, WebSearch: dependencies.WebSearch,
		FetchPublicPage: dependencies.FetchPublicPage, CreateDiagnosisTask: dependencies.CreateDiagnosisTask,
		AttachmentReader:          dependencies.AttachmentReader,
		ConversationMemorySources: dependencies.ConversationMemorySources,
		DegradationObserver:       NewToolDegradationLogObserver(dependencies.Logger, "diagnosis"),
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
		ContextPreflight:      dependencies.ContextPreflight,
		ModelProvider:         dependencies.ModelProvider,
		ModelID:               dependencies.ModelID,
		Logger:                dependencies.Logger,
	})
}

func NewToolDegradationLogObserver(log *zap.Logger, component string) resilience.Observer {
	if log == nil {
		return nil
	}
	return resilience.ObserverFunc(func(event resilience.DegradationEvent) {
		log.Warn("Agent Tool degraded",
			zap.String("component", component),
			zap.String("operation", event.Operation), zap.String("policy", string(event.Policy)),
			zap.String("fallback", event.Fallback), zap.String("reason_code", event.ReasonCode),
			zap.String("run_id", event.RunID), zap.String("trace_id", event.TraceID),
			zap.Int64("duration_millis", event.DurationMillis),
		)
	})
}

// NewConversationDefaultToolCatalog 构造绑定 Conversation Runtime Profile 的
// 默认 Catalog，供会话 Runner 与会话测试使用。
func NewConversationDefaultToolCatalog(ctx context.Context, dependencies DefaultToolCatalogDependencies) (*ToolCatalog, error) {
	return newDefaultToolCatalog(ctx, dependencies, agentruntime.ToolProfileConversation)
}

// NewDiagnosisDefaultToolCatalog 构造绑定 Diagnosis Runtime Profile 的默认
// Catalog，供诊断 Runner 使用。它与会话 Catalog 相互独立，不共享可变
// Observer 状态。
func NewDiagnosisDefaultToolCatalog(ctx context.Context, dependencies DefaultToolCatalogDependencies) (*ToolCatalog, error) {
	return newDefaultToolCatalog(ctx, dependencies, agentruntime.ToolProfileDiagnosis)
}

func newDefaultToolCatalog(
	ctx context.Context,
	dependencies DefaultToolCatalogDependencies,
	profileID agentruntime.ToolProfileID,
) (*ToolCatalog, error) {
	if dependencies.ExternalCases == nil {
		return nil, errors.New("default tool catalog external cases are required")
	}
	conversationToolResultRegistration, err := NewConversationToolResultRegistration()
	if err != nil {
		return nil, fmt.Errorf("build conversation Tool result reader: %w", err)
	}
	readExternalCase, err := NewReadExternalCaseTool(dependencies.ExternalCases)
	if err != nil {
		return nil, fmt.Errorf("build external case tool: %w", err)
	}
	roles := []auth.Role{auth.RoleAnalyst, auth.RoleAdmin}
	registrations := []ToolRegistration{
		conversationToolResultRegistration,
		{
			Tool: readExternalCase, FailurePolicy: resilience.PolicyBestEffort, AllowedRoles: roles,
			AllowedTaskTypes:     []TaskType{TaskTypeDiagnosis, TaskTypeConversation},
			RequiredCapabilities: []ToolCapability{ToolCapabilityCase},
			RequiredDependencies: []ToolDependency{ToolDependencyExternalCase},
			RequiredPermissions:  []agentruntime.Permission{agentruntime.PermissionCaseRead},
		},
	}
	if dependencies.SkillReference != nil {
		registrations = append(registrations, ToolRegistration{
			Tool: dependencies.SkillReference, FailurePolicy: resilience.PolicyBestEffort, AllowedRoles: roles,
			AllowedTaskTypes: []TaskType{TaskTypeDiagnosis, TaskTypeKnowledge},
		})
	}
	if dependencies.KnowledgeSearch != nil {
		registrations = append(registrations, ToolRegistration{
			Tool: dependencies.KnowledgeSearch, FailurePolicy: resilience.PolicyBestEffort, AllowedRoles: roles,
			AllowedTaskTypes:     []TaskType{TaskTypeDiagnosis, TaskTypeKnowledge, TaskTypeConversation},
			RequiredCapabilities: []ToolCapability{ToolCapabilityKnowledge},
			RequiredDependencies: []ToolDependency{ToolDependencyKnowledge},
			RequiredPermissions:  []agentruntime.Permission{agentruntime.PermissionKnowledgeRead},
		})
	}
	for _, webTool := range []tool.BaseTool{dependencies.WebSearch, dependencies.FetchPublicPage} {
		if webTool == nil {
			continue
		}
		registrations = append(registrations, ToolRegistration{
			Tool: webTool, FailurePolicy: resilience.PolicyBestEffort, AllowedRoles: roles,
			AllowedTaskTypes:     []TaskType{TaskTypeDiagnosis, TaskTypeKnowledge, TaskTypeConversation},
			RequiredCapabilities: []ToolCapability{ToolCapabilityWebSearch},
			RequiredDependencies: []ToolDependency{ToolDependencyWebSearch},
			RequiredPermissions:  []agentruntime.Permission{agentruntime.PermissionWebRead},
		})
	}
	if dependencies.CreateDiagnosisTask != nil {
		createDiagnosisTask, err := NewCreateDiagnosisTaskTool(dependencies.CreateDiagnosisTask)
		if err != nil {
			return nil, fmt.Errorf("build create diagnosis task Tool: %w", err)
		}
		// 任务创建命令只属于 Conversation：Diagnosis Worker 的 ReAct 循环
		// 永远不暴露 create_diagnosis_task。旧 Schema 过滤仍保留 case 能力
		// 与 external_case 依赖，保持 Conversation 中的动态可见条件不变；
		// 执行期由 RequiredPermissions 的 diagnosis.create 做粗粒度校验。
		registrations = append(registrations, ToolRegistration{
			Tool: createDiagnosisTask, FailurePolicy: resilience.PolicyStrict, AllowedRoles: roles,
			AllowedTaskTypes:     []TaskType{TaskTypeConversation},
			RequiredCapabilities: []ToolCapability{ToolCapabilityCase},
			RequiredDependencies: []ToolDependency{ToolDependencyExternalCase},
			RequiredPermissions:  []agentruntime.Permission{agentruntime.PermissionDiagnosisCreate},
		})
	}
	if dependencies.DiagnosisTaskStatus != nil {
		getDiagnosisTaskStatus, err := NewGetDiagnosisTaskStatusTool(dependencies.DiagnosisTaskStatus)
		if err != nil {
			return nil, fmt.Errorf("build diagnosis task status Tool: %w", err)
		}
		registrations = append(registrations, ToolRegistration{
			Tool: getDiagnosisTaskStatus, FailurePolicy: resilience.PolicyBestEffort, AllowedRoles: roles,
			AllowedTaskTypes:     []TaskType{TaskTypeConversation},
			RequiredCapabilities: []ToolCapability{ToolCapabilityTask},
			RequiredPermissions:  []agentruntime.Permission{agentruntime.PermissionTaskRead},
		})
	}
	if dependencies.AttachmentReader != nil {
		readAttachment, err := NewReadAttachmentTool(dependencies.AttachmentReader)
		if err != nil {
			return nil, fmt.Errorf("build read attachment Tool: %w", err)
		}
		registrations = append(registrations, ToolRegistration{
			Tool: readAttachment, FailurePolicy: resilience.PolicyBestEffort, AllowedRoles: roles,
			AllowedTaskTypes:     []TaskType{TaskTypeConversation},
			RequiredCapabilities: []ToolCapability{ToolCapabilityAttachment},
			RequiredDependencies: []ToolDependency{ToolDependencyAttachment},
			RequiredPermissions:  []agentruntime.Permission{agentruntime.PermissionAttachmentRead},
		})
	}
	if dependencies.ConversationMemorySources != nil {
		registration, err := NewConversationMemorySourceToolRegistration(dependencies.ConversationMemorySources)
		if err != nil {
			return nil, fmt.Errorf("build conversation memory source Tool registration: %w", err)
		}
		registrations = append(registrations, registration)
	}
	for _, sqlTool := range []tool.BaseTool{
		dependencies.SQLObjectDefinitions, dependencies.SchemaCatalog, dependencies.ReadonlyQuery,
	} {
		if sqlTool == nil {
			continue
		}
		registrations = append(registrations, ToolRegistration{
			Tool: sqlTool, FailurePolicy: resilience.PolicyBestEffort, AllowedRoles: roles,
			AllowedTaskTypes: []TaskType{TaskTypeDiagnosis},
			AllowedDataRoles: []DataSourceRole{
				DataSourceRoleCaseSource, DataSourceRoleProduction, DataSourceRoleProductReplica,
			},
			AllowedSafetyModes:   []DataSourceSafetyMode{DataSourceSafetyReadOnly},
			RequiredCapabilities: []ToolCapability{ToolCapabilitySQL},
			RequiredDependencies: []ToolDependency{ToolDependencySQLServer},
			RequiredPermissions:  []agentruntime.Permission{agentruntime.PermissionSQLRead},
		})
	}
	// 注册阶段已经读取并校验过 GitHub Tool 名（allowlist），
	// Profile 推导直接复用这些已验证名称，不再次调用 Tool.Info。
	githubNames := make([]string, 0, len(dependencies.GitHubTools))
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
		githubNames = append(githubNames, info.Name)
		registrations = append(registrations, ToolRegistration{
			Tool: githubTool, FailurePolicy: resilience.PolicyBestEffort, AllowedRoles: roles,
			AllowedTaskTypes:     []TaskType{TaskTypeDiagnosis},
			RequiredCapabilities: []ToolCapability{ToolCapabilityCode},
			RequiredDependencies: []ToolDependency{ToolDependencyGitHubMCP},
			RequiredPermissions:  []agentruntime.Permission{agentruntime.PermissionCodeRead},
		})
	}
	for index := range registrations {
		registrations[index].DegradationObserver = dependencies.DegradationObserver
	}
	catalog, err := NewToolCatalog(ctx, registrations...)
	if err != nil {
		return nil, err
	}
	profiles, err := BuildDefaultToolProfiles(toolProfileConfigFromDependencies(dependencies, githubNames))
	if err != nil {
		return nil, fmt.Errorf("build deployment Tool profiles: %w", err)
	}
	profile, ok := profiles.Profile(profileID)
	if !ok {
		return nil, fmt.Errorf("deployment Tool profiles are missing %q", profileID)
	}
	if err := catalog.BindProfile(profile, []string{ToolSkill}); err != nil {
		return nil, fmt.Errorf("bind deployment Tool profile %q: %w", profileID, err)
	}
	return catalog, nil
}

// toolProfileConfigFromDependencies 从启动时实际成功构造并注册的 Adapter
// 推导部署级 ToolProfileConfig；它不读取任何 per-run 引用、权限或依赖健康。
// githubNames 必须来自注册阶段已验证的 GitHub Tool 名称，不再重复调用
// Tool.Info。read_conversation_tool_result 是 Conversation Runtime 的固有
// 续读工具，总是注册，因此恒为 true。SQL 三件套与 Web 两件套按各自成功与否
// 分别声明。
func toolProfileConfigFromDependencies(
	dependencies DefaultToolCatalogDependencies,
	githubNames []string,
) ToolProfileConfig {
	return ToolProfileConfig{
		ExternalCaseConfigured:           true,
		SkillReferenceConfigured:         dependencies.SkillReference != nil,
		KnowledgeConfigured:              dependencies.KnowledgeSearch != nil,
		WebSearchConfigured:              dependencies.WebSearch != nil,
		FetchPublicPageConfigured:        dependencies.FetchPublicPage != nil,
		AttachmentConfigured:             dependencies.AttachmentReader != nil,
		SQLObjectDefinitionsConfigured:   dependencies.SQLObjectDefinitions != nil,
		SchemaCatalogConfigured:          dependencies.SchemaCatalog != nil,
		ReadonlyQueryConfigured:          dependencies.ReadonlyQuery != nil,
		GitHubToolNames:                  githubNames,
		DiagnosisCommandConfigured:       dependencies.CreateDiagnosisTask != nil,
		DiagnosisStatusConfigured:        dependencies.DiagnosisTaskStatus != nil,
		ConversationMemoryConfigured:     dependencies.ConversationMemorySources != nil,
		ConversationToolResultConfigured: true,
	}
}
