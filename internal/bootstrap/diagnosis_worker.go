package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/chitandabb/GoAgent/internal/diagnosisworker"
	"github.com/chitandabb/GoAgent/internal/externalcase"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	platformrabbitmq "github.com/chitandabb/GoAgent/internal/platform/rabbitmq"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

type DiagnosisWorkerApp struct {
	consumer *platformrabbitmq.DiagnosisConsumer
	agent    *agentRuntime
	deps     *runtimeDependencies
	workerID string
	log      *zap.Logger
}

func NewDiagnosisWorkerApp(
	ctx context.Context,
	cfg config.Config,
	log *zap.Logger,
) (*DiagnosisWorkerApp, error) {
	if log == nil {
		return nil, errors.New("diagnosis worker logger is nil")
	}
	if !cfg.RabbitMQ.Enabled {
		return nil, errors.New("diagnosis worker requires RabbitMQ")
	}
	deps, err := openRuntimeDependencies(ctx, cfg, log, defaultDependencyOpeners())
	if err != nil {
		return nil, err
	}
	closeDependencies := func() { _ = deps.close() }
	runtime, err := buildAgentRuntime(
		ctx, cfg, snapshotExternalCaseGetter{}, deps.sqlServer, deps.db,
		log.Named("agent"), defaultAgentRuntimeBuilders(),
	)
	if err != nil {
		closeDependencies()
		return nil, err
	}
	if runtime == nil || runtime.orchestrator == nil {
		_ = runtime.close()
		closeDependencies()
		if runtime != nil && runtime.unavailable != nil {
			return nil, fmt.Errorf("diagnosis Agent runtime unavailable: %w", runtime.unavailable)
		}
		return nil, errors.New("diagnosis Agent runtime is disabled")
	}

	taskRepository := platformpostgres.NewDiagnosisTaskRepository(deps.db)
	leaseService, err := diagnosis.NewTaskExecutionService(
		taskRepository, time.Duration(cfg.RabbitMQ.WorkerLeaseMillis)*time.Millisecond,
	)
	if err != nil {
		_ = runtime.close()
		closeDependencies()
		return nil, err
	}
	workerID := "diagnosis-worker-" + uuid.NewString()
	worker, err := diagnosisworker.New(
		leaseService,
		platformpostgres.NewDiagnosisWorkerRepository(deps.db),
		diagnosisAgentExecutor{runtime: runtime},
		diagnosisworker.Config{
			WorkerID:      workerID,
			RenewInterval: time.Duration(cfg.RabbitMQ.WorkerRenewIntervalMillis) * time.Millisecond,
			MaxAttempts:   cfg.RabbitMQ.WorkerMaxAttempts,
		},
	)
	if err != nil {
		_ = runtime.close()
		closeDependencies()
		return nil, err
	}
	consumer, err := platformrabbitmq.OpenDiagnosisConsumer(
		cfg.RabbitMQ, worker, workerID, log.Named("rabbitmq_consumer"),
	)
	if err != nil {
		_ = runtime.close()
		closeDependencies()
		return nil, err
	}
	return &DiagnosisWorkerApp{
		consumer: consumer, agent: runtime, deps: deps,
		workerID: workerID, log: log,
	}, nil
}

func (a *DiagnosisWorkerApp) Run(ctx context.Context) error {
	if a == nil || a.consumer == nil {
		return errors.New("diagnosis worker app is unavailable")
	}
	a.log.Info("diagnosis worker started", zap.String("worker_id", a.workerID))
	err := a.consumer.Run(ctx)
	if ctx.Err() != nil && errors.Is(err, context.Canceled) {
		a.log.Info("diagnosis worker stopped", zap.String("worker_id", a.workerID))
		return nil
	}
	return err
}

