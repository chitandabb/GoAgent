package agent

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
)

const (
	contextGovernancePilotScenarioCount   = 4
	contextGovernancePilotCheckpointCount = 3
	contextGovernancePilotMaxHistory      = 256
)

type ContextGovernancePilotArm string

const (
	PilotArmCurrent    ContextGovernancePilotArm = "current"
	PilotArmBaseline   ContextGovernancePilotArm = "baseline"
	PilotArmExperiment ContextGovernancePilotArm = "experiment"
)

func (a ContextGovernancePilotArm) Valid() bool {
	return a == PilotArmCurrent || a == PilotArmBaseline || a == PilotArmExperiment
}

// ContextGovernancePilotDataset is the immutable, provider-free M3 Pilot
// fixture. History is materialized locally; constructing the dataset never
// calls a model provider.
type ContextGovernancePilotDataset struct {
	DatasetVersion string                           `json:"datasetVersion"`
	FixtureVersion string                           `json:"fixtureVersion"`
	Scenarios      []ContextGovernancePilotScenario `json:"scenarios"`
}

type ContextGovernancePilotScenario struct {
	ScenarioID  string                             `json:"scenarioId"`
	Title       string                             `json:"title"`
	History     []ContextGovernancePilotMessage    `json:"history"`
	Checkpoints []ContextGovernancePilotCheckpoint `json:"checkpoints"`
}

type ContextGovernancePilotMessage struct {
	Seq     int64  `json:"seq"`
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ContextGovernancePilotCheckpoint struct {
	CheckpointID      string                     `json:"checkpointId"`
	HistoryThroughSeq int64                      `json:"historyThroughSeq"`
	Question          string                     `json:"question"`
	Gold              ContextGovernancePilotGold `json:"gold"`
}

type ContextGovernancePilotGold struct {
	RequiredAnswerTerms []string                               `json:"requiredAnswerTerms"`
	Facts               []string                               `json:"facts"`
	Decisions           []string                               `json:"decisions"`
	Corrections         []ContextGovernancePilotCorrectionGold `json:"corrections"`
	Todos               []ContextGovernancePilotTodoGold       `json:"todos"`
	EvidenceReferences  []string                               `json:"evidenceReferences"`
	ForbiddenClaims     []string                               `json:"forbiddenClaims"`
}

type ContextGovernancePilotCorrectionGold struct {
	CurrentTerms    []string `json:"currentTerms"`
	SupersededTerms []string `json:"supersededTerms"`
}

type ContextGovernancePilotTodoGold struct {
	ContentTerms []string `json:"contentTerms"`
	StatusTerms  []string `json:"statusTerms"`
}

func (d ContextGovernancePilotDataset) Validate() error {
	if !conversationQualityLabelPattern.MatchString(d.DatasetVersion) ||
		!conversationQualityLabelPattern.MatchString(d.FixtureVersion) ||
		len(d.Scenarios) != contextGovernancePilotScenarioCount {
		return errors.New("context governance Pilot dataset identity or scenario count is invalid")
	}
	seenScenarios := make(map[string]struct{}, len(d.Scenarios))
	seenCheckpoints := make(map[string]struct{}, contextGovernancePilotScenarioCount*contextGovernancePilotCheckpointCount)
	for scenarioIndex, scenario := range d.Scenarios {
		if !conversationQualityLabelPattern.MatchString(scenario.ScenarioID) ||
			strings.TrimSpace(scenario.Title) == "" || scenario.Title != strings.TrimSpace(scenario.Title) ||
			len(scenario.History) < contextGovernancePilotCheckpointCount ||
			len(scenario.History) > contextGovernancePilotMaxHistory ||
			len(scenario.Checkpoints) != contextGovernancePilotCheckpointCount {
			return fmt.Errorf("scenario %d is invalid", scenarioIndex)
		}
		if _, duplicate := seenScenarios[scenario.ScenarioID]; duplicate {
			return fmt.Errorf("duplicate scenarioId %q", scenario.ScenarioID)
		}
		seenScenarios[scenario.ScenarioID] = struct{}{}
		for messageIndex, message := range scenario.History {
			if message.Seq != int64(messageIndex+1) ||
				(message.Role != "user" && message.Role != "assistant") ||
				strings.TrimSpace(message.Content) == "" || message.Content != strings.TrimSpace(message.Content) ||
				len([]rune(message.Content)) > 20_000 {
				return fmt.Errorf("scenario %s history message %d is invalid", scenario.ScenarioID, messageIndex)
			}
		}
		var previousThrough int64
		for checkpointIndex, checkpoint := range scenario.Checkpoints {
			if !conversationQualityLabelPattern.MatchString(checkpoint.CheckpointID) ||
				checkpoint.HistoryThroughSeq <= previousThrough ||
				checkpoint.HistoryThroughSeq > int64(len(scenario.History)) ||
				strings.TrimSpace(checkpoint.Question) == "" || checkpoint.Question != strings.TrimSpace(checkpoint.Question) ||
				checkpoint.Gold.validate() != nil {
				return fmt.Errorf("scenario %s checkpoint %d is invalid", scenario.ScenarioID, checkpointIndex)
			}
			if _, duplicate := seenCheckpoints[checkpoint.CheckpointID]; duplicate {
				return fmt.Errorf("duplicate checkpointId %q", checkpoint.CheckpointID)
			}
			seenCheckpoints[checkpoint.CheckpointID] = struct{}{}
			previousThrough = checkpoint.HistoryThroughSeq
		}
	}
	return nil
}

func (g ContextGovernancePilotGold) validate() error {
	groups := [][]string{g.RequiredAnswerTerms, g.Facts, g.Decisions, g.EvidenceReferences, g.ForbiddenClaims}
	if len(g.RequiredAnswerTerms) == 0 {
		return errors.New("Pilot gold requires answer terms")
	}
	for _, values := range groups {
		if len(values) > 32 || hasDuplicate(values) {
			return errors.New("Pilot gold string group is invalid")
		}
		for _, value := range values {
			if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) || len([]rune(value)) > 256 {
				return errors.New("Pilot gold term is invalid")
			}
		}
	}
	if len(g.Corrections) > 16 || len(g.Todos) > 16 {
		return errors.New("Pilot gold structured group is too large")
	}
	for _, correction := range g.Corrections {
		if !validPilotTermGroup(correction.CurrentTerms, true) || !validPilotTermGroup(correction.SupersededTerms, true) {
			return errors.New("Pilot correction gold is invalid")
		}
	}
	for _, todo := range g.Todos {
		if !validPilotTermGroup(todo.ContentTerms, true) || !validPilotTermGroup(todo.StatusTerms, true) {
			return errors.New("Pilot Todo gold is invalid")
		}
	}
	return nil
}

func validPilotTermGroup(values []string, required bool) bool {
	if required && len(values) == 0 || len(values) > 16 || hasDuplicate(values) {
		return false
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) || len([]rune(value)) > 256 {
			return false
		}
	}
	return true
}

