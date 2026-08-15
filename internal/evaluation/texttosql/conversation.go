package texttosql

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/contextgovernance"
)

var (
	evaluationIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	toolNamePattern     = regexp.MustCompile(`^[a-z][a-z0-9_]{1,127}$`)
)

// TextToSQLConversationObservationSchemaVersion 是 Text-to-SQL conversation
// 入口评测的独立 v2 数据合同。它和 direct 模式的历史 v1 合同
// （TextToSQLEvaluationObservation，无 observationSchemaVersion）是两个
// 完全独立的数据流：conversation v2 汇总明确拒绝任何非本合同的观测，
// 历史 direct v1 数据不得混入 conversation v2 汇总。
const TextToSQLConversationObservationSchemaVersion = "text-to-sql-conversation-observation-v2"

// TextToSQLConversationEntryMode 标识观测来自 conversation 生产入口
// （真实 Conversation Agent：自然语言输入 -> 模型自主
// search_schema_catalog -> execute_readonly_query -> 最终自然语言答案）。
const TextToSQLConversationEntryMode = "conversation"

// TextToSQLConversationToolCall 是模型在一次 conversation 评测回合中实际
// 发起的一次 Tool 调用。execute_readonly_query 调用必须携带生成 SQL 的
// 稳定 SHA-256 hash；失败调用记录稳定 errorType。
type TextToSQLConversationToolCall struct {
	ToolName  string `json:"toolName"`
	QueryHash string `json:"queryHash,omitempty"`
	Succeeded bool   `json:"succeeded"`
	ErrorType string `json:"errorType,omitempty"`
}

// TextToSQLConversationEvaluationObservation 记录一次自然语言 Text-to-SQL
// conversation 评测回合。字段覆盖 v2 身份合同（observationSchemaVersion、
// entryMode、model/profile fingerprint、implementationRevision/Dirty、
// toolProfileId、toolSchemaFingerprint）与实际执行事实（actualToolCalls、
// 生成 SQL hash、执行结果、答案、usage、duration、correct、errorType）。
type TextToSQLConversationEvaluationObservation struct {
	ObservationSchemaVersion string                          `json:"observationSchemaVersion"`
	EntryMode                string                          `json:"entryMode"`
	DatasetVersion           string                          `json:"datasetVersion"`
	CaseID                   string                          `json:"caseId"`
	RunID                    string                          `json:"runId"`
	ModelProvider            string                          `json:"modelProvider"`
	ModelID                  string                          `json:"modelId"`
	ReasoningEffort          string                          `json:"reasoningEffort"`
	PromptVersion            string                          `json:"promptVersion"`
	ModelProfileFingerprint  string                          `json:"modelProfileFingerprint"`
	ImplementationRevision   string                          `json:"implementationRevision"`
	ImplementationDirty      bool                            `json:"implementationDirty"`
	ToolProfileID            string                          `json:"toolProfileId"`
	ToolSchemaFingerprint    string                          `json:"toolSchemaFingerprint"`
	ActualToolCallCount      int                             `json:"actualToolCallCount"`
	ToolTraceComplete        bool                            `json:"toolTraceComplete"`
	ToolSequenceCorrect      bool                            `json:"toolSequenceCorrect"`
	ActualToolCalls          []TextToSQLConversationToolCall `json:"actualToolCalls"`
	Answer                   string                          `json:"answer,omitempty"`
	GeneratedQuery           string                          `json:"generatedQuery,omitempty"`
	QueryHash                string                          `json:"queryHash,omitempty"`
	Columns                  []string                        `json:"columns,omitempty"`
	Rows                     [][]any                         `json:"rows,omitempty"`
	Truncated                bool                            `json:"truncated"`
	ExecutionCorrect         bool                            `json:"executionCorrect"`
	AnswerCorrect            bool                            `json:"answerCorrect"`
	Correct                  bool                            `json:"correct"`
	ErrorType                string                          `json:"errorType,omitempty"`
	Usage                    mesagent.ModelUsage             `json:"usage"`
	DurationMillis           int64                           `json:"durationMillis"`
}

