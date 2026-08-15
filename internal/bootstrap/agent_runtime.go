package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/attachment"
	"github.com/chitandabb/GoAgent/internal/contextgovernance"
	"github.com/chitandabb/GoAgent/internal/conversationmemory"
	"github.com/chitandabb/GoAgent/internal/externalcase"
	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/observability"
	"github.com/chitandabb/GoAgent/internal/platform/chatmodel"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformembedding "github.com/chitandabb/GoAgent/internal/platform/dashscopeembedding"
	platformrerank "github.com/chitandabb/GoAgent/internal/platform/dashscopererank"
	platformdirectweb "github.com/chitandabb/GoAgent/internal/platform/directweb"
	platformfirecrawl "github.com/chitandabb/GoAgent/internal/platform/firecrawl"
	"github.com/chitandabb/GoAgent/internal/platform/githubmcp"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	platformqueryrewrite "github.com/chitandabb/GoAgent/internal/platform/queryrewrite"
	platformsearxng "github.com/chitandabb/GoAgent/internal/platform/searxng"
	platformsqlserver "github.com/chitandabb/GoAgent/internal/platform/sqlserver"
	"github.com/chitandabb/GoAgent/internal/resilience"
	"github.com/chitandabb/GoAgent/internal/webresearch"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type agentRuntime struct {
	runner                    *mesagent.Runner
	conversation              *mesagent.ConversationRunner
	orchestrator              *mesagent.EvidenceOrchestrator
	availableDependencies     []mesagent.ToolDependency
	modelProvider             string
	modelID                   string
	promptVersion             string
	conversationPromptVersion string
	unavailable               error
	closeMCP                  func() error
	closeTracing              func() error
	webResearch               *webresearch.Service
}

type agentRuntimeRole uint8

const (
	agentRuntimeRoleConversation agentRuntimeRole = iota + 1
	agentRuntimeRoleDiagnosis
)

type agentRuntimeBuilders struct {
	conversationCreator    mesagent.DiagnosisTaskCreator
	conversationTaskStatus mesagent.DiagnosisTaskStatusReader
	attachmentReader       attachment.Reader
	chatModel              func(context.Context, config.ChatModelConfig) (model.ToolCallingChatModel, error)
	githubMCP              func(context.Context, config.GitHubMCPConfig, *zap.Logger) ([]tool.BaseTool, func() error, error)
	sqlObjectDefinitions   func(*sql.DB, config.SQLServerConfig, *zap.Logger) (tool.BaseTool, error)
	schemaCatalog          func(*gorm.DB, uuid.UUID, *zap.Logger) (tool.BaseTool, error)
	readonlyQuery          func(*sql.DB, config.SQLServerConfig, *gorm.DB, *zap.Logger) (tool.BaseTool, error)
	knowledgeSearch        func(context.Context, *gorm.DB, config.Config, model.ToolCallingChatModel, *zap.Logger) (tool.BaseTool, error)
	webResearch            func(context.Context, config.WebSearchConfig, *zap.Logger) (*webresearch.Service, error)
	conversationMemory     func(context.Context, *gorm.DB, config.Config) (mesagent.ConversationMemory, error)
}

