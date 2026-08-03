package agent

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

type ToolSelectionVariant string

const (
	ToolSelectionWide     ToolSelectionVariant = "wide"
	ToolSelectionFiltered ToolSelectionVariant = "filtered"
)

func (v ToolSelectionVariant) Valid() bool {
	return v == ToolSelectionWide || v == ToolSelectionFiltered
}

type ToolSelectionScope string

const (
	ToolSelectionTicket ToolSelectionScope = "ticket"
	ToolSelectionGitHub ToolSelectionScope = "github"
	ToolSelectionSQL    ToolSelectionScope = "sql"
)

func (s ToolSelectionScope) Valid() bool {
	return s == ToolSelectionTicket || s == ToolSelectionGitHub || s == ToolSelectionSQL
}

type ToolSelectionCase struct {
	DatasetVersion string             `json:"datasetVersion"`
	CaseID         string             `json:"caseId"`
	Scope          ToolSelectionScope `json:"scope"`
	UserQuery      string             `json:"userQuery"`
	ExpectedTool   string             `json:"expectedTool"`
}

func (c ToolSelectionCase) Validate() error {
	if strings.TrimSpace(c.DatasetVersion) == "" || strings.TrimSpace(c.CaseID) == "" ||
		strings.TrimSpace(c.UserQuery) == "" {
		return errors.New("datasetVersion, caseId, and userQuery are required")
	}
	if !c.Scope.Valid() {
		return fmt.Errorf("invalid tool selection scope %q", c.Scope)
	}
	if !toolNamePattern.MatchString(c.ExpectedTool) {
		return fmt.Errorf("invalid expected tool %q", c.ExpectedTool)
	}
	return nil
}

type ToolSelectionObservation struct {
	DatasetVersion         string               `json:"datasetVersion"`
	CaseID                 string               `json:"caseId"`
	Variant                ToolSelectionVariant `json:"variant"`
	RunID                  string               `json:"runId"`
	ModelProvider          string               `json:"modelProvider"`
	ModelID                string               `json:"modelId"`
	ReasoningEffort        string               `json:"reasoningEffort"`
	PromptVersion          string               `json:"promptVersion"`
	MaxOutputTokens        int                  `json:"maxOutputTokens"`
	AvailableTools         []string             `json:"availableTools"`
	SelectedTool           string               `json:"selectedTool,omitempty"`
	ToolCallCount          int                  `json:"toolCallCount"`
	FinishReason           string               `json:"finishReason,omitempty"`
	ModelText              string               `json:"modelText,omitempty"`
	ToolSchemaHash         string               `json:"toolSchemaHash"`
	ToolSchemaBytes        int                  `json:"toolSchemaBytes"`
	BasePromptTokens       int                  `json:"basePromptTokens"`
	ToolSchemaPromptTokens int                  `json:"toolSchemaPromptTokens"`
	Usage                  ModelUsage           `json:"usage"`
	DurationMillis         int64                `json:"durationMillis"`
	ErrorType              string               `json:"errorType,omitempty"`
}

func (o ToolSelectionObservation) Validate() error {
	if strings.TrimSpace(o.DatasetVersion) == "" || strings.TrimSpace(o.CaseID) == "" ||
		strings.TrimSpace(o.RunID) == "" || strings.TrimSpace(o.ModelProvider) == "" ||
		strings.TrimSpace(o.ModelID) == "" || strings.TrimSpace(o.ReasoningEffort) == "" ||
		strings.TrimSpace(o.PromptVersion) == "" {
		return errors.New("observation identity and model metadata are required")
	}
	if !o.Variant.Valid() {
		return fmt.Errorf("invalid tool selection variant %q", o.Variant)
	}
	if len(o.AvailableTools) == 0 || strings.TrimSpace(o.ToolSchemaHash) == "" || o.ToolSchemaBytes <= 0 {
		return errors.New("available tools and schema metadata are required")
	}
	if o.MaxOutputTokens <= 0 {
		return errors.New("maxOutputTokens must be positive")
	}
	if len(o.ModelText) > 4096 {
		return errors.New("modelText exceeds 4096 bytes")
	}
	if o.ToolCallCount < 0 || o.ToolCallCount > 32 {
		return errors.New("toolCallCount is invalid")
	}
	if o.SelectedTool != "" && !toolNamePattern.MatchString(o.SelectedTool) {
		return fmt.Errorf("invalid selected tool %q", o.SelectedTool)
	}
	if o.ToolCallCount == 1 && o.SelectedTool == "" {
		return errors.New("single tool call requires selectedTool")
	}
	if o.ToolCallCount != 1 && o.SelectedTool != "" {
		return errors.New("selectedTool is only valid for exactly one tool call")
	}
	for _, name := range o.AvailableTools {
		if !toolNamePattern.MatchString(name) {
			return fmt.Errorf("invalid available tool %q", name)
		}
	}
	if o.Usage.ModelCalls < 0 || o.Usage.ModelCalls > 1 || o.Usage.PromptTokens < 0 ||
		o.Usage.TotalTokens < 0 || o.Usage.CompletionTokens < 0 || o.Usage.CachedTokens < 0 ||
		o.Usage.ReasoningTokens < 0 || o.DurationMillis < 0 {
		return errors.New("provider usage and duration must be non-negative")
	}
	if o.BasePromptTokens < 0 || o.ToolSchemaPromptTokens < 0 {
		return errors.New("prompt token calibration must be non-negative")
	}
	if o.Usage.ModelCalls == 1 && o.Usage.PromptTokens > 0 &&
		(o.BasePromptTokens <= 0 || o.ToolSchemaPromptTokens <= 0 ||
			o.ToolSchemaPromptTokens != o.Usage.PromptTokens-o.BasePromptTokens) {
		return errors.New("provider prompt tokens require a consistent schema-token calibration")
	}
	if o.ErrorType == "" && (o.Usage.ModelCalls != 1 || o.Usage.PromptTokens <= 0 || o.Usage.TotalTokens <= 0) {
		return errors.New("successful observation requires one-call provider usage")
	}
	return nil
}

