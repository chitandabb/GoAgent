package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type PilotModelCallClass string

const (
	PilotMainModelCall    PilotModelCallClass = "main"
	PilotSummaryModelCall PilotModelCallClass = "summary"
)

type pilotBudgetExceededError struct{}

func (pilotBudgetExceededError) Error() string                 { return "Pilot model call budget exceeded" }
func (pilotBudgetExceededError) CompactionFailureCode() string { return "local_budget_exceeded" }
func (pilotBudgetExceededError) NonRetryableCompaction() bool  { return true }

func newPilotBudgetExceededError() error { return pilotBudgetExceededError{} }

// PilotModelCallBudget reserves every main, Summary, and retry call before it
// reaches a Provider. It never refunds conservative estimates during one run.
type PilotModelCallBudget struct {
	mu                          sync.Mutex
	maxCalls                    int
	maxMainCalls                int
	maxSummaryCalls             int
	maxMainPromptTokens         int
	maxSummaryPromptTokens      int
	maxCostCNY                  float64
	usedCalls                   int
	usedMainCalls               int
	usedSummaryCalls            int
	reservedMainPromptTokens    int
	reservedSummaryPromptTokens int
	reservedCostCNY             float64
	mainInputPrice              float64
	mainOutputPrice             float64
	summaryInputPrice           float64
	summaryOutputPrice          float64
	mainOutputReserve           int
	summaryOutputReserve        int
}

// PilotModelCallLimits are enforced immediately before each Provider call.
// Prompt token fields are cumulative conservative estimates for one process
// run; reservations are never refunded after failed requests.
type PilotModelCallLimits struct {
	MaxProviderCalls                int
	MaxMainCalls                    int
	MaxSummaryCalls                 int
	MaxEstimatedMainPromptTokens    int
	MaxEstimatedSummaryPromptTokens int
	MaxEstimatedCostCNY             float64
}

func (l PilotModelCallLimits) validate() error {
	if l.MaxProviderCalls < 1 || l.MaxProviderCalls > 200 ||
		l.MaxMainCalls < 0 || l.MaxMainCalls > l.MaxProviderCalls ||
		l.MaxSummaryCalls < 0 || l.MaxSummaryCalls > l.MaxProviderCalls ||
		l.MaxMainCalls+l.MaxSummaryCalls < 1 ||
		l.MaxEstimatedMainPromptTokens < 0 || l.MaxEstimatedMainPromptTokens > 10_000_000 ||
		l.MaxEstimatedSummaryPromptTokens < 0 || l.MaxEstimatedSummaryPromptTokens > 10_000_000 ||
		l.MaxEstimatedCostCNY <= 0 || l.MaxEstimatedCostCNY > 10 {
		return errors.New("Pilot model call limits are invalid")
	}
	if (l.MaxMainCalls > 0 && l.MaxEstimatedMainPromptTokens < 1) ||
		(l.MaxSummaryCalls > 0 && l.MaxEstimatedSummaryPromptTokens < 1) {
		return errors.New("Pilot model call Token limits are invalid")
	}
	return nil
}

func NewPilotModelCallBudget(
	plan ContextGovernancePilotPlan,
	pricing ContextGovernancePilotPricing,
	mainOutputReserve, summaryOutputReserve int,
) (*PilotModelCallBudget, error) {
	return NewPilotModelCallBudgetWithLimits(PilotModelCallLimits{
		MaxProviderCalls: plan.MaxProviderCalls,
		MaxMainCalls:     plan.MaxProviderCalls, MaxSummaryCalls: plan.MaxProviderCalls,
		MaxEstimatedMainPromptTokens: 10_000_000, MaxEstimatedSummaryPromptTokens: 10_000_000,
		MaxEstimatedCostCNY: plan.MaxCostCNY,
	}, pricing, mainOutputReserve, summaryOutputReserve)
}

func NewPilotModelCallBudgetWithLimits(
	limits PilotModelCallLimits,
	pricing ContextGovernancePilotPricing,
	mainOutputReserve, summaryOutputReserve int,
) (*PilotModelCallBudget, error) {
	if limits.validate() != nil || pricing.validate() != nil || mainOutputReserve < 1 || summaryOutputReserve < 1 {
		return nil, errors.New("Pilot model call budget configuration is invalid")
	}
	return &PilotModelCallBudget{
		maxCalls:     limits.MaxProviderCalls,
		maxMainCalls: limits.MaxMainCalls, maxSummaryCalls: limits.MaxSummaryCalls,
		maxMainPromptTokens:    limits.MaxEstimatedMainPromptTokens,
		maxSummaryPromptTokens: limits.MaxEstimatedSummaryPromptTokens,
		maxCostCNY:             limits.MaxEstimatedCostCNY,
		mainInputPrice:         pricing.MainInputCNYPerMillion, mainOutputPrice: pricing.MainOutputCNYPerMillion,
		summaryInputPrice: pricing.SummaryInputCNYPerMillion, summaryOutputPrice: pricing.SummaryOutputCNYPerMillion,
		mainOutputReserve: mainOutputReserve, summaryOutputReserve: summaryOutputReserve,
	}, nil
}

