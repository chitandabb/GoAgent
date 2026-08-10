package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// singleSearchQualityModel forces the sole knowledge Tool on the first call and
// forbids Tool calls after the first successful Tool message. It deliberately
// keeps the Tool schema on the final request because OpenAI-compatible providers
// may need it to validate Tool call/result messages already present in history.
// Production Conversation model wiring is unchanged.
type singleSearchQualityModel struct {
	base        model.ToolCallingChatModel
	tools       []*schema.ToolInfo
	diagnostics *qualityModelDiagnostics
}

func newSingleSearchQualityModel(base model.ToolCallingChatModel) *singleSearchQualityModel {
	return &singleSearchQualityModel{
		base:        base,
		diagnostics: &qualityModelDiagnostics{},
	}
}

func (m *singleSearchQualityModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return &singleSearchQualityModel{
		base:        m.base,
		tools:       append([]*schema.ToolInfo(nil), tools...),
		diagnostics: m.diagnostics,
	}, nil
}

func (m *singleSearchQualityModel) Generate(
	ctx context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.Message, error) {
	tools, searchCompleted := m.toolsFor(input, opts)
	if searchCompleted {
		opts = append(opts, model.WithToolChoice(schema.ToolChoiceForbidden))
	} else {
		// This observer measures grounded answer quality, not Tool selection.
		// Its knowledge-only catalog exposes search_knowledge as the sole Tool,
		// so force exactly one retrieval before the final answer. Tool selection
		// remains covered by the independent tool-selection evaluation.
		opts = append(opts, model.WithToolChoice(schema.ToolChoiceForced))
	}
	opts = append(opts, model.WithTools(tools))
	callIndex := m.diagnostics.start(searchCompleted, tools, opts)
	current, err := m.base.WithTools(tools)
	if err != nil {
		m.diagnostics.observe(callIndex, nil, err)
		return nil, err
	}
	message, err := current.Generate(isolatedQualityProviderContext(ctx), input, opts...)
	m.diagnostics.observe(callIndex, message, err)
	return message, err
}

func (m *singleSearchQualityModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	tools, searchCompleted := m.toolsFor(input, opts)
	if searchCompleted {
		opts = append(opts, model.WithToolChoice(schema.ToolChoiceForbidden))
	} else {
		opts = append(opts, model.WithToolChoice(schema.ToolChoiceForced))
	}
	opts = append(opts, model.WithTools(tools))
	callIndex := m.diagnostics.start(searchCompleted, tools, opts)
	current, err := m.base.WithTools(tools)
	if err != nil {
		m.diagnostics.observe(callIndex, nil, err)
		return nil, err
	}
	reader, err := current.Stream(isolatedQualityProviderContext(ctx), input, opts...)
	if err != nil {
		m.diagnostics.observe(callIndex, nil, err)
		return nil, err
	}
	return schema.StreamReaderWithConvert(reader, func(message *schema.Message) (*schema.Message, error) {
		m.diagnostics.observe(callIndex, message, nil)
		return message, nil
	}), nil
}

func isolatedQualityProviderContext(ctx context.Context) context.Context {
	// Eino's Agent node observes the outer quality model invocation, while the
	// OpenAI-compatible client also emits callbacks internally. Reusing the same
	// handlers counts one provider request twice. Preserve all business context
	// values but replace callbacks for the inner client; the outer model result
	// remains the single source of Provider usage for the Runner budget.
	return callbacks.InitCallbacks(ctx, &callbacks.RunInfo{
		Name: "conversation-quality-provider-inner", Type: "quality-provider-inner",
		Component: components.ComponentOfChatModel,
	})
}

func (m *singleSearchQualityModel) toolsFor(
	input []*schema.Message,
	opts []model.Option,
) ([]*schema.ToolInfo, bool) {
	tools := m.tools
	// ChatModelAgent currently supplies its schema as call-time options. Prefer
	// that non-nil snapshot so the first model call is not accidentally stripped
	// when WithTools was not invoked on this wrapper instance.
	if optionTools := model.GetCommonOptions(nil, opts...).Tools; optionTools != nil {
		tools = optionTools
	}
	searchCompleted := qualitySearchCompleted(input)
	return append([]*schema.ToolInfo(nil), tools...), searchCompleted
}

