package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/contextgovernance"

	"github.com/cloudwego/eino/schema"
)

type ToolSelectionVariant string

const (
	ToolSelectionWide       ToolSelectionVariant = "wide"
	ToolSelectionProduction ToolSelectionVariant = "production"
)

func (v ToolSelectionVariant) Valid() bool {
	return v == ToolSelectionWide || v == ToolSelectionProduction
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

// ToolSelectionComparability 是两臂最终模型 Schema 的可比性合同，来自
// VerifyToolSelectionComparability。Observation 只记录其中三个最小字段：
// comparisonFingerprint、sharedToolNames、baselineOnlyToolNames。
type ToolSelectionComparability struct {
	ComparisonFingerprint string   `json:"comparisonFingerprint"`
	SharedToolNames       []string `json:"sharedToolNames"`
	BaselineOnlyToolNames []string `json:"baselineOnlyToolNames"`
}

// modelVisibleToolSchema 是 ToolInfo 的模型可见规范化投影：Eino 的
// OpenAI 兼容适配器只暴露 Name、Desc 与 JSON Schema 参数（ToolInfo.Extra
// 不是模型可见字段，刻意排除）。
type modelVisibleToolSchema struct {
	Name       string          `json:"name"`
	Desc       string          `json:"desc"`
	Parameters json.RawMessage `json:"parameters"`
}

func projectModelVisibleToolSchema(info *schema.ToolInfo) (modelVisibleToolSchema, error) {
	if info == nil {
		return modelVisibleToolSchema{}, errors.New("nil ToolInfo")
	}
	parameters, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil {
		return modelVisibleToolSchema{}, fmt.Errorf("convert Tool %q parameters: %w", info.Name, err)
	}
	var encoded json.RawMessage
	if parameters != nil {
		encoded, err = json.Marshal(parameters)
		if err != nil {
			return modelVisibleToolSchema{}, fmt.Errorf("encode Tool %q parameters: %w", info.Name, err)
		}
	}
	return modelVisibleToolSchema{Name: info.Name, Desc: info.Desc, Parameters: encoded}, nil
}

// VerifyToolSelectionComparability 基于两臂最终真正发送给模型的 ToolInfo
// 校验评测可比性，在创建 Chat Provider 之前 fail-closed：
//   - production 与 wide 名单都非空、无重复；
//   - production 是 wide 的名字子集，且 wide 是严格超集（至少多一个 Tool）；
//   - 同名共享 Tool 的规范化 Schema（Name/Desc/Parameters）完全一致；
//   - 两臂均恰好包含一次 skill（Eino Skill Middleware 恰好追加一次）；
//
// 返回稳定 comparisonFingerprint（sha256: 前缀）、sharedToolNames（= 排序后
// 的 production 名单）与 baselineOnlyToolNames（= wide 独有名单）。不满足
// 任一条件时返回错误，调用方必须在创建 Provider 前终止评测。
func VerifyToolSelectionComparability(
	productionInfos, wideInfos []*schema.ToolInfo,
) (ToolSelectionComparability, error) {
	if len(productionInfos) == 0 {
		return ToolSelectionComparability{}, errors.New("production Tool schema is empty")
	}
	if len(wideInfos) == 0 {
		return ToolSelectionComparability{}, errors.New("wide Tool schema is empty")
	}
	productionSchemas, err := projectModelVisibleSchemas(productionInfos)
	if err != nil {
		return ToolSelectionComparability{}, err
	}
	wideSchemas, err := projectModelVisibleSchemas(wideInfos)
	if err != nil {
		return ToolSelectionComparability{}, err
	}
	if err := verifyUniqueComparabilityToolNames(productionSchemas, "production"); err != nil {
		return ToolSelectionComparability{}, err
	}
	if err := verifyUniqueComparabilityToolNames(wideSchemas, "wide"); err != nil {
		return ToolSelectionComparability{}, err
	}
	// production ⊆ wide（名字层面）。
	for _, current := range productionSchemas {
		if !slices.ContainsFunc(wideSchemas, func(other modelVisibleToolSchema) bool {
			return other.Name == current.Name
		}) {
			return ToolSelectionComparability{}, fmt.Errorf(
				"production Tool %q is missing from the wide arm", current.Name,
			)
		}
	}
	// wide 是严格超集：至少多一个 Tool。
	if len(wideSchemas) <= len(productionSchemas) {
		return ToolSelectionComparability{}, errors.New(
			"wide arm must be a strict superset of the production arm (at least one additional Tool)",
		)
	}
	// 两臂均恰好包含一次 skill（同一 Skill Middleware 恰好追加一次）。
	if err := verifySingleComparabilitySkill(productionSchemas, "production"); err != nil {
		return ToolSelectionComparability{}, err
	}
	if err := verifySingleComparabilitySkill(wideSchemas, "wide"); err != nil {
		return ToolSelectionComparability{}, err
	}
	// 同名共享 Tool 的规范化 Schema 必须完全一致。
	for _, current := range productionSchemas {
		wideIndex := slices.IndexFunc(wideSchemas, func(other modelVisibleToolSchema) bool {
			return other.Name == current.Name
		})
		if wideIndex < 0 {
			return ToolSelectionComparability{}, fmt.Errorf(
				"production Tool %q is missing from the wide arm", current.Name,
			)
		}
		if !slices.Equal(current.Parameters, wideSchemas[wideIndex].Parameters) ||
			current.Desc != wideSchemas[wideIndex].Desc {
			return ToolSelectionComparability{}, fmt.Errorf(
				"shared Tool %q schema drifted between the production and wide arms", current.Name,
			)
		}
	}
	sharedNames := make([]string, 0, len(productionSchemas))
	for _, current := range productionSchemas {
		sharedNames = append(sharedNames, current.Name)
	}
	var baselineOnlyNames []string
	for _, current := range wideSchemas {
		if !slices.Contains(sharedNames, current.Name) {
			baselineOnlyNames = append(baselineOnlyNames, current.Name)
		}
	}
	slices.Sort(sharedNames)
	slices.Sort(baselineOnlyNames)
	fingerprint, err := comparabilityFingerprint(productionSchemas, wideSchemas)
	if err != nil {
		return ToolSelectionComparability{}, err
	}
	return ToolSelectionComparability{
		ComparisonFingerprint: fingerprint,
		SharedToolNames:       sharedNames,
		BaselineOnlyToolNames: baselineOnlyNames,
	}, nil
}

func projectModelVisibleSchemas(infos []*schema.ToolInfo) ([]modelVisibleToolSchema, error) {
	projected := make([]modelVisibleToolSchema, 0, len(infos))
	for _, info := range infos {
		current, err := projectModelVisibleToolSchema(info)
		if err != nil {
			return nil, err
		}
		projected = append(projected, current)
	}
	slices.SortFunc(projected, func(left, right modelVisibleToolSchema) int {
		return strings.Compare(left.Name, right.Name)
	})
	return projected, nil
}

func verifyUniqueComparabilityToolNames(schemas []modelVisibleToolSchema, arm string) error {
	for index := 1; index < len(schemas); index++ {
		if schemas[index].Name == schemas[index-1].Name {
			return fmt.Errorf("%s arm contains duplicate Tool %q", arm, schemas[index].Name)
		}
	}
	return nil
}

func verifySingleComparabilitySkill(schemas []modelVisibleToolSchema, arm string) error {
	skillCount := 0
	for _, current := range schemas {
		if current.Name == ToolSkill {
			skillCount++
		}
	}
	if skillCount != 1 {
		return fmt.Errorf(
			"%s arm must contain exactly one skill Tool (Eino Skill Middleware), got %d",
			arm, skillCount,
		)
	}
	return nil
}

func comparabilityFingerprint(
	productionSchemas, wideSchemas []modelVisibleToolSchema,
) (string, error) {
	encoded, err := json.Marshal(struct {
		Production []modelVisibleToolSchema `json:"production"`
		Wide       []modelVisibleToolSchema `json:"wide"`
	}{Production: productionSchemas, Wide: wideSchemas})
	if err != nil {
		return "", fmt.Errorf("marshal comparability contract: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
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
	// ToolSelectionObservationV3 是当前评测数据合同：显式
	// observationSchemaVersion 加上完整身份（Profile 合同、模型可见名单、
	// 模型 Profile 指纹、实现 revision/dirty）与两臂 comparison 身份
	// （comparisonFingerprint/sharedToolNames/baselineOnlyToolNames）。历史
	// v1（无版本字段）与 v2 资产一律拒绝，不保留兼容分支。
	ToolSelectionObservationV3 = "tool-selection-observation-v3"
	// ToolSelectionEvaluationWideProfile 是 wide 实验臂的评测合同 ID，与
	// agentruntime.ToolProfileEvaluationWide（evaluation-wide-v2）一致。
	// wide baseline 是 conversation-default ∪ diagnosis-default 的并集，
	// 不是生产 Runtime Profile；ToolProfileID 字段记录该评测合同，避免与
	// diagnosis-default 混同。
	ToolSelectionEvaluationWideProfile = string(agentruntime.ToolProfileEvaluationWide)
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
	ComparisonFingerprint    string               `json:"comparisonFingerprint,omitempty"`
	SharedToolNames          []string             `json:"sharedToolNames,omitempty"`
	BaselineOnlyToolNames    []string             `json:"baselineOnlyToolNames,omitempty"`
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
	// 数据合同：v3 是唯一支持的合同，不保留 v1/v2 兼容分支。历史资产必须
	// 标记 historical，不得进入正式归约。
	if o.ObservationSchemaVersion != ToolSelectionObservationV3 {
		return fmt.Errorf(
			"unsupported observationSchemaVersion %q: the tool selection evaluator only accepts %q; historical v1/v2 observations must be marked historical and excluded from formal reduction",
			o.ObservationSchemaVersion, ToolSelectionObservationV3,
		)
	}
	if err := o.validateV3Identity(); err != nil {
		return err
	}
	return nil
}

// validateV3Identity 强制执行 v3 数据合同。两个实验臂的 Profile 合同不同：
// production 必须是生产 diagnosis-default，wide 必须是评测合同
// evaluation-wide-v2；不能互相伪装。两臂都必须记录模型可见名单（与真正
// 发送的 AvailableTools 完全一致）和合法的 SHA-256 模型 Profile 指纹。
// implementationRevision 规则：空值拒绝；unknown 且 clean 拒绝（unknown 且
// dirty 仅限本地 smoke）。v3 额外要求两臂记录 comparison 身份：
// comparisonFingerprint 必须是合法 SHA-256，sharedToolNames 非空且与
// baselineOnlyToolNames 不相交。
func (o ToolSelectionObservation) validateV3Identity() error {
	switch o.Variant {
	case ToolSelectionWide:
		if o.ToolProfileID != ToolSelectionEvaluationWideProfile {
			return fmt.Errorf("v3 wide toolProfileId = %q, want %q",
				o.ToolProfileID, ToolSelectionEvaluationWideProfile)
		}
	case ToolSelectionProduction:
		if o.ToolProfileID != string(agentruntime.ToolProfileDiagnosis) {
			return fmt.Errorf("v3 production toolProfileId = %q, want %q",
				o.ToolProfileID, agentruntime.ToolProfileDiagnosis)
		}
	default:
		return fmt.Errorf("invalid tool selection variant %q", o.Variant)
	}
	if len(o.ModelVisibleNames) == 0 {
		return errors.New("v3 observation requires modelVisibleNames")
	}
	for _, name := range o.ModelVisibleNames {
		if !toolNamePattern.MatchString(name) {
			return fmt.Errorf("v3 observation has invalid modelVisibleName %q", name)
		}
	}
	if !contextgovernance.IsSHA256Hex(o.ModelProfileFingerprint) {
		return errors.New("v3 observation requires a valid SHA-256 modelProfileFingerprint")
	}
	revision := strings.TrimSpace(o.ImplementationRevision)
	if revision == "" {
		return errors.New("v3 observation requires an implementationRevision")
	}
	if revision == "unknown" && !o.ImplementationDirty {
		return errors.New("v3 observation with unknown revision must set implementationDirty=true (local smoke only)")
	}
	if !slices.Equal(o.ModelVisibleNames, o.AvailableTools) {
		return errors.New("v3 modelVisibleNames must equal the actual availableTools sent to the model")
	}
	if err := validateToolComparisonIdentity(
		o.ComparisonFingerprint, o.SharedToolNames, o.BaselineOnlyToolNames,
	); err != nil {
		return fmt.Errorf("v3 observation comparison identity: %w", err)
	}
	if o.Variant == ToolSelectionProduction && !equalToolNameSets(o.AvailableTools, o.SharedToolNames) {
		return errors.New("v3 production availableTools must equal sharedToolNames")
	}
	if o.Variant == ToolSelectionWide {
		expected := append(append([]string(nil), o.SharedToolNames...), o.BaselineOnlyToolNames...)
		if !equalToolNameSets(o.AvailableTools, expected) {
			return errors.New("v3 wide availableTools must equal sharedToolNames plus baselineOnlyToolNames")
		}
	}
	return nil
}

func equalToolNameSets(left, right []string) bool {
	leftCopy := append([]string(nil), left...)
	rightCopy := append([]string(nil), right...)
	slices.Sort(leftCopy)
	slices.Sort(rightCopy)
	return slices.Equal(leftCopy, rightCopy)
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

// ToolSelectionProviderAccounting 是独立于两臂指标的真实成本记账：
//   - ModelGenerateAttempts：每次调用 ChatModel.Generate（校准请求 + 两臂
//     请求，无论成功失败）恰好 +1；它不声称 HTTP 已到达 Provider；
//   - UsageReportedAttempts：实际返回 Usage 的尝试 +1，并把该次 Usage 的
//     累计值累加进 PromptTokens/CompletionTokens/TotalTokens/CachedTokens/
//     ReasoningTokens；
//   - UsageMissingAttempts：没有返回 Usage 的尝试 +1（失败请求或缺失
//     Usage），不估算 Token、不估算价格。
//
// 校准请求计入本块，但绝不混入 wide/production 的准确率、Token 对比与
// paired reduction。记账只随成功生成的 Summary 落盘，由
// EvaluateToolSelection 从观测与调用方显式传入的校准 Usage 列表推导，
// 无全局状态，天然并发安全；fatal 运行不会生成 Summary，由预运行预算上界
// 约束最坏成本。
type ToolSelectionProviderAccounting struct {
	ModelGenerateAttempts int `json:"modelGenerateAttempts"`
	UsageReportedAttempts int `json:"usageReportedAttempts"`
	UsageMissingAttempts  int `json:"usageMissingAttempts"`
	PromptTokens          int `json:"promptTokens"`
	CompletionTokens      int `json:"completionTokens"`
	TotalTokens           int `json:"totalTokens"`
	CachedTokens          int `json:"cachedTokens"`
	ReasoningTokens       int `json:"reasoningTokens"`
}

type ToolSelectionSummary struct {
	DatasetVersion                 string                          `json:"datasetVersion"`
	Cases                          int                             `json:"cases"`
	Runs                           int                             `json:"runs"`
	PairedCases                    int                             `json:"pairedCases"`
	UnpairedRuns                   int                             `json:"unpairedRuns"`
	Wide                           ToolSelectionVariantSummary     `json:"wide"`
	Production                     ToolSelectionVariantSummary     `json:"production"`
	PairedPromptTokenReduction     float64                         `json:"pairedPromptTokenReduction"`
	PairedToolSchemaTokenReduction float64                         `json:"pairedToolSchemaTokenReduction"`
	PairedSchemaByteReduction      float64                         `json:"pairedSchemaByteReduction"`
	FailureTypes                   map[string]int                  `json:"failureTypes,omitempty"`
	ProviderAccounting             ToolSelectionProviderAccounting `json:"providerAccounting"`
}

// EvaluateToolSelection 汇总两臂指标与配对归约。可选的第三个参数是每次
// 基础无 Tool 校准请求实际返回的 Usage（每个 Case 恰好一个元素，零值表示
// 该校准尝试未返回 Usage）；它只进入 ProviderAccounting，绝不进入两臂
// 指标、Token 对比或 paired reduction。不传该参数时记账只覆盖两臂观测
// （历史 model_error 等失败观测仍按 usageMissing 计入）。
func EvaluateToolSelection(
	cases []ToolSelectionCase,
	observations []ToolSelectionObservation,
	calibrationUsage ...[]ModelUsage,
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
	summary.Production = summarizeToolSelection(cases, observations, ToolSelectionProduction)
	var wideTokens, productionTokens, wideSchemaTokens, productionSchemaTokens, wideBytes, productionBytes int64
	for _, pair := range pairs {
		wide, wideOK := pair[ToolSelectionWide]
		production, productionOK := pair[ToolSelectionProduction]
		if !wideOK || !productionOK {
			if wideOK {
				summary.UnpairedRuns++
			}
			if productionOK {
				summary.UnpairedRuns++
			}
			continue
		}
		if !hasProviderPromptUsage(wide) || !hasProviderPromptUsage(production) ||
			wide.ModelProvider != production.ModelProvider || wide.ModelID != production.ModelID ||
			wide.ReasoningEffort != production.ReasoningEffort || wide.PromptVersion != production.PromptVersion ||
			wide.MaxOutputTokens != production.MaxOutputTokens ||
			!sameObservationIdentity(wide, production) {
			summary.UnpairedRuns += 2
			continue
		}
		summary.PairedCases++
		wideTokens += int64(wide.Usage.PromptTokens)
		productionTokens += int64(production.Usage.PromptTokens)
		wideSchemaTokens += int64(wide.ToolSchemaPromptTokens)
		productionSchemaTokens += int64(production.ToolSchemaPromptTokens)
		wideBytes += int64(wide.ToolSchemaBytes)
		productionBytes += int64(production.ToolSchemaBytes)
	}
	if wideTokens > 0 {
		summary.PairedPromptTokenReduction = reductionRate(wideTokens, productionTokens)
	}
	if wideSchemaTokens > 0 {
		summary.PairedToolSchemaTokenReduction = reductionRate(wideSchemaTokens, productionSchemaTokens)
	}
	if wideBytes > 0 {
		summary.PairedSchemaByteReduction = reductionRate(wideBytes, productionBytes)
	}
	summary.ProviderAccounting = providerAccounting(observations, calibrationUsage)
	if len(summary.FailureTypes) == 0 {
		summary.FailureTypes = nil
	}
	return summary, nil
}

// providerAccounting 从两臂观测与调用方显式传入的校准 Usage 推导成本记账。
// 每条观测/每个校准元素恰好对应一次 Generate 调用：返回了有效 Usage 的调用计入
// usageReportedAttempts 并累加累计值，其余计入 usageMissingAttempts（不
// 估算 Token）。纯函数，无全局状态。
func providerAccounting(
	observations []ToolSelectionObservation,
	calibrationUsage [][]ModelUsage,
) ToolSelectionProviderAccounting {
	var accounting ToolSelectionProviderAccounting
	for _, usage := range calibrationUsage {
		accounting.ModelGenerateAttempts += len(usage)
		for _, attempt := range usage {
			accumulateProviderUsage(&accounting, attempt)
		}
	}
	for _, observation := range observations {
		accounting.ModelGenerateAttempts++
		accumulateProviderUsage(&accounting, observation.Usage)
	}
	return accounting
}

func accumulateProviderUsage(accounting *ToolSelectionProviderAccounting, usage ModelUsage) {
	if usage.ModelCalls == 1 && usage.PromptTokens > 0 && usage.TotalTokens > 0 {
		accounting.UsageReportedAttempts++
		accounting.PromptTokens += usage.PromptTokens
		accounting.CompletionTokens += usage.CompletionTokens
		accounting.TotalTokens += usage.TotalTokens
		accounting.CachedTokens += usage.CachedTokens
		accounting.ReasoningTokens += usage.ReasoningTokens
		return
	}
	accounting.UsageMissingAttempts++
}

// sameObservationIdentity 要求两个实验臂属于同一观测合同与同一实现身份：
// observationSchemaVersion、模型 Profile 指纹、实现 revision、dirty 状态
// 与 comparison 身份（comparisonFingerprint/sharedToolNames/
// baselineOnlyToolNames）必须一致，且两边都不能是 dirty。dirty 观测只允许
// 生成单臂统计供本地 smoke，不得计入正式 paired reduction，也不得贡献任何
// paired 归约指标。ToolProfileID 刻意不参与比较：wide/production 本来就是
// 不同评测合同，各自的 v3 Validate 已分别校验其合同 ID。
func sameObservationIdentity(wide, production ToolSelectionObservation) bool {
	if wide.ObservationSchemaVersion != production.ObservationSchemaVersion ||
		wide.ModelProfileFingerprint != production.ModelProfileFingerprint ||
		wide.ImplementationRevision != production.ImplementationRevision ||
		wide.ImplementationDirty != production.ImplementationDirty ||
		wide.ComparisonFingerprint != production.ComparisonFingerprint ||
		!slices.Equal(wide.SharedToolNames, production.SharedToolNames) ||
		!slices.Equal(wide.BaselineOnlyToolNames, production.BaselineOnlyToolNames) {
		return false
	}
	return !wide.ImplementationDirty && !production.ImplementationDirty
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
