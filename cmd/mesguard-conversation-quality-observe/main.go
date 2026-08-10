// Command mesguard-conversation-quality-observe runs a bounded real-model
// Conversation quality sample over a transaction-scoped public knowledge fixture.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/knowledge"
	platformconfig "github.com/chitandabb/GoAgent/internal/platform/config"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const (
	defaultDatasetVersion               = "conversation-quality-recorded-v1"
	defaultMaxCases                     = 1
	defaultQualityRetrievalMaxResults   = 3
	defaultMaxChatTokensPerCase         = 6_000
	defaultMaxProviderCostCNY           = 0.05
	defaultChatInputPriceCNYPerMillion  = 1.0
	defaultChatOutputPriceCNYPerMillion = 4.0
	defaultEmbeddingPriceCNYPerMillion  = 0.5
	providerSettlementLagReserveTokens  = 4_096
)

var errRollbackConversationQualityFixture = errors.New("rollback Conversation quality fixture")

type commandOptions struct {
	corpusPath                   string
	datasetPath                  string
	resolvedDatasetPath          string
	observationsPath             string
	summaryPath                  string
	caseID                       string
	chatProfile                  string
	maxCases                     int
	maxChatTokensPerCase         int
	maxProviderCostCNY           float64
	chatInputPriceCNYPerMillion  float64
	chatOutputPriceCNYPerMillion float64
	embeddingPriceCNYPerMillion  float64
	timeout                      time.Duration
	validateOnly                 bool
	estimateOnly                 bool
	executeProvider              bool
	overwrite                    bool
}