type ToolSelectionVariantSummary struct {
	Runs                   int     `json:"runs"`
	Correct                int     `json:"correct"`
	Accuracy               float64 `json:"accuracy"`
	InvalidSelectionRate   float64 `json:"invalidSelectionRate"`
	OutOfWhitelistRate     float64 `json:"outOfWhitelistRate"`
	PromptTokens           int     `json:"promptTokens"`
	CompletionTokens       int     `json:"completionTokens"`
	TotalTokens            int     `json:"totalTokens"`
	ToolSchemaBytes        int     `json:"toolSchemaBytes"`
	BasePromptTokens       int     `json:"basePromptTokens"`
	ToolSchemaPromptTokens int     `json:"toolSchemaPromptTokens"`
	AverageDurationMillis  float64 `json:"averageDurationMillis"`
	FailedRuns             int     `json:"failedRuns"`
}

type ToolSelectionSummary struct {
	DatasetVersion                 string                      `json:"datasetVersion"`
	Cases                          int                         `json:"cases"`
	Runs                           int                         `json:"runs"`
	PairedCases                    int                         `json:"pairedCases"`
	UnpairedRuns                   int                         `json:"unpairedRuns"`
	Wide                           ToolSelectionVariantSummary `json:"wide"`
	Filtered                       ToolSelectionVariantSummary `json:"filtered"`
	PairedPromptTokenReduction     float64                     `json:"pairedPromptTokenReduction"`
	PairedToolSchemaTokenReduction float64                     `json:"pairedToolSchemaTokenReduction"`
	PairedSchemaByteReduction      float64                     `json:"pairedSchemaByteReduction"`
	FailureTypes                   map[string]int              `json:"failureTypes,omitempty"`
}

func EvaluateToolSelection(
	cases []ToolSelectionCase,
	observations []ToolSelectionObservation,
) (ToolSelectionSummary, error) {
	caseByID, version, err := indexToolSelectionCases(cases)
	if err != nil {
		return ToolSelectionSummary{}, err
	}
	summary := ToolSelectionSummary{
		DatasetVersion: version, Cases: len(cases), Runs: len(observations),
		FailureTypes: make(map[string]int),
	}
	pairs := make(map[string]map[ToolSelectionVariant]ToolSelectionObservation, len(cases))
	seenRuns := make(map[string]struct{}, len(observations))
	for index, observation := range observations {
		if err := observation.Validate(); err != nil {
			return ToolSelectionSummary{}, fmt.Errorf("observation %d: %w", index, err)
		}
		definition, ok := caseByID[observation.CaseID]
		if !ok || observation.DatasetVersion != version {
			return ToolSelectionSummary{}, fmt.Errorf("observation %q does not belong to dataset %q", observation.RunID, version)
		}
		if _, exists := seenRuns[observation.RunID]; exists {
			return ToolSelectionSummary{}, fmt.Errorf("duplicate runId %q", observation.RunID)
		}
		seenRuns[observation.RunID] = struct{}{}
		if pairs[definition.CaseID] == nil {
			pairs[definition.CaseID] = make(map[ToolSelectionVariant]ToolSelectionObservation, 2)
		}
		if _, exists := pairs[definition.CaseID][observation.Variant]; exists {
			return ToolSelectionSummary{}, fmt.Errorf("case %q contains duplicate %s observation", definition.CaseID, observation.Variant)
		}
		pairs[definition.CaseID][observation.Variant] = observation
		if observation.ErrorType != "" {
			summary.FailureTypes[observation.ErrorType]++
		}
	}
	summary.Wide = summarizeToolSelection(cases, observations, ToolSelectionWide)
	summary.Filtered = summarizeToolSelection(cases, observations, ToolSelectionFiltered)
	var wideTokens, filteredTokens, wideSchemaTokens, filteredSchemaTokens, wideBytes, filteredBytes int64
	for _, pair := range pairs {
		wide, wideOK := pair[ToolSelectionWide]
		filtered, filteredOK := pair[ToolSelectionFiltered]
		if !wideOK || !filteredOK {
			if wideOK {
				summary.UnpairedRuns++
			}
			if filteredOK {
				summary.UnpairedRuns++
			}
			continue
		}
		if !hasProviderPromptUsage(wide) || !hasProviderPromptUsage(filtered) ||
			wide.ModelProvider != filtered.ModelProvider || wide.ModelID != filtered.ModelID ||
			wide.ReasoningEffort != filtered.ReasoningEffort || wide.PromptVersion != filtered.PromptVersion ||
			wide.MaxOutputTokens != filtered.MaxOutputTokens {
			summary.UnpairedRuns += 2
			continue
		}
		summary.PairedCases++
		wideTokens += int64(wide.Usage.PromptTokens)
		filteredTokens += int64(filtered.Usage.PromptTokens)
		wideSchemaTokens += int64(wide.ToolSchemaPromptTokens)
		filteredSchemaTokens += int64(filtered.ToolSchemaPromptTokens)
		wideBytes += int64(wide.ToolSchemaBytes)
		filteredBytes += int64(filtered.ToolSchemaBytes)
	}
	if wideTokens > 0 {
		summary.PairedPromptTokenReduction = reductionRate(wideTokens, filteredTokens)
	}
	if wideSchemaTokens > 0 {
		summary.PairedToolSchemaTokenReduction = reductionRate(wideSchemaTokens, filteredSchemaTokens)
	}
	if wideBytes > 0 {
		summary.PairedSchemaByteReduction = reductionRate(wideBytes, filteredBytes)
	}
	if len(summary.FailureTypes) == 0 {
		summary.FailureTypes = nil
	}
	return summary, nil
}