func qualitySearchCompleted(input []*schema.Message) bool {
	for _, message := range input {
		if message != nil && message.Role == schema.Tool && message.ToolName == mesagent.ToolSearchKnowledge {
			return true
		}
	}
	return false
}

// qualityModelCallDiagnostic is deliberately content-free. It records only
// protocol shape needed to distinguish a repeated Tool call from a final text
// response. Prompts, answer text, Tool arguments, call IDs and provider error
// messages never enter this structure.
type qualityModelCallDiagnostic struct {
	Sequence            int      `json:"sequence"`
	SearchCompleted     bool     `json:"searchCompleted"`
	VisibleTools        []string `json:"visibleTools,omitempty"`
	RequestedToolChoice string   `json:"requestedToolChoice,omitempty"`
	ResponseRole        string   `json:"responseRole,omitempty"`
	ContentPresent      bool     `json:"contentPresent"`
	ReturnedToolCalls   []string `json:"returnedToolCalls,omitempty"`
	FinishReason        string   `json:"finishReason,omitempty"`
	InvocationErrorType string   `json:"invocationErrorType,omitempty"`
}

type qualityModelDiagnostics struct {
	mu    sync.Mutex
	calls []qualityModelCallDiagnostic
}

func (d *qualityModelDiagnostics) count() int {
	if d == nil {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.calls)
}

func (d *qualityModelDiagnostics) start(
	searchCompleted bool,
	tools []*schema.ToolInfo,
	opts []model.Option,
) int {
	if d == nil {
		return -1
	}
	visibleTools := make([]string, 0, len(tools))
	for _, info := range tools {
		if info == nil {
			continue
		}
		if name := boundedDiagnosticLabel(info.Name); name != "" {
			visibleTools = append(visibleTools, name)
		}
	}
	toolChoice := ""
	if choice := model.GetCommonOptions(nil, opts...).ToolChoice; choice != nil {
		toolChoice = boundedDiagnosticLabel(string(*choice))
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	index := len(d.calls)
	d.calls = append(d.calls, qualityModelCallDiagnostic{
		Sequence: index + 1, SearchCompleted: searchCompleted,
		VisibleTools: visibleTools, RequestedToolChoice: toolChoice,
	})
	return index
}

func (d *qualityModelDiagnostics) observe(index int, message *schema.Message, err error) {
	if d == nil || index < 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if index >= len(d.calls) {
		return
	}
	current := &d.calls[index]
	if err != nil {
		current.InvocationErrorType = boundedDiagnosticLabel(fmt.Sprintf("%T", err))
		return
	}
	if message == nil {
		return
	}
	if role := boundedDiagnosticLabel(string(message.Role)); role != "" {
		current.ResponseRole = role
	}
	current.ContentPresent = current.ContentPresent || strings.TrimSpace(message.Content) != ""
	for _, call := range message.ToolCalls {
		name := boundedDiagnosticLabel(call.Function.Name)
		if name == "" || containsString(current.ReturnedToolCalls, name) || len(current.ReturnedToolCalls) >= 16 {
			continue
		}
		current.ReturnedToolCalls = append(current.ReturnedToolCalls, name)
	}
	if message.ResponseMeta != nil {
		if reason := boundedDiagnosticLabel(message.ResponseMeta.FinishReason); reason != "" {
			current.FinishReason = reason
		}
	}
}

func (d *qualityModelDiagnostics) snapshotFrom(start int) []qualityModelCallDiagnostic {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if start < 0 || start > len(d.calls) {
		start = len(d.calls)
	}
	result := make([]qualityModelCallDiagnostic, len(d.calls)-start)
	for index := range result {
		result[index] = d.calls[start+index]
		result[index].VisibleTools = append([]string(nil), result[index].VisibleTools...)
		result[index].ReturnedToolCalls = append([]string(nil), result[index].ReturnedToolCalls...)
	}
	return result
}

func boundedDiagnosticLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) > 128 {
		runes = runes[:128]
	}
	for index, current := range runes {
		if current < 0x20 || current == 0x7f {
			runes[index] = '?'
		}
	}
	return string(runes)
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
