package agent

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
)

// HandoffRequest 是 Skill 之间的结构化交接单。
// 它只传递结论、检索问题和线索，不传递模型的内部思维过程。
type HandoffRequest struct {
	TargetSkill SkillID  `json:"targetSkill"`
	Reason      string   `json:"reason"`
	Query       string   `json:"query"`
	Clues       []string `json:"clues,omitempty"`
}

type HandoffRecord struct {
	FromSkill SkillID  `json:"fromSkill"`
	ToSkill   SkillID  `json:"toSkill"`
	Reason    string   `json:"reason"`
	Query     string   `json:"query"`
	Clues     []string `json:"clues,omitempty"`
}

type handoffTrace struct {
	mu      sync.Mutex
	request *HandoffRequest
}

type handoffTraceContextKey struct{}

func withHandoffTrace(ctx context.Context, trace *handoffTrace) context.Context {
	return context.WithValue(ctx, handoffTraceContextKey{}, trace)
}

func handoffTraceFromContext(ctx context.Context) *handoffTrace {
	trace, _ := ctx.Value(handoffTraceContextKey{}).(*handoffTrace)
	return trace
}

func (t *handoffTrace) set(request HandoffRequest) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	copyRequest := request
	copyRequest.Clues = append([]string(nil), request.Clues...)
	t.request = &copyRequest
}

func (t *handoffTrace) snapshot() *HandoffRequest {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.request == nil {
		return nil
	}
	copyRequest := *t.request
	copyRequest.Clues = append([]string(nil), t.request.Clues...)
	return &copyRequest
}

type requestCodeInvestigationInput struct {
	Reason string   `json:"reason" jsonschema:"required" jsonschema_description:"为什么现有工单证据需要进一步调查代码"`
	Query  string   `json:"query" jsonschema:"required" jsonschema_description:"交给代码调查 Skill 的精确检索问题"`
	Clues  []string `json:"clues,omitempty" jsonschema_description:"错误信息、类名、函数名、模块名等代码线索"`
}

type requestCodeInvestigationOutput struct {
	Accepted bool   `json:"accepted"`
	Message  string `json:"message"`
}

// NewRequestCodeInvestigationTool 创建一个不访问外部系统的内部编排 Tool。
// 模型通过调用它表达交接意图，真正的 Skill 跳转仍由外层 Graph 校验和执行。
func NewRequestCodeInvestigationTool() (tool.InvokableTool, error) {
	return toolutils.InferTool(
		ToolRequestCodeInvestigation,
		"当工单中出现明确的错误信息、模块、类名或函数名，且需要代码证据才能继续判断时，请求只读代码调查",
		func(ctx context.Context, input requestCodeInvestigationInput) (requestCodeInvestigationOutput, error) {
			reason := strings.TrimSpace(input.Reason)
			query := strings.TrimSpace(input.Query)
			if reason == "" || query == "" {
				return requestCodeInvestigationOutput{}, errors.New("handoff reason and query are required")
			}
			if len(reason) > 1000 || len(query) > 2000 || len(input.Clues) > 20 {
				return requestCodeInvestigationOutput{}, errors.New("handoff request exceeds safety limits")
			}
			clues := make([]string, 0, len(input.Clues))
			for _, rawClue := range input.Clues {
				clue := strings.TrimSpace(rawClue)
				if clue == "" {
					continue
				}
				if len(clue) > 500 {
					return requestCodeInvestigationOutput{}, errors.New("handoff clue exceeds safety limit")
				}
				clues = append(clues, clue)
			}
			trace := handoffTraceFromContext(ctx)
			if trace == nil {
				return requestCodeInvestigationOutput{}, errors.New("handoff runtime is unavailable")
			}
			trace.set(HandoffRequest{
				TargetSkill: SkillCodeInvestigation,
				Reason:      reason,
				Query:       query,
				Clues:       clues,
			})
			return requestCodeInvestigationOutput{
				Accepted: true, Message: "代码调查请求已登记；请先总结当前工单证据，外层编排将在本 Skill 结束后执行交接",
			}, nil
		},
	)
}