// ContextGovernancePilotFixture returns four deterministic long-conversation
// scenarios. The repeated operational notes create context pressure locally;
// only the three checkpoint questions require provider calls.
func ContextGovernancePilotFixture() ContextGovernancePilotDataset {
	return ContextGovernancePilotDataset{
		DatasetVersion: "context-governance-pilot-v1",
		FixtureVersion: "fixture-2026-08-12-v3",
		Scenarios: []ContextGovernancePilotScenario{
			buildContextPilotScenario(
				"incident-correction", "生产故障判断被后续证据修正",
				[]pilotMilestone{
					{20, "用户确认故障发生于工单 TKT-2048，数据库报告 1205，初步根因写为网络抖动。证据引用 report:diag-2048-a。"},
					{40, "团队决定优先检查数据库锁等待，不执行服务无限重启；待办是采集锁等待图，状态为进行中。"},
					{70, "新证据证明根因是库存行锁死锁，不是网络抖动；report:diag-2048-b 取代旧判断。"},
					{100, "锁等待图已经采集完成；团队决定先修正事务加锁顺序，再观察 30 分钟。"},
				},
				[]ContextGovernancePilotCheckpoint{
					pilotCheckpoint("incident-cp1", 40, "当前故障、初步判断和待办是什么？", []string{"TKT-2048", "网络抖动", "采集锁等待图", "进行中"}, []string{"TKT-2048", "数据库 1205"}, []string{"优先检查数据库锁等待"}, nil, []ContextGovernancePilotTodoGold{{[]string{"采集锁等待图"}, []string{"进行中"}}}, []string{"report:diag-2048-a"}, nil),
					pilotCheckpoint("incident-cp2", 80, "根据新证据给出当前根因并说明被推翻的判断。", []string{"库存行锁死锁", "不是网络抖动", "report:diag-2048-b"}, []string{"库存行锁死锁"}, nil, []ContextGovernancePilotCorrectionGold{{[]string{"库存行锁死锁"}, []string{"根因是网络抖动"}}}, nil, []string{"report:diag-2048-b"}, []string{"根因仍是网络抖动"}),
					pilotCheckpoint("incident-cp3", 120, "总结最终根因、处置决策和待办状态。", []string{"库存行锁死锁", "修正事务加锁顺序", "采集锁等待图", "已完成"}, []string{"库存行锁死锁"}, []string{"修正事务加锁顺序", "观察 30 分钟"}, nil, []ContextGovernancePilotTodoGold{{[]string{"采集锁等待图"}, []string{"已完成"}}}, []string{"report:diag-2048-b"}, []string{"无限重启"}),
				},
			),
			buildContextPilotScenario(
				"release-policy", "发布方案与回滚窗口在长会话中变更",
				[]pilotMilestone{
					{15, "用户提出 MES 3.8 发布，最初计划全量发布，回滚观察窗口为 30 分钟。证据引用 knowledge:release-38-v1。"},
					{35, "评审决定采用 10% 灰度发布，不再直接全量；待办是核对迁移脚本，状态为进行中。"},
					{65, "审批将回滚观察窗口从 30 分钟改为 45 分钟，引用 knowledge:release-38-v2。"},
					{95, "迁移脚本核对完成，最终决定错误率超过 1% 时回滚。"},
				},
				[]ContextGovernancePilotCheckpoint{
					pilotCheckpoint("release-cp1", 40, "当前发布方式和待办是什么？", []string{"10% 灰度发布", "核对迁移脚本", "进行中"}, nil, []string{"10% 灰度发布"}, nil, []ContextGovernancePilotTodoGold{{[]string{"核对迁移脚本"}, []string{"进行中"}}}, []string{"knowledge:release-38-v1"}, []string{"直接全量发布"}),
					pilotCheckpoint("release-cp2", 80, "回滚观察窗口最终是多少，旧值是什么？", []string{"45 分钟", "原来 30 分钟"}, []string{"回滚观察窗口为 45 分钟"}, nil, []ContextGovernancePilotCorrectionGold{{[]string{"45 分钟"}, []string{"仍为 30 分钟"}}}, nil, []string{"knowledge:release-38-v2"}, []string{"观察窗口仍为 30 分钟"}),
					pilotCheckpoint("release-cp3", 120, "给出最终发布、回滚和迁移脚本状态。", []string{"10% 灰度发布", "45 分钟", "错误率超过 1%", "核对完成"}, nil, []string{"错误率超过 1% 时回滚"}, nil, []ContextGovernancePilotTodoGold{{[]string{"迁移脚本"}, []string{"核对完成"}}}, []string{"knowledge:release-38-v2"}, []string{"直接全量发布"}),
				},
			),
			buildContextPilotScenario(
				"policy-version", "知识制度更新后保留当前版本语义",
				[]pilotMilestone{
					{18, "旧制度规定高温停机阈值为 80 摄氏度，来源 knowledge:safety-v3。"},
					{38, "管理员发布新制度：阈值调整为 75 摄氏度，旧 80 摄氏度规则作废，来源 knowledge:safety-v4。"},
					{68, "团队决定所有新回答只引用 v4；待办是通知二厂，状态为待处理。"},
					{98, "二厂已经收到通知并确认，待办状态改为已完成。"},
				},
				[]ContextGovernancePilotCheckpoint{
					pilotCheckpoint("policy-cp1", 40, "当前高温停机阈值和有效来源是什么？", []string{"75 摄氏度", "knowledge:safety-v4"}, []string{"高温停机阈值为 75 摄氏度"}, nil, []ContextGovernancePilotCorrectionGold{{[]string{"75 摄氏度"}, []string{"阈值仍为 80 摄氏度"}}}, nil, []string{"knowledge:safety-v4"}, []string{"当前阈值为 80 摄氏度"}),
					pilotCheckpoint("policy-cp2", 80, "制度引用决策和通知待办是什么？", []string{"只引用 v4", "通知二厂", "待处理"}, nil, []string{"所有新回答只引用 v4"}, nil, []ContextGovernancePilotTodoGold{{[]string{"通知二厂"}, []string{"待处理"}}}, []string{"knowledge:safety-v4"}, []string{"继续引用 v3"}),
					pilotCheckpoint("policy-cp3", 120, "总结当前阈值、有效版本和通知状态。", []string{"75 摄氏度", "knowledge:safety-v4", "二厂", "已完成"}, []string{"高温停机阈值为 75 摄氏度"}, []string{"只引用 v4"}, nil, []ContextGovernancePilotTodoGold{{[]string{"通知二厂"}, []string{"已完成"}}}, []string{"knowledge:safety-v4"}, []string{"80 摄氏度仍有效"}),
				},
			),
			buildContextPilotScenario(
				"attachment-evidence", "附件识别结论和诊断报告引用演进",
				[]pilotMilestone{
					{16, "附件 attachment:invoice-778 初次 OCR 读到批次号 B-17，置信度较低。"},
					{36, "VLM 复核确认批次号实际为 B-71，不是 B-17，证据哈希 sha256:invoice-778-v2。"},
					{66, "团队决定用 B-71 查询追溯表；待办是核对供应商批次，状态为进行中。"},
					{96, "供应商确认 B-71 属于华东二厂，核对待办已完成，报告引用 report:invoice-778。"},
				},
				[]ContextGovernancePilotCheckpoint{
					pilotCheckpoint("attachment-cp1", 40, "附件中的批次号最终识别为什么？", []string{"B-71", "不是 B-17", "sha256:invoice-778-v2"}, []string{"批次号为 B-71"}, nil, []ContextGovernancePilotCorrectionGold{{[]string{"B-71"}, []string{"批次号仍为 B-17"}}}, nil, []string{"attachment:invoice-778"}, []string{"最终批次号是 B-17"}),
					pilotCheckpoint("attachment-cp2", 80, "当前查询决策和供应商核对待办是什么？", []string{"用 B-71 查询追溯表", "核对供应商批次", "进行中"}, nil, []string{"用 B-71 查询追溯表"}, nil, []ContextGovernancePilotTodoGold{{[]string{"核对供应商批次"}, []string{"进行中"}}}, []string{"attachment:invoice-778"}, []string{"用 B-17 查询"}),
					pilotCheckpoint("attachment-cp3", 120, "总结批次、供应商、待办状态和报告引用。", []string{"B-71", "华东二厂", "已完成", "report:invoice-778"}, []string{"B-71 属于华东二厂"}, nil, nil, []ContextGovernancePilotTodoGold{{[]string{"核对供应商批次"}, []string{"已完成"}}}, []string{"report:invoice-778"}, []string{"B-17"}),
				},
			),
		},
	}
}

