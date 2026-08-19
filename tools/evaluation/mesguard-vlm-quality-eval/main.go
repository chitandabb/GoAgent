// Command mesguard-vlm-quality-eval runs an explicitly enabled, bounded VLM comparison.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/draw"
	_ "image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"
	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/knowledgeenrichment"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
	platformchatmodel "github.com/chitandabb/GoAgent/internal/platform/chatmodel"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/platform/visualmodel"
	"github.com/joho/godotenv"
)

const (
	evaluatorVersion         = "vlm-quality-eval-v1"
	stepFunComparisonProfile = "stepfun-main"
	maxCases                 = 3
	maxCropPixels            = 4_000_000
	maxSourceBytes           = 20 * 1024 * 1024
)

type options struct {
	configPath                     string
	fixturePath                    string
	rootPath                       string
	outputPath                     string
	cropOutputPath                 string
	executeProvider                bool
	maxOutputTokens                int
	dashScopeInputPricePerMillion  float64
	dashScopeOutputPricePerMillion float64
	stepFunInputPricePerMillion    float64
	stepFunOutputPricePerMillion   float64
	providerTimeout                time.Duration
	timeout                        time.Duration
}

type fixture struct {
	Version string           `json:"version"`
	Cases   []evaluationCase `json:"cases"`
}

type evaluationCase struct {
	ID                    string         `json:"id"`
	SourcePath            string         `json:"sourcePath"`
	SourceSHA256          string         `json:"sourceSha256"`
	Crop                  cropRectangle  `json:"crop"`
	TextAnchors           []string       `json:"textAnchors"`
	SemanticFacts         []semanticFact `json:"semanticFacts"`
	MinimumTextAnchorRate float64        `json:"minimumTextAnchorRate"`
	MinimumSemanticRate   float64        `json:"minimumSemanticRate"`
}

type cropRectangle struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type semanticFact struct {
	ID    string     `json:"id"`
	Terms [][]string `json:"terms"`
}

type preparedCase struct {
	definition evaluationCase
	content    []byte
}

type caseInput struct {
	CaseID       string        `json:"caseId"`
	SourcePath   string        `json:"sourcePath"`
	SourceSHA256 string        `json:"sourceSha256"`
	Crop         cropRectangle `json:"crop"`
	RasterBytes  int           `json:"rasterBytes"`
	RasterSHA256 string        `json:"rasterSha256"`
}

type pricing struct {
	Currency              string   `json:"currency"`
	InputPricePerMillion  *float64 `json:"inputPricePerMillion,omitempty"`
	OutputPricePerMillion *float64 `json:"outputPricePerMillion,omitempty"`
	Basis                 string   `json:"basis"`
}

type observation struct {
	CaseID                 string                             `json:"caseId"`
	Provider               string                             `json:"provider"`
	Model                  string                             `json:"model"`
	Attempted              bool                               `json:"attempted"`
	SkippedReason          string                             `json:"skippedReason,omitempty"`
	ProviderMillis         float64                            `json:"providerMillis,omitempty"`
	StrictJSONSuccess      bool                               `json:"strictJsonSuccess"`
	MatchedTextAnchors     []string                           `json:"matchedTextAnchors,omitempty"`
	TextAnchorRecall       float64                            `json:"textAnchorRecall"`
	MatchedSemanticFactIDs []string                           `json:"matchedSemanticFactIds,omitempty"`
	SemanticFactRecall     float64                            `json:"semanticFactRecall"`
	CitationUseful         bool                               `json:"citationUseful"`
	Usage                  *knowledgeenrichment.ProviderUsage `json:"usage,omitempty"`
	EstimatedCostCNY       *float64                           `json:"estimatedCostCny,omitempty"`
	OCRText                string                             `json:"ocrText,omitempty"`
	Description            string                             `json:"description,omitempty"`
	Error                  string                             `json:"error,omitempty"`
}

