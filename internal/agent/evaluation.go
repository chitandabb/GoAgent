package agent

// EvaluationObservation 是一次固定评测样本的实际结果。
// Token 必须来自模型供应商 usage；不能用字符数冒充 Token。
type EvaluationObservation struct {
	CaseID              string   `json:"caseId"`
	ExpectedSkill       SkillID  `json:"expectedSkill"`
	SelectedSkill       SkillID  `json:"selectedSkill"`
	ExpectedFirstTool   string   `json:"expectedFirstTool"`
	ActualToolCalls     []string `json:"actualToolCalls"`
	BaselineInputTokens int      `json:"baselineInputTokens"`
	DynamicInputTokens  int      `json:"dynamicInputTokens"`
}

type EvaluationSummary struct {
	Total                   int     `json:"total"`
	SkillRoutingAccuracy    float64 `json:"skillRoutingAccuracy"`
	ToolSelectionAccuracy   float64 `json:"toolSelectionAccuracy"`
	OutOfWhitelistCallRate  float64 `json:"outOfWhitelistCallRate"`
	InputTokenReductionRate float64 `json:"inputTokenReductionRate"`
	BaselineInputTokens     int     `json:"baselineInputTokens"`
	DynamicInputTokens      int     `json:"dynamicInputTokens"`
}

func SummarizeEvaluation(observations []EvaluationObservation, registry *Registry) EvaluationSummary {
	summary := EvaluationSummary{Total: len(observations)}
	if len(observations) == 0 {
		return summary
	}
	var correctSkills, correctFirstTools, evaluatedFirstTools int
	var totalCalls, outOfWhitelistCalls int
	for _, observation := range observations {
		skillCorrect := observation.SelectedSkill == observation.ExpectedSkill
		if skillCorrect {
			correctSkills++
		}
		if observation.ExpectedFirstTool != "" {
			evaluatedFirstTools++
			if skillCorrect && len(observation.ActualToolCalls) > 0 && observation.ActualToolCalls[0] == observation.ExpectedFirstTool {
				correctFirstTools++
			}
		}
		definition, err := registry.Get(observation.SelectedSkill)
		allowed := make(map[string]struct{})
		if err == nil {
			for _, name := range definition.AllowedTools {
				allowed[name] = struct{}{}
			}
		}
		for _, name := range observation.ActualToolCalls {
			totalCalls++
			if _, ok := allowed[name]; !ok {
				outOfWhitelistCalls++
			}
		}
		summary.BaselineInputTokens += observation.BaselineInputTokens
		summary.DynamicInputTokens += observation.DynamicInputTokens
	}
	summary.SkillRoutingAccuracy = float64(correctSkills) / float64(len(observations))
	if evaluatedFirstTools > 0 {
		summary.ToolSelectionAccuracy = float64(correctFirstTools) / float64(evaluatedFirstTools)
	}
	if totalCalls > 0 {
		summary.OutOfWhitelistCallRate = float64(outOfWhitelistCalls) / float64(totalCalls)
	}
	if summary.BaselineInputTokens > 0 {
		summary.InputTokenReductionRate = float64(summary.BaselineInputTokens-summary.DynamicInputTokens) /
			float64(summary.BaselineInputTokens)
	}
	return summary
}
