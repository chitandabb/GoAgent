package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"go.uber.org/zap"
)

type DefaultRunnerDependencies struct {
	ChatModel             model.ToolCallingChatModel
	ExternalCases         ExternalCaseGetter
	SkillDefinitions      []SkillDefinition
	GitHubTools           []tool.BaseTool
	GitHubArgumentRewrite ArgumentRewriter
	Logger                *zap.Logger
}

// NewDefaultRunner 完成 Agent 模块的手动依赖装配。
// GitHub MCP 不可用时只移除 code-investigation，工单诊断仍可继续运行。
func NewDefaultRunner(ctx context.Context, dependencies DefaultRunnerDependencies) (*Runner, error) {
	if dependencies.ChatModel == nil || dependencies.ExternalCases == nil || dependencies.Logger == nil {
		return nil, errors.New("default runner model, external cases, and logger are required")
	}
	if len(dependencies.SkillDefinitions) == 0 {
		return nil, errors.New("default runner skill definitions are required")
	}
	readExternalCase, err := NewReadExternalCaseTool(dependencies.ExternalCases)
	if err != nil {
		return nil, fmt.Errorf("build external case tool: %w", err)
	}
	requestCodeInvestigation, err := NewRequestCodeInvestigationTool()
	if err != nil {
		return nil, fmt.Errorf("build code investigation handoff tool: %w", err)
	}

	activeDefinitions := make([]SkillDefinition, 0, len(dependencies.SkillDefinitions))
	for _, definition := range dependencies.SkillDefinitions {
		// GitHub MCP 是可降级依赖；不可用时不把代码调查节点加入已编译 Graph。
		if definition.ID == SkillCodeInvestigation && len(dependencies.GitHubTools) == 0 {
			continue
		}
		activeDefinitions = append(activeDefinitions, definition)
	}
	if len(activeDefinitions) == 0 {
		return nil, errors.New("default runner has no active skills")
	}
	allTools := []tool.BaseTool{readExternalCase, requestCodeInvestigation}
	if len(dependencies.GitHubTools) > 0 {
		if dependencies.GitHubArgumentRewrite == nil {
			return nil, errors.New("github argument rewriter is required when github tools are enabled")
		}
		allTools = append(allTools, dependencies.GitHubTools...)
	}

	skillRegistry, err := NewRegistry(activeDefinitions...)
	if err != nil {
		return nil, fmt.Errorf("build skill registry: %w", err)
	}
	toolRegistry, err := NewToolRegistry(ctx, allTools...)
	if err != nil {
		return nil, fmt.Errorf("build tool registry: %w", err)
	}
	router, err := NewRuleRouter(skillRegistry)
	if err != nil {
		return nil, err
	}
	executors := make(map[SkillID]SkillExecutor, len(activeDefinitions))
	for _, definition := range activeDefinitions {
		var rewrite ArgumentRewriter
		if definition.ID == SkillCodeInvestigation {
			rewrite = dependencies.GitHubArgumentRewrite
		}
		executor, buildErr := NewReActExecutor(
			ctx, definition, dependencies.ChatModel, toolRegistry, rewrite, dependencies.Logger,
		)
		if buildErr != nil {
			return nil, buildErr
		}
		executors[definition.ID] = executor
	}
	return NewRunner(ctx, router, skillRegistry, executors)
}