type pilotMilestone struct {
	seq     int64
	content string
}

func buildContextPilotScenario(
	id, title string,
	milestones []pilotMilestone,
	checkpoints []ContextGovernancePilotCheckpoint,
) ContextGovernancePilotScenario {
	history := make([]ContextGovernancePilotMessage, 120)
	for index := range history {
		seq := int64(index + 1)
		role := "user"
		if seq%2 == 0 {
			role = "assistant"
		}
		base := fmt.Sprintf("%s 的第 %03d 条运行记录：保持当前调查边界，记录时间线、已验证条件、未决项和禁止越权写操作。", title, seq)
		content := strings.Repeat(base, 19)
		for _, milestone := range milestones {
			if milestone.seq == seq {
				content = milestone.content + " " + content
				role = "user"
				break
			}
		}
		history[index] = ContextGovernancePilotMessage{Seq: seq, Role: role, Content: strings.TrimSpace(content)}
	}
	return ContextGovernancePilotScenario{ScenarioID: id, Title: title, History: history, Checkpoints: checkpoints}
}

func pilotCheckpoint(
	id string,
	historyThrough int64,
	question string,
	required, facts, decisions []string,
	corrections []ContextGovernancePilotCorrectionGold,
	todos []ContextGovernancePilotTodoGold,
	evidence, forbidden []string,
) ContextGovernancePilotCheckpoint {
	return ContextGovernancePilotCheckpoint{
		CheckpointID: id, HistoryThroughSeq: historyThrough, Question: question,
		Gold: ContextGovernancePilotGold{
			RequiredAnswerTerms: required, Facts: facts, Decisions: decisions,
			Corrections: corrections, Todos: todos, EvidenceReferences: evidence, ForbiddenClaims: forbidden,
		},
	}
}

type ContextGovernancePilotContract struct {
	ModelProvider           string `json:"modelProvider"`
	ModelID                 string `json:"modelId"`
	ModelProfile            string `json:"modelProfile"`
	ModelProfileFingerprint string `json:"modelProfileFingerprint"`
	ReasoningMode           string `json:"reasoningMode"`
	ToolContractFingerprint string `json:"toolContractFingerprint"`
	OutputReserveTokens     int    `json:"outputReserveTokens"`
	PromptVersion           string `json:"promptVersion"`
}

func (c ContextGovernancePilotContract) Validate() error {
	if !conversationQualityLabelPattern.MatchString(c.ModelProvider) ||
		!conversationQualityDisplayLabel(c.ModelID) ||
		!conversationQualityLabelPattern.MatchString(c.ModelProfile) ||
		!validConversationQualitySHA256(c.ModelProfileFingerprint) ||
		!conversationQualityLabelPattern.MatchString(c.ReasoningMode) ||
		!validConversationQualitySHA256(c.ToolContractFingerprint) ||
		c.OutputReserveTokens < 1 || c.OutputReserveTokens > 200_000 ||
		!conversationQualityLabelPattern.MatchString(c.PromptVersion) {
		return errors.New("context governance Pilot run contract is invalid")
	}
	return nil
}

type ContextGovernancePilotUsage struct {
	ModelCalls       int `json:"modelCalls"`
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
	CachedTokens     int `json:"cachedTokens"`
	ReasoningTokens  int `json:"reasoningTokens"`
}

type ContextGovernancePilotSummaryContract struct {
	ModelProvider           string `json:"modelProvider"`
	ModelID                 string `json:"modelId"`
	ModelProfile            string `json:"modelProfile"`
	ModelProfileFingerprint string `json:"modelProfileFingerprint"`
	PromptVersion           string `json:"promptVersion"`
}

func (c ContextGovernancePilotSummaryContract) Validate() error {
	if !conversationQualityLabelPattern.MatchString(c.ModelProvider) ||
		!conversationQualityDisplayLabel(c.ModelID) ||
		!conversationQualityLabelPattern.MatchString(c.ModelProfile) ||
		!validConversationQualitySHA256(c.ModelProfileFingerprint) ||
		!conversationQualityLabelPattern.MatchString(c.PromptVersion) {
		return errors.New("context governance Pilot Summary contract is invalid")
	}
	return nil
}

func (u ContextGovernancePilotUsage) validate() error {
	if u.ModelCalls < 0 || u.PromptTokens < 0 || u.CompletionTokens < 0 || u.TotalTokens < 0 ||
		u.CachedTokens < 0 || u.CachedTokens > u.PromptTokens || u.ReasoningTokens < 0 ||
		u.TotalTokens < u.PromptTokens+u.CompletionTokens ||
		(u.ModelCalls == 0 && (u.PromptTokens != 0 || u.CompletionTokens != 0 || u.TotalTokens != 0 ||
			u.CachedTokens != 0 || u.ReasoningTokens != 0)) {
		return errors.New("context governance Pilot usage is invalid")
	}
	return nil
}

type ContextGovernancePilotJudge struct {
	JudgeID           string  `json:"judgeId"`
	RubricVersion     string  `json:"rubricVersion"`
	Faithfulness      float64 `json:"faithfulness"`
	AnswerCorrectness float64 `json:"answerCorrectness"`
}

func (j ContextGovernancePilotJudge) validate() error {
	if !conversationQualityLabelPattern.MatchString(j.JudgeID) ||
		!conversationQualityLabelPattern.MatchString(j.RubricVersion) {
		return errors.New("context governance Pilot Judge identity is invalid")
	}
	for _, score := range []float64{j.Faithfulness, j.AnswerCorrectness} {
		if math.IsNaN(score) || math.IsInf(score, 0) || score < 0 || score > 1 {
			return errors.New("context governance Pilot Judge score is invalid")
		}
	}
	return nil
}

type ContextGovernancePilotObservation struct {
	DatasetVersion             string                                 `json:"datasetVersion"`
	FixtureVersion             string                                 `json:"fixtureVersion"`
	FixtureFingerprint         string                                 `json:"fixtureFingerprint"`
	ScenarioID                 string                                 `json:"scenarioId"`
	CheckpointID               string                                 `json:"checkpointId"`
	RunID                      string                                 `json:"runId"`
	Arm                        ContextGovernancePilotArm              `json:"arm"`
	Contract                   ContextGovernancePilotContract         `json:"contract"`
	SummaryContract            *ContextGovernancePilotSummaryContract `json:"summaryContract,omitempty"`
	Answer                     string                                 `json:"answer,omitempty"`
	MainUsage                  ContextGovernancePilotUsage            `json:"mainUsage"`
	SummaryUsage               ContextGovernancePilotUsage            `json:"summaryUsage"`
	EstimatedPromptTokens      int                                    `json:"estimatedPromptTokens"`
	WithinHardWindow           bool                                   `json:"withinHardWindow"`
	FirstTokenLatencyMillis    int64                                  `json:"firstTokenLatencyMillis"`
	PromptEpochID              string                                 `json:"promptEpochId"`
	Judge                      *ContextGovernancePilotJudge           `json:"judge,omitempty"`
	ErrorType                  string                                 `json:"errorType,omitempty"`
	SummaryAttemptFailureCodes []string                               `json:"summaryAttemptFailureCodes,omitempty"`
}

