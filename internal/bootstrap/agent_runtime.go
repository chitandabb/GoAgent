package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/externalcase"
	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/platform/chatmodel"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformembedding "github.com/chitandabb/GoAgent/internal/platform/dashscopeembedding"
	platformrerank "github.com/chitandabb/GoAgent/internal/platform/dashscopererank"
	"github.com/chitandabb/GoAgent/internal/platform/githubmcp"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	platformqueryrewrite "github.com/chitandabb/GoAgent/internal/platform/queryrewrite"
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
	modelProvider         string
	modelID               string
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
	knowledgeSearch      func(context.Context, *gorm.DB, config.Config, model.ToolCallingChatModel, *zap.Logger) (tool.BaseTool, error)
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
		knowledgeSearch: buildKnowledgeSearchTool,
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
		modelProvider: strings.ToLower(strings.TrimSpace(cfg.Models.Chat.Provider)),
		modelID:       strings.TrimSpace(cfg.Models.Chat.Model),
		promptVersion: strings.TrimSpace(cfg.Agent.PromptVersion),
	}
	if !cfg.Models.Chat.Enabled {
		return runtime, nil
	}
	if log == nil {
		return nil, errors.New("agent runtime logger is nil")
	}
	prompts, err := cfg.Agent.LoadPrompts()
	if err != nil {
		return nil, fmt.Errorf("load Agent prompts: %w", err)
	}
	if externalCases == nil {
		externalCases = unavailableExternalCaseGetter{}
	} else {
		runtime.availableDependencies = append(runtime.availableDependencies, mesagent.ToolDependencyExternalCase)
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

	var knowledgeSearch tool.BaseTool
	if postgresDB != nil {
		builder := builders.knowledgeSearch
		if builder == nil {
			builder = defaultAgentRuntimeBuilders().knowledgeSearch
		}
		knowledgeSearch, err = builder(ctx, postgresDB, cfg, chatModel, log.Named("knowledge_search"))
		if err != nil {
			log.Warn("knowledge search Tool unavailable; continuing with other Agent capabilities", zap.Error(err))
			knowledgeSearch = nil
		} else if knowledgeSearch != nil {
			runtime.availableDependencies = append(runtime.availableDependencies, mesagent.ToolDependencyKnowledge)
		}
	}

	runtime.runner, err = mesagent.NewDefaultRunner(ctx, mesagent.DefaultRunnerDependencies{
		ChatModel:             chatModel,
		ExternalCases:         externalCases,
		SkillRoot:             cfg.Agent.SkillsDirectory,
		SystemInstruction:     prompts.SystemInstruction,
		BaselineInstruction:   prompts.BaselineInstruction,
		GitHubTools:           githubTools,
		GitHubArgumentRewrite: argumentRewrite,
		SQLObjectDefinitions:  sqlObjectDefinitions,
		SchemaCatalog:         schemaCatalog,
		ReadonlyQuery:         readonlyQuery,
		KnowledgeSearch:       knowledgeSearch,
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
		Timeout:                   time.Duration(cfg.Agent.TimeoutMillis) * time.Millisecond,
		ReportContractInstruction: prompts.ReportContractInstruction,
	})
	if err != nil {
		_ = runtime.close()
		return nil, fmt.Errorf("build Evidence orchestrator: %w", err)
	}
	log.Info("Agent runtime initialized",
		zap.String("skills_directory", cfg.Agent.SkillsDirectory),
		zap.String("prompt_version", runtime.promptVersion),
	)
	return runtime, nil
}

func buildKnowledgeSearchTool(
	ctx context.Context,
	db *gorm.DB,
	cfg config.Config,
	chatModel model.ToolCallingChatModel,
	log *zap.Logger,
) (tool.BaseTool, error) {
	if db == nil {
		return nil, errors.New("knowledge search PostgreSQL database is nil")
	}
	repository := platformpostgres.NewKnowledgeRepository(db)
	var embedder knowledge.Embedder
	var profile knowledge.EmbeddingProfile
	if cfg.Models.Embedding.Enabled {
		client, err := platformembedding.NewClient(cfg.Models.Embedding, nil)
		if err != nil {
			log.Warn("knowledge vector search unavailable; using FTS fallback", zap.Error(err))
		} else if profile, err = cfg.Models.Embedding.Profile(); err != nil {
			log.Warn("knowledge vector profile unavailable; using FTS fallback", zap.Error(err))
		} else if err := platformpostgres.NewKnowledgeWorkerRepository(db).EnsureEmbeddingProfile(ctx, profile); err != nil {
			log.Warn("knowledge vector profile is not active; using FTS fallback", zap.Error(err))
		} else {
			embedder = client
		}
	}
	var reranker knowledge.Reranker
	rerankCandidateN := 0
	if cfg.Models.Rerank.Enabled {
		client, err := platformrerank.NewClient(cfg.Models.Rerank, nil)
		if err != nil {
			log.Warn("knowledge rerank unavailable; using retrieval order", zap.Error(err))
		} else {
			reranker = client
			rerankCandidateN = cfg.Models.Rerank.MaxCandidates
		}
	}
	retrievalCandidateN := 20
	if rerankCandidateN > retrievalCandidateN {
		retrievalCandidateN = rerankCandidateN
	}
	var contextExpander knowledge.ContextExpander
	if cfg.Knowledge.Retrieval.ContextExpansionEnabled {
		contextExpander = repository
	}
	var queryRewriter knowledge.QueryRewriter
	rewriteConfig := cfg.Knowledge.Retrieval.QueryRewrite
	if rewriteConfig.Enabled {
		prompt, promptErr := rewriteConfig.LoadPrompt()
		if promptErr != nil {
			log.Warn("knowledge query rewrite unavailable; using original query", zap.Error(promptErr))
		} else if chatModel == nil {
			log.Warn("knowledge query rewrite unavailable; chat model is nil")
		} else {
			queryRewriter, promptErr = platformqueryrewrite.New(
				chatModel, prompt, strings.TrimSpace(rewriteConfig.PromptVersion),
				time.Duration(rewriteConfig.TimeoutMillis)*time.Millisecond,
				rewriteConfig.MaxSubqueries, rewriteConfig.MaxOutputRunes,
			)
			if promptErr != nil {
				log.Warn("knowledge query rewrite unavailable; using original query", zap.Error(promptErr))
				queryRewriter = nil
			}
		}
	}
	service, err := knowledge.NewSearchServiceWithOptions(
		repository, embedder, profile, retrievalCandidateN, knowledge.SearchServiceOptions{
			Reranker: reranker, RerankCandidateN: rerankCandidateN,
			ContextExpander: contextExpander,
			ContextWindow:   cfg.Knowledge.Retrieval.ContextWindow,
			ContextMaxRunes: cfg.Knowledge.Retrieval.ContextMaxRunes,
			QueryRewriter:   queryRewriter, MaxSubqueries: rewriteConfig.MaxSubqueries,
		},
	)
	if err != nil {
		return nil, err
	}
	return mesagent.NewSearchKnowledgeTool(service)
}

type unavailableExternalCaseGetter struct{}

func (unavailableExternalCaseGetter) Get(context.Context, uuid.UUID) (*externalcase.ExternalCase, error) {
	return nil, errors.New("external case service is unavailable")
}

func (r *agentRuntime) close() error {
	if r == nil || r.closeMCP == nil {
		return nil
	}
	return r.closeMCP()
}
