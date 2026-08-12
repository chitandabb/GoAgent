package agent

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type pilotModelStubState struct {
	generateCalls int
	streamCalls   int
	generate      *schema.Message
	generateErr   error
	stream        []*schema.Message
	streamErr     error
}

type pilotModelStub struct {
	state *pilotModelStubState
}

func (m pilotModelStub) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	m.state.generateCalls++
	return m.state.generate, m.state.generateErr
}

func (m pilotModelStub) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.state.streamCalls++
	if m.state.streamErr != nil {
		return nil, m.state.streamErr
	}
	return schema.StreamReaderFromArray(m.state.stream), nil
}

func (m pilotModelStub) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return pilotModelStub{state: m.state}, nil
}

func TestPilotMeasuredModelSharesUsageAcrossToolBinding(t *testing.T) {
	state := &pilotModelStubState{generate: pilotUsageMessage("ok", 120, 30, 25, 10)}
	measured := newPilotMeasuredModelForTest(t, pilotModelStub{state: state}, 4, 10)
	bound, err := measured.WithTools([]*schema.ToolInfo{{Name: "fixture_tool"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bound.Generate(context.Background(), []*schema.Message{schema.UserMessage("hello")}); err != nil {
		t.Fatal(err)
	}

	snapshot := measured.Snapshot()
	if snapshot.Usage.ModelCalls != 1 || snapshot.Usage.PromptTokens != 120 ||
		snapshot.Usage.CompletionTokens != 30 || snapshot.Usage.TotalTokens != 150 ||
		snapshot.Usage.CachedTokens != 25 || snapshot.Usage.ReasoningTokens != 10 ||
		snapshot.LastFirstTokenLatencyMS < 1 {
		t.Fatalf("measured snapshot = %+v", snapshot)
	}
}

func TestPilotMeasuredModelRecordsFailedProviderAttempt(t *testing.T) {
	want := errors.New("provider unavailable")
	state := &pilotModelStubState{generateErr: want}
	measured := newPilotMeasuredModelForTest(t, pilotModelStub{state: state}, 4, 10)
	before := measured.Snapshot()
	if _, err := measured.Generate(context.Background(), []*schema.Message{schema.UserMessage("hello")}); !errors.Is(err, want) {
		t.Fatalf("Generate() error = %v, want %v", err, want)
	}
	delta := measured.Delta(before)
	if delta.Usage.ModelCalls != 1 || delta.Usage.TotalTokens != 0 || delta.LastFirstTokenLatencyMS != 0 {
		t.Fatalf("failed attempt delta = %+v", delta)
	}
}

func TestPilotMeasuredModelRecordsStreamingUsageAndFirstToken(t *testing.T) {
	state := &pilotModelStubState{stream: []*schema.Message{
		{Role: schema.Assistant},
		pilotUsageMessage("streamed", 80, 20, 16, 4),
	}}
	measured := newPilotMeasuredModelForTest(t, pilotModelStub{state: state}, 4, 10)
	stream, err := measured.Stream(context.Background(), []*schema.Message{schema.UserMessage("hello")})
	if err != nil {
		t.Fatal(err)
	}
	for {
		_, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatal(recvErr)
		}
	}
	stream.Close()

	snapshot := measured.Snapshot()
	if snapshot.Usage.ModelCalls != 1 || snapshot.Usage.TotalTokens != 100 ||
		snapshot.Usage.CachedTokens != 16 || snapshot.Usage.ReasoningTokens != 4 ||
		snapshot.LastFirstTokenLatencyMS < 1 {
		t.Fatalf("stream snapshot = %+v", snapshot)
	}
}

func TestPilotMeasuredModelStopsBeforeProviderCallWhenCallBudgetIsExhausted(t *testing.T) {
	state := &pilotModelStubState{generate: schema.AssistantMessage("ok", nil)}
	measured := newPilotMeasuredModelForTest(t, pilotModelStub{state: state}, 1, 10)
	input := []*schema.Message{schema.UserMessage("hello")}
	if _, err := measured.Generate(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if _, err := measured.Generate(context.Background(), input); err == nil {
		t.Fatal("Generate() accepted a provider call after the Pilot call budget was exhausted")
	}
	if state.generateCalls != 1 {
		t.Fatalf("provider generate calls = %d, want 1", state.generateCalls)
	}
}

func TestPilotMeasuredModelStopsBeforeProviderCallWhenCostBudgetIsExceeded(t *testing.T) {
	plan := ContextGovernancePilotPlan{MaxProviderCalls: 10, MaxCostCNY: 0.0001}
	pricing := ContextGovernancePilotPricing{MainInputCNYPerMillion: 10_000, MainOutputCNYPerMillion: 10_000}
	budget, err := NewPilotModelCallBudget(plan, pricing, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	state := &pilotModelStubState{generate: schema.AssistantMessage("ok", nil)}
	measured, err := NewPilotMeasuredModel(pilotModelStub{state: state}, PilotMainModelCall, budget)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := measured.Generate(context.Background(), []*schema.Message{schema.UserMessage("hello")}); err == nil {
		t.Fatal("Generate() reached a provider after the Pilot cost budget was exceeded")
	}
	if state.generateCalls != 0 {
		t.Fatalf("provider generate calls = %d, want 0", state.generateCalls)
	}
}

func TestPilotMeasuredModelReservesBoundToolSchemaCost(t *testing.T) {
	plan := ContextGovernancePilotPlan{MaxProviderCalls: 10, MaxCostCNY: 10}
	pricing := ContextGovernancePilotPricing{MainInputCNYPerMillion: 1}
	budget, err := NewPilotModelCallBudget(plan, pricing, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	state := &pilotModelStubState{generate: schema.AssistantMessage("ok", nil)}
	measured, err := NewPilotMeasuredModel(pilotModelStub{state: state}, PilotMainModelCall, budget)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := measured.WithTools([]*schema.ToolInfo{{Name: "fixture_tool", Desc: "read a bounded fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bound.Generate(context.Background(), []*schema.Message{schema.UserMessage("hello")}); err != nil {
		t.Fatal(err)
	}
	_, reservedCost := budget.Snapshot()
	messagesOnly := float64(estimatePilotMessages([]*schema.Message{schema.UserMessage("hello")})) / 1_000_000
	if reservedCost <= messagesOnly {
		t.Fatalf("reserved cost = %f, want more than messages-only %f", reservedCost, messagesOnly)
	}
}

func newPilotMeasuredModelForTest(t *testing.T, inner model.ToolCallingChatModel, maxCalls int, maxCost float64) *PilotMeasuredModel {
	t.Helper()
	budget, err := NewPilotModelCallBudget(
		ContextGovernancePilotPlan{MaxProviderCalls: maxCalls, MaxCostCNY: maxCost},
		ContextGovernancePilotPricing{}, 64, 64,
	)
	if err != nil {
		t.Fatal(err)
	}
	measured, err := NewPilotMeasuredModel(inner, PilotMainModelCall, budget)
	if err != nil {
		t.Fatal(err)
	}
	return measured
}

func pilotUsageMessage(content string, prompt, completion, cached, reasoning int) *schema.Message {
	return &schema.Message{
		Role: schema.Assistant, Content: content,
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
			PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion,
			PromptTokenDetails:      schema.PromptTokenDetails{CachedTokens: cached},
			CompletionTokensDetails: schema.CompletionTokensDetails{ReasoningTokens: reasoning},
		}},
	}
}