type providerSummary struct {
	Provider                   string   `json:"provider"`
	Model                      string   `json:"model"`
	Pricing                    pricing  `json:"pricing"`
	AttemptedCalls             int      `json:"attemptedCalls"`
	SuccessfulCalls            int      `json:"successfulCalls"`
	StrictJSONRate             float64  `json:"strictJsonRate"`
	MeanTextAnchorRecall       float64  `json:"meanTextAnchorRecall"`
	MeanSemanticFactRecall     float64  `json:"meanSemanticFactRecall"`
	CitationUsefulRate         float64  `json:"citationUsefulRate"`
	MeanProviderMillis         float64  `json:"meanProviderMillis"`
	P50ProviderMillis          float64  `json:"p50ProviderMillis"`
	P95ProviderMillis          float64  `json:"p95ProviderMillis"`
	TotalPromptTokens          int      `json:"totalPromptTokens"`
	TotalCompletionTokens      int      `json:"totalCompletionTokens"`
	TotalTokens                int      `json:"totalTokens"`
	TotalEstimatedCostCNY      *float64 `json:"totalEstimatedCostCny,omitempty"`
	CostPerSuccessfulRegionCNY *float64 `json:"costPerSuccessfulRegionCny,omitempty"`
}

type report struct {
	EvaluatorVersion      string            `json:"evaluatorVersion"`
	FixtureVersion        string            `json:"fixtureVersion"`
	RecordedAt            time.Time         `json:"recordedAt"`
	ProviderExecuted      bool              `json:"providerExecuted"`
	MaxProviderCalls      int               `json:"maxProviderCalls"`
	MaxOutputTokens       int               `json:"maxOutputTokens"`
	ProviderTimeoutMillis int               `json:"providerTimeoutMillis"`
	Cases                 []caseInput       `json:"cases"`
	Providers             []providerSummary `json:"providers"`
	Observations          []observation     `json:"observations,omitempty"`
}

type providerRunner struct {
	name      string
	model     string
	pricing   pricing
	processor *visualmodel.Processor
	failed    bool
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	opts, err := parseFlags(args)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(opts.configPath)
	if err != nil {
		return err
	}
	fixture, err := readFixture(opts.fixturePath)
	if err != nil {
		return err
	}
	if err := validateFixture(fixture); err != nil {
		return err
	}
	prepared, inputs, err := prepareCases(fixture.Cases, opts.rootPath, opts.cropOutputPath)
	if err != nil {
		return err
	}
	result := report{
		EvaluatorVersion: evaluatorVersion, FixtureVersion: fixture.Version,
		RecordedAt: time.Now().UTC(), ProviderExecuted: opts.executeProvider,
		MaxProviderCalls: len(prepared) * 2, MaxOutputTokens: opts.maxOutputTokens,
		ProviderTimeoutMillis: int(opts.providerTimeout.Milliseconds()),
		Cases:                 inputs,
	}
	providerDefinitions, err := providerPricing(cfg, opts)
	if err != nil {
		return err
	}
	for _, provider := range providerDefinitions {
		result.Providers = append(result.Providers, providerSummary{
			Provider: provider.name, Model: provider.model, Pricing: provider.pricing,
		})
	}
	fmt.Fprintf(os.Stdout,
		"budget cases=%d providers=2 max_calls=%d max_output_tokens=%d dashscope_price=%.4f/%.4f_cny_per_million stepfun_price=%s\n",
		len(prepared), result.MaxProviderCalls, opts.maxOutputTokens,
		opts.dashScopeInputPricePerMillion, opts.dashScopeOutputPricePerMillion,
		pricingLabel(providerDefinitions[1].pricing),
	)
	if !opts.executeProvider {
		return writeReport(opts.outputPath, result)
	}
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	runners, err := buildProviderRunners(ctx, cfg, opts, providerDefinitions)
	if err != nil {
		return err
	}
	for providerIndex := range runners {
		for caseIndex := range prepared {
			if err := ctx.Err(); err != nil {
				result.Providers = summarizeProviders(providerDefinitions, result.Observations)
				return errors.Join(err, writeReport(opts.outputPath, result))
			}
			current := evaluateProviderCase(ctx, &runners[providerIndex], prepared[caseIndex])
			result.Observations = append(result.Observations, current)
			fmt.Fprintf(os.Stdout,
				"provider=%s case=%s attempted=%t strict_json=%t text_recall=%.4f semantic_recall=%.4f citation_useful=%t latency_ms=%.2f tokens=%d error=%q\n",
				current.Provider, current.CaseID, current.Attempted, current.StrictJSONSuccess,
				current.TextAnchorRecall, current.SemanticFactRecall, current.CitationUseful,
				current.ProviderMillis, usageTotal(current.Usage), current.Error,
			)
		}
	}
	result.Providers = summarizeProviders(providerDefinitions, result.Observations)
	if err := writeReport(opts.outputPath, result); err != nil {
		return err
	}
	totalSuccess := 0
	for _, provider := range result.Providers {
		totalSuccess += provider.SuccessfulCalls
	}
	if totalSuccess == 0 {
		return errors.New("VLM quality evaluation produced no successful provider response")
	}
	return nil
}

