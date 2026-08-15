package agent

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/contextgovernance"
)

type EvaluationVariant string

const (
	EvaluationBaseline   EvaluationVariant = "baseline"
	EvaluationExperiment EvaluationVariant = "experiment"

	// EvaluationObservationV3 是通用 Agent 评测的 v3 数据合同：显式
	// observationSchemaVersion 加上完整身份（Tool Profile 合同
	// toolProfileId/toolSchemaFingerprint、模型 Profile 指纹、实现
	// revision/dirty）。active evaluator 不保留 v1/v2 兼容分支：历史资产
	// 只能标记 historical，不得进入正式归约。
	EvaluationObservationV3 = "evaluation-observation-v3"
)

func (v EvaluationVariant) Valid() bool {
	return v == EvaluationBaseline || v == EvaluationExperiment
}

// EvaluationCase 是版本化评测集中的人工标注，不包含任何一次运行的实际结果。
// TaskType 只保留 "diagnosis" 单值以兼容既有数据集格式：统一 Runtime v2
// 的评测只针对 Diagnosis 入口，不再按旧 TaskType 过滤 Schema。
type EvaluationCase struct {
	DatasetVersion               string             `json:"datasetVersion"`
	CaseID                       string             `json:"caseId"`
	TaskType                     string             `json:"taskType"`
	UserQuery                    string             `json:"userQuery"`
	ExpectedSkill                SkillID            `json:"expectedSkill"`
	ExpectedFirstTool            string             `json:"expectedFirstTool,omitempty"`
	ExpectedTools                []string           `json:"expectedTools,omitempty"`
	ForbiddenTools               []string           `json:"forbiddenTools,omitempty"`
	RequiredEvidence             []string           `json:"requiredEvidence,omitempty"`
	RequiredLimitations          []string           `json:"requiredLimitations,omitempty"`
	ExpectedRootCause            string             `json:"expectedRootCause,omitempty"`
	AcceptableConclusionStatuses []ConclusionStatus `json:"acceptableConclusionStatuses"`
	Tags                         []string           `json:"tags,omitempty"`
}

func (c EvaluationCase) Validate() error {
	if strings.TrimSpace(c.DatasetVersion) == "" || strings.TrimSpace(c.CaseID) == "" {
		return errors.New("datasetVersion and caseId are required")
	}
	if c.TaskType != "diagnosis" || strings.TrimSpace(c.UserQuery) == "" {
		return errors.New("taskType and userQuery are required")
	}
	if !skillIDPattern.MatchString(string(c.ExpectedSkill)) {
		return fmt.Errorf("invalid expectedSkill %q", c.ExpectedSkill)
	}
	if c.ExpectedFirstTool != "" && !toolNamePattern.MatchString(c.ExpectedFirstTool) {
		return fmt.Errorf("invalid expectedFirstTool %q", c.ExpectedFirstTool)
	}
	for _, name := range append(append([]string(nil), c.ExpectedTools...), c.ForbiddenTools...) {
		if !toolNamePattern.MatchString(name) {
			return fmt.Errorf("invalid Tool name %q", name)
		}
	}
	if hasDuplicate(c.ExpectedTools) || hasDuplicate(c.ForbiddenTools) ||
		hasDuplicate(c.RequiredEvidence) || hasDuplicate(c.RequiredLimitations) || hasDuplicate(c.Tags) {
		return errors.New("evaluation case contains duplicate values")
	}
	for _, expected := range c.ExpectedTools {
		if slices.Contains(c.ForbiddenTools, expected) {
			return fmt.Errorf("tool %q cannot be both expected and forbidden", expected)
		}
	}
	if len(c.AcceptableConclusionStatuses) == 0 || hasDuplicate(c.AcceptableConclusionStatuses) {
		return errors.New("acceptableConclusionStatuses must contain unique values")
	}
	for _, status := range c.AcceptableConclusionStatuses {
		if !status.Valid() {
			return fmt.Errorf("invalid conclusion status %q", status)
		}
	}
	return nil
}