func (o TextToSQLConversationEvaluationObservation) Validate() error {
	if o.ObservationSchemaVersion != TextToSQLConversationObservationSchemaVersion {
		return fmt.Errorf(
			"unsupported observationSchemaVersion %q: the conversation evaluator only accepts %q; historical direct v1 Text-to-SQL observations must not be mixed into conversation v2 summaries",
			o.ObservationSchemaVersion, TextToSQLConversationObservationSchemaVersion,
		)
	}
	if o.EntryMode != TextToSQLConversationEntryMode {
		return fmt.Errorf("entryMode = %q, want %q", o.EntryMode, TextToSQLConversationEntryMode)
	}
	if !evaluationIDPattern.MatchString(o.DatasetVersion) || !evaluationIDPattern.MatchString(o.CaseID) ||
		strings.TrimSpace(o.RunID) == "" {
		return errors.New("datasetVersion, caseId, and runId are required")
	}
	if strings.TrimSpace(o.ModelProvider) == "" || strings.TrimSpace(o.ModelID) == "" ||
		strings.TrimSpace(o.ReasoningEffort) == "" || strings.TrimSpace(o.PromptVersion) == "" {
		return errors.New("model identity metadata is required")
	}
	if !contextgovernance.IsSHA256Hex(o.ModelProfileFingerprint) {
		return errors.New("conversation observation requires a valid SHA-256 modelProfileFingerprint")
	}
	if strings.TrimSpace(o.ImplementationRevision) == "" {
		return errors.New("conversation observation requires an implementationRevision")
	}
	if strings.EqualFold(strings.TrimSpace(o.ImplementationRevision), "unknown") && !o.ImplementationDirty {
		return errors.New("unknown implementationRevision requires implementationDirty=true")
	}
	if o.ToolProfileID != string(agentruntime.ToolProfileConversation) {
		return fmt.Errorf("conversation toolProfileId = %q, want %q", o.ToolProfileID, agentruntime.ToolProfileConversation)
	}
	if !contextgovernance.IsSHA256Hex(o.ToolSchemaFingerprint) {
		return errors.New("conversation observation requires a valid SHA-256 toolSchemaFingerprint")
	}
	if o.ActualToolCallCount < 0 || o.ActualToolCallCount > 32 || len(o.ActualToolCalls) > 32 {
		return errors.New("actualToolCalls exceeds the maximum of 32")
	}
	traceComplete, sequenceCorrect := TextToSQLConversationToolTraceMatchesRequiredSequence(
		o.ActualToolCallCount, o.ActualToolCalls,
	)
	if o.ToolTraceComplete != traceComplete || o.ToolSequenceCorrect != sequenceCorrect {
		return errors.New("Tool trace completeness or required sequence is inconsistent")
	}
	for _, call := range o.ActualToolCalls {
		if !toolNamePattern.MatchString(call.ToolName) {
			return fmt.Errorf("invalid Tool name %q", call.ToolName)
		}
		if call.ToolName == mesagent.ToolExecuteReadonlyQuery {
			digest := strings.TrimPrefix(call.QueryHash, "sha256:")
			malformedArguments := !call.Succeeded && call.ErrorType == "invalid_tool_arguments" && call.QueryHash == ""
			if !malformedArguments && (call.QueryHash == digest || !contextgovernance.IsSHA256Hex(digest)) {
				return errors.New("execute_readonly_query call requires a sha256 queryHash")
			}
		}
		if call.Succeeded && call.ErrorType != "" {
			return fmt.Errorf("succeeded Tool call %q cannot contain errorType", call.ToolName)
		}
	}
	if o.Correct && o.ErrorType != "" {
		return errors.New("correct observation cannot contain errorType")
	}
	if o.Correct != (o.ToolTraceComplete && o.ToolSequenceCorrect && o.ExecutionCorrect && o.AnswerCorrect && o.ErrorType == "") {
		return errors.New("correct must require a complete Tool trace, the required Tool sequence, executionCorrect, answerCorrect, and no errorType")
	}
	if o.AnswerCorrect && strings.TrimSpace(o.Answer) == "" {
		return errors.New("answerCorrect requires a non-empty answer")
	}
	if o.GeneratedQuery != "" && o.QueryHash == "" {
		return errors.New("generated query requires queryHash")
	}
	if o.Usage.ModelCalls < 0 || o.Usage.PromptTokens < 0 || o.Usage.CompletionTokens < 0 ||
		o.Usage.TotalTokens < 0 || o.Usage.CachedTokens < 0 || o.Usage.ReasoningTokens < 0 ||
		o.DurationMillis < 0 {
		return errors.New("usage and duration values cannot be negative")
	}
	if o.Usage.ModelCalls == 0 && o.ErrorType == "" {
		return errors.New("conversation observation with zero model calls requires errorType")
	}
	return nil
}

