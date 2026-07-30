package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/compose"
)

type ToolExecution struct {
	Name       string `json:"name"`
	DurationMS int64  `json:"durationMs"`
	Succeeded  bool   `json:"succeeded"`
	Error      string `json:"error,omitempty"`
}

type RunResult struct {
	SkillID         SkillID         `json:"skillId"`
	SkillVersion    string          `json:"skillVersion"`
	RouteReason     string          `json:"routeReason"`
	RouteConfidence float64         `json:"routeConfidence"`
	Answer          string          `json:"answer"`
	AllowedTools    []string        `json:"allowedTools"`
	ToolExecutions  []ToolExecution `json:"toolExecutions"`
	Usage           ModelUsage      `json:"usage"`
	Budget          ContextBudget   `json:"budget"`
	ExecutedSkills  []SkillID       `json:"executedSkills"`
	Handoffs        []HandoffRecord `json:"handoffs,omitempty"`

	// Handoff 只供外层 Graph 在本次运行中决策，不直接暴露给 HTTP 调用方。
	Handoff *HandoffRequest `json:"-"`
}

type SkillExecutor interface {
	Execute(ctx context.Context, request RunRequest, definition SkillDefinition) (RunResult, error)
}

type orchestrationState struct {
	Original      RunRequest
	Decision      RouteDecision
	Results       []RunResult
	Handoffs      []HandoffRecord
	ExecutedSkill map[SkillID]bool
}

type Runner struct {
	runnable compose.Runnable[RunRequest, RunResult]
}

const maxSkillHandoffs = 3

// NewRunner 使用 Eino Graph 构建“静态安全边界、动态运行路径”的 Skill 编排。
// Graph 只包含启动时已经校验的 Skill；一次请求实际经过哪些节点由结构化 Handoff 决定。
func NewRunner(
	ctx context.Context,
	router Router,
	registry *Registry,
	executors map[SkillID]SkillExecutor,
) (*Runner, error) {
	if router == nil || registry == nil {
		return nil, errors.New("runner router and registry are required")
	}

	graph := compose.NewGraph[RunRequest, RunResult]()
	routeNode := compose.InvokableLambda(func(ctx context.Context, request RunRequest) (orchestrationState, error) {
		decision, err := router.Route(ctx, request)
		if err != nil {
			return orchestrationState{}, err
		}
		if _, err = registry.Get(decision.SkillID); err != nil {
			return orchestrationState{}, err
		}
		return orchestrationState{
			Original: request, Decision: decision, ExecutedSkill: make(map[SkillID]bool),
		}, nil
	})
	if err := graph.AddLambdaNode("intent_router", routeNode); err != nil {
		return nil, fmt.Errorf("add intent router: %w", err)
	}
	if err := graph.AddEdge(compose.START, "intent_router"); err != nil {
		return nil, fmt.Errorf("connect intent router: %w", err)
	}

	branchTargets := make(map[string]bool, len(registry.IDs()))
	for _, id := range registry.IDs() {
		definition, err := registry.Get(id)
		if err != nil {
			return nil, err
		}
		executor := executors[id]
		if executor == nil {
			return nil, fmt.Errorf("skill %q executor is nil", id)
		}
		nodeName := string(id)
		branchTargets[nodeName] = true
		currentDefinition := definition
		currentExecutor := executor
		node := compose.InvokableLambda(func(ctx context.Context, input orchestrationState) (orchestrationState, error) {
			if input.ExecutedSkill[currentDefinition.ID] {
				return orchestrationState{}, fmt.Errorf("skill handoff cycle detected: %s", currentDefinition.ID)
			}
			executeRequest, buildErr := requestForSkill(input, currentDefinition.ID)
			if buildErr != nil {
				return orchestrationState{}, buildErr
			}
			result, executeErr := currentExecutor.Execute(ctx, executeRequest, currentDefinition)
			if executeErr != nil {
				return orchestrationState{}, executeErr
			}
			result.SkillID = currentDefinition.ID
			result.SkillVersion = currentDefinition.Version
			result.AllowedTools = append([]string(nil), currentDefinition.AllowedTools...)
			result.Budget = currentDefinition.Budget
			input.Results = append(input.Results, result)
			input.ExecutedSkill[currentDefinition.ID] = true
			if result.Handoff != nil {
				input.Handoffs = append(input.Handoffs, HandoffRecord{
					FromSkill: currentDefinition.ID, ToSkill: result.Handoff.TargetSkill,
					Reason: result.Handoff.Reason, Query: result.Handoff.Query,
					Clues: append([]string(nil), result.Handoff.Clues...),
				})
			}
			return input, nil
		})
		if err = graph.AddLambdaNode(nodeName, node); err != nil {
			return nil, fmt.Errorf("add skill node %q: %w", id, err)
		}
	}

	const synthesisNode = "report_synthesis"
	synthesis := compose.InvokableLambda(func(_ context.Context, input orchestrationState) (RunResult, error) {
		return synthesizeRunResult(input), nil
	})
	if err := graph.AddLambdaNode(synthesisNode, synthesis); err != nil {
		return nil, fmt.Errorf("add report synthesis: %w", err)
	}

	const dispatcherNode = "handoff_dispatcher"
	dispatcher := compose.InvokableLambda(func(_ context.Context, input orchestrationState) (orchestrationState, error) {
		return input, nil
	})
	if err := graph.AddLambdaNode(dispatcherNode, dispatcher); err != nil {
		return nil, fmt.Errorf("add handoff dispatcher: %w", err)
	}
	if err := graph.AddEdge("intent_router", dispatcherNode); err != nil {
		return nil, fmt.Errorf("connect intent router to dispatcher: %w", err)
	}
	for _, id := range registry.IDs() {
		if err := graph.AddEdge(string(id), dispatcherNode); err != nil {
			return nil, fmt.Errorf("connect skill %q to dispatcher: %w", id, err)
		}
	}
	branchTargets[synthesisNode] = true
	branch := compose.NewGraphBranch(func(_ context.Context, input orchestrationState) (string, error) {
		if len(input.Results) == 0 {
			return string(input.Decision.SkillID), nil
		}
		last := input.Results[len(input.Results)-1]
		if last.Handoff == nil {
			return synthesisNode, nil
		}
		if len(input.Handoffs) > maxSkillHandoffs {
			return "", fmt.Errorf("skill handoff limit exceeded: %d", maxSkillHandoffs)
		}
		target := last.Handoff.TargetSkill
		if input.ExecutedSkill[target] {
			return "", fmt.Errorf("skill handoff cycle detected: %s", target)
		}
		if _, err := registry.Get(target); err != nil {
			return synthesisNode, nil
		}
		return string(target), nil
	}, branchTargets)
	if err := graph.AddBranch(dispatcherNode, branch); err != nil {
		return nil, fmt.Errorf("add handoff branch: %w", err)
	}

	if err := graph.AddEdge(synthesisNode, compose.END); err != nil {
		return nil, fmt.Errorf("connect report synthesis: %w", err)
	}

	runnable, err := graph.Compile(ctx, compose.WithGraphName("mesguard_skill_router"), compose.WithMaxRunSteps(16))
	if err != nil {
		return nil, fmt.Errorf("compile skill graph: %w", err)
	}
	return &Runner{runnable: runnable}, nil
}