func defaultAgentRuntimeBuilders() agentRuntimeBuilders {
	return agentRuntimeBuilders{
		chatModel: func(ctx context.Context, cfg config.ChatModelConfig) (model.ToolCallingChatModel, error) {
			instance, err := chatmodel.NewActive(ctx, cfg)
			if err != nil {
				return nil, err
			}
			return instance.Model, nil
		},
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
		webResearch:     buildWebResearchService,
		conversationMemory: func(ctx context.Context, db *gorm.DB, cfg config.Config) (mesagent.ConversationMemory, error) {
			return BuildConversationMemoryService(ctx, db, cfg)
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
	return buildAgentRuntimeForRole(
		ctx, agentRuntimeRoleConversation, cfg, externalCases, sqlServer, postgresDB, log, builders,
	)
}

func buildAgentRuntimeForRole(
	ctx context.Context,
	role agentRuntimeRole,
	cfg config.Config,
	externalCases mesagent.ExternalCaseGetter,
	sqlServer *sql.DB,
	postgresDB *gorm.DB,
	log *zap.Logger,
	builders agentRuntimeBuilders,
) (*agentRuntime, error) {
	runtime := &agentRuntime{
		promptVersion:             strings.TrimSpace(cfg.Agent.PromptVersion),
		conversationPromptVersion: strings.TrimSpace(cfg.Agent.ConversationPromptVersion),
	}
	if activeProfile, profileErr := cfg.Models.Chat.ActiveProfile(); profileErr == nil {
		runtime.modelProvider = strings.ToLower(strings.TrimSpace(activeProfile.Provider))
		runtime.modelID = strings.TrimSpace(activeProfile.Model)
	}
	if !cfg.Models.Chat.Enabled {
		return runtime, nil
	}
	if log == nil {
		return nil, errors.New("agent runtime logger is nil")
	}
	if cfg.Observability.Enabled {
		headers, headersErr := cfg.Observability.Headers()
		if headersErr != nil {
			log.Warn("OpenTelemetry exporter unavailable; continuing without trace export", zap.Error(headersErr))
		} else {
			traceRuntime, traceErr := observability.NewRuntime(ctx, observability.RuntimeConfig{
				ServiceName: cfg.Observability.ServiceName, Environment: cfg.Observability.Environment,
				Endpoint: cfg.Observability.OTLPEndpoint, Headers: headers,
				SampleRatio:   cfg.Observability.SampleRatio,
				ExportTimeout: time.Duration(cfg.Observability.ExportTimeoutMillis) * time.Millisecond,
				ErrorHandler: observability.NewRateLimitedErrorHandler(
					time.Duration(cfg.Observability.ErrorLogIntervalMillis)*time.Millisecond,
					func(exportErr error) {
						log.Warn("OpenTelemetry export failed", zap.Error(exportErr))
					},
				),
			})
			if traceErr != nil {
				log.Warn("OpenTelemetry exporter unavailable; continuing without trace export", zap.Error(traceErr))
			} else {
				runtime.closeTracing = func() error {
					shutdownCtx, cancel := context.WithTimeout(
						context.Background(),
						time.Duration(cfg.Observability.ExportTimeoutMillis)*time.Millisecond,
					)
					defer cancel()
					if shutdownErr := traceRuntime.Shutdown(shutdownCtx); shutdownErr != nil {
						log.Warn("OpenTelemetry exporter shutdown failed", zap.Error(shutdownErr))
					}
					return nil
				}
			}
		}
	}
	prompts, err := cfg.Agent.LoadPrompts()
	if err != nil {
		_ = runtime.close()
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
	// conversationSQLDataSourceID 是 cfg.SQLServer.ID 解析出的 Conversation
	// 只读数据源 Grant；无效 UUID 必须 fail-closed，不能静默开放 SQL。
	var conversationSQLDataSourceID uuid.UUID
	if sqlServer != nil && cfg.SQLServer.Enabled && len(cfg.SQLServer.Investigation.AllowedSchemas) > 0 {
		dataSourceID, parseErr := uuid.Parse(cfg.SQLServer.ID)
		if parseErr != nil {
			_ = runtime.close()
			return nil, fmt.Errorf("parse SQL Server data source id: %w", parseErr)
		}
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
		if schemaCatalog != nil || readonlyQuery != nil {
			conversationSQLDataSourceID = dataSourceID
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
		knowledgeSearch, err = builder(ctx, postgresDB, cfg, nil, log.Named("knowledge_search"))
		if err != nil {
			log.Warn("knowledge search Tool unavailable; continuing with other Agent capabilities", zap.Error(err))
			knowledgeSearch = nil
		} else if knowledgeSearch != nil {
			runtime.availableDependencies = append(runtime.availableDependencies, mesagent.ToolDependencyKnowledge)
		}
	}

	var webSearch tool.BaseTool
	var fetchPublicPage tool.BaseTool
	if cfg.WebSearch.Enabled {
		builder := builders.webResearch
		if builder == nil {
			builder = defaultAgentRuntimeBuilders().webResearch
		}
		runtime.webResearch, err = builder(ctx, cfg.WebSearch, log.Named("web_research"))
		if err != nil {
			log.Warn("public Web Search unavailable; continuing without web tools", zap.Error(err))
			runtime.webResearch = nil
		} else {
			webSearch, err = mesagent.NewWebSearchTool(runtime.webResearch)
			if err == nil {
				fetchPublicPage, err = mesagent.NewFetchPublicPageTool(runtime.webResearch)
			}
			if err != nil {
				return nil, fmt.Errorf("build public web tools: %w", err)
			}
			runtime.availableDependencies = append(runtime.availableDependencies, mesagent.ToolDependencyWebSearch)
		}
	}

	summaryTailEnabled := role == agentRuntimeRoleConversation &&
		cfg.Agent.ContextMemory.SummaryTailEnabled
	sourceRecoveryEnabled := role == agentRuntimeRoleConversation &&
		cfg.Agent.ContextMemory.SourceRecoveryEnabled
	var tokenBudget ConversationTokenBudgetRuntime
	if cfg.Agent.ContextMemory.ShadowPreflightEnabled ||
		cfg.Agent.ContextMemory.DiagnosisPreflightEnabled || sourceRecoveryEnabled {
		tokenBudget, err = buildConversationTokenBudgetRuntime(cfg)
		if err != nil {
			_ = runtime.close()
			return nil, err
		}
	}
	var conversationMemory mesagent.ConversationMemory
	if summaryTailEnabled || sourceRecoveryEnabled {
		if builders.conversationMemory == nil {
			_ = runtime.close()
			return nil, errors.New("conversation memory builder is unavailable")
		}
		conversationMemory, err = builders.conversationMemory(ctx, postgresDB, cfg)
		if err != nil {
			_ = runtime.close()
			return nil, fmt.Errorf("build active conversation memory: %w", err)
		}
		if conversationMemory == nil {
			_ = runtime.close()
			return nil, errors.New("active conversation memory is unavailable")
		}
	}
	var conversationMemorySources mesagent.ConversationMemorySourceReader
	if sourceRecoveryEnabled {
		if postgresDB == nil {
			_ = runtime.close()
			return nil, errors.New("conversation memory source recovery requires PostgreSQL")
		}
		tokenCounter, counterErr := conversationmemory.NewSourceTokenCounter(
			tokenBudget.Estimator, tokenBudget.Profile.Name,
		)
		if counterErr != nil {
			_ = runtime.close()
			return nil, fmt.Errorf("build conversation memory source token counter: %w", counterErr)
		}
		conversationMemorySources, err = conversationmemory.NewSourceRecovery(conversationmemory.SourceRecoveryConfig{
			ActiveSnapshots: conversationMemory,
			Messages:        platformpostgres.NewConversationRepository(postgresDB),
			TokenCounter:    tokenCounter,
			MaxMessages:     cfg.Agent.ContextMemory.SourceRecoveryMaxMessages,
			MaxTokens:       cfg.Agent.ContextMemory.SourceRecoveryMaxTokens,
		})
		if err != nil {
			_ = runtime.close()
			return nil, fmt.Errorf("build conversation memory source recovery: %w", err)
		}
	}

	var diagnosisPreflight mesagent.DiagnosisContextPreflightConfig
	if cfg.Agent.ContextMemory.DiagnosisPreflightEnabled {
		diagnosisPreflight = mesagent.DiagnosisContextPreflightConfig{
			Enabled: true, Planner: tokenBudget.Planner, ModelProfile: tokenBudget.Profile,
			SoftThresholdRatio:      cfg.Agent.ContextMemory.SoftThresholdRatio,
			HardThresholdRatio:      cfg.Agent.ContextMemory.HardThresholdRatio,
			ToolGrowthReserveTokens: cfg.Agent.ContextMemory.ToolGrowthReserveTokens,
			PreflightTimeout: time.Duration(
				cfg.Agent.ContextMemory.PreflightTimeoutMillis,
			) * time.Millisecond,
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
		WebSearch:             webSearch,
		FetchPublicPage:       fetchPublicPage,
		CreateDiagnosisTask:   builders.conversationCreator,
		AttachmentReader:      builders.attachmentReader,
		ContextPreflight:      diagnosisPreflight,
		ModelProvider:         runtime.modelProvider,
		ModelID:               runtime.modelID,
		Logger:                log.Named("runner"),
	})
	if err != nil {
		_ = runtime.close()
		return nil, fmt.Errorf("build Agent runner: %w", err)
	}
	conversationCatalog, err := mesagent.NewConversationDefaultToolCatalog(ctx, mesagent.DefaultToolCatalogDependencies{
		ExternalCases: externalCases, KnowledgeSearch: knowledgeSearch,
		WebSearch: webSearch, FetchPublicPage: fetchPublicPage,
		SchemaCatalog: schemaCatalog, ReadonlyQuery: readonlyQuery,
		CreateDiagnosisTask:       builders.conversationCreator,
		DiagnosisTaskStatus:       builders.conversationTaskStatus,
		AttachmentReader:          builders.attachmentReader,
		ConversationMemorySources: conversationMemorySources,
		DegradationObserver:       mesagent.NewToolDegradationLogObserver(log, "conversation"),
	})
	if err != nil {
		_ = runtime.close()
		return nil, fmt.Errorf("build conversation Tool catalog: %w", err)
	}
	if builders.attachmentReader != nil {
		runtime.availableDependencies = append(runtime.availableDependencies, mesagent.ToolDependencyAttachment)
	}
	var citationRepairer mesagent.ConversationCitationRepairer
	var citationRepairPolicy resilience.Policy
	if cfg.Agent.ConversationCitationRepairEnabled {
		citationRepairer, err = mesagent.NewModelConversationCitationRepairer(
			mesagent.ModelConversationCitationRepairerConfig{
				ChatModel:       chatModel,
				Instruction:     prompts.ConversationCitationRepairInstruction,
				PromptVersion:   cfg.Agent.ConversationCitationRepairPromptVersion,
				Timeout:         time.Duration(cfg.Agent.ConversationCitationRepairTimeoutMillis) * time.Millisecond,
				MaxOutputTokens: cfg.Agent.ConversationCitationRepairMaxOutputTokens,
			},
		)
		if err != nil {
			_ = runtime.close()
			return nil, fmt.Errorf("build conversation citation repairer: %w", err)
		}
		citationRepairPolicy = resilience.PolicyRepairThenFail
	}
	var contextPreflight mesagent.ConversationContextPreflightConfig
	if cfg.Agent.ContextMemory.ShadowPreflightEnabled {
		tailSelector, tailSelectorErr := contextgovernance.NewContinuousTailSelector(tokenBudget.Estimator)
		if tailSelectorErr != nil {
			_ = runtime.close()
			return nil, fmt.Errorf("build conversation continuous Tail selector: %w", tailSelectorErr)
		}
		contextPreflight = mesagent.ConversationContextPreflightConfig{
			Enabled: true, Planner: tokenBudget.Planner, TailSelector: tailSelector,
			ContinuousTailEnabled:   cfg.Agent.ContextMemory.ContinuousTailEnabled,
			SummaryTailEnabled:      summaryTailEnabled,
			Memory:                  conversationMemory,
			MemoryMaxRatio:          cfg.Agent.ContextMemory.MemoryMaxRatio,
			SummaryMaxRatio:         cfg.Agent.ContextMemory.SummaryMaxRatio,
			SummaryPromptMaxEntries: cfg.Agent.ContextMemory.Summary.EffectivePromptMaxEntries(),
			TailMaxRatio:            cfg.Agent.ContextMemory.TailMaxRatio,
			PreflightTimeout:        time.Duration(cfg.Agent.ContextMemory.PreflightTimeoutMillis) * time.Millisecond,
			SyncCompactionTimeout:   time.Duration(cfg.Agent.ContextMemory.SyncCompactionTimeoutMillis) * time.Millisecond,
			ModelProfile:            tokenBudget.Profile,
			SoftThresholdRatio:      cfg.Agent.ContextMemory.SoftThresholdRatio,
			HardThresholdRatio:      cfg.Agent.ContextMemory.HardThresholdRatio,
			ToolGrowthReserveTokens: cfg.Agent.ContextMemory.ToolGrowthReserveTokens,
		}
	}
	runtime.conversation, err = mesagent.NewConversationRunner(mesagent.ConversationRunnerConfig{
		ChatModel: chatModel, CitationRepairer: citationRepairer,
		CitationRepairPolicy: citationRepairPolicy, ToolCatalog: conversationCatalog,
		SystemInstruction:            prompts.ConversationInstruction,
		ModelProvider:                runtime.modelProvider,
		ModelID:                      runtime.modelID,
		PromptVersion:                runtime.conversationPromptVersion,
		AvailableDependencies:        runtime.availableDependencies,
		Logger:                       log.Named("conversation_runner"),
		MaxIterations:                cfg.Agent.ConversationMaxIterations,
		MaxToolCalls:                 cfg.Agent.MaxToolCalls,
		MaxTotalTokens:               cfg.Agent.MaxTotalTokens,
		MaxContextRunes:              cfg.Agent.ConversationMaxContextRunes,
		Timeout:                      time.Duration(cfg.Agent.ConversationTimeoutMillis) * time.Millisecond,
		MemorySourceRecoveryEnabled:  sourceRecoveryEnabled,
		MemorySourceRecoveryMaxCalls: cfg.Agent.ContextMemory.SourceRecoveryMaxCalls,
		ContextPreflight:             contextPreflight,
		SQLDataSourceID:              conversationSQLDataSourceID,
	})
	if err != nil {
		_ = runtime.close()
		return nil, fmt.Errorf("build conversation Agent runner: %w", err)
	}
	runtime.orchestrator, err = mesagent.NewEvidenceOrchestrator(ctx, mesagent.EvidenceOrchestratorConfig{
		Runner: runtime.runner, Logger: log.Named("evidence_orchestrator"),
		ReportPolicy: resilience.PolicyRepairThenFail,
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
		zap.String("conversation_prompt_version", runtime.conversationPromptVersion),
	)
	return runtime, nil
}

func buildWebResearchService(
	ctx context.Context,
	cfg config.WebSearchConfig,
	_ *zap.Logger,
) (*webresearch.Service, error) {
	sensitiveTerms, err := cfg.Redaction.SensitiveTerms()
	if err != nil {
		return nil, err
	}
	queryPolicy, err := webresearch.NewQueryPolicy(webresearch.QueryPolicyConfig{
		MaxInputRunes: cfg.Redaction.MaxInputRunes, MaxOutputRunes: cfg.Redaction.MaxOutputRunes,
		MinOutputRunes: cfg.Redaction.MinOutputRunes, SensitiveTerms: sensitiveTerms,
	})
	if err != nil {
		return nil, err
	}
	urlPolicy := webresearch.NewURLPolicy(nil)
	searchProviderName, contentProviderName := cfg.EffectiveProviders()
	searchProvider, contentProvider, err := buildWebProviders(ctx, cfg, searchProviderName, contentProviderName, urlPolicy)
	if err != nil {
		return nil, err
	}
	return webresearch.NewService(webresearch.ServiceConfig{
		SearchProvider: searchProvider, ContentProvider: contentProvider,
		QueryPolicy: queryPolicy, URLPolicy: urlPolicy,
		MaxResults: cfg.MaxResults, MaxFetchedPages: cfg.MaxFetchedPages,
		MaxPageChars: cfg.MaxPageChars, MaxRounds: cfg.MaxRounds,
		OfficialDomains: cfg.OfficialDomains, TrustedDomains: cfg.TrustedDomains,
	})
}

func buildWebProviders(
	ctx context.Context,
	cfg config.WebSearchConfig,
	searchName, contentName string,
	urlPolicy *webresearch.URLPolicy,
) (webresearch.SearchProvider, webresearch.ContentProvider, error) {
	searchName = strings.ToLower(strings.TrimSpace(searchName))
	contentName = strings.ToLower(strings.TrimSpace(contentName))
	timeout := time.Duration(cfg.TimeoutMillis) * time.Millisecond

	var firecrawlClient *platformfirecrawl.Client
	getFirecrawl := func() (*platformfirecrawl.Client, error) {
		if firecrawlClient != nil {
			return firecrawlClient, nil
		}
		apiKey, err := cfg.APIKeyFor("firecrawl")
		if err != nil {
			return nil, err
		}
		providerConfig := cfg.ProviderConfig("firecrawl")
		firecrawlClient, err = platformfirecrawl.New(platformfirecrawl.Config{
			BaseURL: providerConfig.BaseURL, APIKey: apiKey,
			Timeout: timeout, MaxResponseBytes: cfg.MaxResponseBytes,
		})
		return firecrawlClient, err
	}

	var searchProvider webresearch.SearchProvider
	switch searchName {
	case "firecrawl":
		client, err := getFirecrawl()
		if err != nil {
			return nil, nil, err
		}
		searchProvider = client
	case "searxng":
		providerConfig := cfg.ProviderConfig("searxng")
		client, err := platformsearxng.New(platformsearxng.Config{
			BaseURL: providerConfig.BaseURL, Timeout: timeout,
			MaxResponseBytes: cfg.MaxResponseBytes,
		})
		if err != nil {
			return nil, nil, err
		}
		searchProvider = client
	default:
		return nil, nil, fmt.Errorf("unsupported web search provider %q", searchName)
	}

	var contentProvider webresearch.ContentProvider
	switch contentName {
	case "firecrawl":
		client, err := getFirecrawl()
		if err != nil {
			return nil, nil, err
		}
		contentProvider = client
	case "direct":
		client, err := platformdirectweb.New(platformdirectweb.Config{
			Timeout: timeout, MaxResponseBytes: cfg.MaxResponseBytes,
			ValidateRedirect: func(redirectCtx context.Context, rawURL string) error {
				_, err := urlPolicy.Validate(redirectCtx, rawURL)
				return err
			},
		})
		if err != nil {
			return nil, nil, err
		}
		contentProvider = client
	default:
		return nil, nil, fmt.Errorf("unsupported web content provider %q", contentName)
	}
	return searchProvider, contentProvider, nil
}

func buildKnowledgeSearchTool(
	ctx context.Context,
	db *gorm.DB,
	cfg config.Config,
	chatModel model.ToolCallingChatModel,
	log *zap.Logger,
) (tool.BaseTool, error) {
	service, err := BuildKnowledgeSearchService(ctx, db, cfg, chatModel, log)
	if err != nil {
		return nil, err
	}
	return mesagent.NewSearchKnowledgeTool(service)
}

// BuildKnowledgeSearchService assembles the production retrieval chain so runtime tools and
// fixed-set evaluations exercise the same provider, fallback, rerank, and context behavior.
func BuildKnowledgeSearchService(
	ctx context.Context,
	db *gorm.DB,
	cfg config.Config,
	queryRewriteModelOverride model.ToolCallingChatModel,
	log *zap.Logger,
) (*knowledge.SearchService, error) {
	if db == nil {
		return nil, errors.New("knowledge search PostgreSQL database is nil")
	}
	if log == nil {
		log = zap.NewNop()
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
	rewriteConfig := cfg.Knowledge.Retrieval.QueryRewrite
	queryRewriter := buildQueryRewriter(ctx, cfg, queryRewriteModelOverride, log)
	var queryRewriteProvider, queryRewriteModel string
	if rewriteConfig.Enabled {
		if rewriteProfile, profileErr := cfg.Models.Chat.Profile(rewriteConfig.ModelProfile); profileErr == nil {
			queryRewriteProvider = strings.TrimSpace(rewriteProfile.Provider)
			queryRewriteModel = strings.TrimSpace(rewriteProfile.Model)
		}
	}
	degradationObserver := resilience.ObserverFunc(func(event resilience.DegradationEvent) {
		log.Warn("knowledge retrieval degraded",
			zap.String("operation", event.Operation), zap.String("policy", string(event.Policy)),
			zap.String("fallback", event.Fallback), zap.String("reason_code", event.ReasonCode),
			zap.String("run_id", event.RunID), zap.String("trace_id", event.TraceID),
			zap.String("provider", event.Provider), zap.String("model", event.Model),
			zap.Int64("duration_millis", event.DurationMillis),
		)
	})
	service, err := knowledge.NewSearchServiceWithOptions(
		repository, embedder, profile, retrievalCandidateN, knowledge.SearchServiceOptions{
			Reranker: reranker, RerankCandidateN: rerankCandidateN,
			ContextExpander: contextExpander,
			ContextWindow:   cfg.Knowledge.Retrieval.ContextWindow,
			ContextMaxRunes: cfg.Knowledge.Retrieval.ContextMaxRunes,
			ContextCompression: knowledge.ContextCompressionConfig{
				Enabled:   cfg.Knowledge.Retrieval.ContextCompression.Enabled,
				MaxChunks: cfg.Knowledge.Retrieval.ContextCompression.MaxChunks,
				MaxRunes:  cfg.Knowledge.Retrieval.ContextCompression.MaxRunes,
				MinScore:  cfg.Knowledge.Retrieval.ContextCompression.MinScore,
			},
			QueryRewriter: queryRewriter, MaxSubqueries: rewriteConfig.MaxSubqueries,
			QueryRewriteProvider: queryRewriteProvider, QueryRewriteModel: queryRewriteModel,
			RerankProvider:      strings.TrimSpace(cfg.Models.Rerank.Provider),
			RerankModel:         strings.TrimSpace(cfg.Models.Rerank.Model),
			DegradationObserver: degradationObserver,
		},
	)
	if err != nil {
		return nil, err
	}
	return service, nil
}

func buildQueryRewriter(
	ctx context.Context,
	cfg config.Config,
	override model.ToolCallingChatModel,
	log *zap.Logger,
) knowledge.QueryRewriter {
	rewriteConfig := cfg.Knowledge.Retrieval.QueryRewrite
	if !rewriteConfig.Enabled {
		return nil
	}
	if log == nil {
		log = zap.NewNop()
	}
	prompt, err := rewriteConfig.LoadPrompt()
	if err != nil {
		log.Warn("knowledge query rewrite unavailable; using original query", zap.Error(err))
		return unavailableQueryRewriter{err: err}
	}
	rewriteModel := override
	if rewriteModel == nil {
		instance, modelErr := chatmodel.NewProfile(ctx, cfg.Models.Chat, rewriteConfig.ModelProfile)
		if modelErr != nil {
			log.Warn("knowledge query rewrite model unavailable; using original query", zap.Error(modelErr))
			return unavailableQueryRewriter{err: modelErr}
		}
		rewriteModel = instance.Model
	}
	rewriter, err := platformqueryrewrite.New(
		rewriteModel, prompt, strings.TrimSpace(rewriteConfig.PromptVersion),
		time.Duration(rewriteConfig.TimeoutMillis)*time.Millisecond,
		rewriteConfig.MaxSubqueries, rewriteConfig.MaxOutputRunes,
	)
	if err != nil {
		log.Warn("knowledge query rewrite unavailable; using original query", zap.Error(err))
		return unavailableQueryRewriter{err: err}
	}
	return rewriter
}

type unavailableQueryRewriter struct{ err error }

func (r unavailableQueryRewriter) Rewrite(context.Context, string) (knowledge.QueryRewriteResult, error) {
	if r.err == nil {
		return knowledge.QueryRewriteResult{}, errors.New("query rewrite model is unavailable")
	}
	return knowledge.QueryRewriteResult{}, r.err
}

type unavailableExternalCaseGetter struct{}

func (unavailableExternalCaseGetter) Get(context.Context, uuid.UUID) (*externalcase.ExternalCase, error) {
	return nil, errors.New("external case service is unavailable")
}

func (r *agentRuntime) close() error {
	if r == nil {
		return nil
	}
	var mcpErr, tracingErr error
	if r.closeMCP != nil {
		mcpErr = r.closeMCP()
	}
	if r.closeTracing != nil {
		tracingErr = r.closeTracing()
	}
	return errors.Join(mcpErr, tracingErr)
}