// EvaluationObservation 是 baseline 或 experiment 的一次真实运行记录。
// Usage 必须来自供应商响应，不能用字符数或静态 Schema 字节数代替。
// v3 身份字段：ObservationSchemaVersion 必须等于 EvaluationObservationV3
// （无 v1/v2 兼容分支）；ToolProfileID/ToolSchemaFingerprint 是实验臂特有合同
// （baseline=evaluation-wide-v2，experiment=diagnosis-default），同一 variant
// 跨样本必须一致，两臂之间按合同允许不同；comparisonFingerprint 与两组
// Tool 名单描述整组 wide/production 对照合同，必须在两臂观测中一致。
type EvaluationObservation struct {
	DatasetVersion           string            `json:"datasetVersion"`
	CaseID                   string            `json:"caseId"`
	Variant                  EvaluationVariant `json:"variant"`
	RunID                    string            `json:"runId"`
	ObservationSchemaVersion string            `json:"observationSchemaVersion,omitempty"`
	Model                    string            `json:"model"`
	ModelVersion             string            `json:"modelVersion"`
	ReasoningEffort          string            `json:"reasoningEffort"`
	PromptVersion            string            `json:"promptVersion"`
	ToolProfileID            string            `json:"toolProfileId,omitempty"`
	ToolSchemaFingerprint    string            `json:"toolSchemaFingerprint,omitempty"`
	ModelProfileFingerprint  string            `json:"modelProfileFingerprint,omitempty"`
	ImplementationRevision   string            `json:"implementationRevision,omitempty"`
	ImplementationDirty      bool              `json:"implementationDirty,omitempty"`
	ComparisonFingerprint    string            `json:"comparisonFingerprint,omitempty"`
	SharedToolNames          []string          `json:"sharedToolNames,omitempty"`
	BaselineOnlyToolNames    []string          `json:"baselineOnlyToolNames,omitempty"`
	SelectedSkill            SkillID           `json:"selectedSkill"`
	ActualToolCalls          []string          `json:"actualToolCalls"`
	AllowedTools             []string          `json:"allowedTools"`
	Evidence                 []string          `json:"evidence,omitempty"`
	Limitations              []string          `json:"limitations,omitempty"`
	ConclusionStatus         ConclusionStatus  `json:"conclusionStatus"`
	RootCauseMatched         bool              `json:"rootCauseMatched"`
	HumanReviewed            bool              `json:"humanReviewed"`
	Partial                  bool              `json:"partial"`
	Usage                    ModelUsage        `json:"usage"`
	TTFTMillis               int64             `json:"ttftMillis"`
	DurationMillis           int64             `json:"durationMillis"`
	ErrorType                string            `json:"errorType,omitempty"`
}