// TextToSQLConversationEvaluationSummary 是 conversation 入口评测的汇总。
// 它只汇总 TextToSQLConversationObservationSchemaVersion 观测；任何其他
// 合同（包括历史 direct v1）都会在归约时被拒绝，保证两套数据流永不混用。
type TextToSQLConversationEvaluationSummary struct {
	DatasetVersion           string              `json:"datasetVersion"`
	ObservationSchemaVersion string              `json:"observationSchemaVersion"`
	EntryMode                string              `json:"entryMode"`
	ModelProvider            string              `json:"modelProvider"`
	ModelID                  string              `json:"modelId"`
	ReasoningEffort          string              `json:"reasoningEffort"`
	PromptVersion            string              `json:"promptVersion"`
	ModelProfileFingerprint  string              `json:"modelProfileFingerprint"`
	ImplementationRevision   string              `json:"implementationRevision"`
	ToolProfileID            string              `json:"toolProfileId"`
	ToolSchemaFingerprint    string              `json:"toolSchemaFingerprint"`
	ImplementationDirty      bool                `json:"implementationDirty"`
	Formal                   bool                `json:"formal"`
	Cases                    int                 `json:"cases"`
	ToolSequenceCorrect      int                 `json:"toolSequenceCorrect"`
	ExecutionCorrect         int                 `json:"executionCorrect"`
	AnswerCorrect            int                 `json:"answerCorrect"`
	EndToEndCorrect          int                 `json:"endToEndCorrect"`
	ExecutionAccuracy        float64             `json:"executionAccuracy"`
	AnswerAccuracy           float64             `json:"answerAccuracy"`
	EndToEndAccuracy         float64             `json:"endToEndAccuracy"`
	ToolSequenceAccuracy     float64             `json:"toolSequenceAccuracy"`
	Usage                    mesagent.ModelUsage `json:"usage"`
	AverageDurationMillis    float64             `json:"averageDurationMillis"`
	FailureTypes             map[string]int      `json:"failureTypes,omitempty"`
}

