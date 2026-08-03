package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var textToSQLEvaluationIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

type SQLResultOrder string

const (
	SQLResultOrdered   SQLResultOrder = "ordered"
	SQLResultUnordered SQLResultOrder = "unordered"
)

func (o SQLResultOrder) Valid() bool {
	return o == SQLResultOrdered || o == SQLResultUnordered
}

type TextToSQLEvaluationCase struct {
	DatasetVersion  string         `json:"datasetVersion"`
	CaseID          string         `json:"caseId"`
	UserQuery       string         `json:"userQuery"`
	ExpectedColumns []string       `json:"expectedColumns"`
	ExpectedRows    [][]any        `json:"expectedRows"`
	ResultOrder     SQLResultOrder `json:"resultOrder"`
}

func (c TextToSQLEvaluationCase) Validate() error {
	if !textToSQLEvaluationIDPattern.MatchString(c.DatasetVersion) || !textToSQLEvaluationIDPattern.MatchString(c.CaseID) {
		return errors.New("datasetVersion and caseId must be stable identifiers")
	}
	if strings.TrimSpace(c.UserQuery) == "" || len(c.UserQuery) > 4096 {
		return errors.New("userQuery must be non-empty and at most 4096 bytes")
	}
	if len(c.ExpectedColumns) == 0 || len(c.ExpectedColumns) > 32 || len(c.ExpectedRows) > 200 {
		return errors.New("expected result dimensions are invalid")
	}
	for _, column := range c.ExpectedColumns {
		if strings.TrimSpace(column) == "" {
			return errors.New("expected column names must be non-empty")
		}
	}
	for _, row := range c.ExpectedRows {
		if len(row) != len(c.ExpectedColumns) {
			return errors.New("expected row width does not match expected columns")
		}
	}
	if !c.ResultOrder.Valid() {
		return fmt.Errorf("invalid resultOrder %q", c.ResultOrder)
	}
	return nil
}

type TextToSQLEvaluationObservation struct {
	DatasetVersion  string     `json:"datasetVersion"`
	CaseID          string     `json:"caseId"`
	RunID           string     `json:"runId"`
	ModelProvider   string     `json:"modelProvider"`
	ModelID         string     `json:"modelId"`
	ReasoningEffort string     `json:"reasoningEffort"`
	PromptVersion   string     `json:"promptVersion"`
	SelectedTool    string     `json:"selectedTool,omitempty"`
	ToolCallCount   int        `json:"toolCallCount"`
	GeneratedQuery  string     `json:"generatedQuery,omitempty"`
	QueryHash       string     `json:"queryHash,omitempty"`
	Columns         []string   `json:"columns,omitempty"`
	Rows            [][]any    `json:"rows,omitempty"`
	Truncated       bool       `json:"truncated"`
	Correct         bool       `json:"correct"`
	ErrorType       string     `json:"errorType,omitempty"`
	Usage           ModelUsage `json:"usage"`
	DurationMillis  int64      `json:"durationMillis"`
}

func (o TextToSQLEvaluationObservation) Validate() error {
	if !textToSQLEvaluationIDPattern.MatchString(o.DatasetVersion) || !textToSQLEvaluationIDPattern.MatchString(o.CaseID) ||
		strings.TrimSpace(o.RunID) == "" || strings.TrimSpace(o.ModelProvider) == "" ||
		strings.TrimSpace(o.ModelID) == "" || strings.TrimSpace(o.ReasoningEffort) == "" ||
		strings.TrimSpace(o.PromptVersion) == "" {
		return errors.New("observation identity and model metadata are required")
	}
	if o.ToolCallCount < 0 || o.ToolCallCount > 8 {
		return errors.New("toolCallCount is invalid")
	}
	if o.SelectedTool != "" && !toolNamePattern.MatchString(o.SelectedTool) {
		return errors.New("selectedTool is invalid")
	}
	if o.GeneratedQuery != "" && o.QueryHash == "" {
		return errors.New("generated query requires queryHash")
	}
	if o.Correct && o.ErrorType != "" {
		return errors.New("correct observation cannot contain errorType")
	}
	if o.Usage.ModelCalls < 0 || o.Usage.ModelCalls > 1 || o.Usage.PromptTokens < 0 ||
		o.Usage.CompletionTokens < 0 || o.Usage.TotalTokens < 0 || o.DurationMillis < 0 {
		return errors.New("usage and duration must be non-negative")
	}
	return nil
}

type TextToSQLEvaluationSummary struct {
	DatasetVersion        string         `json:"datasetVersion"`
	Cases                 int            `json:"cases"`
	Correct               int            `json:"correct"`
	ExecutionAccuracy     float64        `json:"executionAccuracy"`
	Usage                 ModelUsage     `json:"usage"`
	AverageDurationMillis float64        `json:"averageDurationMillis"`
	FailureTypes          map[string]int `json:"failureTypes,omitempty"`
}