func parseFlags(args []string) (options, error) {
	flags := flag.NewFlagSet("mesguard-vlm-quality-eval", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var result options
	flags.StringVar(&result.configPath, "config", "config/mesguard.toml", "MESGuard TOML configuration")
	flags.StringVar(&result.fixturePath, "fixture", "testdata/vlm-quality-local-v1.json", "reviewed VLM fixture")
	flags.StringVar(&result.rootPath, "root", "", "directory containing SHA-pinned source images")
	flags.StringVar(&result.outputPath, "output", "output/evaluation/vlm-quality-local-v1.summary.json", "summary JSON output")
	flags.StringVar(&result.cropOutputPath, "crop-output", "output/evaluation/vlm-quality-local-v1-crops", "directory for reviewed crop PNG files")
	flags.BoolVar(&result.executeProvider, "execute-provider", false, "perform at most six bounded provider calls")
	flags.IntVar(&result.maxOutputTokens, "max-output-tokens", 2048, "equal output limit for both providers")
	flags.Float64Var(&result.dashScopeInputPricePerMillion, "dashscope-input-price-per-million-cny", 1, "DashScope input price per million tokens")
	flags.Float64Var(&result.dashScopeOutputPricePerMillion, "dashscope-output-price-per-million-cny", 10, "DashScope output price per million tokens")
	flags.Float64Var(&result.stepFunInputPricePerMillion, "stepfun-input-price-per-million-cny", -1, "StepFun input price per million tokens; -1 means subscription/unpriced")
	flags.Float64Var(&result.stepFunOutputPricePerMillion, "stepfun-output-price-per-million-cny", -1, "StepFun output price per million tokens; -1 means subscription/unpriced")
	flags.DurationVar(&result.providerTimeout, "provider-timeout", 120*time.Second, "equal per-call timeout for both providers")
	flags.DurationVar(&result.timeout, "timeout", 10*time.Minute, "total evaluation timeout")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || strings.TrimSpace(result.rootPath) == "" ||
		result.timeout <= 0 || result.providerTimeout < time.Second || result.providerTimeout > 300*time.Second ||
		result.maxOutputTokens < 128 || result.maxOutputTokens > 4096 ||
		result.dashScopeInputPricePerMillion < 0 || result.dashScopeOutputPricePerMillion < 0 ||
		result.stepFunInputPricePerMillion < -1 || result.stepFunOutputPricePerMillion < -1 ||
		(result.stepFunInputPricePerMillion < 0) != (result.stepFunOutputPricePerMillion < 0) {
		return options{}, errors.New("usage: mesguard-vlm-quality-eval -root directory [-execute-provider] [-fixture path] [-output path] [-timeout duration]")
	}
	return result, nil
}

func loadConfig(path string) (config.Config, error) {
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		return config.Config{}, err
	}
	var cfg config.Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return config.Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return config.Config{}, err
	}
	return cfg, nil
}