type contextGovernancePilotObservationKey struct {
	checkpoint string
	arm        ContextGovernancePilotArm
}

func (o ContextGovernancePilotObservation) Validate() error {
	if !conversationQualityLabelPattern.MatchString(o.DatasetVersion) ||
		!conversationQualityLabelPattern.MatchString(o.FixtureVersion) ||
		!validConversationQualitySHA256(o.FixtureFingerprint) ||
		!conversationQualityLabelPattern.MatchString(o.ScenarioID) ||
		!conversationQualityLabelPattern.MatchString(o.CheckpointID) ||
		!conversationQualityLabelPattern.MatchString(o.RunID) || !o.Arm.Valid() ||
		o.Contract.Validate() != nil || o.MainUsage.validate() != nil || o.SummaryUsage.validate() != nil ||
		o.EstimatedPromptTokens < 0 || o.FirstTokenLatencyMillis < 0 ||
		(o.PromptEpochID != "" && !conversationQualityLabelPattern.MatchString(o.PromptEpochID)) ||
		(o.ErrorType != "" && !conversationQualityLabelPattern.MatchString(o.ErrorType)) ||
		len(o.SummaryAttemptFailureCodes) > 10 ||
		len([]rune(o.Answer)) > 20_000 {
		return errors.New("context governance Pilot observation is invalid")
	}
	if o.Arm != PilotArmExperiment && o.SummaryUsage.TotalTokens != 0 {
		return errors.New("only the Experiment arm may contain Summary usage")
	}
	if o.Arm != PilotArmExperiment && len(o.SummaryAttemptFailureCodes) > 0 {
		return errors.New("only the Experiment arm may contain Summary failure codes")
	}
	for _, code := range o.SummaryAttemptFailureCodes {
		if !conversationQualityLabelPattern.MatchString(code) || len(code) > 96 {
			return errors.New("Summary failure code is invalid")
		}
	}
	if o.Arm == PilotArmExperiment {
		if o.SummaryContract == nil || o.SummaryContract.Validate() != nil {
			return errors.New("Experiment observation requires the configured Summary contract")
		}
	} else if o.SummaryContract != nil {
		return errors.New("Current and Baseline observations cannot contain a Summary contract")
	}
	if o.WithinHardWindow && o.ErrorType == "" &&
		(strings.TrimSpace(o.Answer) == "" || o.MainUsage.ModelCalls < 1 || o.MainUsage.TotalTokens < 1 ||
			o.FirstTokenLatencyMillis < 1) {
		return errors.New("successful Pilot observation is incomplete")
	}
	if !o.WithinHardWindow && !conversationQualityLabelPattern.MatchString(o.ErrorType) {
		return errors.New("failed Pilot observation requires a bounded error type")
	}
	if o.Judge != nil && o.Judge.validate() != nil {
		return errors.New("context governance Pilot Judge observation is invalid")
	}
	return nil
}

func ContextGovernancePilotDatasetFingerprint(dataset ContextGovernancePilotDataset) (string, error) {
	encoded, err := json.Marshal(dataset)
	if err != nil {
		return "", fmt.Errorf("marshal context governance Pilot dataset: %w", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded)), nil
}

type ContextGovernancePilotBudget struct {
	MaxProviderCalls    int     `json:"maxProviderCalls"`
	MaxEstimatedCostCNY float64 `json:"maxEstimatedCostCny"`
	Concurrency         int     `json:"concurrency"`
}

func (b ContextGovernancePilotBudget) validate() error {
	if b.MaxProviderCalls < 1 || b.MaxProviderCalls > 200 ||
		math.IsNaN(b.MaxEstimatedCostCNY) || math.IsInf(b.MaxEstimatedCostCNY, 0) ||
		b.MaxEstimatedCostCNY <= 0 || b.MaxEstimatedCostCNY > 10 || b.Concurrency != 1 {
		return errors.New("context governance Pilot budget must stay within 200 calls, 10 CNY, and concurrency 1")
	}
	return nil
}

type ContextGovernancePilotPricing struct {
	MainInputCNYPerMillion          float64 `json:"mainInputCnyPerMillion"`
	MainCachedInputCNYPerMillion    float64 `json:"mainCachedInputCnyPerMillion"`
	MainOutputCNYPerMillion         float64 `json:"mainOutputCnyPerMillion"`
	SummaryInputCNYPerMillion       float64 `json:"summaryInputCnyPerMillion"`
	SummaryCachedInputCNYPerMillion float64 `json:"summaryCachedInputCnyPerMillion"`
	SummaryOutputCNYPerMillion      float64 `json:"summaryOutputCnyPerMillion"`
	JudgeInputCNYPerMillion         float64 `json:"judgeInputCnyPerMillion"`
	JudgeOutputCNYPerMillion        float64 `json:"judgeOutputCnyPerMillion"`
}

func (p ContextGovernancePilotPricing) validate() error {
	for _, value := range []float64{
		p.MainInputCNYPerMillion, p.MainCachedInputCNYPerMillion, p.MainOutputCNYPerMillion,
		p.SummaryInputCNYPerMillion, p.SummaryCachedInputCNYPerMillion, p.SummaryOutputCNYPerMillion,
		p.JudgeInputCNYPerMillion, p.JudgeOutputCNYPerMillion,
	} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 10_000 {
			return errors.New("context governance Pilot pricing is invalid")
		}
	}
	return nil
}

type ContextGovernancePilotPlanOptions struct {
	SummaryCallsPerExperimentCheckpoint int                           `json:"summaryCallsPerExperimentCheckpoint"`
	JudgeCallsPerCheckpoint             int                           `json:"judgeCallsPerCheckpoint"`
	EstimatedMainPromptTokens           int                           `json:"estimatedMainPromptTokens"`
	EstimatedMainOutputTokens           int                           `json:"estimatedMainOutputTokens"`
	EstimatedSummaryPromptTokens        int                           `json:"estimatedSummaryPromptTokens"`
	EstimatedSummaryOutputTokens        int                           `json:"estimatedSummaryOutputTokens"`
	EstimatedJudgePromptTokens          int                           `json:"estimatedJudgePromptTokens"`
	EstimatedJudgeOutputTokens          int                           `json:"estimatedJudgeOutputTokens"`
	Pricing                             ContextGovernancePilotPricing `json:"pricing"`
	Budget                              ContextGovernancePilotBudget  `json:"budget"`
}

type ContextGovernancePilotPlan struct {
	DatasetVersion   string  `json:"datasetVersion"`
	FixtureVersion   string  `json:"fixtureVersion"`
	Scenarios        int     `json:"scenarios"`
	Checkpoints      int     `json:"checkpoints"`
	MainCalls        int     `json:"mainCalls"`
	SummaryCalls     int     `json:"summaryCalls"`
	JudgeCalls       int     `json:"judgeCalls"`
	ProviderCalls    int     `json:"providerCalls"`
	EstimatedCostCNY float64 `json:"estimatedCostCny"`
	MaxProviderCalls int     `json:"maxProviderCalls"`
	MaxCostCNY       float64 `json:"maxCostCny"`
	Concurrency      int     `json:"concurrency"`
}

