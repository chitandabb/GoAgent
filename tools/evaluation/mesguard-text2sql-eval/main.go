// Command mesguard-text2sql-eval evaluates model-generated read-only T-SQL against the fixed SQL Server fixture.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	platformchatmodel "github.com/chitandabb/GoAgent/internal/platform/chatmodel"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformsqlserver "github.com/chitandabb/GoAgent/internal/platform/sqlserver"
	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	textToSQLPromptVersion = "text-to-sql-v1"
	textToSQLMaxTokens     = 1024
)

var textToSQLCatalogVersionID = uuid.MustParse("55555555-5555-5555-5555-555555555555")

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("mesguard-text2sql-eval", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	datasetPath := flags.String("dataset", "testdata/text-to-sql-v1.jsonl", "versioned JSONL execution cases")
	outputPath := flags.String("output", "testdata/text-to-sql-v1.observations.jsonl", "observation JSONL output")
	summaryPath := flags.String("summary", "testdata/text-to-sql-v1.summary.json", "summary JSON output")
	timeout := flags.Duration("timeout", 10*time.Minute, "total evaluation timeout")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("usage: mesguard-text2sql-eval [-dataset path] [-output path] [-summary path] [-timeout duration]")
	}
	if *timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	cases, err := readTextToSQLCases(*datasetPath)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !cfg.Models.Chat.Enabled || !cfg.SQLServer.Enabled {
		return errors.New("chat model and SQL Server must be enabled")
	}
	profile, err := cfg.Models.Chat.ActiveProfile()
	if err != nil {
		return err
	}
	if strings.TrimSpace(profile.ReasoningEffort) != "" {
		profile.ReasoningEffort = "low"
	}
	cfg.Models.Chat.Profiles[cfg.Models.Chat.ActiveProfileName] = profile

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	instance, err := platformchatmodel.NewActive(ctx, cfg.Models.Chat)
	if err != nil {
		return fmt.Errorf("build chat model: %w", err)
	}
	chatModel := instance.Model
	selectionTool, err := mesagent.NewExecuteReadonlyQueryTool(noopQueryExecutor{})
	if err != nil {
		return fmt.Errorf("build evaluation Tool schema: %w", err)
	}
	toolInfo, err := selectionTool.Info(ctx)
	if err != nil {
		return fmt.Errorf("read evaluation Tool schema: %w", err)
	}
	boundModel, err := chatModel.WithTools([]*schema.ToolInfo{toolInfo})
	if err != nil {
		return fmt.Errorf("bind evaluation Tool: %w", err)
	}

	dataSourceID, err := uuid.Parse(cfg.SQLServer.ID)
	if err != nil {
		return errors.New("configured SQL Server ID is invalid")
	}
	db, err := platformsqlserver.Open(ctx, cfg.SQLServer)
	if err != nil {
		return fmt.Errorf("open SQL Server: %w", err)
	}
	defer db.Close()
	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	err = db.PingContext(pingCtx)
	pingCancel()
	if err != nil {
		return fmt.Errorf("ping SQL Server: %w", err)
	}
	executor, err := platformsqlserver.NewReadonlyQueryExecutor(
		db, cfg.SQLServer, fixedCatalogAuthorizer{dataSourceID: dataSourceID}, zap.NewNop(),
	)
	if err != nil {
		return fmt.Errorf("build readonly query executor: %w", err)
	}

	observations := make([]mesagent.TextToSQLEvaluationObservation, 0, len(cases))
	for _, definition := range cases {
		if err := ctx.Err(); err != nil {
			return err
		}
		observation := observeTextToSQL(ctx, boundModel, executor, cfg, dataSourceID, definition)
		if err := observation.Validate(); err != nil {
			return fmt.Errorf("validate case %q: %w", definition.CaseID, err)
		}
		observations = append(observations, observation)
		fmt.Fprintf(os.Stdout, "%s correct=%t error=%s\n", definition.CaseID, observation.Correct, observation.ErrorType)
	}
	summary, err := mesagent.EvaluateTextToSQL(cases, observations)
	if err != nil {
		return err
	}
	return writeTextToSQLEvaluationFiles(*outputPath, *summaryPath, observations, summary)
}

