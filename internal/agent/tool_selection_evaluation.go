package agent

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/contextgovernance"
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

const (
	// ToolSelectionObservationV2 是新评测数据合同：显式 observationSchemaVersion
	// 加上完整身份（Profile 合同、模型可见名单、模型 Profile 指纹、实现
	// revision/dirty）。历史 v1 资产没有该字段，按 v1 处理以保持可重放。
	ToolSelectionObservationV2 = "tool-selection-observation-v2"
	// ToolSelectionEvaluationWideProfile 是 wide 实验臂的评测合同 ID，与
	// agentruntime.ToolProfileEvaluationWide（evaluation-wide-v1）一致。wide
	// baseline 不是生产 Runtime Profile，生产两个 Runner 只绑定
	// conversation-default/diagnosis-default；ToolProfileID 字段记录该评测
	// 合同，避免与 diagnosis-default 混同。
	ToolSelectionEvaluationWideProfile = "evaluation-wide-v1"
)

type ToolSelectionObservation struct {
	DatasetVersion           string               `json:"datasetVersion"`
	CaseID                   string               `json:"caseId"`
	Variant                  ToolSelectionVariant `json:"variant"`
	RunID                    string               `json:"runId"`
	ObservationSchemaVersion string               `json:"observationSchemaVersion,omitempty"`
	ModelProvider            string               `json:"modelProvider"`
	ModelID                  string               `json:"modelId"`
	ReasoningEffort          string               `json:"reasoningEffort"`
	PromptVersion            string               `json:"promptVersion"`
	MaxOutputTokens          int                  `json:"maxOutputTokens"`
	ToolProfileID            string               `json:"toolProfileId,omitempty"`
	ModelVisibleNames        []string             `json:"modelVisibleNames,omitempty"`
	ModelProfileFingerprint  string               `json:"modelProfileFingerprint,omitempty"`
	ImplementationRevision   string               `json:"implementationRevision,omitempty"`
	ImplementationDirty      bool                 `json:"implementationDirty,omitempty"`
	AvailableTools           []string             `json:"availableTools"`
	SelectedTool             string               `json:"selectedTool,omitempty"`
	ToolCallCount            int                  `json:"toolCallCount"`
	FinishReason             string               `json:"finishReason,omitempty"`
	ModelText                string               `json:"modelText,omitempty"`
	ToolSchemaHash           string               `json:"toolSchemaHash"`
	ToolSchemaBytes          int                  `json:"toolSchemaBytes"`
	BasePromptTokens         int                  `json:"basePromptTokens"`
	ToolSchemaPromptTokens   int                  `json:"toolSchemaPromptTokens"`
	Usage                    ModelUsage           `json:"usage"`
	DurationMillis           int64                `json:"durationMillis"`
	ErrorType                string               `json:"errorType,omitempty"`
}

func (o ToolSelectionObservation) Validate() error {
	if strings.TrimSpace(o.DatasetVersion) == "" || strings.TrimSpace(o.CaseID) == "" ||
		strings.TrimSpace(o.RunID) == "" || strings.TrimSpace(o.ModelProvider) == "" ||
		strings.TrimSpace(o.ModelID) == "" || strings.TrimSpace(o.PromptVersion) == "" {
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
	// 数据合同版本：历史 v1 资产没有 observationSchemaVersion，按 v1 校验
	// 保持可重放；v2 强制执行完整身份校验；其他非空版本一律拒绝。
	switch o.ObservationSchemaVersion {
	case "":
	case ToolSelectionObservationV2:
		if err := o.validateV2(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported observationSchemaVersion %q", o.ObservationSchemaVersion)
	}
	return nil
}

// validateV2 强制执行 v2 数据合同。两个实验臂的 Profile 合同不同：
// filtered 必须是生产 diagnosis-default，wide 必须是评测合同
// evaluation-wide-v1；不能互相伪装。两臂都必须记录模型可见名单（与真正
// 发送的 AvailableTools 完全一致）和合法的 SHA-256 模型 Profile 指纹。
// implementationRevision 规则：空值拒绝；unknown 且 clean 拒绝；unknown 且
// dirty 允许（本地 smoke）；具体 revision 允许。ImplementationDirty 原样
// 保留，dirty 观测由配对逻辑排除在正式 paired reduction 之外。
func (o ToolSelectionObservation) validateV2() error {
	switch o.Variant {
	case ToolSelectionWide:
		if o.ToolProfileID != ToolSelectionEvaluationWideProfile {
			return fmt.Errorf("v2 wide toolProfileId = %q, want %q", o.ToolProfileID, ToolSelectionEvaluationWideProfile)
		}
	case ToolSelectionFiltered:
		if o.ToolProfileID != string(agentruntime.ToolProfileDiagnosis) {
			return fmt.Errorf("v2 filtered toolProfileId = %q, want %q", o.ToolProfileID, agentruntime.ToolProfileDiagnosis)
		}
	default:
		return fmt.Errorf("invalid tool selection variant %q", o.Variant)
	}
	if len(o.ModelVisibleNames) == 0 {
		return errors.New("v2 observation requires modelVisibleNames")
	}
	for _, name := range o.ModelVisibleNames {
		if !toolNamePattern.MatchString(name) {
			return fmt.Errorf("v2 observation has invalid modelVisibleName %q", name)
		}
	}
	if !contextgovernance.IsSHA256Hex(o.ModelProfileFingerprint) {
		return errors.New("v2 observation requires a valid SHA-256 modelProfileFingerprint")
	}
	revision := strings.TrimSpace(o.ImplementationRevision)
	if revision == "" {
		return errors.New("v2 observation requires an implementationRevision")
	}
	if revision == "unknown" && !o.ImplementationDirty {
		return errors.New("v2 observation with unknown revision must set implementationDirty=true (local smoke only)")
	}
	if !slices.Equal(o.ModelVisibleNames, o.AvailableTools) {
		return errors.New("v2 modelVisibleNames must equal the actual availableTools sent to the model")
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
			wide.MaxOutputTokens != filtered.MaxOutputTokens ||
			!sameObservationIdentity(wide, filtered) {
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

// sameObservationIdentity 要求两个实验臂属于同一观测合同与同一实现身份：
// observationSchemaVersion、模型 Profile 指纹、实现 revision 和 dirty 状态
// 必须一致，且两边都不能是 dirty。dirty 观测只允许生成单臂统计供本地
// smoke，不得计入正式 paired reduction，也不得贡献任何 paired 归约指标。
// ToolProfileID 刻意不参与比较：wide/filtered 本来就是不同评测合同，
// 各自的 v2 Validate 已分别校验其合同 ID。
func sameObservationIdentity(wide, filtered ToolSelectionObservation) bool {
	if wide.ObservationSchemaVersion != filtered.ObservationSchemaVersion ||
		wide.ModelProfileFingerprint != filtered.ModelProfileFingerprint ||
		wide.ImplementationRevision != filtered.ImplementationRevision ||
		wide.ImplementationDirty != filtered.ImplementationDirty {
		return false
	}
	return !wide.ImplementationDirty && !filtered.ImplementationDirty
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