func readFixture(path string) (fixture, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return fixture{}, err
	}
	var decoded fixture
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return fixture{}, fmt.Errorf("decode VLM fixture: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fixture{}, errors.New("decode VLM fixture: trailing content")
	}
	return decoded, nil
}

func validateFixture(value fixture) error {
	if strings.TrimSpace(value.Version) == "" || len(value.Cases) < 1 || len(value.Cases) > maxCases {
		return fmt.Errorf("VLM fixture must contain between 1 and %d cases", maxCases)
	}
	ids := make(map[string]struct{}, len(value.Cases))
	for _, item := range value.Cases {
		if strings.TrimSpace(item.ID) == "" || !filepath.IsLocal(filepath.FromSlash(item.SourcePath)) ||
			path.Clean(item.SourcePath) != item.SourcePath || strings.Contains(item.SourcePath, "\\") ||
			!validSHA256(item.SourceSHA256) || item.Crop.X < 0 || item.Crop.Y < 0 || item.Crop.Width < 1 || item.Crop.Height < 1 ||
			int64(item.Crop.Width)*int64(item.Crop.Height) > maxCropPixels || len(item.TextAnchors) == 0 || len(item.SemanticFacts) == 0 ||
			item.MinimumTextAnchorRate < 0 || item.MinimumTextAnchorRate > 1 || item.MinimumSemanticRate < 0 || item.MinimumSemanticRate > 1 {
			return fmt.Errorf("VLM fixture case %q is invalid", item.ID)
		}
		if _, exists := ids[item.ID]; exists {
			return fmt.Errorf("VLM fixture case id %q is duplicated", item.ID)
		}
		ids[item.ID] = struct{}{}
		for _, anchor := range item.TextAnchors {
			if normalize(anchor) == "" {
				return fmt.Errorf("VLM fixture case %q contains an empty text anchor", item.ID)
			}
		}
		factIDs := make(map[string]struct{}, len(item.SemanticFacts))
		for _, fact := range item.SemanticFacts {
			if strings.TrimSpace(fact.ID) == "" || len(fact.Terms) < 2 {
				return fmt.Errorf("VLM fixture case %q contains an invalid semantic fact", item.ID)
			}
			if _, exists := factIDs[fact.ID]; exists {
				return fmt.Errorf("VLM fixture case %q duplicates semantic fact %q", item.ID, fact.ID)
			}
			factIDs[fact.ID] = struct{}{}
			for _, alternatives := range fact.Terms {
				if len(alternatives) == 0 {
					return fmt.Errorf("VLM fixture case %q contains an empty fact term", item.ID)
				}
				for _, alternative := range alternatives {
					if normalize(alternative) == "" {
						return fmt.Errorf("VLM fixture case %q contains an empty fact alternative", item.ID)
					}
				}
			}
		}
	}
	return nil
}