func observeTextToSQL(
	ctx context.Context,
	chatModel model.ToolCallingChatModel,
	executor repository.ReadonlyQueryExecutor,
	cfg config.Config,
	dataSourceID uuid.UUID,
	definition mesagent.TextToSQLEvaluationCase,
) mesagent.TextToSQLEvaluationObservation {
	startedAt := time.Now()
	profile, _ := cfg.Models.Chat.ActiveProfile()
	observation := mesagent.TextToSQLEvaluationObservation{
		DatasetVersion:  definition.DatasetVersion,
		CaseID:          definition.CaseID,
		RunID:           definition.CaseID + "-" + uuid.NewString(),
		ModelProvider:   profile.Provider,
		ModelID:         profile.Model,
		ReasoningEffort: profile.ReasoningEffort,
		PromptVersion:   textToSQLPromptVersion,
	}
	message, generateErr := chatModel.Generate(ctx, []*schema.Message{
		schema.SystemMessage(textToSQLSystemPrompt(dataSourceID)),
		schema.UserMessage(definition.UserQuery),
	}, model.WithTemperature(0), model.WithMaxTokens(textToSQLMaxTokens), model.WithToolChoice(schema.ToolChoiceForced))
	observation.DurationMillis = time.Since(startedAt).Milliseconds()
	if generateErr != nil {
		observation.ErrorType = "model_error"
		return observation
	}
	if message == nil {
		observation.ErrorType = "empty_model_response"
		return observation
	}
	if message.ResponseMeta == nil || message.ResponseMeta.Usage == nil {
		observation.ErrorType = "missing_provider_usage"
		return observation
	}
	usage := message.ResponseMeta.Usage
	observation.Usage = mesagent.ModelUsage{
		ModelCalls: 1, PromptTokens: usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens, TotalTokens: usage.TotalTokens,
		CachedTokens:    usage.PromptTokenDetails.CachedTokens,
		ReasoningTokens: usage.CompletionTokensDetails.ReasoningTokens,
	}
	observation.ToolCallCount = len(message.ToolCalls)
	if len(message.ToolCalls) != 1 {
		observation.ErrorType = "invalid_tool_call_count"
		return observation
	}
	call := message.ToolCalls[0]
	observation.SelectedTool = call.Function.Name
	if call.Function.Name != mesagent.ToolExecuteReadonlyQuery {
		observation.ErrorType = "unexpected_tool"
		return observation
	}
	arguments, err := decodeQueryArguments(call.Function.Arguments)
	if err != nil {
		observation.ErrorType = "invalid_tool_arguments"
		return observation
	}
	if arguments.DataSourceID != "" && arguments.DataSourceID != dataSourceID.String() {
		observation.ErrorType = "wrong_data_source"
		return observation
	}
	observation.GeneratedQuery = arguments.Query
	observation.QueryHash = hashQuery(arguments.Query)
	result, err := executor.Execute(ctx, dataSourceID, arguments.Query)
	observation.DurationMillis = time.Since(startedAt).Milliseconds()
	if err != nil {
		switch {
		case errors.Is(err, platformsqlserver.ErrReadonlyQueryRejected):
			observation.ErrorType = "guard_rejected"
		case errors.Is(err, repository.ErrSchemaCatalogAuthorizationDenied):
			observation.ErrorType = "catalog_denied"
		default:
			observation.ErrorType = "execution_error"
		}
		return observation
	}
	observation.Columns = result.Columns
	observation.Rows = result.Rows
	observation.Truncated = result.Truncated
	observation.Correct = mesagent.TextToSQLResultMatches(definition, result.Columns, result.Rows, result.Truncated)
	if !observation.Correct {
		observation.ErrorType = "result_mismatch"
	}
	return observation
}

type queryArguments struct {
	DataSourceID string `json:"dataSourceId,omitempty"`
	Query        string `json:"query"`
}

