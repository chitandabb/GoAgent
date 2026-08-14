// Command mesguard-rag-judge runs an independent, bounded LLM Judge over
// prepared RAG answer-quality inputs. It never generates or rewrites answers.
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
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/chitandabb/GoAgent/internal/platform/chatmodel"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/ragjudge"
)

const (
	defaultMaxCases                    = 1
	defaultMaxProviderCostCNY          = 0.05
	defaultInputPriceCNYPerMillion     = 4.0
	defaultOutputPriceCNYPerMillion    = 12.0
	providerSettlementLagReserveTokens = 512
)

type options struct {
	inputPath                string
	outputPath               string
	maxCases                 int
	maxProviderCostCNY       float64
	inputPriceCNYPerMillion  float64
	outputPriceCNYPerMillion float64
	timeout                  time.Duration
	validateOnly             bool
	estimateOnly             bool
	executeProvider          bool
	overwrite                bool
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	opts, err := parseOptions(args)
	if err != nil {
		return err
	}
	inputs, err := readInputs(opts.inputPath)
	if err != nil {
		return err
	}
	if len(inputs) > opts.maxCases {
		inputs = append([]ragjudge.Input(nil), inputs[:opts.maxCases]...)
	}
	if opts.validateOnly {
		fmt.Fprintf(stdout, "rag_judge_validation cases=%d schema=%s provider_calls=0\n", len(inputs), ragjudge.SchemaVersion)
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := validateJudgeRuntimeConfig(cfg.Models.Judge); err != nil {
		return err
	}
	prompt, err := cfg.Models.Judge.LoadPrompt()
	if err != nil {
		return fmt.Errorf("load Judge prompt: %w", err)
	}
	plannedTokens := estimatePlanTokens(inputs, prompt, cfg.Models.Judge.MaxOutputTokens)
	plannedCost := usageCost(
		plannedTokens.prompt, plannedTokens.completion,
		opts.inputPriceCNYPerMillion, opts.outputPriceCNYPerMillion,
	)
	fmt.Fprintf(
		stdout,
		"rag_judge_preflight cases=%d prompt_tokens<=%d completion_tokens<=%d estimated_cost_cny=%.6f cost_guard_cny=%.6f execute_provider=%t provider_settlement_may_overshoot=true\n",
		len(inputs), plannedTokens.prompt, plannedTokens.completion, plannedCost,
		opts.maxProviderCostCNY, opts.executeProvider,
	)
	if opts.estimateOnly {
		return nil
	}
	if !opts.executeProvider {
		return errors.New("Judge provider execution is disabled; review the preflight and add -execute-provider")
	}
	if !cfg.Models.Judge.Enabled {
		return errors.New("models.judge is disabled")
	}
	if plannedCost > opts.maxProviderCostCNY {
		return fmt.Errorf("Judge preflight blocked: estimated cost %.6f CNY exceeds budget %.6f CNY", plannedCost, opts.maxProviderCostCNY)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()
	judge, err := buildJudge(ctx, cfg.Models.Judge, prompt)
	if err != nil {
		return err
	}
	observations := make([]ragjudge.Observation, 0, len(inputs))
	totalCost := 0.0
	for _, input := range inputs {
		observation, err := judge.Evaluate(ctx, input)
		if err != nil {
			return fmt.Errorf("judge case %s: %w", input.CaseID, err)
		}
		observation.EstimatedCostCNY = usageCost(
			observation.Usage.PromptTokens, observation.Usage.CompletionTokens,
			opts.inputPriceCNYPerMillion, opts.outputPriceCNYPerMillion,
		)
		totalCost += observation.EstimatedCostCNY
		if totalCost > opts.maxProviderCostCNY {
			return fmt.Errorf("Judge cost budget exceeded after case %s", input.CaseID)
		}
		observations = append(observations, observation)
	}
	if err := writeObservations(opts.outputPath, observations, opts.overwrite); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "rag_judge_result cases=%d estimated_cost_cny=%.6f output=%s\n", len(observations), totalCost, opts.outputPath)
	return nil
}

func validateJudgeRuntimeConfig(cfg config.JudgeModelConfig) error {
	validated := cfg
	validated.Enabled = true
	if err := validated.Validate(); err != nil {
		return fmt.Errorf("validate Judge runtime config: %w", err)
	}
	if validated.PromptVersion != ragjudge.SchemaVersion {
		return fmt.Errorf(
			"Judge promptVersion %q does not match schema %q",
			validated.PromptVersion, ragjudge.SchemaVersion,
		)
	}
	return nil
}

func parseOptions(args []string) (options, error) {
	flags := flag.NewFlagSet("mesguard-rag-judge", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	opts := options{}
	flags.StringVar(&opts.inputPath, "input", "", "prepared RAG Judge JSONL input")
	flags.StringVar(&opts.outputPath, "output", "output/evaluation/rag-judge-v2.observations.jsonl", "Judge observation JSONL output")
	flags.IntVar(&opts.maxCases, "max-cases", defaultMaxCases, "maximum cases to evaluate")
	flags.Float64Var(&opts.maxProviderCostCNY, "max-provider-cost-cny", defaultMaxProviderCostCNY, "estimated provider cost guard")
	flags.Float64Var(&opts.inputPriceCNYPerMillion, "input-price-cny-per-million", defaultInputPriceCNYPerMillion, "reviewed Judge input price or conservative coefficient")
	flags.Float64Var(&opts.outputPriceCNYPerMillion, "output-price-cny-per-million", defaultOutputPriceCNYPerMillion, "reviewed Judge output price or conservative coefficient")
	flags.DurationVar(&opts.timeout, "timeout", 5*time.Minute, "whole Judge run timeout")
	flags.BoolVar(&opts.validateOnly, "validate-only", false, "validate inputs without loading config or calling a Provider")
	flags.BoolVar(&opts.estimateOnly, "estimate-only", false, "load config and print a conservative plan without calling a Provider")
	flags.BoolVar(&opts.executeProvider, "execute-provider", false, "allow bounded Judge Provider calls")
	flags.BoolVar(&opts.overwrite, "overwrite", false, "replace an existing output file")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return options{}, errors.New("usage: mesguard-rag-judge -input file [-validate-only|-estimate-only|-execute-provider]")
	}
	if strings.TrimSpace(opts.inputPath) == "" || strings.TrimSpace(opts.outputPath) == "" ||
		opts.maxCases < 1 || opts.maxCases > 20 || opts.timeout < time.Second || opts.timeout > 30*time.Minute ||
		!validPositiveCost(opts.maxProviderCostCNY) || !validPositiveCost(opts.inputPriceCNYPerMillion) ||
		!validPositiveCost(opts.outputPriceCNYPerMillion) {
		return options{}, errors.New("Judge paths, limits, timeout, or cost settings are invalid")
	}
	selectedModes := boolInt(opts.validateOnly) + boolInt(opts.estimateOnly) + boolInt(opts.executeProvider)
	if selectedModes != 1 {
		return options{}, errors.New("exactly one of -validate-only, -estimate-only, or -execute-provider is required")
	}
	return opts, nil
}

func readInputs(path string) ([]ragjudge.Input, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	inputs := make([]ragjudge.Input, 0)
	seen := make(map[string]struct{})
	line := 0
	for scanner.Scan() {
		line++
		contents := bytes.TrimSpace(scanner.Bytes())
		if len(contents) == 0 {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(contents))
		decoder.DisallowUnknownFields()
		var input ragjudge.Input
		if err := decoder.Decode(&input); err != nil {
			return nil, fmt.Errorf("decode Judge input line %d: %w", line, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("decode Judge input line %d: trailing content", line)
		}
		if err := input.Validate(); err != nil {
			return nil, fmt.Errorf("validate Judge input line %d: %w", line, err)
		}
		key := input.DatasetVersion + "/" + input.CaseID
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("duplicate Judge case %q", key)
		}
		seen[key] = struct{}{}
		inputs = append(inputs, input)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(inputs) == 0 {
		return nil, errors.New("Judge input contains no cases")
	}
	return inputs, nil
}

func buildJudge(ctx context.Context, cfg config.JudgeModelConfig, prompt string) (*ragjudge.Judge, error) {
	zero := float32(0)
	instance, err := chatmodel.New(ctx, "rag-judge", config.ChatModelProfileConfig{
		Provider: cfg.Provider, BaseURL: cfg.BaseURL, APIKeyEnv: cfg.APIKeyEnv,
		Model: cfg.Model, ThinkingMode: "disabled", Temperature: &zero,
		TimeoutMillis: cfg.TimeoutMillis, MaxOutputTokens: cfg.MaxOutputTokens,
	})
	if err != nil {
		return nil, err
	}
	return ragjudge.New(
		instance.Model, prompt, cfg.PromptVersion,
		instance.Identity.Provider, instance.Identity.ModelID,
		time.Duration(cfg.TimeoutMillis)*time.Millisecond,
	)
}

type plannedUsage struct {
	prompt     int
	completion int
}

func estimatePlanTokens(inputs []ragjudge.Input, prompt string, maxOutputTokens int) plannedUsage {
	result := plannedUsage{}
	for _, input := range inputs {
		payload, _ := json.Marshal(input)
		result.prompt += estimateTokens(prompt) + estimateTokens(string(payload)) + providerSettlementLagReserveTokens
		result.completion += maxOutputTokens
	}
	return result
}

func estimateTokens(value string) int {
	ascii, nonASCII := 0, 0
	for _, current := range value {
		if current <= unicode.MaxASCII {
			ascii++
		} else {
			nonASCII++
		}
	}
	return max(1, (ascii+3)/4+nonASCII+16)
}

func usageCost(promptTokens, completionTokens int, inputPrice, outputPrice float64) float64 {
	return float64(promptTokens)*inputPrice/1_000_000 + float64(completionTokens)*outputPrice/1_000_000
}

func writeObservations(path string, observations []ragjudge.Observation, overwrite bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
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
		return err
	}
	encoder := json.NewEncoder(file)
	for _, observation := range observations {
		if err := encoder.Encode(observation); err != nil {
			_ = file.Close()
			return err
		}
	}
	return file.Close()
}

func validPositiveCost(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value > 0 && value <= 1_000
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
