package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/chitandabb/GoAgent/internal/attachment"
	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/conversationworker"
	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/chitandabb/GoAgent/internal/externalcase"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	platformrabbitmq "github.com/chitandabb/GoAgent/internal/platform/rabbitmq"
	platformsqlserver "github.com/chitandabb/GoAgent/internal/platform/sqlserver"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

type ConversationWorkerApp struct {
	consumer *platformrabbitmq.ConversationConsumer
	agent    *agentRuntime
	deps     *runtimeDependencies
	workerID string
	log      *zap.Logger
}

func NewConversationWorkerApp(
	ctx context.Context,
	cfg config.Config,
	log *zap.Logger,
) (*ConversationWorkerApp, error) {
	if log == nil {
		return nil, errors.New("conversation worker logger is nil")
	}
	if !cfg.RabbitMQ.Enabled {
		return nil, errors.New("conversation worker requires RabbitMQ")
	}
	deps, err := openRuntimeDependencies(ctx, cfg, log, defaultDependencyOpeners())
	if err != nil {
		return nil, err
	}
	closeDependencies := func() { _ = deps.close() }

	conversationRepository := platformpostgres.NewConversationRepository(deps.db)
	conversationService, err := conversation.NewService(conversationRepository)
	if err != nil {
		closeDependencies()
		return nil, fmt.Errorf("build conversation service: %w", err)
	}
	var attachmentService *attachment.Service
	if deps.objectStore != nil {
		attachmentService, err = buildAttachmentService(cfg, deps.db, deps.objectStore, deps.objectStoreError)
		if err != nil {
			closeDependencies()
			return nil, fmt.Errorf("build conversation attachment service: %w", err)
		}
	}
	externalCaseService, err := buildConversationWorkerExternalCases(ctx, cfg, deps, log)
	if err != nil {
		closeDependencies()
		return nil, err
	}
	var diagnosisTaskService *diagnosis.DiagnosisTaskService
	if externalCaseService != nil {
		diagnosisTaskService, err = diagnosis.NewDiagnosisTaskService(
			platformpostgres.NewDiagnosisTaskRepository(deps.db), externalCaseService,
		)
		if err != nil {
			closeDependencies()
			return nil, fmt.Errorf("build conversation diagnosis task service: %w", err)
		}
		if _, err := conversationService.WithDiagnosisCommandDependencies(
			diagnosisTaskService, diagnosisTaskService, externalCaseService,
		); err != nil {
			closeDependencies()
			return nil, fmt.Errorf("wire conversation diagnosis command: %w", err)
		}
	}

	runtimeBuilders := defaultAgentRuntimeBuilders()
	runtimeBuilders.conversationCreator = conversationService
	runtimeBuilders.attachmentReader = attachmentService
	if diagnosisTaskService != nil {
		runtimeBuilders.conversationTaskStatus = conversationService
	}
	runtime, err := buildAgentRuntime(
		ctx, cfg, externalCaseService, deps.sqlServer, deps.db,
		log.Named("agent"), runtimeBuilders,
	)
	if err != nil {
		closeDependencies()
		return nil, err
	}
	if runtime == nil || runtime.conversation == nil {
		if runtime != nil {
			_ = runtime.close()
		}
		closeDependencies()
		if runtime != nil && runtime.unavailable != nil {
			return nil, fmt.Errorf("conversation Agent runtime unavailable: %w", runtime.unavailable)
		}
		return nil, errors.New("conversation Agent runtime is disabled")
	}
	if _, err := conversationService.WithAgentResponder(runtime.conversation); err != nil {
		_ = runtime.close()
		closeDependencies()
		return nil, fmt.Errorf("wire conversation Agent responder: %w", err)
	}

	workerID := "conversation-worker-" + uuid.NewString()
	worker, err := conversationworker.New(
		conversationRepository, conversationService,
		conversationworker.Config{
			WorkerID:      workerID,
			LeaseDuration: time.Duration(cfg.RabbitMQ.WorkerLeaseMillis) * time.Millisecond,
			RenewInterval: time.Duration(cfg.RabbitMQ.WorkerRenewIntervalMillis) * time.Millisecond,
			MaxAttempts:   cfg.RabbitMQ.WorkerMaxAttempts,
		},
	)
	if err != nil {
		_ = runtime.close()
		closeDependencies()
		return nil, err
	}
	consumer, err := platformrabbitmq.OpenConversationConsumer(
		cfg.RabbitMQ, worker, workerID, log.Named("rabbitmq_consumer"),
	)
	if err != nil {
		_ = runtime.close()
		closeDependencies()
		return nil, err
	}
	return &ConversationWorkerApp{
		consumer: consumer, agent: runtime, deps: deps,
		workerID: workerID, log: log,
	}, nil
}

func (a *ConversationWorkerApp) Run(ctx context.Context) error {
	if a == nil || a.consumer == nil {
		return errors.New("conversation worker app is unavailable")
	}
	a.log.Info("conversation worker started", zap.String("worker_id", a.workerID))
	err := a.consumer.Run(ctx)
	if ctx.Err() != nil && errors.Is(err, context.Canceled) {
		a.log.Info("conversation worker stopped", zap.String("worker_id", a.workerID))
		return nil
	}
	return err
}

func (a *ConversationWorkerApp) Close() error {
	if a == nil {
		return nil
	}
	var consumerErr, agentErr, dependencyErr error
	if a.consumer != nil {
		consumerErr = a.consumer.Close()
	}
	if a.agent != nil {
		agentErr = a.agent.close()
	}
	if a.deps != nil {
		dependencyErr = a.deps.close()
	}
	return errors.Join(consumerErr, agentErr, dependencyErr)
}

func buildConversationWorkerExternalCases(
	ctx context.Context,
	cfg config.Config,
	deps *runtimeDependencies,
	log *zap.Logger,
) (*externalcase.Service, error) {
	if !cfg.SQLServer.Enabled {
		return nil, nil
	}
	dataSourceID, err := uuid.Parse(cfg.SQLServer.ID)
	if err != nil {
		return nil, fmt.Errorf("parse SQL Server data source id: %w", err)
	}
	externalCaseRepository := platformpostgres.NewExternalCaseRepository(deps.db)
	if err := externalCaseRepository.EnsureCaseSource(
		ctx, dataSourceID, cfg.SQLServer.Code, cfg.SQLServer.Name, cfg.SQLServer.Environment,
	); err != nil {
		return nil, fmt.Errorf("sync ERP case source: %w", err)
	}
	var reader externalcase.Reader
	if deps.sqlServer == nil {
		reader = externalcase.NewUnavailableReader(deps.sqlServerError)
	} else {
		reader, err = platformsqlserver.NewExternalCaseReader(
			deps.sqlServer, cfg.SQLServer, log.Named("sqlserver"),
		)
		if err != nil {
			return nil, fmt.Errorf("build ERP external case reader: %w", err)
		}
	}
	service, err := externalcase.NewService(externalcase.DataSource{
		ID: dataSourceID, Code: cfg.SQLServer.Code, Name: cfg.SQLServer.Name,
		Type: "sqlserver", Role: "case_source", Environment: cfg.SQLServer.Environment,
		SafetyMode: "read_only", Status: "active",
	}, reader, externalCaseRepository)
	if err != nil {
		return nil, fmt.Errorf("build ERP external case service: %w", err)
	}
	return service, nil
}