// DefaultContextGovernancePilotPlanOptions is shared by the provider-free
// evaluator and the explicit observer. Prices are conservative preflight
// assumptions rather than a provider invoice.
func DefaultContextGovernancePilotPlanOptions() ContextGovernancePilotPlanOptions {
	return ContextGovernancePilotPlanOptions{
		SummaryCallsPerExperimentCheckpoint: 1,
		EstimatedMainPromptTokens:           100_000,
		EstimatedMainOutputTokens:           5_000,
		EstimatedSummaryPromptTokens:        50_000,
		EstimatedSummaryOutputTokens:        4_000,
		Pricing: ContextGovernancePilotPricing{
			MainInputCNYPerMillion: 1, MainOutputCNYPerMillion: 4,
			SummaryInputCNYPerMillion: 0.5, SummaryOutputCNYPerMillion: 2,
			JudgeInputCNYPerMillion: 1, JudgeOutputCNYPerMillion: 4,
		},
		Budget: ContextGovernancePilotBudget{
			MaxProviderCalls: 200, MaxEstimatedCostCNY: 10, Concurrency: 1,
		},
	}
}

func BuildContextGovernancePilotPlan(
	dataset ContextGovernancePilotDataset,
	options ContextGovernancePilotPlanOptions,
) (ContextGovernancePilotPlan, error) {
	if err := dataset.Validate(); err != nil {
		return ContextGovernancePilotPlan{}, err
	}
	if err := options.Budget.validate(); err != nil {
		return ContextGovernancePilotPlan{}, err
	}
	if err := options.Pricing.validate(); err != nil {
		return ContextGovernancePilotPlan{}, err
	}
	if options.SummaryCallsPerExperimentCheckpoint < 0 || options.SummaryCallsPerExperimentCheckpoint > 5 ||
		options.JudgeCallsPerCheckpoint < 0 || options.JudgeCallsPerCheckpoint > 3 {
		return ContextGovernancePilotPlan{}, errors.New("context governance Pilot call multiplier is invalid")
	}
	estimates := []int{
		options.EstimatedMainPromptTokens, options.EstimatedMainOutputTokens,
		options.EstimatedSummaryPromptTokens, options.EstimatedSummaryOutputTokens,
		options.EstimatedJudgePromptTokens, options.EstimatedJudgeOutputTokens,
	}
	for _, value := range estimates {
		if value < 0 || value > 10_000_000 {
			return ContextGovernancePilotPlan{}, errors.New("context governance Pilot Token estimate is invalid")
		}
	}
	checkpoints := len(dataset.Scenarios) * contextGovernancePilotCheckpointCount
	plan := ContextGovernancePilotPlan{
		DatasetVersion: dataset.DatasetVersion, FixtureVersion: dataset.FixtureVersion,
		Scenarios: len(dataset.Scenarios), Checkpoints: checkpoints,
		MainCalls:        checkpoints * 3,
		SummaryCalls:     checkpoints * options.SummaryCallsPerExperimentCheckpoint,
		JudgeCalls:       checkpoints * 3 * options.JudgeCallsPerCheckpoint,
		MaxProviderCalls: options.Budget.MaxProviderCalls, MaxCostCNY: options.Budget.MaxEstimatedCostCNY,
		Concurrency: options.Budget.Concurrency,
	}
	plan.ProviderCalls = plan.MainCalls + plan.SummaryCalls + plan.JudgeCalls
	plan.EstimatedCostCNY =
		modelCost(plan.MainCalls, options.EstimatedMainPromptTokens, options.EstimatedMainOutputTokens,
			options.Pricing.MainInputCNYPerMillion, options.Pricing.MainOutputCNYPerMillion) +
			modelCost(plan.SummaryCalls, options.EstimatedSummaryPromptTokens, options.EstimatedSummaryOutputTokens,
				options.Pricing.SummaryInputCNYPerMillion, options.Pricing.SummaryOutputCNYPerMillion) +
			modelCost(plan.JudgeCalls, options.EstimatedJudgePromptTokens, options.EstimatedJudgeOutputTokens,
				options.Pricing.JudgeInputCNYPerMillion, options.Pricing.JudgeOutputCNYPerMillion)
	if plan.ProviderCalls > plan.MaxProviderCalls {
		return ContextGovernancePilotPlan{}, fmt.Errorf(
			"provider call budget exceeded before execution: planned %d, max %d", plan.ProviderCalls, plan.MaxProviderCalls,
		)
	}
	if plan.EstimatedCostCNY > plan.MaxCostCNY {
		return ContextGovernancePilotPlan{}, fmt.Errorf(
			"provider cost budget exceeded before execution: planned %.4f CNY, max %.4f CNY",
			plan.EstimatedCostCNY, plan.MaxCostCNY,
		)
	}
	return plan, nil
}

func modelCost(calls, prompt, completion int, inputPrice, outputPrice float64) float64 {
	return float64(calls) * (float64(prompt)*inputPrice + float64(completion)*outputPrice) / 1_000_000
}

type ContextGovernancePilotMetric struct {
	Matched int     `json:"matched"`
	Total   int     `json:"total"`
	Rate    float64 `json:"rate"`
}

type ContextGovernancePilotQuality struct {
	Runs                     int                          `json:"runs"`
	FactRecall               ContextGovernancePilotMetric `json:"factRecall"`
	DecisionRecall           ContextGovernancePilotMetric `json:"decisionRecall"`
	CorrectionAccuracy       ContextGovernancePilotMetric `json:"correctionAccuracy"`
	TodoStateAccuracy        ContextGovernancePilotMetric `json:"todoStateAccuracy"`
	EvidenceReferenceRecall  ContextGovernancePilotMetric `json:"evidenceReferenceRecall"`
	HallucinationRate        float64                      `json:"hallucinationRate"`
	JudgedRuns               int                          `json:"judgedRuns"`
	AverageFaithfulness      float64                      `json:"averageFaithfulness"`
	AverageAnswerCorrectness float64                      `json:"averageAnswerCorrectness"`
}

type ContextGovernancePilotEpochChurn struct {
	Current    int `json:"current"`
	Baseline   int `json:"baseline"`
	Experiment int `json:"experiment"`
}

// ContextGovernancePilotAccounting keeps every observed provider call visible,
// including failed retries and observations excluded from paired comparison.
type ContextGovernancePilotAccounting struct {
	Observations     int                         `json:"observations"`
	MainUsage        ContextGovernancePilotUsage `json:"mainUsage"`
	SummaryUsage     ContextGovernancePilotUsage `json:"summaryUsage"`
	AllModelTokens   int                         `json:"allModelTokens"`
	EstimatedCostCNY float64                     `json:"estimatedCostCny"`
}

