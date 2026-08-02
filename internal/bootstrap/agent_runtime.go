package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/platform/chatmodel"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/platform/githubmcp"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	platformsqlserver "github.com/chitandabb/GoAgent/internal/platform/sqlserver"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type agentRuntime struct {
	runner                *mesagent.Runner
	orchestrator          *mesagent.EvidenceOrchestrator
	availableDependencies []mesagent.ToolDependency
	modelName             string
	modelVersion          string
	promptVersion         string
	unavailable           error
	closeMCP              func() error
}

type agentRuntimeBuilders struct {
	chatModel            func(context.Context, config.ChatModelConfig) (model.ToolCallingChatModel, error)
	githubMCP            func(context.Context, config.GitHubMCPConfig, *zap.Logger) ([]tool.BaseTool, func() error, error)
	sqlObjectDefinitions func(*sql.DB, config.SQLServerConfig, *zap.Logger) (tool.BaseTool, error)
	schemaCatalog        func(*gorm.DB, uuid.UUID, *zap.Logger) (tool.BaseTool, error)
	readonlyQuery        func(*sql.DB, config.SQLServerConfig, *gorm.DB, *zap.Logger) (tool.BaseTool, error)
}

func defaultAgentRuntimeBuilders() agentRuntimeBuilders {
	return agentRuntimeBuilders{
		chatModel: chatmodel.NewStepFun,
		githubMCP: func(ctx context.Context, cfg config.GitHubMCPConfig, log *zap.Logger) (
			[]tool.BaseTool, func() error, error,
		) {
			connection, err := githubmcp.Connect(ctx, cfg, log)
			if err != nil {
				return nil, nil, err
			}
			return connection.Tools, connection.Close, nil
		},
		sqlObjectDefinitions: func(db *sql.DB, cfg config.SQLServerConfig, log *zap.Logger) (tool.BaseTool, error) {
			reader, err := platformsqlserver.NewObjectDefinitionReader(db, cfg, log)
			if err != nil {
				return nil, err
			}
			return mesagent.NewDatabaseObjectDefinitionTool(reader)
		},
		schemaCatalog: func(db *gorm.DB, dataSourceID uuid.UUID, _ *zap.Logger) (tool.BaseTool, error) {
			repository := platformpostgres.NewSchemaCatalogRepository(db)
			return mesagent.NewSearchSchemaCatalogTool(repository)
		},
		readonlyQuery: func(db *sql.DB, cfg config.SQLServerConfig, postgresDB *gorm.DB, log *zap.Logger) (tool.BaseTool, error) {
			catalog := platformpostgres.NewSchemaCatalogRepository(postgresDB)
			executor, err := platformsqlserver.NewReadonlyQueryExecutor(db, cfg, catalog, log)
			if err != nil {
				return nil, err
			}
			return mesagent.NewExecuteReadonlyQueryTool(executor)
		},
	}
}

