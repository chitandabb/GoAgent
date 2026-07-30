package bootstrap

import (
	"context"
	"errors"
	"fmt"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/platform/chatmodel"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/platform/githubmcp"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"go.uber.org/zap"
)

type agentRuntime struct {
	runner      *mesagent.Runner
	unavailable error
	closeMCP    func() error
}

type agentRuntimeBuilders struct {
	loadSkills func(string) ([]mesagent.SkillDefinition, error)
	chatModel  func(context.Context, config.ChatModelConfig) (model.ToolCallingChatModel, error)
	githubMCP  func(context.Context, config.GitHubMCPConfig, *zap.Logger) ([]tool.BaseTool, func() error, error)
}

func defaultAgentRuntimeBuilders() agentRuntimeBuilders {
	return agentRuntimeBuilders{
		loadSkills: mesagent.LoadSkillDefinitions,
		chatModel:  chatmodel.NewStepFun,
		githubMCP: func(ctx context.Context, cfg config.GitHubMCPConfig, log *zap.Logger) (
			[]tool.BaseTool, func() error, error,
		) {
			connection, err := githubmcp.Connect(ctx, cfg, log)
			if err != nil {
				return nil, nil, err
			}
			return connection.Tools, connection.Close, nil
		},
	}
}

func buildAgentRuntime(
	ctx context.Context,
	cfg config.Config,
	externalCases mesagent.ExternalCaseGetter,
	log *zap.Logger,
	builders agentRuntimeBuilders,
) (*agentRuntime, error) {
	runtime := &agentRuntime{}
	if !cfg.Models.Chat.Enabled {
		return runtime, nil
	}
	if log == nil {
		return nil, errors.New("agent runtime logger is nil")
	}
	definitions, err := builders.loadSkills(cfg.Agent.SkillsDirectory)
	if err != nil {
		return nil, fmt.Errorf("load Agent skills: %w", err)
	}
	if externalCases == nil {
		runtime.unavailable = errors.New("external case service is unavailable")
		log.Warn("Agent unavailable; continuing without Agent runtime", zap.Error(runtime.unavailable))
		return runtime, nil
	}
	chatModel, err := builders.chatModel(ctx, cfg.Models.Chat)
	if err != nil {
		runtime.unavailable = fmt.Errorf("build chat model: %w", err)
		log.Warn("Agent unavailable; continuing without Agent runtime", zap.Error(runtime.unavailable))
		return runtime, nil
	}

	var githubTools []tool.BaseTool
	var argumentRewrite mesagent.ArgumentRewriter
	if cfg.GitHubMCP.Enabled {
		githubTools, runtime.closeMCP, err = builders.githubMCP(ctx, cfg.GitHubMCP, log.Named("github_mcp"))
		if err != nil {
			log.Warn("GitHub MCP unavailable; code investigation Skill disabled", zap.Error(err))
			githubTools = nil
			runtime.closeMCP = nil
		} else {
			argumentRewrite = githubmcp.NewArgumentRewriter(cfg.GitHubMCP)
		}
	}

	runtime.runner, err = mesagent.NewDefaultRunner(ctx, mesagent.DefaultRunnerDependencies{
		ChatModel:             chatModel,
		ExternalCases:         externalCases,
		SkillDefinitions:      definitions,
		GitHubTools:           githubTools,
		GitHubArgumentRewrite: argumentRewrite,
		Logger:                log.Named("runner"),
	})
	if err != nil {
		_ = runtime.close()
		return nil, fmt.Errorf("build Agent runner: %w", err)
	}
	log.Info("Agent runtime initialized", zap.Int("skills", len(definitions)))
	return runtime, nil
}

func (r *agentRuntime) close() error {
	if r == nil || r.closeMCP == nil {
		return nil
	}
	return r.closeMCP()
}