type ContextGovernancePilotReport struct {
	DatasetVersion                    string                                                         `json:"datasetVersion"`
	FixtureVersion                    string                                                         `json:"fixtureVersion"`
	Contract                          ContextGovernancePilotContract                                 `json:"contract"`
	SummaryContract                   *ContextGovernancePilotSummaryContract                         `json:"summaryContract,omitempty"`
	ExpectedRuns                      int                                                            `json:"expectedRuns"`
	ObservedRuns                      int                                                            `json:"observedRuns"`
	FailedRuns                        int                                                            `json:"failedRuns"`
	ComparablePairs                   int                                                            `json:"comparablePairs"`
	BaselineOverWindowCount           int                                                            `json:"baselineOverWindowCount"`
	BaselineContinuationRate          float64                                                        `json:"baselineContinuationRate"`
	ExperimentOverWindowCount         int                                                            `json:"experimentOverWindowCount"`
	ProviderHardWindowViolationCount  int                                                            `json:"providerHardWindowViolationCount"`
	BaselineAllModelTokens            int                                                            `json:"baselineAllModelTokens"`
	ExperimentAllModelTokens          int                                                            `json:"experimentAllModelTokens"`
	RawTokenReduction                 float64                                                        `json:"rawTokenReduction"`
	BaselineMainPromptTokens          int                                                            `json:"baselineMainPromptTokens"`
	ExperimentMainPromptTokens        int                                                            `json:"experimentMainPromptTokens"`
	MainPromptReduction               float64                                                        `json:"mainPromptReduction"`
	SummaryOverheadTokens             int                                                            `json:"summaryOverheadTokens"`
	ObservedAccountingByArm           map[ContextGovernancePilotArm]ContextGovernancePilotAccounting `json:"observedAccountingByArm"`
	FailedAccountingByArm             map[ContextGovernancePilotArm]ContextGovernancePilotAccounting `json:"failedAccountingByArm"`
	IncomparableAccountingByArm       map[ContextGovernancePilotArm]ContextGovernancePilotAccounting `json:"incomparableAccountingByArm"`
	CacheHitRatio                     float64                                                        `json:"cacheHitRatio"`
	CacheAdjustedCostCNY              float64                                                        `json:"cacheAdjustedCostCny"`
	BaselineFirstTokenLatencyMillis   float64                                                        `json:"baselineFirstTokenLatencyMillis"`
	ExperimentFirstTokenLatencyMillis float64                                                        `json:"experimentFirstTokenLatencyMillis"`
	PromptEpochChurn                  ContextGovernancePilotEpochChurn                               `json:"promptEpochChurn"`
	EstimatorP95UnderestimateRatio    float64                                                        `json:"estimatorP95UnderestimateRatio"`
	QualityByArm                      map[ContextGovernancePilotArm]ContextGovernancePilotQuality    `json:"qualityByArm"`
	AnswerCorrectnessDelta            float64                                                        `json:"answerCorrectnessDelta"`
	GateFailures                      []string                                                       `json:"gateFailures"`
}

func EvaluateContextGovernancePilot(
	dataset ContextGovernancePilotDataset,
	observations []ContextGovernancePilotObservation,
	pricing ContextGovernancePilotPricing,
) (ContextGovernancePilotReport, error) {
	if err := dataset.Validate(); err != nil {
		return ContextGovernancePilotReport{}, err
	}
	if err := pricing.validate(); err != nil {
		return ContextGovernancePilotReport{}, err
	}
	checkpointIndex := make(map[string]ContextGovernancePilotCheckpoint)
	scenarioByCheckpoint := make(map[string]string)
	checkpointOrder := make(map[string]int)
	for _, scenario := range dataset.Scenarios {
		for index, checkpoint := range scenario.Checkpoints {
			checkpointIndex[checkpoint.CheckpointID] = checkpoint
			scenarioByCheckpoint[checkpoint.CheckpointID] = scenario.ScenarioID
			checkpointOrder[checkpoint.CheckpointID] = index
		}
	}
	report := ContextGovernancePilotReport{
		DatasetVersion: dataset.DatasetVersion, FixtureVersion: dataset.FixtureVersion,
		ExpectedRuns: len(checkpointIndex) * 3,
		QualityByArm: map[ContextGovernancePilotArm]ContextGovernancePilotQuality{
			PilotArmCurrent: {}, PilotArmBaseline: {}, PilotArmExperiment: {},
		},
		ObservedAccountingByArm:     newPilotAccountingByArm(),
		FailedAccountingByArm:       newPilotAccountingByArm(),
		IncomparableAccountingByArm: newPilotAccountingByArm(),
	}
	expectedFixtureFingerprint, err := ContextGovernancePilotDatasetFingerprint(dataset)
	if err != nil {
		return ContextGovernancePilotReport{}, err
	}
	if len(observations) != report.ExpectedRuns {
		return ContextGovernancePilotReport{}, fmt.Errorf(
			"Pilot requires exactly %d observations, got %d", report.ExpectedRuns, len(observations),
		)
	}
	byKey := make(map[contextGovernancePilotObservationKey]ContextGovernancePilotObservation, len(observations))
	seenRuns := make(map[string]struct{}, len(observations))
	var commonContract *ContextGovernancePilotContract
	var commonSummaryContract *ContextGovernancePilotSummaryContract
	for index, observation := range observations {
		if err := observation.Validate(); err != nil {
			return ContextGovernancePilotReport{}, fmt.Errorf("observation %d: %w", index, err)
		}
		if observation.DatasetVersion != dataset.DatasetVersion || observation.FixtureVersion != dataset.FixtureVersion ||
			observation.FixtureFingerprint != expectedFixtureFingerprint ||
			scenarioByCheckpoint[observation.CheckpointID] != observation.ScenarioID {
			return ContextGovernancePilotReport{}, fmt.Errorf("observation %q does not match the Pilot fixture", observation.RunID)
		}
		if _, duplicate := seenRuns[observation.RunID]; duplicate {
			return ContextGovernancePilotReport{}, fmt.Errorf("duplicate runId %q", observation.RunID)
		}
		seenRuns[observation.RunID] = struct{}{}
		key := contextGovernancePilotObservationKey{checkpoint: observation.CheckpointID, arm: observation.Arm}
		if _, duplicate := byKey[key]; duplicate {
			return ContextGovernancePilotReport{}, fmt.Errorf("duplicate %s observation for %s", observation.Arm, observation.CheckpointID)
		}
		byKey[key] = observation
		if commonContract == nil {
			contract := observation.Contract
			commonContract = &contract
		} else if *commonContract != observation.Contract {
			return ContextGovernancePilotReport{}, errors.New("Pilot arms do not share one model, reasoning, Tool, output, and Prompt contract")
		}
		if observation.SummaryContract != nil {
			if commonSummaryContract == nil {
				contract := *observation.SummaryContract
				commonSummaryContract = &contract
			} else if *commonSummaryContract != *observation.SummaryContract {
				return ContextGovernancePilotReport{}, errors.New("Experiment observations do not share one Summary model/Profile/Prompt contract")
			}
		}
		quality := report.QualityByArm[observation.Arm]
		scoreContextGovernancePilot(checkpointIndex[observation.CheckpointID].Gold, observation, &quality)
		report.QualityByArm[observation.Arm] = quality
		observedAccounting := report.ObservedAccountingByArm[observation.Arm]
		addPilotObservationAccounting(&observedAccounting, observation)
		report.ObservedAccountingByArm[observation.Arm] = observedAccounting
		if observation.Arm == PilotArmExperiment && !observation.WithinHardWindow {
			report.ExperimentOverWindowCount++
		}
		if !observation.WithinHardWindow && observation.MainUsage.ModelCalls > 0 {
			report.ProviderHardWindowViolationCount++
		}
		if pilotObservationFailed(observation) {
			report.FailedRuns++
			failedAccounting := report.FailedAccountingByArm[observation.Arm]
			addPilotObservationAccounting(&failedAccounting, observation)
			report.FailedAccountingByArm[observation.Arm] = failedAccounting
		}
	}
	report.ObservedRuns = len(observations)
	if commonContract != nil {
		report.Contract = *commonContract
	}
	report.SummaryContract = commonSummaryContract
	for arm, quality := range report.QualityByArm {
		finalizeContextGovernancePilotQuality(&quality)
		report.QualityByArm[arm] = quality
	}

	baselineObserved, baselineWithin := 0, 0
	var baselineLatency, experimentLatency int64
	var latencyPairs int
	var estimatorUnderestimates []float64
	for checkpointID := range checkpointIndex {
		baseline, baselineOK := byKey[contextGovernancePilotObservationKey{checkpoint: checkpointID, arm: PilotArmBaseline}]
		experiment, experimentOK := byKey[contextGovernancePilotObservationKey{checkpoint: checkpointID, arm: PilotArmExperiment}]
		if baselineOK {
			baselineObserved++
			if baseline.WithinHardWindow {
				baselineWithin++
			} else {
				report.BaselineOverWindowCount++
			}
		}
		if !baselineOK || !experimentOK || !pilotObservationSucceeded(baseline) ||
			!pilotObservationSucceeded(experiment) {
			if baselineOK {
				accounting := report.IncomparableAccountingByArm[PilotArmBaseline]
				addPilotObservationAccounting(&accounting, baseline)
				report.IncomparableAccountingByArm[PilotArmBaseline] = accounting
			}
			if experimentOK {
				accounting := report.IncomparableAccountingByArm[PilotArmExperiment]
				addPilotObservationAccounting(&accounting, experiment)
				report.IncomparableAccountingByArm[PilotArmExperiment] = accounting
			}
			continue
		}
		report.ComparablePairs++
		report.BaselineAllModelTokens += baseline.MainUsage.TotalTokens
		report.ExperimentAllModelTokens += experiment.MainUsage.TotalTokens + experiment.SummaryUsage.TotalTokens
		report.BaselineMainPromptTokens += baseline.MainUsage.PromptTokens
		report.ExperimentMainPromptTokens += experiment.MainUsage.PromptTokens
		report.SummaryOverheadTokens += experiment.SummaryUsage.TotalTokens
		baselineLatency += baseline.FirstTokenLatencyMillis
		experimentLatency += experiment.FirstTokenLatencyMillis
		latencyPairs++
		if experiment.MainUsage.PromptTokens > 0 && experiment.EstimatedPromptTokens > 0 {
			ratio := float64(experiment.MainUsage.PromptTokens-experiment.EstimatedPromptTokens) /
				float64(experiment.MainUsage.PromptTokens)
			if ratio > 0 {
				estimatorUnderestimates = append(estimatorUnderestimates, ratio)
			} else {
				estimatorUnderestimates = append(estimatorUnderestimates, 0)
			}
		}
	}
	if baselineObserved > 0 {
		report.BaselineContinuationRate = float64(baselineWithin) / float64(baselineObserved)
	}
	if report.BaselineAllModelTokens > 0 {
		report.RawTokenReduction = reductionRate(int64(report.BaselineAllModelTokens), int64(report.ExperimentAllModelTokens))
	}
	if report.BaselineMainPromptTokens > 0 {
		report.MainPromptReduction = reductionRate(int64(report.BaselineMainPromptTokens), int64(report.ExperimentMainPromptTokens))
	}
	if latencyPairs > 0 {
		report.BaselineFirstTokenLatencyMillis = float64(baselineLatency) / float64(latencyPairs)
		report.ExperimentFirstTokenLatencyMillis = float64(experimentLatency) / float64(latencyPairs)
	}
	report.EstimatorP95UnderestimateRatio = nearestRankP95(estimatorUnderestimates)
	report.PromptEpochChurn = pilotPromptEpochChurn(byKey, checkpointOrder, scenarioByCheckpoint)
	finalizePilotAccounting(report.ObservedAccountingByArm, pricing)
	finalizePilotAccounting(report.FailedAccountingByArm, pricing)
	finalizePilotAccounting(report.IncomparableAccountingByArm, pricing)
	report.CacheHitRatio, report.CacheAdjustedCostCNY = pilotCacheAndCost(byKey, pricing)
	baselineQuality := report.QualityByArm[PilotArmBaseline]
	experimentQuality := report.QualityByArm[PilotArmExperiment]
	if baselineQuality.JudgedRuns > 0 && experimentQuality.JudgedRuns > 0 {
		report.AnswerCorrectnessDelta = experimentQuality.AverageAnswerCorrectness - baselineQuality.AverageAnswerCorrectness
	}
	report.GateFailures = contextGovernancePilotGateFailures(report)
	return report, nil
}