func (b *PilotModelCallBudget) Reserve(class PilotModelCallClass, promptTokens int) error {
	if b == nil || promptTokens < 0 || (class != PilotMainModelCall && class != PilotSummaryModelCall) {
		return errors.New("Pilot model call budget is unavailable")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.usedCalls >= b.maxCalls {
		return newPilotBudgetExceededError()
	}
	inputPrice, outputPrice, outputReserve := b.mainInputPrice, b.mainOutputPrice, b.mainOutputReserve
	if class == PilotSummaryModelCall {
		if b.usedSummaryCalls >= b.maxSummaryCalls {
			return newPilotBudgetExceededError()
		}
		if b.reservedSummaryPromptTokens+promptTokens > b.maxSummaryPromptTokens {
			return newPilotBudgetExceededError()
		}
		inputPrice, outputPrice, outputReserve = b.summaryInputPrice, b.summaryOutputPrice, b.summaryOutputReserve
	} else {
		if b.usedMainCalls >= b.maxMainCalls {
			return newPilotBudgetExceededError()
		}
		if b.reservedMainPromptTokens+promptTokens > b.maxMainPromptTokens {
			return newPilotBudgetExceededError()
		}
	}
	cost := (float64(promptTokens)*inputPrice + float64(outputReserve)*outputPrice) / 1_000_000
	if b.reservedCostCNY+cost > b.maxCostCNY+1e-9 {
		return newPilotBudgetExceededError()
	}
	b.usedCalls++
	if class == PilotSummaryModelCall {
		b.usedSummaryCalls++
		b.reservedSummaryPromptTokens += promptTokens
	} else {
		b.usedMainCalls++
		b.reservedMainPromptTokens += promptTokens
	}
	b.reservedCostCNY += cost
	return nil
}

func (b *PilotModelCallBudget) Snapshot() (calls int, reservedCostCNY float64) {
	if b == nil {
		return 0, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.usedCalls, b.reservedCostCNY
}

type PilotMeasuredModelSnapshot struct {
	Usage                   ContextGovernancePilotUsage
	LastFirstTokenLatencyMS int64
}

type pilotMeasuredModelState struct {
	mu                      sync.Mutex
	usage                   ContextGovernancePilotUsage
	lastFirstTokenLatencyMS int64
}

// PilotMeasuredModel records Provider attempts independently from Eino's
// callbacks. WithTools shares the same state so the per-run Agent binding is
// still visible to the observer.
type PilotMeasuredModel struct {
	inner           model.ToolCallingChatModel
	class           PilotModelCallClass
	budget          *PilotModelCallBudget
	state           *pilotMeasuredModelState
	boundToolTokens int
}

func NewPilotMeasuredModel(
	inner model.ToolCallingChatModel,
	class PilotModelCallClass,
	budget *PilotModelCallBudget,
) (*PilotMeasuredModel, error) {
	if inner == nil || (class != PilotMainModelCall && class != PilotSummaryModelCall) || budget == nil {
		return nil, errors.New("Pilot measured model configuration is invalid")
	}
	return &PilotMeasuredModel{inner: inner, class: class, budget: budget, state: &pilotMeasuredModelState{}}, nil
}

func (m *PilotMeasuredModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	if m == nil || m.inner == nil || m.state == nil {
		return nil, errors.New("Pilot measured model is unavailable")
	}
	toolTokens, err := estimatePilotToolInfos(tools)
	if err != nil {
		return nil, err
	}
	bound, err := m.inner.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &PilotMeasuredModel{
		inner: bound, class: m.class, budget: m.budget, state: m.state, boundToolTokens: toolTokens,
	}, nil
}

func (m *PilotMeasuredModel) Generate(
	ctx context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.Message, error) {
	started := time.Now()
	if err := m.reserve(input, opts); err != nil {
		return nil, err
	}
	result, err := m.inner.Generate(ctx, input, opts...)
	m.record(pilotUsageFromMessage(result), meaningfulPilotMessage(result), started)
	return result, err
}

func (m *PilotMeasuredModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	started := time.Now()
	if err := m.reserve(input, opts); err != nil {
		return nil, err
	}
	source, err := m.inner.Stream(ctx, input, opts...)
	if err != nil {
		m.record(ContextGovernancePilotUsage{}, false, started)
		return nil, err
	}
	reader, writer := schema.Pipe[*schema.Message](1)
	go func() {
		defer source.Close()
		defer writer.Close()
		var usage ContextGovernancePilotUsage
		firstSeen := false
		firstLatency := time.Duration(0)
		defer func() { m.recordWithLatency(usage, firstSeen, firstLatency) }()
		for {
			chunk, recvErr := source.Recv()
			if errors.Is(recvErr, io.EOF) {
				return
			}
			if recvErr != nil {
				writer.Send(nil, recvErr)
				return
			}
			if !firstSeen && meaningfulPilotMessage(chunk) {
				firstSeen, firstLatency = true, time.Since(started)
			}
			if current := pilotUsageFromMessage(chunk); current.TotalTokens > 0 || current.PromptTokens > 0 {
				usage = current
			}
			if writer.Send(chunk, nil) {
				return
			}
		}
	}()
	return reader, nil
}

func (m *PilotMeasuredModel) reserve(input []*schema.Message, opts []model.Option) error {
	if m == nil || m.inner == nil || m.state == nil || m.budget == nil {
		return errors.New("Pilot measured model is unavailable")
	}
	toolTokens := m.boundToolTokens
	if options := model.GetCommonOptions(nil, opts...); options.Tools != nil {
		var err error
		toolTokens, err = estimatePilotToolInfos(options.Tools)
		if err != nil {
			return err
		}
	}
	return m.budget.Reserve(m.class, estimatePilotMessages(input)+toolTokens)
}

func estimatePilotToolInfos(tools []*schema.ToolInfo) (int, error) {
	contract, err := canonicalConversationToolInfoContract(tools)
	if err != nil {
		return 0, fmt.Errorf("estimate Pilot Tool Schema: %w", err)
	}
	return len([]rune(contract.ModelVisibleJSON)), nil
}

func (m *PilotMeasuredModel) record(usage ContextGovernancePilotUsage, meaningful bool, started time.Time) {
	latency := time.Duration(0)
	if meaningful {
		latency = time.Since(started)
	}
	m.recordWithLatency(usage, meaningful, latency)
}

func (m *PilotMeasuredModel) recordWithLatency(
	usage ContextGovernancePilotUsage,
	meaningful bool,
	latency time.Duration,
) {
	usage.ModelCalls = 1
	if usage.TotalTokens < usage.PromptTokens+usage.CompletionTokens {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	m.state.mu.Lock()
	m.state.usage.ModelCalls++
	m.state.usage.PromptTokens += usage.PromptTokens
	m.state.usage.CompletionTokens += usage.CompletionTokens
	m.state.usage.TotalTokens += usage.TotalTokens
	m.state.usage.CachedTokens += usage.CachedTokens
	m.state.usage.ReasoningTokens += usage.ReasoningTokens
	if meaningful {
		millis := latency.Milliseconds()
		if millis < 1 {
			millis = 1
		}
		m.state.lastFirstTokenLatencyMS = millis
	} else {
		m.state.lastFirstTokenLatencyMS = 0
	}
	m.state.mu.Unlock()
}

func (m *PilotMeasuredModel) Snapshot() PilotMeasuredModelSnapshot {
	if m == nil || m.state == nil {
		return PilotMeasuredModelSnapshot{}
	}
	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	return PilotMeasuredModelSnapshot{
		Usage: m.state.usage, LastFirstTokenLatencyMS: m.state.lastFirstTokenLatencyMS,
	}
}

func (m *PilotMeasuredModel) Delta(before PilotMeasuredModelSnapshot) PilotMeasuredModelSnapshot {
	after := m.Snapshot()
	after.Usage.ModelCalls -= before.Usage.ModelCalls
	after.Usage.PromptTokens -= before.Usage.PromptTokens
	after.Usage.CompletionTokens -= before.Usage.CompletionTokens
	after.Usage.TotalTokens -= before.Usage.TotalTokens
	after.Usage.CachedTokens -= before.Usage.CachedTokens
	after.Usage.ReasoningTokens -= before.Usage.ReasoningTokens
	if after.Usage.ModelCalls == 0 {
		after.LastFirstTokenLatencyMS = 0
	}
	return after
}

func pilotUsageFromMessage(message *schema.Message) ContextGovernancePilotUsage {
	if message == nil || message.ResponseMeta == nil || message.ResponseMeta.Usage == nil {
		return ContextGovernancePilotUsage{}
	}
	usage := message.ResponseMeta.Usage
	return ContextGovernancePilotUsage{
		PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
		TotalTokens: usage.TotalTokens, CachedTokens: usage.PromptTokenDetails.CachedTokens,
		ReasoningTokens: usage.CompletionTokensDetails.ReasoningTokens,
	}
}

func meaningfulPilotMessage(message *schema.Message) bool {
	return message != nil && (message.Content != "" || message.ReasoningContent != "" || len(message.ToolCalls) > 0)
}

func estimatePilotMessages(messages []*schema.Message) int {
	total := 0
	for _, message := range messages {
		if message != nil {
			total += len([]rune(message.Content)) + len([]rune(message.ReasoningContent)) + 8
		}
	}
	return total
}

var _ model.ToolCallingChatModel = (*PilotMeasuredModel)(nil)