func (o EvaluationObservation) Validate() error {
	if strings.TrimSpace(o.DatasetVersion) == "" || strings.TrimSpace(o.CaseID) == "" ||
		strings.TrimSpace(o.RunID) == "" {
		return errors.New("datasetVersion, caseId, and runId are required")
	}
	if !o.Variant.Valid() {
		return fmt.Errorf("invalid evaluation variant %q", o.Variant)
	}
	// 数据合同：v3 是唯一支持的合同，不保留 v1/v2 兼容分支。历史资产
	// （没有 observationSchemaVersion 或 v2 版本）只能标记 historical，不得
	// 进入正式归约。
	if o.ObservationSchemaVersion != EvaluationObservationV3 {
		return fmt.Errorf(
			"unsupported observationSchemaVersion %q: the active evaluator only accepts %q; historical v1/v2 assets must be marked historical and excluded from formal reduction",
			o.ObservationSchemaVersion, EvaluationObservationV3,
		)
	}
	if err := o.validateV3Identity(); err != nil {
		return err
	}
	if strings.TrimSpace(o.Model) == "" || strings.TrimSpace(o.ModelVersion) == "" ||
		strings.TrimSpace(o.ReasoningEffort) == "" || strings.TrimSpace(o.PromptVersion) == "" {
		return errors.New("model, modelVersion, reasoningEffort, and promptVersion are required")
	}
	if !skillIDPattern.MatchString(string(o.SelectedSkill)) {
		return fmt.Errorf("invalid selectedSkill %q", o.SelectedSkill)
	}
	if len(o.AllowedTools) == 0 {
		return errors.New("allowedTools is required")
	}
	for _, name := range append(append([]string(nil), o.ActualToolCalls...), o.AllowedTools...) {
		if !toolNamePattern.MatchString(name) && name != ToolSkill {
			return fmt.Errorf("invalid Tool name %q", name)
		}
	}
	if !o.ConclusionStatus.Valid() {
		return fmt.Errorf("invalid conclusionStatus %q", o.ConclusionStatus)
	}
	if o.RootCauseMatched && !o.HumanReviewed {
		return errors.New("rootCauseMatched requires humanReviewed")
	}
	if o.Usage.ModelCalls < 0 || o.Usage.PromptTokens < 0 || o.Usage.CompletionTokens < 0 ||
		o.Usage.TotalTokens < 0 || o.Usage.CachedTokens < 0 || o.Usage.ReasoningTokens < 0 ||
		o.TTFTMillis < 0 || o.DurationMillis < 0 {
		return errors.New("usage and duration values cannot be negative")
	}
	return nil
}