func pilotObservationSucceeded(observation ContextGovernancePilotObservation) bool {
	return observation.WithinHardWindow && observation.ErrorType == ""
}

func pilotObservationFailed(observation ContextGovernancePilotObservation) bool {
	return observation.ErrorType != "" && observation.ErrorType != "prompt_window_exceeded"
}

func newPilotAccountingByArm() map[ContextGovernancePilotArm]ContextGovernancePilotAccounting {
	return map[ContextGovernancePilotArm]ContextGovernancePilotAccounting{
		PilotArmCurrent: {}, PilotArmBaseline: {}, PilotArmExperiment: {},
	}
}

func addPilotObservationAccounting(
	accounting *ContextGovernancePilotAccounting,
	observation ContextGovernancePilotObservation,
) {
	accounting.Observations++
	addPilotUsage(&accounting.MainUsage, observation.MainUsage)
	addPilotUsage(&accounting.SummaryUsage, observation.SummaryUsage)
}

func addPilotUsage(total *ContextGovernancePilotUsage, usage ContextGovernancePilotUsage) {
	total.ModelCalls += usage.ModelCalls
	total.PromptTokens += usage.PromptTokens
	total.CompletionTokens += usage.CompletionTokens
	total.TotalTokens += usage.TotalTokens
	total.CachedTokens += usage.CachedTokens
	total.ReasoningTokens += usage.ReasoningTokens
}

func finalizePilotAccounting(
	accountingByArm map[ContextGovernancePilotArm]ContextGovernancePilotAccounting,
	pricing ContextGovernancePilotPricing,
) {
	for arm, accounting := range accountingByArm {
		accounting.AllModelTokens = accounting.MainUsage.TotalTokens + accounting.SummaryUsage.TotalTokens
		accounting.EstimatedCostCNY = usageCost(accounting.MainUsage, pricing.MainInputCNYPerMillion,
			pricing.MainCachedInputCNYPerMillion, pricing.MainOutputCNYPerMillion) +
			usageCost(accounting.SummaryUsage, pricing.SummaryInputCNYPerMillion,
				pricing.SummaryCachedInputCNYPerMillion, pricing.SummaryOutputCNYPerMillion)
		accountingByArm[arm] = accounting
	}
}