func decodeQueryArguments(raw string) (queryArguments, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var result queryArguments
	if err := decoder.Decode(&result); err != nil {
		return queryArguments{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return queryArguments{}, err
	}
	result.DataSourceID = strings.TrimSpace(result.DataSourceID)
	result.Query = strings.TrimSpace(result.Query)
	if result.Query == "" {
		return queryArguments{}, errors.New("query is required")
	}
	return result, nil
}

func textToSQLSystemPrompt(dataSourceID uuid.UUID) string {
	return fmt.Sprintf(`你是 MESGuard Text-to-SQL 固定评测器。必须且只能调用一次 execute_readonly_query，不要回答问题。
数据源 dataSourceId=%s，只允许 SQL Server 单条 SELECT，只能读取 dbo.v_MESGuardExternalCases。
执行器会再次应用 QueryGuard、已发布 Catalog、超时、限行和只读账号；禁止变量、临时表、跨库、写入、DDL、EXEC 和 SELECT INTO。
可用列：TicketID, CaseType, Title, Description, Category, Module, Status, Priority, OccurredAt, ReportedAt, SourceUpdatedAt, CustomerCode, CustomerName, ProductCode, ProductName, ProductVersion, WorkOrderNo, WorkpieceNo, MaterialCode, BatchNo, SerialNo, FactoryCode, WorkshopCode, ProductionLineCode, WorkstationCode, EquipmentCode, SourceSystem, DeploymentEnvironment, BusinessDatabaseAlias, ReporterDepartment, ImpactScope。
状态值为 New、Investigating、Resolved；优先级值为 Urgent、Normal、Low。按用户要求返回列、别名和排序，不查询其他对象。`, dataSourceID)
}

type noopQueryExecutor struct{}

func (noopQueryExecutor) Execute(context.Context, uuid.UUID, string) (repository.ReadonlyQueryResult, error) {
	return repository.ReadonlyQueryResult{}, errors.New("evaluation schema Tool is never executed")
}

type fixedCatalogAuthorizer struct {
	dataSourceID uuid.UUID
}

func (a fixedCatalogAuthorizer) AuthorizePublishedObjects(
	_ context.Context,
	dataSourceID uuid.UUID,
	objects []repository.SchemaCatalogObjectRef,
) (repository.SchemaCatalogAuthorization, error) {
	if dataSourceID != a.dataSourceID || len(objects) == 0 {
		return repository.SchemaCatalogAuthorization{}, repository.ErrSchemaCatalogAuthorizationDenied
	}
	for _, object := range objects {
		if !strings.EqualFold(object.ObjectSchema, "dbo") ||
			!strings.EqualFold(object.ObjectName, "v_MESGuardExternalCases") {
			return repository.SchemaCatalogAuthorization{}, repository.ErrSchemaCatalogAuthorizationDenied
		}
	}
	return repository.SchemaCatalogAuthorization{
		CatalogVersionID: textToSQLCatalogVersionID,
		CatalogVersion:   1,
		Objects:          append([]repository.SchemaCatalogObjectRef(nil), objects...),
	}, nil
}

func readTextToSQLCases(path string) ([]mesagent.TextToSQLEvaluationCase, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open dataset: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var result []mesagent.TextToSQLEvaluationCase
	for line := 1; scanner.Scan(); line++ {
		contents := bytes.TrimSpace(scanner.Bytes())
		if len(contents) == 0 {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(contents))
		decoder.DisallowUnknownFields()
		decoder.UseNumber()
		var current mesagent.TextToSQLEvaluationCase
		if err := decoder.Decode(&current); err != nil {
			return nil, fmt.Errorf("decode line %d: %w", line, err)
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			if err == nil {
				err = errors.New("multiple JSON values")
			}
			return nil, fmt.Errorf("decode line %d: %w", line, err)
		}
		if err := current.Validate(); err != nil {
			return nil, fmt.Errorf("validate line %d: %w", line, err)
		}
		result = append(result, current)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, errors.New("dataset contains no cases")
	}
	return result, nil
}

func hashQuery(query string) string {
	digest := sha256.Sum256([]byte(query))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func writeTextToSQLEvaluationFiles(
	outputPath, summaryPath string,
	observations []mesagent.TextToSQLEvaluationObservation,
	summary mesagent.TextToSQLEvaluationSummary,
) error {
	outputTemp := outputPath + ".tmp-" + uuid.NewString()
	summaryTemp := summaryPath + ".tmp-" + uuid.NewString()
	defer os.Remove(outputTemp)
	defer os.Remove(summaryTemp)
	file, err := os.OpenFile(outputTemp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	for _, observation := range observations {
		if err := encoder.Encode(observation); err != nil {
			_ = file.Close()
			return err
		}
	}
	if err := file.Close(); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(summaryTemp, append(encoded, '\n'), 0o600); err != nil {
		return err
	}
	if err := replaceEvaluationFile(outputTemp, outputPath); err != nil {
		return err
	}
	return replaceEvaluationFile(summaryTemp, summaryPath)
}

func replaceEvaluationFile(source, target string) error {
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(source, target)
}