// validateV3Identity 强制执行 v3 身份合同。ToolProfileID 是实验臂特有合同：
// baseline 固定 evaluation-wide-v2（评测 wide 合同，不是生产 Runtime
// Profile），experiment 固定生产 diagnosis-default；两臂不得互相伪装。
// ToolSchemaFingerprint/ModelProfileFingerprint 必须是合法 SHA-256；
// implementationRevision 非空，unknown 且 clean 拒绝（unknown 且 dirty 仅限
// 本地 smoke）。
func (o EvaluationObservation) validateV3Identity() error {
	switch o.Variant {
	case EvaluationBaseline:
		if o.ToolProfileID != string(agentruntime.ToolProfileEvaluationWide) {
			return fmt.Errorf("v3 baseline toolProfileId = %q, want %q", o.ToolProfileID, agentruntime.ToolProfileEvaluationWide)
		}
	case EvaluationExperiment:
		if o.ToolProfileID != string(agentruntime.ToolProfileDiagnosis) {
			return fmt.Errorf("v3 experiment toolProfileId = %q, want %q", o.ToolProfileID, agentruntime.ToolProfileDiagnosis)
		}
	default:
		return fmt.Errorf("invalid evaluation variant %q", o.Variant)
	}
	if !contextgovernance.IsSHA256Hex(o.ToolSchemaFingerprint) {
		return errors.New("v3 observation requires a valid SHA-256 toolSchemaFingerprint")
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
	if err := validateToolComparisonIdentity(
		o.ComparisonFingerprint, o.SharedToolNames, o.BaselineOnlyToolNames,
	); err != nil {
		return fmt.Errorf("v3 observation comparison identity: %w", err)
	}
	return nil
}

func validateToolComparisonIdentity(fingerprint string, sharedToolNames, baselineOnlyToolNames []string) error {
	if !strings.HasPrefix(fingerprint, "sha256:") ||
		!contextgovernance.IsSHA256Hex(strings.TrimPrefix(fingerprint, "sha256:")) {
		return errors.New("requires a valid SHA-256 comparisonFingerprint")
	}
	if len(sharedToolNames) == 0 {
		return errors.New("requires non-empty sharedToolNames")
	}
	if len(baselineOnlyToolNames) == 0 {
		return errors.New("requires non-empty baselineOnlyToolNames (the wide arm is a strict superset)")
	}
	for _, name := range append(append([]string(nil), sharedToolNames...), baselineOnlyToolNames...) {
		if !toolNamePattern.MatchString(name) {
			return fmt.Errorf("invalid comparison Tool name %q", name)
		}
	}
	if hasDuplicate(sharedToolNames) || hasDuplicate(baselineOnlyToolNames) {
		return errors.New("comparison Tool names must be unique")
	}
	for _, shared := range sharedToolNames {
		if slices.Contains(baselineOnlyToolNames, shared) {
			return fmt.Errorf("baseline-only Tool %q overlaps sharedToolNames", shared)
		}
	}
	if !slices.IsSorted(sharedToolNames) || !slices.IsSorted(baselineOnlyToolNames) {
		return errors.New("comparison Tool names must use canonical sorted order")
	}
	return nil
}

type EvaluationVariantSummary struct {
	Runs                   int     `json:"runs"`
	SkillRoutingAccuracy   float64 `json:"skillRoutingAccuracy"`
	FirstToolAccuracy      float64 `json:"firstToolAccuracy"`
	ExpectedToolRecall     float64 `json:"expectedToolRecall"`
	EvidenceCoverage       float64 `json:"evidenceCoverage"`
	TaskCompletionRate     float64 `json:"taskCompletionRate"`
	OutOfWhitelistCallRate float64 `json:"outOfWhitelistCallRate"`
	ForbiddenToolCallRate  float64 `json:"forbiddenToolCallRate"`
	PromptTokens           int     `json:"promptTokens"`
	CompletionTokens       int     `json:"completionTokens"`
	TotalTokens            int     `json:"totalTokens"`
	AverageTTFTMillis      float64 `json:"averageTtftMillis"`
	AverageDurationMillis  float64 `json:"averageDurationMillis"`
	FailedRuns             int     `json:"failedRuns"`
}

type EvaluationSummary struct {
	DatasetVersion            string                   `json:"datasetVersion"`
	Cases                     int                      `json:"cases"`
	Runs                      int                      `json:"runs"`
	PairedCases               int                      `json:"pairedCases"`
	UnpairedRuns              int                      `json:"unpairedRuns"`
	Baseline                  EvaluationVariantSummary `json:"baseline"`
	Experiment                EvaluationVariantSummary `json:"experiment"`
	PairedInputTokenReduction float64                  `json:"pairedInputTokenReduction"`
	PairedTTFTReduction       float64                  `json:"pairedTtftReduction"`
	PairedDurationReduction   float64                  `json:"pairedDurationReduction"`
	FailureTypes              map[string]int           `json:"failureTypes,omitempty"`
}

type scoredObservation struct {
	observation       EvaluationObservation
	skillCorrect      bool
	firstToolCorrect  bool
	firstToolExpected bool
	expectedToolHits  int
	expectedToolTotal int
	evidenceHits      int
	evidenceTotal     int
	completed         bool
	outOfWhitelist    int
	forbiddenCalls    int
	totalCalls        int
}

func EvaluateDataset(cases []EvaluationCase, observations []EvaluationObservation) (EvaluationSummary, error) {
	caseByID, version, err := indexEvaluationCases(cases)
	if err != nil {
		return EvaluationSummary{}, err
	}
	summary := EvaluationSummary{
		DatasetVersion: version, Cases: len(cases), Runs: len(observations),
		FailureTypes: make(map[string]int),
	}
	scores := make([]scoredObservation, 0, len(observations))
	seenRuns := make(map[string]struct{}, len(observations))
	pairs := make(map[string]map[EvaluationVariant]EvaluationObservation)
	for index, observation := range observations {
		if err := observation.Validate(); err != nil {
			return EvaluationSummary{}, fmt.Errorf("observation %d: %w", index, err)
		}
		if observation.DatasetVersion != version {
			return EvaluationSummary{}, fmt.Errorf(
				"observation %q uses dataset version %q, expected %q",
				observation.RunID, observation.DatasetVersion, version,
			)
		}
		caseDefinition, ok := caseByID[observation.CaseID]
		if !ok {
			return EvaluationSummary{}, fmt.Errorf("observation %q references unknown case %q", observation.RunID, observation.CaseID)
		}
		if _, duplicate := seenRuns[observation.RunID]; duplicate {
			return EvaluationSummary{}, fmt.Errorf("duplicate runId %q", observation.RunID)
		}
		seenRuns[observation.RunID] = struct{}{}
		if pairs[observation.CaseID] == nil {
			pairs[observation.CaseID] = make(map[EvaluationVariant]EvaluationObservation, 2)
		}
		if _, duplicate := pairs[observation.CaseID][observation.Variant]; duplicate {
			return EvaluationSummary{}, fmt.Errorf("case %q contains duplicate %s observations", observation.CaseID, observation.Variant)
		}
		pairs[observation.CaseID][observation.Variant] = observation
		score := scoreObservation(caseDefinition, observation)
		scores = append(scores, score)
		if observation.ErrorType != "" {
			summary.FailureTypes[observation.ErrorType]++
		}
	}
	if err := checkVariantToolProfileConsistency(observations); err != nil {
		return EvaluationSummary{}, err
	}
	summary.Baseline = summarizeVariant(scores, EvaluationBaseline)
	summary.Experiment = summarizeVariant(scores, EvaluationExperiment)
	calculatePairedMetrics(&summary, pairs)
	if len(summary.FailureTypes) == 0 {
		summary.FailureTypes = nil
	}
	return summary, nil
}

// checkVariantToolProfileConsistency 对同一 variant 跨样本执行 fail-closed
// 一致性检查：同一实验臂的所有样本必须保持同一个 ToolProfileID 与
// toolSchemaFingerprint（启动 Epoch 的固定装配合同）。ToolProfileID/Schema
// 指纹是实验臂特有合同，两臂之间允许（且预期）不同；comparison identity
// 则描述整组 wide/production 对照合同，所有样本和两臂必须完全一致。
func checkVariantToolProfileConsistency(observations []EvaluationObservation) error {
	type contract struct {
		profileID         string
		schemaFingerprint string
	}
	byVariant := make(map[EvaluationVariant]contract, 2)
	var comparison *ToolSelectionComparability
	for _, observation := range observations {
		currentComparison := ToolSelectionComparability{
			ComparisonFingerprint: observation.ComparisonFingerprint,
			SharedToolNames:       append([]string(nil), observation.SharedToolNames...),
			BaselineOnlyToolNames: append([]string(nil), observation.BaselineOnlyToolNames...),
		}
		if comparison == nil {
			comparison = &currentComparison
		} else if comparison.ComparisonFingerprint != currentComparison.ComparisonFingerprint ||
			!slices.Equal(comparison.SharedToolNames, currentComparison.SharedToolNames) ||
			!slices.Equal(comparison.BaselineOnlyToolNames, currentComparison.BaselineOnlyToolNames) {
			return fmt.Errorf("evaluation dataset mixes Tool comparison contracts: all variants and samples must keep one comparisonFingerprint/sharedToolNames/baselineOnlyToolNames identity")
		}
		previous, exists := byVariant[observation.Variant]
		if !exists {
			byVariant[observation.Variant] = contract{
				profileID: observation.ToolProfileID, schemaFingerprint: observation.ToolSchemaFingerprint,
			}
			continue
		}
		if previous.profileID != observation.ToolProfileID ||
			previous.schemaFingerprint != observation.ToolSchemaFingerprint {
			return fmt.Errorf(
				"variant %s mixes Tool Profile contracts (toolProfileId=%q toolSchemaFingerprint=%q vs %q/%q): same variant across samples must keep one Profile ID and Schema fingerprint",
				observation.Variant, previous.profileID, previous.schemaFingerprint,
				observation.ToolProfileID, observation.ToolSchemaFingerprint,
			)
		}
	}
	return nil
}

func indexEvaluationCases(cases []EvaluationCase) (map[string]EvaluationCase, string, error) {
	if len(cases) == 0 {
		return nil, "", errors.New("evaluation dataset contains no cases")
	}
	result := make(map[string]EvaluationCase, len(cases))
	version := ""
	for index, current := range cases {
		if err := current.Validate(); err != nil {
			return nil, "", fmt.Errorf("case %d: %w", index, err)
		}
		if version == "" {
			version = current.DatasetVersion
		} else if current.DatasetVersion != version {
			return nil, "", fmt.Errorf("dataset mixes versions %q and %q", version, current.DatasetVersion)
		}
		if _, duplicate := result[current.CaseID]; duplicate {
			return nil, "", fmt.Errorf("duplicate caseId %q", current.CaseID)
		}
		result[current.CaseID] = current
	}
	return result, version, nil
}

func scoreObservation(definition EvaluationCase, observation EvaluationObservation) scoredObservation {
	score := scoredObservation{
		observation:       observation,
		skillCorrect:      observation.SelectedSkill == definition.ExpectedSkill,
		firstToolExpected: definition.ExpectedFirstTool != "",
		expectedToolTotal: len(definition.ExpectedTools),
		evidenceTotal:     len(definition.RequiredEvidence),
		totalCalls:        len(observation.ActualToolCalls),
	}
	if score.firstToolExpected && len(observation.ActualToolCalls) > 0 {
		score.firstToolCorrect = observation.ActualToolCalls[0] == definition.ExpectedFirstTool
	}
	for _, expected := range definition.ExpectedTools {
		if slices.Contains(observation.ActualToolCalls, expected) {
			score.expectedToolHits++
		}
	}
	for _, required := range definition.RequiredEvidence {
		if slices.Contains(observation.Evidence, required) {
			score.evidenceHits++
		}
	}
	for _, actual := range observation.ActualToolCalls {
		if !slices.Contains(observation.AllowedTools, actual) {
			score.outOfWhitelist++
		}
		if slices.Contains(definition.ForbiddenTools, actual) {
			score.forbiddenCalls++
		}
	}
	rootCauseOK := definition.ExpectedRootCause == "" ||
		(observation.HumanReviewed && observation.RootCauseMatched)
	limitationsOK := containsAll(observation.Limitations, definition.RequiredLimitations)
	conclusionOK := slices.Contains(definition.AcceptableConclusionStatuses, observation.ConclusionStatus)
	score.completed = observation.ErrorType == "" && !observation.Partial && score.skillCorrect &&
		(!score.firstToolExpected || score.firstToolCorrect) &&
		score.expectedToolHits == score.expectedToolTotal && score.forbiddenCalls == 0 &&
		score.evidenceHits == score.evidenceTotal && limitationsOK && conclusionOK && rootCauseOK
	return score
}

func summarizeVariant(scores []scoredObservation, variant EvaluationVariant) EvaluationVariantSummary {
	var summary EvaluationVariantSummary
	var skillCorrect, firstToolCorrect, firstToolTotal, completed int
	var expectedHits, expectedTotal, evidenceHits, evidenceTotal int
	var outOfWhitelist, forbiddenCalls, totalCalls int
	var totalTTFT, totalDuration int64
	for _, score := range scores {
		if score.observation.Variant != variant {
			continue
		}
		summary.Runs++
		if score.skillCorrect {
			skillCorrect++
		}
		if score.firstToolExpected {
			firstToolTotal++
			if score.firstToolCorrect {
				firstToolCorrect++
			}
		}
		if score.completed {
			completed++
		}
		expectedHits += score.expectedToolHits
		expectedTotal += score.expectedToolTotal
		evidenceHits += score.evidenceHits
		evidenceTotal += score.evidenceTotal
		outOfWhitelist += score.outOfWhitelist
		forbiddenCalls += score.forbiddenCalls
		totalCalls += score.totalCalls
		summary.PromptTokens += score.observation.Usage.PromptTokens
		summary.CompletionTokens += score.observation.Usage.CompletionTokens
		summary.TotalTokens += score.observation.Usage.TotalTokens
		totalTTFT += score.observation.TTFTMillis
		totalDuration += score.observation.DurationMillis
		if score.observation.ErrorType != "" {
			summary.FailedRuns++
		}
	}
	if summary.Runs == 0 {
		return summary
	}
	summary.SkillRoutingAccuracy = float64(skillCorrect) / float64(summary.Runs)
	summary.TaskCompletionRate = float64(completed) / float64(summary.Runs)
	summary.AverageTTFTMillis = float64(totalTTFT) / float64(summary.Runs)
	summary.AverageDurationMillis = float64(totalDuration) / float64(summary.Runs)
	if firstToolTotal > 0 {
		summary.FirstToolAccuracy = float64(firstToolCorrect) / float64(firstToolTotal)
	}
	if expectedTotal > 0 {
		summary.ExpectedToolRecall = float64(expectedHits) / float64(expectedTotal)
	}
	if evidenceTotal > 0 {
		summary.EvidenceCoverage = float64(evidenceHits) / float64(evidenceTotal)
	}
	if totalCalls > 0 {
		summary.OutOfWhitelistCallRate = float64(outOfWhitelist) / float64(totalCalls)
		summary.ForbiddenToolCallRate = float64(forbiddenCalls) / float64(totalCalls)
	}
	return summary
}

func calculatePairedMetrics(
	summary *EvaluationSummary,
	pairs map[string]map[EvaluationVariant]EvaluationObservation,
) {
	var baselineInput, experimentInput int
	var baselineTTFT, experimentTTFT int64
	var baselineDuration, experimentDuration int64
	for _, pair := range pairs {
		baseline, baselineOK := pair[EvaluationBaseline]
		experiment, experimentOK := pair[EvaluationExperiment]
		if !baselineOK || !experimentOK {
			if baselineOK {
				summary.UnpairedRuns++
			}
			if experimentOK {
				summary.UnpairedRuns++
			}
			continue
		}
		// 配对只允许相同运行身份：model/modelVersion/reasoningEffort/promptVersion/
		// modelProfileFingerprint/implementationRevision/comparison identity 必须
		// 一致，且两臂
		// implementationDirty 都必须为 false（dirty 观测只保留单臂统计供本地
		// smoke，不进入正式 paired 归约）。ToolProfileID 与 toolSchemaFingerprint
		// 刻意不参与比较：它们是实验臂特有合同，两臂按设计不同，臂内一致性由
		// checkVariantToolProfileConsistency 在 EvaluateDataset 层 fail-closed。
		if baseline.Model != experiment.Model || baseline.ModelVersion != experiment.ModelVersion ||
			baseline.ReasoningEffort != experiment.ReasoningEffort ||
			baseline.PromptVersion != experiment.PromptVersion ||
			baseline.ModelProfileFingerprint != experiment.ModelProfileFingerprint ||
			baseline.ImplementationRevision != experiment.ImplementationRevision ||
			baseline.ComparisonFingerprint != experiment.ComparisonFingerprint ||
			!slices.Equal(baseline.SharedToolNames, experiment.SharedToolNames) ||
			!slices.Equal(baseline.BaselineOnlyToolNames, experiment.BaselineOnlyToolNames) ||
			baseline.ImplementationDirty || experiment.ImplementationDirty {
			summary.UnpairedRuns += 2
			continue
		}
		summary.PairedCases++
		baselineInput += baseline.Usage.PromptTokens
		experimentInput += experiment.Usage.PromptTokens
		baselineTTFT += baseline.TTFTMillis
		experimentTTFT += experiment.TTFTMillis
		baselineDuration += baseline.DurationMillis
		experimentDuration += experiment.DurationMillis
	}
	if baselineInput > 0 {
		summary.PairedInputTokenReduction = reductionRate(int64(baselineInput), int64(experimentInput))
	}
	if baselineTTFT > 0 {
		summary.PairedTTFTReduction = reductionRate(baselineTTFT, experimentTTFT)
	}
	if baselineDuration > 0 {
		summary.PairedDurationReduction = reductionRate(baselineDuration, experimentDuration)
	}
}

func reductionRate(baseline, experiment int64) float64 {
	return float64(baseline-experiment) / float64(baseline)
}

func containsAll(actual, required []string) bool {
	for _, value := range required {
		if !slices.Contains(actual, value) {
			return false
		}
	}
	return true
}