func scoreContextGovernancePilot(
	gold ContextGovernancePilotGold,
	observation ContextGovernancePilotObservation,
	quality *ContextGovernancePilotQuality,
) {
	if quality == nil || !observation.WithinHardWindow || observation.ErrorType != "" {
		return
	}
	quality.Runs++
	answer := strings.ToLower(observation.Answer)
	mergePilotTermMetric(&quality.FactRecall, answer, gold.Facts)
	mergePilotTermMetric(&quality.DecisionRecall, answer, gold.Decisions)
	for _, correction := range gold.Corrections {
		quality.CorrectionAccuracy.Total++
		if containsAllFold(answer, correction.CurrentTerms) && containsNoneFold(answer, correction.SupersededTerms) {
			quality.CorrectionAccuracy.Matched++
		}
	}
	for _, todo := range gold.Todos {
		quality.TodoStateAccuracy.Total++
		if containsAllFold(answer, todo.ContentTerms) && containsAllFold(answer, todo.StatusTerms) {
			quality.TodoStateAccuracy.Matched++
		}
	}
	mergePilotTermMetric(&quality.EvidenceReferenceRecall, answer, gold.EvidenceReferences)
	for _, forbidden := range gold.ForbiddenClaims {
		if strings.Contains(answer, strings.ToLower(forbidden)) {
			quality.HallucinationRate++
			break
		}
	}
	if observation.Judge != nil {
		quality.JudgedRuns++
		quality.AverageFaithfulness += observation.Judge.Faithfulness
		quality.AverageAnswerCorrectness += observation.Judge.AnswerCorrectness
	}
}

func mergePilotTermMetric(metric *ContextGovernancePilotMetric, answer string, terms []string) {
	for _, term := range terms {
		metric.Total++
		if strings.Contains(answer, strings.ToLower(term)) {
			metric.Matched++
		}
	}
}

func containsAllFold(answer string, terms []string) bool {
	for _, term := range terms {
		if !strings.Contains(answer, strings.ToLower(term)) {
			return false
		}
	}
	return true
}

func containsNoneFold(answer string, terms []string) bool {
	for _, term := range terms {
		if strings.Contains(answer, strings.ToLower(term)) {
			return false
		}
	}
	return true
}

func finalizeContextGovernancePilotQuality(quality *ContextGovernancePilotQuality) {
	for _, metric := range []*ContextGovernancePilotMetric{
		&quality.FactRecall, &quality.DecisionRecall, &quality.CorrectionAccuracy,
		&quality.TodoStateAccuracy, &quality.EvidenceReferenceRecall,
	} {
		if metric.Total > 0 {
			metric.Rate = float64(metric.Matched) / float64(metric.Total)
		}
	}
	if quality.Runs > 0 {
		quality.HallucinationRate /= float64(quality.Runs)
	}
	if quality.JudgedRuns > 0 {
		quality.AverageFaithfulness /= float64(quality.JudgedRuns)
		quality.AverageAnswerCorrectness /= float64(quality.JudgedRuns)
	}
}

func nearestRankP95(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	index := int(math.Ceil(0.95*float64(len(ordered)))) - 1
	return ordered[max(0, index)]
}

func pilotPromptEpochChurn(
	observations map[contextGovernancePilotObservationKey]ContextGovernancePilotObservation,
	checkpointOrder map[string]int,
	scenarioByCheckpoint map[string]string,
) ContextGovernancePilotEpochChurn {
	result := ContextGovernancePilotEpochChurn{}
	for _, arm := range []ContextGovernancePilotArm{PilotArmCurrent, PilotArmBaseline, PilotArmExperiment} {
		byScenario := make(map[string][]ContextGovernancePilotObservation)
		for key, observation := range observations {
			if key.arm == arm && observation.PromptEpochID != "" {
				byScenario[scenarioByCheckpoint[key.checkpoint]] = append(byScenario[scenarioByCheckpoint[key.checkpoint]], observation)
			}
		}
		churn := 0
		for _, items := range byScenario {
			sort.Slice(items, func(i, j int) bool {
				return checkpointOrder[items[i].CheckpointID] < checkpointOrder[items[j].CheckpointID]
			})
			for index := 1; index < len(items); index++ {
				if items[index].PromptEpochID != items[index-1].PromptEpochID {
					churn++
				}
			}
		}
		switch arm {
		case PilotArmCurrent:
			result.Current = churn
		case PilotArmBaseline:
			result.Baseline = churn
		case PilotArmExperiment:
			result.Experiment = churn
		}
	}
	return result
}

func pilotCacheAndCost(
	observations map[contextGovernancePilotObservationKey]ContextGovernancePilotObservation,
	pricing ContextGovernancePilotPricing,
) (float64, float64) {
	prompt, cached := 0, 0
	cost := 0.0
	for key, observation := range observations {
		if key.arm != PilotArmExperiment || !observation.WithinHardWindow {
			continue
		}
		prompt += observation.MainUsage.PromptTokens + observation.SummaryUsage.PromptTokens
		cached += observation.MainUsage.CachedTokens + observation.SummaryUsage.CachedTokens
		cost += usageCost(observation.MainUsage, pricing.MainInputCNYPerMillion,
			pricing.MainCachedInputCNYPerMillion, pricing.MainOutputCNYPerMillion)
		cost += usageCost(observation.SummaryUsage, pricing.SummaryInputCNYPerMillion,
			pricing.SummaryCachedInputCNYPerMillion, pricing.SummaryOutputCNYPerMillion)
	}
	if prompt == 0 {
		return 0, cost
	}
	return float64(cached) / float64(prompt), cost
}

func usageCost(usage ContextGovernancePilotUsage, inputPrice, cachedInputPrice, outputPrice float64) float64 {
	uncached := usage.PromptTokens - usage.CachedTokens
	return (float64(uncached)*inputPrice + float64(usage.CachedTokens)*cachedInputPrice +
		float64(usage.CompletionTokens)*outputPrice) / 1_000_000
}

func contextGovernancePilotGateFailures(report ContextGovernancePilotReport) []string {
	quality := report.QualityByArm[PilotArmExperiment]
	var failures []string
	checks := []struct {
		name      string
		metric    ContextGovernancePilotMetric
		threshold float64
		equal     bool
	}{
		{"fact_recall", quality.FactRecall, 0.95, false},
		{"decision_recall", quality.DecisionRecall, 0.95, false},
		{"correction_accuracy", quality.CorrectionAccuracy, 1, true},
		{"todo_state_accuracy", quality.TodoStateAccuracy, 0.95, false},
		{"evidence_reference_recall", quality.EvidenceReferenceRecall, 0.95, false},
	}
	for _, check := range checks {
		if check.metric.Total == 0 {
			continue
		}
		if check.equal && check.metric.Rate != check.threshold || !check.equal && check.metric.Rate < check.threshold {
			failures = append(failures, check.name)
		}
	}
	if quality.Runs > 0 && quality.HallucinationRate > 0.02 {
		failures = append(failures, "hallucination_rate")
	}
	if report.QualityByArm[PilotArmBaseline].JudgedRuns > 0 && quality.JudgedRuns > 0 &&
		report.AnswerCorrectnessDelta < -0.02 {
		failures = append(failures, "answer_correctness_delta")
	}
	if report.ComparablePairs > 0 && report.RawTokenReduction < 0.60 {
		failures = append(failures, "raw_token_reduction")
	}
	if report.EstimatorP95UnderestimateRatio > 0.05 {
		failures = append(failures, "token_estimator_p95")
	}
	if report.ProviderHardWindowViolationCount > 0 {
		failures = append(failures, "hard_window_violation")
	}
	if report.FailedRuns > 0 {
		failures = append(failures, "run_failure")
	}
	slices.Sort(failures)
	return failures
}