func prepareCases(cases []evaluationCase, root, cropOutput string) ([]preparedCase, []caseInput, error) {
	prepared := make([]preparedCase, 0, len(cases))
	inputs := make([]caseInput, 0, len(cases))
	if err := os.MkdirAll(cropOutput, 0o755); err != nil {
		return nil, nil, err
	}
	for _, item := range cases {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(item.SourcePath)))
		if err != nil {
			return nil, nil, fmt.Errorf("read VLM source %q: %w", item.ID, err)
		}
		if len(content) < 1 || len(content) > maxSourceBytes {
			return nil, nil, fmt.Errorf("VLM source %q size is invalid", item.ID)
		}
		digest := sha256.Sum256(content)
		if hex.EncodeToString(digest[:]) != item.SourceSHA256 {
			return nil, nil, fmt.Errorf("VLM source %q SHA-256 mismatch", item.ID)
		}
		cropped, err := cropPNG(content, item.Crop)
		if err != nil {
			return nil, nil, fmt.Errorf("crop VLM source %q: %w", item.ID, err)
		}
		cropDigest := sha256.Sum256(cropped)
		cropSHA := hex.EncodeToString(cropDigest[:])
		if err := os.WriteFile(filepath.Join(cropOutput, item.ID+".png"), cropped, 0o600); err != nil {
			return nil, nil, err
		}
		prepared = append(prepared, preparedCase{definition: item, content: cropped})
		inputs = append(inputs, caseInput{
			CaseID: item.ID, SourcePath: item.SourcePath, SourceSHA256: item.SourceSHA256,
			Crop: item.Crop, RasterBytes: len(cropped), RasterSHA256: cropSHA,
		})
	}
	return prepared, inputs, nil
}

func cropPNG(content []byte, crop cropRectangle) ([]byte, error) {
	decoded, _, err := image.Decode(bytes.NewReader(content))
	if err != nil {
		return nil, err
	}
	bounds := image.Rect(crop.X, crop.Y, crop.X+crop.Width, crop.Y+crop.Height)
	if !bounds.In(decoded.Bounds()) {
		return nil, errors.New("crop rectangle exceeds source image bounds")
	}
	result := image.NewRGBA(image.Rect(0, 0, crop.Width, crop.Height))
	draw.Draw(result, result.Bounds(), decoded, bounds.Min, draw.Src)
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, result); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}