// EvaluateTextToSQLConversation 归约 conversation 入口的 Text-to-SQL 评测。
// 与 direct 的 EvaluateTextToSQL 完全独立：任何非 conversation v2 观测
// （含历史 direct v1 数据）都会被显式拒绝，绝不混入本汇总。
func EvaluateTextToSQLConversation(
	cases []mesagent.TextToSQLEvaluationCase,
	observations []TextToSQLConversationEvaluationObservation,
) (TextToSQLConversationEvaluationSummary, error) {
	if len(cases) == 0 {
		return TextToSQLConversationEvaluationSummary{}, errors.New("Text-to-SQL conversation dataset contains no cases")
	}
	definitions := make(map[string]mesagent.TextToSQLEvaluationCase, len(cases))
	version := ""
	for index, current := range cases {
		if err := current.Validate(); err != nil {
			return TextToSQLConversationEvaluationSummary{}, fmt.Errorf("case %d: %w", index, err)
		}
		if version == "" {
			version = current.DatasetVersion
		} else if current.DatasetVersion != version {
			return TextToSQLConversationEvaluationSummary{}, errors.New("Text-to-SQL conversation dataset mixes versions")
		}
		if _, exists := definitions[current.CaseID]; exists {
			return TextToSQLConversationEvaluationSummary{}, fmt.Errorf("duplicate caseId %q", current.CaseID)
		}
		definitions[current.CaseID] = current
	}
	if len(observations) != len(cases) {
		return TextToSQLConversationEvaluationSummary{}, fmt.Errorf(
			"observation count %d does not match case count %d", len(observations), len(cases),
		)
	}
	summary := TextToSQLConversationEvaluationSummary{
		DatasetVersion:           version,
		ObservationSchemaVersion: TextToSQLConversationObservationSchemaVersion,
		EntryMode:                TextToSQLConversationEntryMode,
		Cases:                    len(cases),
		FailureTypes:             make(map[string]int),
	}
	seen := make(map[string]struct{}, len(observations))
	var totalDuration int64
	var formalIdentity textToSQLConversationFormalIdentity
	for index, observation := range observations {
		if err := observation.Validate(); err != nil {
			return TextToSQLConversationEvaluationSummary{}, fmt.Errorf("observation %d: %w", index, err)
		}
		// 显式防混入闸门：conversation 汇总只接受本 v2 合同；历史 direct v1
		// 数据（无 observationSchemaVersion）在这里被拒绝而不是被折算。
		if observation.ObservationSchemaVersion != TextToSQLConversationObservationSchemaVersion {
			return TextToSQLConversationEvaluationSummary{}, fmt.Errorf(
				"observation %d uses %q: historical direct v1 Text-to-SQL observations must not be mixed into conversation v2 summaries",
				index, observation.ObservationSchemaVersion,
			)
		}
		currentIdentity, identityErr := newTextToSQLConversationFormalIdentity(observation)
		if identityErr != nil {
			return TextToSQLConversationEvaluationSummary{}, fmt.Errorf("observation %d: %w", index, identityErr)
		}
		if index == 0 {
			formalIdentity = currentIdentity
			summary.ModelProvider = currentIdentity.modelProvider
			summary.ModelID = currentIdentity.modelID
			summary.ReasoningEffort = currentIdentity.reasoningEffort
			summary.PromptVersion = currentIdentity.promptVersion
			summary.ModelProfileFingerprint = currentIdentity.modelProfileFingerprint
			summary.ImplementationRevision = currentIdentity.implementationRevision
			summary.ToolProfileID = currentIdentity.toolProfileID
			summary.ToolSchemaFingerprint = currentIdentity.toolSchemaFingerprint
			summary.ImplementationDirty = currentIdentity.implementationDirty
			summary.Formal = !currentIdentity.implementationDirty &&
				!strings.EqualFold(strings.TrimSpace(currentIdentity.implementationRevision), "unknown")
		} else if currentIdentity != formalIdentity {
			return TextToSQLConversationEvaluationSummary{}, errors.New(
				"Text-to-SQL conversation observations mix model, prompt, implementation, or Tool Profile identities",
			)
		}
		definition, ok := definitions[observation.CaseID]
		if !ok || observation.DatasetVersion != version {
			return TextToSQLConversationEvaluationSummary{}, fmt.Errorf(
				"observation %q is outside dataset", observation.RunID,
			)
		}
		if _, exists := seen[observation.CaseID]; exists {
			return TextToSQLConversationEvaluationSummary{}, fmt.Errorf(
				"duplicate observation for case %q", observation.CaseID,
			)
		}
		seen[observation.CaseID] = struct{}{}
		executionMatched := mesagent.TextToSQLResultMatches(definition, observation.Columns, observation.Rows, observation.Truncated)
		answerMatched := TextToSQLAnswerMatchesExpectedValues(definition, observation.Answer)
		if observation.ExecutionCorrect != executionMatched || observation.AnswerCorrect != answerMatched ||
			observation.Correct != (observation.ToolTraceComplete && observation.ToolSequenceCorrect &&
				observation.ErrorType == "" && executionMatched && answerMatched) {
			return TextToSQLConversationEvaluationSummary{}, fmt.Errorf(
				"observation %q contains inconsistent correctness", observation.RunID,
			)
		}
		if observation.ToolSequenceCorrect {
			summary.ToolSequenceCorrect++
		}
		if observation.ExecutionCorrect {
			summary.ExecutionCorrect++
		}
		if observation.AnswerCorrect {
			summary.AnswerCorrect++
		}
		if observation.Correct {
			summary.EndToEndCorrect++
		}
		if observation.ErrorType != "" {
			summary.FailureTypes[observation.ErrorType]++
		}
		summary.Usage.Add(observation.Usage)
		totalDuration += observation.DurationMillis
	}
	summary.ExecutionAccuracy = float64(summary.ExecutionCorrect) / float64(summary.Cases)
	summary.AnswerAccuracy = float64(summary.AnswerCorrect) / float64(summary.Cases)
	summary.EndToEndAccuracy = float64(summary.EndToEndCorrect) / float64(summary.Cases)
	summary.ToolSequenceAccuracy = float64(summary.ToolSequenceCorrect) / float64(summary.Cases)
	summary.AverageDurationMillis = float64(totalDuration) / float64(summary.Cases)
	if len(summary.FailureTypes) == 0 {
		summary.FailureTypes = nil
	}
	return summary, nil
}