func requestForSkill(input orchestrationState, skillID SkillID) (RunRequest, error) {
	if len(input.Results) == 0 {
		return input.Original, nil
	}
	previous := input.Results[len(input.Results)-1]
	if previous.Handoff == nil || previous.Handoff.TargetSkill != skillID {
		return input.Original, nil
	}
	payload := struct {
		Question             string   `json:"question"`
		HandoffReason        string   `json:"handoffReason"`
		Clues                []string `json:"clues,omitempty"`
		TicketFindingSummary string   `json:"ticketFindingSummary"`
	}{
		Question: previous.Handoff.Query, HandoffReason: previous.Handoff.Reason,
		Clues: previous.Handoff.Clues, TicketFindingSummary: previous.Answer,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return RunRequest{}, fmt.Errorf("marshal skill handoff: %w", err)
	}
	return RunRequest{UserQuery: string(encoded), RequestedSkill: skillID}, nil
}

func synthesizeRunResult(input orchestrationState) RunResult {
	if len(input.Results) == 0 {
		return RunResult{RouteReason: input.Decision.Reason, RouteConfidence: input.Decision.Confidence}
	}
	first := input.Results[0]
	if len(input.Results) == 1 && len(input.Handoffs) == 0 {
		first.RouteReason = input.Decision.Reason
		first.RouteConfidence = input.Decision.Confidence
		first.ExecutedSkills = []SkillID{first.SkillID}
		first.Handoff = nil
		return first
	}
	result := RunResult{
		SkillID: first.SkillID, SkillVersion: first.SkillVersion,
		RouteReason: input.Decision.Reason, RouteConfidence: input.Decision.Confidence,
		Budget: first.Budget, Handoffs: append([]HandoffRecord(nil), input.Handoffs...),
	}
	answers := make([]string, 0, len(input.Results)+1)
	seenTools := make(map[string]bool)
	for _, current := range input.Results {
		result.ExecutedSkills = append(result.ExecutedSkills, current.SkillID)
		result.ToolExecutions = append(result.ToolExecutions, current.ToolExecutions...)
		result.Usage.Add(current.Usage)
		for _, toolName := range current.AllowedTools {
			if !seenTools[toolName] {
				seenTools[toolName] = true
				result.AllowedTools = append(result.AllowedTools, toolName)
			}
		}
		if strings.TrimSpace(current.Answer) != "" {
			answers = append(answers, fmt.Sprintf("[%s]\n%s", current.SkillID, current.Answer))
		}
	}
	if len(input.Handoffs) > 0 {
		lastHandoff := input.Handoffs[len(input.Handoffs)-1]
		if !input.ExecutedSkill[lastHandoff.ToSkill] {
			answers = append(answers, unavailableSkillMessage(lastHandoff.ToSkill))
		}
	}
	result.Answer = strings.Join(answers, "\n\n")
	return result
}

func unavailableSkillMessage(skillID SkillID) string {
	if skillID == SkillCodeInvestigation {
		return "[code-investigation]\nGitHub MCP 工具暂时不可用。"
	}
	return fmt.Sprintf("[%s]\n目标 Skill 暂时不可用。", skillID)
}

func (r *Runner) Invoke(ctx context.Context, request RunRequest) (RunResult, error) {
	if r == nil || r.runnable == nil {
		return RunResult{}, errors.New("agent runner is nil")
	}
	return r.runnable.Invoke(ctx, request)
}