type qualityCaseDefinition struct {
	DatasetVersion         string                                  `json:"datasetVersion"`
	CaseID                 string                                  `json:"caseId"`
	UserQuery              string                                  `json:"userQuery"`
	RetrievalMaxResults    int                                     `json:"retrievalMaxResults,omitempty"`
	RelevantChunks         []knowledge.RetrievalEvaluationChunkRef `json:"relevantChunks"`
	RequiredCitationChunks []knowledge.RetrievalEvaluationChunkRef `json:"requiredCitationChunks,omitempty"`
	RequiredAnswerTerms    []string                                `json:"requiredAnswerTerms,omitempty"`
	ForbiddenAnswerTerms   []string                                `json:"forbiddenAnswerTerms,omitempty"`
	ExpectedOutcome        mesagent.ConversationQualityOutcome     `json:"expectedOutcome"`
	Tags                   []string                                `json:"tags,omitempty"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	options, err := parseOptions(args)
	if err != nil {
		return err
	}
	corpus, err := readStrictJSON[knowledge.AdvancedRetrievalEvaluationCorpus](options.corpusPath)
	if err != nil {
		return err
	}
	definitions, err := readStrictJSONL(options.datasetPath, func(value qualityCaseDefinition) error {
		return value.Validate()
	})
	if err != nil {
		return err
	}
	if err := validateQualityCaseSet(definitions); err != nil {
		return err
	}
	selected, err := selectQualityCases(definitions, options.caseID, options.maxCases)
	if err != nil {
		return err
	}
	fixture, chunksByDocument, err := selectAndValidateFixture(corpus, selected)
	if err != nil {
		return err
	}
	plan := buildProviderPlan(fixture, chunksByDocument, selected, options)
	printProviderPlan(plan, options)
	if options.validateOnly || options.estimateOnly {
		return nil
	}
	if !options.executeProvider {
		return errors.New("provider execution is disabled; review the preflight and add -execute-provider")
	}
	if plan.EstimatedPlanningCostCNY > options.maxProviderCostCNY {
		return fmt.Errorf(
			"provider preflight blocked: estimated planning cost %.4f CNY exceeds budget %.4f CNY",
			plan.EstimatedPlanningCostCNY, options.maxProviderCostCNY,
		)
	}

	cfg, err := platformconfig.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !cfg.Models.Chat.Enabled || !cfg.Models.Embedding.Enabled {
		return errors.New("Conversation quality observation requires chat and embedding models")
	}
	// Keep this quality slice on the measured RRF path. Rewrite and rerank have
	// their own paired evaluations and would add unrelated calls and cost here.
	cfg.Knowledge.Retrieval.QueryRewrite.Enabled = false
	cfg.Models.Rerank.Enabled = false

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, options.timeout)
	defer cancel()
	db, closeDB, err := platformpostgres.Open(ctx, cfg.Postgres, zap.NewNop())
	if err != nil {
		return fmt.Errorf("open postgres: %w", err)
	}
	defer closeDB()

	var resolvedCases []mesagent.ConversationQualityCase
	var observations []mesagent.ConversationQualityObservation
	var summary mesagent.ConversationQualitySummary
	var documentEmbeddingTokens int
	var actualOnlineCostCNY float64
	evaluationErr := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		actorID, sourceByKey, embeddingTokens, err := seedConversationQualityFixture(
			ctx, tx, cfg, fixture, chunksByDocument,
		)
		if err != nil {
			return err
		}
		documentEmbeddingTokens = embeddingTokens
		resolvedCases, err = resolveQualityCases(selected, sourceByKey)
		if err != nil {
			return err
		}
		for index, definition := range resolvedCases {
			runner, preview, modelDiagnostics, err := buildConversationQualityRunner(
				ctx, tx, cfg, options, selected[index].effectiveRetrievalMaxResults(),
			)
			if err != nil {
				return err
			}
			observation, cost, observeErr := observeQualityCase(
				ctx, runner, preview, modelDiagnostics, actorID, definition, options,
			)
			if observeErr != nil {
				return fmt.Errorf("observe case %s: %w", definition.CaseID, observeErr)
			}
			actualOnlineCostCNY += cost
			if actualOnlineCostCNY+embeddingCost(documentEmbeddingTokens, options.embeddingPriceCNYPerMillion) >
				options.maxProviderCostCNY {
				return fmt.Errorf("provider cost budget exceeded after case %s", definition.CaseID)
			}
			observations = append(observations, observation)
		}
		summary, err = mesagent.EvaluateConversationQuality(resolvedCases, observations)
		if err != nil {
			return err
		}
		return errRollbackConversationQualityFixture
	})
	if !errors.Is(evaluationErr, errRollbackConversationQualityFixture) {
		return evaluationErr
	}
	if err := writeEvaluationOutputs(options, resolvedCases, observations, summary); err != nil {
		return err
	}
	fmt.Printf(
		"conversation_quality_result dataset=%s cases=%d passed=%d context_recall=%.4f citation_recall=%.4f answer_term_recall=%.4f total_chat_tokens=%d document_embedding_tokens=%d estimated_online_cost_cny=%.6f fixture_embedding_cost_cny=%.6f\n",
		summary.DatasetVersion, summary.Cases, summary.PassedRuns, summary.ContextRecall,
		summary.CitationRecall, summary.RequiredAnswerTermRecall, summary.TotalTokens,
		documentEmbeddingTokens, actualOnlineCostCNY,
		embeddingCost(documentEmbeddingTokens, options.embeddingPriceCNYPerMillion),
	)
	return nil
}

func parseOptions(args []string) (commandOptions, error) {
	flags := flag.NewFlagSet("mesguard-conversation-quality-observe", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := commandOptions{}
	flags.StringVar(&options.corpusPath, "corpus", "testdata/rag-advanced-v1.corpus.json", "pinned public-source corpus")
	flags.StringVar(&options.datasetPath, "dataset", "testdata/conversation-quality-recorded-v1.jsonl", "versioned quality case definitions")
	flags.StringVar(&options.resolvedDatasetPath, "resolved-dataset", "output/evaluation/conversation-quality-recorded-v1.dataset.jsonl", "resolved source-ref dataset output")
	flags.StringVar(&options.observationsPath, "output", "output/evaluation/conversation-quality-recorded-v1.observations.jsonl", "recorded observation output")
	flags.StringVar(&options.summaryPath, "summary", "output/evaluation/conversation-quality-recorded-v1.summary.json", "quality summary output")
	flags.StringVar(&options.caseID, "case-id", "", "optional exact case id")
	flags.StringVar(&options.chatProfile, "chat-profile", "", "optional named chat profile; empty uses models.chat.activeProfile")
	flags.IntVar(&options.maxCases, "max-cases", defaultMaxCases, "maximum real-model cases")
	flags.IntVar(&options.maxChatTokensPerCase, "max-chat-tokens-per-case", defaultMaxChatTokensPerCase, "Conversation Runner total token budget per case")
	flags.Float64Var(&options.maxProviderCostCNY, "max-provider-cost-cny", defaultMaxProviderCostCNY, "preflight and post-case estimated cost guard; one in-flight case may settle above it")
	flags.Float64Var(&options.chatInputPriceCNYPerMillion, "chat-input-price-cny-per-million", defaultChatInputPriceCNYPerMillion, "reviewed chat input price or conservative guard coefficient")
	flags.Float64Var(&options.chatOutputPriceCNYPerMillion, "chat-output-price-cny-per-million", defaultChatOutputPriceCNYPerMillion, "reviewed chat output price or conservative guard coefficient")
	flags.Float64Var(&options.embeddingPriceCNYPerMillion, "embedding-price-cny-per-million", defaultEmbeddingPriceCNYPerMillion, "reviewed embedding input price or conservative guard coefficient")
	flags.DurationVar(&options.timeout, "timeout", 5*time.Minute, "whole observation timeout")
	flags.BoolVar(&options.validateOnly, "validate-only", false, "validate fixtures without loading config or calling providers")
	flags.BoolVar(&options.estimateOnly, "estimate-only", false, "print conservative provider preflight without calling providers")
	flags.BoolVar(&options.executeProvider, "execute-provider", false, "allow bounded real provider calls")
	flags.BoolVar(&options.overwrite, "overwrite", false, "explicitly replace existing evaluation output files")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return commandOptions{}, errors.New("usage: mesguard-conversation-quality-observe [-validate-only|-estimate-only|-execute-provider] [-case-id id] [-chat-profile name] [-max-cases 1..8] [-max-provider-cost-cny amount]")
	}
	options.caseID = strings.TrimSpace(options.caseID)
	options.chatProfile = strings.TrimSpace(options.chatProfile)
	if options.validateOnly && (options.estimateOnly || options.executeProvider) ||
		options.estimateOnly && options.executeProvider {
		return commandOptions{}, errors.New("validate-only, estimate-only, and execute-provider are mutually exclusive")
	}
	if options.maxCases < 1 || options.maxCases > 8 ||
		options.maxChatTokensPerCase < 1_000 || options.maxChatTokensPerCase > 16_000 ||
		options.timeout < time.Second || options.timeout > 30*time.Minute {
		return commandOptions{}, errors.New("case, token, or timeout limit is outside the safety boundary")
	}
	if !validEvaluationProfileName(options.chatProfile) {
		return commandOptions{}, errors.New("chat profile name is invalid")
	}
	for _, value := range []float64{
		options.maxProviderCostCNY, options.chatInputPriceCNYPerMillion,
		options.chatOutputPriceCNYPerMillion, options.embeddingPriceCNYPerMillion,
	} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value > 1_000 {
			return commandOptions{}, errors.New("provider cost and price guards must be positive and bounded")
		}
	}
	for _, path := range []string{options.corpusPath, options.datasetPath, options.resolvedDatasetPath, options.observationsPath, options.summaryPath} {
		if strings.TrimSpace(path) == "" {
			return commandOptions{}, errors.New("evaluation paths must not be empty")
		}
	}
	return options, nil
}

func validEvaluationProfileName(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 64 {
		return false
	}
	for _, current := range value {
		if current >= 'a' && current <= 'z' || current >= 'A' && current <= 'Z' ||
			current >= '0' && current <= '9' || current == '-' || current == '_' || current == '.' {
			continue
		}
		return false
	}
	return true
}

func (c qualityCaseDefinition) Validate() error {
	if c.DatasetVersion != defaultDatasetVersion || strings.TrimSpace(c.CaseID) == "" ||
		strings.TrimSpace(c.UserQuery) == "" || c.RetrievalMaxResults < 0 ||
		c.RetrievalMaxResults > knowledge.MaxKnowledgeSearchLimit ||
		!c.ExpectedOutcome.Valid() || len(c.RelevantChunks) == 0 ||
		len(c.RelevantChunks) > 20 || len(c.RequiredCitationChunks) > 20 ||
		len(c.RequiredAnswerTerms) > 20 || len(c.ForbiddenAnswerTerms) > 20 {
		return errors.New("Conversation quality case identity or bounds are invalid")
	}
	seen := make(map[string]struct{}, len(c.RelevantChunks))
	for _, ref := range c.RelevantChunks {
		if err := ref.Validate(); err != nil {
			return err
		}
		key := qualityChunkRefKey(ref)
		if _, exists := seen[key]; exists {
			return errors.New("Conversation quality relevant chunks must be unique")
		}
		seen[key] = struct{}{}
	}
	requiredSeen := make(map[string]struct{}, len(c.RequiredCitationChunks))
	for _, ref := range c.effectiveRequiredCitationChunks() {
		if err := ref.Validate(); err != nil {
			return err
		}
		key := qualityChunkRefKey(ref)
		if _, exists := seen[key]; !exists {
			return errors.New("Conversation quality required citation chunk must be relevant")
		}
		if _, exists := requiredSeen[key]; exists {
			return errors.New("Conversation quality required citation chunks must be unique")
		}
		requiredSeen[key] = struct{}{}
	}
	for _, values := range [][]string{c.RequiredAnswerTerms, c.ForbiddenAnswerTerms, c.Tags} {
		seenValues := make(map[string]struct{}, len(values))
		if slices.ContainsFunc(values, func(value string) bool {
			return strings.TrimSpace(value) == "" || value != strings.TrimSpace(value)
		}) {
			return errors.New("Conversation quality case labels and terms must be trimmed")
		}
		for _, value := range values {
			key := strings.ToLower(value)
			if _, exists := seenValues[key]; exists {
				return errors.New("Conversation quality case labels and terms must be unique")
			}
			seenValues[key] = struct{}{}
		}
	}
	return nil
}

func (c qualityCaseDefinition) effectiveRequiredCitationChunks() []knowledge.RetrievalEvaluationChunkRef {
	if len(c.RequiredCitationChunks) == 0 {
		return c.RelevantChunks
	}
	return c.RequiredCitationChunks
}

func (c qualityCaseDefinition) effectiveRetrievalMaxResults() int {
	if c.RetrievalMaxResults == 0 {
		return defaultQualityRetrievalMaxResults
	}
	return c.RetrievalMaxResults
}

func qualityChunkRefKey(ref knowledge.RetrievalEvaluationChunkRef) string {
	return fmt.Sprintf("%s/%d/%s", ref.DocumentKey, ref.Ordinal, ref.ContentSHA256)
}

func validateQualityCaseSet(cases []qualityCaseDefinition) error {
	if len(cases) == 0 {
		return errors.New("Conversation quality dataset is empty")
	}
	seen := make(map[string]struct{}, len(cases))
	for _, item := range cases {
		if item.DatasetVersion != cases[0].DatasetVersion {
			return errors.New("Conversation quality dataset mixes versions")
		}
		if _, exists := seen[item.CaseID]; exists {
			return fmt.Errorf("duplicate Conversation quality case %q", item.CaseID)
		}
		seen[item.CaseID] = struct{}{}
	}
	return nil
}

func selectQualityCases(cases []qualityCaseDefinition, caseID string, maxCases int) ([]qualityCaseDefinition, error) {
	if len(cases) == 0 {
		return nil, errors.New("Conversation quality dataset is empty")
	}
	if caseID != "" {
		for _, item := range cases {
			if item.CaseID == caseID {
				return []qualityCaseDefinition{item}, nil
			}
		}
		return nil, fmt.Errorf("Conversation quality case %q was not found", caseID)
	}
	if maxCases > len(cases) {
		maxCases = len(cases)
	}
	return append([]qualityCaseDefinition(nil), cases[:maxCases]...), nil
}

func readStrictJSON[T any](path string) (T, error) {
	var value T
	encoded, err := os.ReadFile(path)
	if err != nil {
		return value, fmt.Errorf("read %s: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, fmt.Errorf("decode %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return value, fmt.Errorf("decode %s: trailing JSON value", path)
	}
	return value, nil
}

func readStrictJSONL[T any](path string, validate func(T) error) ([]T, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	var values []T
	for line := 1; scanner.Scan(); line++ {
		contents := bytes.TrimSpace(scanner.Bytes())
		if len(contents) == 0 {
			continue
		}
		var value T
		decoder := json.NewDecoder(bytes.NewReader(contents))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode %s line %d: %w", path, line, err)
		}
		if err := validate(value); err != nil {
			return nil, fmt.Errorf("validate %s line %d: %w", path, line, err)
		}
		values = append(values, value)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	return values, nil
}

func writeEvaluationOutputs(
	options commandOptions,
	cases []mesagent.ConversationQualityCase,
	observations []mesagent.ConversationQualityObservation,
	summary mesagent.ConversationQualitySummary,
) error {
	dataset, err := encodeJSONLines(cases)
	if err != nil {
		return err
	}
	runs, err := encodeJSONLines(observations)
	if err != nil {
		return err
	}
	encodedSummary, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	encodedSummary = append(encodedSummary, '\n')
	for _, output := range []struct {
		path string
		data []byte
	}{{options.resolvedDatasetPath, dataset}, {options.observationsPath, runs}, {options.summaryPath, encodedSummary}} {
		if err := writeEvaluationFile(output.path, output.data, options.overwrite); err != nil {
			return err
		}
	}
	return nil
}

func encodeJSONLines[T any](values []T) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	for _, value := range values {
		if err := encoder.Encode(value); err != nil {
			return nil, err
		}
	}
	return output.Bytes(), nil
}

func writeEvaluationFile(path string, contents []byte, overwrite bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	flags := os.O_WRONLY | os.O_CREATE
	if overwrite {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return fmt.Errorf("create evaluation output %s: %w", path, err)
	}
	succeeded := false
	defer func() {
		_ = file.Close()
		if !succeeded {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(contents); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	succeeded = true
	return file.Close()
}

func newRecordedRun(
	definition mesagent.ConversationQualityCase,
	response conversation.AgentResponse,
	runErr error,
) (conversation.RecordedAgentRun, error) {
	run := conversation.RecordedAgentRun{
		TurnID: uuid.New(), ConversationID: uuid.New(), UserMessageID: uuid.New(),
		UserQuery: definition.UserQuery, ObservedAt: time.Now().UTC(),
	}
	if runErr != nil {
		failure, ok := conversation.AgentRunFailureRecordFrom(runErr)
		if !ok {
			return conversation.RecordedAgentRun{}, runErr
		}
		run.Observation = failure.Observation
		run.ErrorType = failure.ErrorType
		return run, nil
	}
	if err := response.Validate(); err != nil || response.RunObservation == nil {
		return conversation.RecordedAgentRun{}, errors.New("Conversation Agent returned an invalid quality response")
	}
	run.Answer = response.Content
	run.Citations = append([]conversation.MessageCitation(nil), response.Citations...)
	run.Observation = *response.RunObservation
	completedAt := time.Now().UTC()
	run.CompletedAt = &completedAt
	return run, nil
}