func buildAgentRuntime(
	ctx context.Context,
	cfg config.Config,
	externalCases mesagent.ExternalCaseGetter,
	sqlServer *sql.DB,
	postgresDB *gorm.DB,
	log *zap.Logger,
	builders agentRuntimeBuilders,
) (*agentRuntime, error) {
	runtime := &agentRuntime{
		modelName:     strings.TrimSpace(cfg.Models.Chat.Model),
		modelVersion:  strings.TrimSpace(cfg.Models.Chat.Model),
		promptVersion: "evidence-gate-v1",
	}
	if !cfg.Models.Chat.Enabled {
		return runtime, nil
	}
	if log == nil {
		return nil, errors.New("agent runtime logger is nil")
	}
	if externalCases == nil {
		runtime.unavailable = errors.New("external case service is unavailable")
		log.Warn("Agent unavailable; continuing without Agent runtime", zap.Error(runtime.unavailable))
		return runtime, nil
	}
	runtime.availableDependencies = append(runtime.availableDependencies, mesagent.ToolDependencyExternalCase)
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
			log.Warn("GitHub MCP unavailable; continuing without GitHub tools", zap.Error(err))
			githubTools = nil
			runtime.closeMCP = nil
		} else {
			argumentRewrite = githubmcp.NewArgumentRewriter()
			runtime.availableDependencies = append(runtime.availableDependencies, mesagent.ToolDependencyGitHubMCP)
		}
	}

	var sqlObjectDefinitions tool.BaseTool
	var schemaCatalog tool.BaseTool
	var readonlyQuery tool.BaseTool
	if sqlServer != nil && cfg.SQLServer.Enabled && len(cfg.SQLServer.Investigation.AllowedSchemas) > 0 {
		builder := builders.sqlObjectDefinitions
		if builder == nil {
			builder = defaultAgentRuntimeBuilders().sqlObjectDefinitions
		}
		sqlObjectDefinitions, err = builder(sqlServer, cfg.SQLServer, log.Named("sqlserver_investigation"))
		if err != nil {
			log.Warn("SQL investigation Tool unavailable; continuing without object definition Tool", zap.Error(err))
			sqlObjectDefinitions = nil
		}
		if postgresDB != nil {
			dataSourceID, parseErr := uuid.Parse(cfg.SQLServer.ID)
			if parseErr != nil {
				log.Warn("SQL schema catalog unavailable; data source id is invalid", zap.Error(parseErr))
			} else {
				catalogBuilder := builders.schemaCatalog
				if catalogBuilder == nil {
					catalogBuilder = defaultAgentRuntimeBuilders().schemaCatalog
				}
				schemaCatalog, err = catalogBuilder(postgresDB, dataSourceID, log.Named("schema_catalog"))
				if err != nil {
					log.Warn("SQL schema catalog Tool unavailable; continuing without catalog search", zap.Error(err))
					schemaCatalog = nil
				}
				queryBuilder := builders.readonlyQuery
				if queryBuilder == nil {
					queryBuilder = defaultAgentRuntimeBuilders().readonlyQuery
				}
				readonlyQuery, err = queryBuilder(sqlServer, cfg.SQLServer, postgresDB, log.Named("readonly_query"))
				if err != nil {
					log.Warn("SQL readonly query Tool unavailable; continuing without query execution", zap.Error(err))
					readonlyQuery = nil
				}
			}
		}
	}
	if sqlObjectDefinitions != nil || schemaCatalog != nil || readonlyQuery != nil {
		runtime.availableDependencies = append(runtime.availableDependencies, mesagent.ToolDependencySQLServer)
	}

	runtime.runner, err = mesagent.NewDefaultRunner(ctx, mesagent.DefaultRunnerDependencies{
		ChatModel:             chatModel,
		ExternalCases:         externalCases,
		SkillRoot:             cfg.Agent.SkillsDirectory,
		GitHubTools:           githubTools,
		GitHubArgumentRewrite: argumentRewrite,
		SQLObjectDefinitions:  sqlObjectDefinitions,
		SchemaCatalog:         schemaCatalog,
		ReadonlyQuery:         readonlyQuery,
		Logger:                log.Named("runner"),
	})
	if err != nil {
		_ = runtime.close()
		return nil, fmt.Errorf("build Agent runner: %w", err)
	}
	runtime.orchestrator, err = mesagent.NewEvidenceOrchestrator(ctx, mesagent.EvidenceOrchestratorConfig{
		Runner: runtime.runner, Logger: log.Named("evidence_orchestrator"),
		MaxAgentRuns: cfg.Agent.MaxAgentRuns, MaxToolCalls: cfg.Agent.MaxToolCalls,
		MaxEvidenceItems: cfg.Agent.MaxEvidenceItems, MaxTotalTokens: cfg.Agent.MaxTotalTokens,
		Timeout: time.Duration(cfg.Agent.TimeoutMillis) * time.Millisecond,
	})
	if err != nil {
		_ = runtime.close()
		return nil, fmt.Errorf("build Evidence orchestrator: %w", err)
	}
	log.Info("Agent runtime initialized", zap.String("skills_directory", cfg.Agent.SkillsDirectory))
	return runtime, nil
}

func (r *agentRuntime) close() error {
	if r == nil || r.closeMCP == nil {
		return nil
	}
	return r.closeMCP()
}