func (a *DiagnosisWorkerApp) Close() error {
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

type diagnosisAgentExecutor struct {
	runtime *agentRuntime
}

func (e diagnosisAgentExecutor) Execute(
	ctx context.Context,
	task diagnosisworker.Task,
) (diagnosisworker.ExecutionResult, error) {
	if e.runtime == nil || e.runtime.orchestrator == nil {
		return diagnosisworker.ExecutionResult{}, errors.New("diagnosis Agent runtime is unavailable")
	}
	dataSources := make([]agent.ScopedDataSource, 0, len(task.DataSources))
	for _, source := range task.DataSources {
		dataSources = append(dataSources, agent.ScopedDataSource{
			ID: source.ID, Role: source.Role, SafetyMode: source.SafetyMode,
		})
	}
	scope, err := agent.NewTaskScope(agent.TaskScopeConfig{
		UserID: task.CreatedBy, Role: task.Role, TaskType: agent.TaskTypeDiagnosis,
		DataSources:           dataSources,
		AvailableDependencies: append([]agent.ToolDependency(nil), e.runtime.availableDependencies...),
	})
	if err != nil {
		return diagnosisworker.ExecutionResult{}, fmt.Errorf("%w: build Agent task scope: %v", diagnosis.ErrInvalidTask, err)
	}
	requestedSkill, err := requestedSkillFromScope(task.RequestScope)
	if err != nil {
		return diagnosisworker.ExecutionResult{}, err
	}
	runCtx := agent.WithTaskScope(ctx, scope)
	runCtx = withExecutionCaseSnapshot(runCtx, task.CaseSnapshot)
	result, err := e.runtime.orchestrator.Invoke(runCtx, agent.RunRequest{
		UserQuery: task.RequestText, ExternalCaseID: task.CaseSnapshot.ID.String(),
		RequestedSkill: requestedSkill,
	})
	if err != nil {
		return diagnosisworker.ExecutionResult{}, err
	}
	return diagnosisworker.ExecutionResult{
		Orchestration: result,
		ModelName:     e.runtime.modelName, ModelVersion: e.runtime.modelVersion,
		PromptVersion: e.runtime.promptVersion,
	}, nil
}

func requestedSkillFromScope(scope map[string]any) (agent.SkillID, error) {
	value, exists := scope["requestedSkill"]
	if !exists || value == nil || strings.TrimSpace(fmt.Sprint(value)) == "" {
		return agent.SkillTicketDiagnosis, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%w: requestScope.requestedSkill must be a string", diagnosis.ErrInvalidTask)
	}
	requested := agent.SkillID(strings.TrimSpace(text))
	if err := (agent.RunRequest{UserQuery: "validation", RequestedSkill: requested}).Validate(); err != nil {
		return "", fmt.Errorf("%w: %v", diagnosis.ErrInvalidTask, err)
	}
	return requested, nil
}

type executionCaseSnapshotContextKey struct{}

func withExecutionCaseSnapshot(ctx context.Context, item externalcase.ExternalCase) context.Context {
	return context.WithValue(ctx, executionCaseSnapshotContextKey{}, cloneExternalCase(item))
}

type snapshotExternalCaseGetter struct{}

func (snapshotExternalCaseGetter) Get(ctx context.Context, id uuid.UUID) (*externalcase.ExternalCase, error) {
	item, ok := ctx.Value(executionCaseSnapshotContextKey{}).(externalcase.ExternalCase)
	if !ok || item.ID == uuid.Nil || item.ID != id {
		return nil, errors.New("diagnosis case snapshot is unavailable in this task context")
	}
	cloned := cloneExternalCase(item)
	return &cloned, nil
}

func cloneExternalCase(item externalcase.ExternalCase) externalcase.ExternalCase {
	item.Attributes = cloneMap(item.Attributes)
	item.Attachments = append([]externalcase.ExternalAttachment(nil), item.Attachments...)
	if item.OccurredAt != nil {
		occurredAt := *item.OccurredAt
		item.OccurredAt = &occurredAt
	}
	return item
}

func cloneMap(source map[string]any) map[string]any {
	if source == nil {
		return map[string]any{}
	}
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