func EvaluateTextToSQL(
	cases []TextToSQLEvaluationCase,
	observations []TextToSQLEvaluationObservation,
) (TextToSQLEvaluationSummary, error) {
	if len(cases) == 0 {
		return TextToSQLEvaluationSummary{}, errors.New("Text-to-SQL dataset contains no cases")
	}
	definitions := make(map[string]TextToSQLEvaluationCase, len(cases))
	version := ""
	for index, current := range cases {
		if err := current.Validate(); err != nil {
			return TextToSQLEvaluationSummary{}, fmt.Errorf("case %d: %w", index, err)
		}
		if version == "" {
			version = current.DatasetVersion
		} else if current.DatasetVersion != version {
			return TextToSQLEvaluationSummary{}, errors.New("Text-to-SQL dataset mixes versions")
		}
		if _, exists := definitions[current.CaseID]; exists {
			return TextToSQLEvaluationSummary{}, fmt.Errorf("duplicate caseId %q", current.CaseID)
		}
		definitions[current.CaseID] = current
	}
	if len(observations) != len(cases) {
		return TextToSQLEvaluationSummary{}, fmt.Errorf("observation count %d does not match case count %d", len(observations), len(cases))
	}
	summary := TextToSQLEvaluationSummary{
		DatasetVersion: version,
		Cases:          len(cases),
		FailureTypes:   make(map[string]int),
	}
	seen := make(map[string]struct{}, len(observations))
	var totalDuration int64
	for index, observation := range observations {
		if err := observation.Validate(); err != nil {
			return TextToSQLEvaluationSummary{}, fmt.Errorf("observation %d: %w", index, err)
		}
		definition, ok := definitions[observation.CaseID]
		if !ok || observation.DatasetVersion != version {
			return TextToSQLEvaluationSummary{}, fmt.Errorf("observation %q is outside dataset", observation.RunID)
		}
		if _, exists := seen[observation.CaseID]; exists {
			return TextToSQLEvaluationSummary{}, fmt.Errorf("duplicate observation for case %q", observation.CaseID)
		}
		seen[observation.CaseID] = struct{}{}
		matched := TextToSQLResultMatches(definition, observation.Columns, observation.Rows, observation.Truncated)
		if observation.Correct != (observation.ErrorType == "" && matched) {
			return TextToSQLEvaluationSummary{}, fmt.Errorf("observation %q contains inconsistent correctness", observation.RunID)
		}
		if observation.Correct {
			summary.Correct++
		}
		if observation.ErrorType != "" {
			summary.FailureTypes[observation.ErrorType]++
		}
		summary.Usage.Add(observation.Usage)
		totalDuration += observation.DurationMillis
	}
	summary.ExecutionAccuracy = float64(summary.Correct) / float64(summary.Cases)
	summary.AverageDurationMillis = float64(totalDuration) / float64(summary.Cases)
	if len(summary.FailureTypes) == 0 {
		summary.FailureTypes = nil
	}
	return summary, nil
}

func TextToSQLResultMatches(
	definition TextToSQLEvaluationCase,
	columns []string,
	rows [][]any,
	truncated bool,
) bool {
	if truncated || len(columns) != len(definition.ExpectedColumns) || len(rows) != len(definition.ExpectedRows) {
		return false
	}
	for index := range columns {
		if !strings.EqualFold(strings.TrimSpace(columns[index]), strings.TrimSpace(definition.ExpectedColumns[index])) {
			return false
		}
	}
	expected := canonicalSQLRows(definition.ExpectedRows)
	actual := canonicalSQLRows(rows)
	if definition.ResultOrder == SQLResultUnordered {
		sort.Strings(expected)
		sort.Strings(actual)
	}
	for index := range expected {
		if expected[index] != actual[index] {
			return false
		}
	}
	return true
}

func canonicalSQLRows(rows [][]any) []string {
	result := make([]string, len(rows))
	for rowIndex, row := range rows {
		values := make([]string, len(row))
		for valueIndex, value := range row {
			values[valueIndex] = canonicalSQLValue(value)
		}
		result[rowIndex] = strings.Join(values, "\x1f")
	}
	return result
}

func canonicalSQLValue(value any) string {
	switch current := value.(type) {
	case nil:
		return "null"
	case string:
		return "s:" + current
	case bool:
		return "b:" + strconv.FormatBool(current)
	case json.Number:
		return "n:" + current.String()
	case int:
		return "n:" + strconv.FormatInt(int64(current), 10)
	case int8:
		return "n:" + strconv.FormatInt(int64(current), 10)
	case int16:
		return "n:" + strconv.FormatInt(int64(current), 10)
	case int32:
		return "n:" + strconv.FormatInt(int64(current), 10)
	case int64:
		return "n:" + strconv.FormatInt(current, 10)
	case uint:
		return "n:" + strconv.FormatUint(uint64(current), 10)
	case uint8:
		return "n:" + strconv.FormatUint(uint64(current), 10)
	case uint16:
		return "n:" + strconv.FormatUint(uint64(current), 10)
	case uint32:
		return "n:" + strconv.FormatUint(uint64(current), 10)
	case uint64:
		return "n:" + strconv.FormatUint(current, 10)
	case float32:
		return "n:" + strconv.FormatFloat(float64(current), 'g', -1, 32)
	case float64:
		return "n:" + strconv.FormatFloat(current, 'g', -1, 64)
	default:
		return fmt.Sprintf("%T:%v", value, value)
	}
}