// TextToSQLConversationToolTraceMatchesRequiredSequence compares the Runner's
// authoritative total Tool-call count with the SQL recorder and then enforces
// the production-entry workflow expected by this evaluation: one or more
// schema searches followed by exactly one final readonly query. Repeated
// schema searches are valid Agent exploration; query-first, repeated query,
// search-after-query, and non-SQL calls are not.
func TextToSQLConversationToolTraceMatchesRequiredSequence(
	actualToolCallCount int,
	calls []TextToSQLConversationToolCall,
) (complete bool, sequenceCorrect bool) {
	complete = actualToolCallCount == len(calls)
	if !complete || len(calls) < 2 || calls[len(calls)-1].ToolName != mesagent.ToolExecuteReadonlyQuery {
		return complete, false
	}
	for _, call := range calls[:len(calls)-1] {
		if call.ToolName != mesagent.ToolSearchSchemaCatalog {
			return complete, false
		}
	}
	sequenceCorrect = true
	return complete, sequenceCorrect
}

// TextToSQLAnswerMatchesExpectedValues performs a deterministic answer check
// for the fixed Text-to-SQL dataset: every expected scalar returned by the
// database must also appear in the final natural-language answer. This is not
// an LLM judge; it deliberately measures grounded value preservation only.
func TextToSQLAnswerMatchesExpectedValues(definition mesagent.TextToSQLEvaluationCase, answer string) bool {
	normalized := strings.ToLower(strings.TrimSpace(answer))
	if normalized == "" {
		return false
	}
	for _, row := range definition.ExpectedRows {
		for _, value := range row {
			if value == nil {
				if !strings.Contains(normalized, "null") && !strings.Contains(normalized, "无") {
					return false
				}
				continue
			}
			expected := strings.ToLower(strings.TrimSpace(fmt.Sprint(value)))
			if expected == "" {
				continue
			}
			if _, isString := value.(string); isString {
				if !strings.Contains(normalized, expected) {
					return false
				}
				continue
			}
			if !containsDelimitedAnswerValue(normalized, expected) {
				return false
			}
		}
	}
	return true
}

func containsDelimitedAnswerValue(answer, expected string) bool {
	for offset := 0; offset <= len(answer)-len(expected); {
		index := strings.Index(answer[offset:], expected)
		if index < 0 {
			return false
		}
		index += offset
		beforeOK := index == 0 || answer[index-1] < '0' || answer[index-1] > '9'
		after := index + len(expected)
		afterOK := after == len(answer) || answer[after] < '0' || answer[after] > '9'
		if beforeOK && afterOK {
			return true
		}
		offset = index + len(expected)
	}
	return false
}

type textToSQLConversationFormalIdentity struct {
	modelProvider           string
	modelID                 string
	reasoningEffort         string
	promptVersion           string
	modelProfileFingerprint string
	implementationRevision  string
	implementationDirty     bool
	toolProfileID           string
	toolSchemaFingerprint   string
}

func newTextToSQLConversationFormalIdentity(
	observation TextToSQLConversationEvaluationObservation,
) (textToSQLConversationFormalIdentity, error) {
	return textToSQLConversationFormalIdentity{
		modelProvider:           observation.ModelProvider,
		modelID:                 observation.ModelID,
		reasoningEffort:         observation.ReasoningEffort,
		promptVersion:           observation.PromptVersion,
		modelProfileFingerprint: observation.ModelProfileFingerprint,
		implementationRevision:  observation.ImplementationRevision,
		implementationDirty:     observation.ImplementationDirty,
		toolProfileID:           observation.ToolProfileID,
		toolSchemaFingerprint:   observation.ToolSchemaFingerprint,
	}, nil
}