func providerPricing(cfg config.Config, opts options) ([]providerRunner, error) {
	stepProfile, err := cfg.Models.Chat.Profile(stepFunComparisonProfile)
	if err != nil {
		return nil, fmt.Errorf("load StepFun comparison profile: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(stepProfile.Provider), "stepfun") {
		return nil, errors.New("StepFun comparison profile must use provider stepfun")
	}
	dashInput, dashOutput := opts.dashScopeInputPricePerMillion, opts.dashScopeOutputPricePerMillion
	providers := []providerRunner{{
		name: strings.TrimSpace(cfg.Models.Vision.Provider), model: strings.TrimSpace(cfg.Models.Vision.Model),
		pricing: pricing{Currency: "CNY", InputPricePerMillion: &dashInput, OutputPricePerMillion: &dashOutput,
			Basis: "Alibaba Cloud China qwen3-vl-plus <=32K listing observed 2026-08-06"},
	}, {
		name: strings.TrimSpace(stepProfile.Provider), model: strings.TrimSpace(stepProfile.Model),
		pricing: pricing{Currency: "CNY", Basis: "Step Plan subscription quota; no per-token amount asserted"},
	}}
	if opts.stepFunInputPricePerMillion >= 0 {
		stepInput, stepOutput := opts.stepFunInputPricePerMillion, opts.stepFunOutputPricePerMillion
		providers[1].pricing.InputPricePerMillion = &stepInput
		providers[1].pricing.OutputPricePerMillion = &stepOutput
		providers[1].pricing.Basis = "operator-supplied StepFun token pricing"
	}
	return providers, nil
}

func buildProviderRunners(ctx context.Context, cfg config.Config, opts options, definitions []providerRunner) ([]providerRunner, error) {
	prompt, err := cfg.Models.Vision.LoadPrompt("models.vision")
	if err != nil {
		return nil, err
	}
	dashConfig := cfg.Models.Vision
	dashConfig.MaxOutputTokens = opts.maxOutputTokens
	dashConfig.TimeoutMillis = int(opts.providerTimeout.Milliseconds())
	dashGenerator, err := visualmodel.NewOpenAICompatibleModel(ctx, dashConfig, "models.vision")
	if err != nil {
		return nil, err
	}
	stepConfig, err := cfg.Models.Chat.Profile(stepFunComparisonProfile)
	if err != nil {
		return nil, err
	}
	stepConfig.ReasoningEffort = "low"
	stepConfig.MaxOutputTokens = opts.maxOutputTokens
	stepConfig.TimeoutMillis = int(opts.providerTimeout.Milliseconds())
	stepInstance, err := platformchatmodel.New(ctx, stepFunComparisonProfile, stepConfig)
	if err != nil {
		return nil, err
	}
	stepGenerator := stepInstance.Model
	generators := []visualmodel.Generator{dashGenerator, stepGenerator}
	result := append([]providerRunner(nil), definitions...)
	for index := range result {
		processor, err := visualmodel.NewProcessor(nil, &visualmodel.Endpoint{
			Generator: generators[index], Provider: result[index].name, Model: result[index].model,
			Prompt: prompt, PromptVersion: cfg.Models.Vision.PromptVersion, ResponseFormat: cfg.Models.Vision.ResponseFormat,
		})
		if err != nil {
			return nil, err
		}
		result[index].processor = processor
	}
	return result, nil
}

func evaluateProviderCase(ctx context.Context, provider *providerRunner, item preparedCase) observation {
	current := observation{CaseID: item.definition.ID, Provider: provider.name, Model: provider.model}
	if provider.failed {
		current.SkippedReason = "provider_stopped_after_first_error"
		return current
	}
	current.Attempted = true
	started := time.Now()
	result, err := provider.processor.Process(ctx, knowledgeenrichment.Request{
		Asset: knowledgeparser.VisualAsset{
			Index: 0, Kind: knowledgeparser.VisualAssetSourceImage,
			SourcePath: item.definition.SourcePath + "#" + item.definition.ID,
			MediaType:  "image/png", Content: item.content,
			Width: item.definition.Crop.Width, Height: item.definition.Crop.Height,
		},
		Route: knowledgeenrichment.RouteOCRVLM, Reason: "quality_evaluation",
	})
	current.ProviderMillis = float64(time.Since(started).Microseconds()) / 1000
	if err != nil {
		current.Error = truncate(err.Error(), 2048)
		provider.failed = true
		return current
	}
	current.StrictJSONSuccess = true
	current.Usage = result.Usage
	for _, element := range result.Elements {
		switch element.ElementType {
		case knowledge.ElementOCRText:
			current.OCRText = element.ContentText
		case knowledge.ElementImageDescription:
			current.Description = element.ContentText
		}
	}
	current.MatchedTextAnchors = matchedAnchors(current.OCRText, item.definition.TextAnchors)
	current.TextAnchorRecall = ratio(len(current.MatchedTextAnchors), len(item.definition.TextAnchors))
	current.MatchedSemanticFactIDs = matchedFacts(current.Description, item.definition.SemanticFacts)
	current.SemanticFactRecall = ratio(len(current.MatchedSemanticFactIDs), len(item.definition.SemanticFacts))
	current.CitationUseful = current.TextAnchorRecall >= item.definition.MinimumTextAnchorRate &&
		current.SemanticFactRecall >= item.definition.MinimumSemanticRate
	current.EstimatedCostCNY = estimatedCost(result.Usage, provider.pricing)
	return current
}

func matchedAnchors(value string, anchors []string) []string {
	normalized := normalize(value)
	result := make([]string, 0, len(anchors))
	for _, anchor := range anchors {
		if strings.Contains(normalized, normalize(anchor)) {
			result = append(result, anchor)
		}
	}
	return result
}

func matchedFacts(value string, facts []semanticFact) []string {
	normalized := normalize(value)
	result := make([]string, 0, len(facts))
	for _, fact := range facts {
		matched := true
		for _, alternatives := range fact.Terms {
			termMatched := false
			for _, alternative := range alternatives {
				if strings.Contains(normalized, normalize(alternative)) {
					termMatched = true
					break
				}
			}
			if !termMatched {
				matched = false
				break
			}
		}
		if matched {
			result = append(result, fact.ID)
		}
	}
	return result
}

func summarizeProviders(definitions []providerRunner, observations []observation) []providerSummary {
	result := make([]providerSummary, 0, len(definitions))
	for _, definition := range definitions {
		current := providerSummary{Provider: definition.name, Model: definition.model, Pricing: definition.pricing}
		var textRecall, semanticRecall, latency, totalCost float64
		latencies := make([]float64, 0)
		priced := definition.pricing.InputPricePerMillion != nil && definition.pricing.OutputPricePerMillion != nil
		for _, item := range observations {
			if item.Provider != definition.name || item.Model != definition.model || !item.Attempted {
				continue
			}
			current.AttemptedCalls++
			if !item.StrictJSONSuccess {
				continue
			}
			current.SuccessfulCalls++
			textRecall += item.TextAnchorRecall
			semanticRecall += item.SemanticFactRecall
			latency += item.ProviderMillis
			latencies = append(latencies, item.ProviderMillis)
			if item.CitationUseful {
				current.CitationUsefulRate++
			}
			if item.Usage != nil {
				current.TotalPromptTokens += item.Usage.PromptTokens
				current.TotalCompletionTokens += item.Usage.CompletionTokens
				current.TotalTokens += item.Usage.TotalTokens
			}
			if item.EstimatedCostCNY != nil {
				totalCost += *item.EstimatedCostCNY
			}
		}
		current.StrictJSONRate = ratio(current.SuccessfulCalls, current.AttemptedCalls)
		if current.SuccessfulCalls > 0 {
			current.MeanTextAnchorRecall = textRecall / float64(current.SuccessfulCalls)
			current.MeanSemanticFactRecall = semanticRecall / float64(current.SuccessfulCalls)
			current.CitationUsefulRate /= float64(current.SuccessfulCalls)
			current.MeanProviderMillis = latency / float64(current.SuccessfulCalls)
			sort.Float64s(latencies)
			current.P50ProviderMillis = nearestRankPercentile(latencies, 0.50)
			current.P95ProviderMillis = nearestRankPercentile(latencies, 0.95)
		}
		if priced {
			current.TotalEstimatedCostCNY = &totalCost
			if current.SuccessfulCalls > 0 {
				costPerSuccess := totalCost / float64(current.SuccessfulCalls)
				current.CostPerSuccessfulRegionCNY = &costPerSuccess
			}
		}
		result = append(result, current)
	}
	return result
}

func nearestRankPercentile(sortedValues []float64, percentile float64) float64 {
	if len(sortedValues) == 0 {
		return 0
	}
	index := int(math.Ceil(float64(len(sortedValues))*percentile)) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sortedValues) {
		index = len(sortedValues) - 1
	}
	return sortedValues[index]
}

func estimatedCost(usage *knowledgeenrichment.ProviderUsage, price pricing) *float64 {
	if usage == nil || price.InputPricePerMillion == nil || price.OutputPricePerMillion == nil {
		return nil
	}
	value := float64(usage.PromptTokens)**price.InputPricePerMillion/1_000_000 +
		float64(usage.CompletionTokens)**price.OutputPricePerMillion/1_000_000
	return &value
}

func pricingLabel(value pricing) string {
	if value.InputPricePerMillion == nil || value.OutputPricePerMillion == nil {
		return "subscription_unpriced"
	}
	return fmt.Sprintf("%.4f/%.4f_cny_per_million", *value.InputPricePerMillion, *value.OutputPricePerMillion)
}

func normalize(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, strings.TrimSpace(value))
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 1
	}
	return float64(numerator) / float64(denominator)
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func usageTotal(value *knowledgeenrichment.ProviderUsage) int {
	if value == nil {
		return 0
	}
	return value.TotalTokens
}

func truncate(value string, maxRunes int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= maxRunes {
		return string(runes)
	}
	return string(runes[:maxRunes])
}

func writeReport(path string, value report) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0o600)
}