func hasProviderPromptUsage(observation ToolSelectionObservation) bool {
	return observation.Usage.ModelCalls == 1 && observation.Usage.PromptTokens > 0 &&
		observation.Usage.TotalTokens > 0 && observation.BasePromptTokens > 0 &&
		observation.ToolSchemaPromptTokens > 0
}

func indexToolSelectionCases(cases []ToolSelectionCase) (map[string]ToolSelectionCase, string, error) {
	if len(cases) == 0 {
		return nil, "", errors.New("tool selection dataset contains no cases")
	}
	result := make(map[string]ToolSelectionCase, len(cases))
	version := ""
	for index, current := range cases {
		if err := current.Validate(); err != nil {
			return nil, "", fmt.Errorf("case %d: %w", index, err)
		}
		if version == "" {
			version = current.DatasetVersion
		} else if current.DatasetVersion != version {
			return nil, "", errors.New("tool selection dataset mixes versions")
		}
		if _, exists := result[current.CaseID]; exists {
			return nil, "", fmt.Errorf("duplicate caseId %q", current.CaseID)
		}
		result[current.CaseID] = current
	}
	return result, version, nil
}

func summarizeToolSelection(
	cases []ToolSelectionCase,
	observations []ToolSelectionObservation,
	variant ToolSelectionVariant,
) ToolSelectionVariantSummary {
	definitions := make(map[string]ToolSelectionCase, len(cases))
	for _, definition := range cases {
		definitions[definition.CaseID] = definition
	}
	var result ToolSelectionVariantSummary
	var invalid, outOfWhitelist int
	var totalDuration int64
	for _, observation := range observations {
		if observation.Variant != variant {
			continue
		}
		result.Runs++
		definition := definitions[observation.CaseID]
		if observation.ErrorType != "" {
			result.FailedRuns++
		}
		if observation.ToolCallCount != 1 {
			invalid++
		} else {
			if !slices.Contains(observation.AvailableTools, observation.SelectedTool) {
				outOfWhitelist++
			}
			if observation.ErrorType == "" && observation.SelectedTool == definition.ExpectedTool {
				result.Correct++
			}
		}
		result.PromptTokens += observation.Usage.PromptTokens
		result.CompletionTokens += observation.Usage.CompletionTokens
		result.TotalTokens += observation.Usage.TotalTokens
		result.ToolSchemaBytes += observation.ToolSchemaBytes
		result.BasePromptTokens += observation.BasePromptTokens
		result.ToolSchemaPromptTokens += observation.ToolSchemaPromptTokens
		totalDuration += observation.DurationMillis
	}
	if result.Runs > 0 {
		result.Accuracy = float64(result.Correct) / float64(result.Runs)
		result.InvalidSelectionRate = float64(invalid) / float64(result.Runs)
		result.OutOfWhitelistRate = float64(outOfWhitelist) / float64(result.Runs)
		result.AverageDurationMillis = float64(totalDuration) / float64(result.Runs)
	}
	return result
}
