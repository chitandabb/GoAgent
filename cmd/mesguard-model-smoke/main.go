// Command mesguard-model-smoke 测量真实 ChatModel 的 Tool Calling、流式时延和 usage 回传。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	platformchatmodel "github.com/chitandabb/GoAgent/internal/platform/chatmodel"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformlogger "github.com/chitandabb/GoAgent/internal/platform/logger"

	"github.com/cloudwego/eino/components/model"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

const probeToolName = "mesguard_capability_probe"

type probeInput struct {
	Value string `json:"value" jsonschema:"required" jsonschema_description:"固定填写 ok"`
}

type probeResult struct {
	Model  string
	Tool   string
	Answer string
	Calls  []probeCallResult
	Total  time.Duration
	Usage  probeUsage
}

type probeCallResult struct {
	Name             string
	StreamReady      time.Duration
	FirstOutput      time.Duration
	FirstReasoning   time.Duration
	FirstContent     time.Duration
	Total            time.Duration
	Chunks           int
	Reasoning        string
	Content          string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CachedTokens     int
	ReasoningTokens  int
}

type probeUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CachedTokens     int
	ReasoningTokens  int
}

type runOptions struct {
	ReasoningEffort string
	ShowReasoning   bool
}

func main() {
	log := platformlogger.NewBootstrapFor("mesguard-model-smoke")
	defer platformlogger.Sync(log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		log.Error("model smoke test failed", zap.Error(err))
		platformlogger.Sync(log)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	opts, err := parseRunOptions(args, cfg.Models.Chat.ReasoningEffort)
	if err != nil {
		return err
	}
	cfg.Models.Chat.ReasoningEffort = opts.ReasoningEffort
	chatModel, err := platformchatmodel.NewStepFun(ctx, cfg.Models.Chat)
	if err != nil {
		return fmt.Errorf("build StepFun model: %w", err)
	}
	result, err := probe(ctx, chatModel, cfg.Models.Chat.Model)
	if err != nil {
		return err
	}
	fmt.Printf("model=%s reasoningEffort=%s tool=%s calls=%d total=%s answerRunes=%d\n",
		result.Model, opts.ReasoningEffort, result.Tool, len(result.Calls),
		formatDuration(result.Total), utf8.RuneCountInString(result.Answer),
	)
	for index, call := range result.Calls {
		fmt.Printf(
			"call=%d stage=%s streamReady=%s ttft=%s reasoningTTFT=%s answerTTFT=%s total=%s chunks=%d reasoningRunes=%d answerRunes=%d completionTPS=%s promptTokens=%d completionTokens=%d totalTokens=%d cachedTokens=%d reasoningTokens=%d\n",
			index+1, call.Name, formatDuration(call.StreamReady), formatOptionalDuration(call.FirstOutput),
			formatOptionalDuration(call.FirstReasoning), formatOptionalDuration(call.FirstContent),
			formatDuration(call.Total), call.Chunks, utf8.RuneCountInString(call.Reasoning),
			utf8.RuneCountInString(call.Content), formatTokenRate(call), call.PromptTokens,
			call.CompletionTokens, call.TotalTokens, call.CachedTokens, call.ReasoningTokens,
		)
		if opts.ShowReasoning && strings.TrimSpace(call.Reasoning) != "" {
			fmt.Printf("reasoning[%s]:\n%s\n", call.Name, call.Reasoning)
		}
	}
	fmt.Printf(
		"usage promptTokens=%d completionTokens=%d totalTokens=%d cachedTokens=%d reasoningTokens=%d\n",
		result.Usage.PromptTokens, result.Usage.CompletionTokens, result.Usage.TotalTokens,
		result.Usage.CachedTokens, result.Usage.ReasoningTokens,
	)
	return nil
}

func parseRunOptions(args []string, defaultEffort string) (runOptions, error) {
	flags := flag.NewFlagSet("mesguard-model-smoke", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	effort := flags.String("reasoning-effort", defaultEffort, "low, medium, or high")
	showReasoning := flags.Bool("show-reasoning", false, "print raw reasoning in this local probe")
	if err := flags.Parse(args); err != nil {
		return runOptions{}, fmt.Errorf("usage: mesguard-model-smoke [-reasoning-effort low|medium|high] [-show-reasoning]: %w", err)
	}
	if flags.NArg() != 0 {
		return runOptions{}, errors.New("usage: mesguard-model-smoke [-reasoning-effort low|medium|high] [-show-reasoning]")
	}
	normalizedEffort := strings.ToLower(strings.TrimSpace(*effort))
	switch normalizedEffort {
	case "low", "medium", "high":
	default:
		return runOptions{}, errors.New("reasoning-effort must be low, medium, or high")
	}
	return runOptions{ReasoningEffort: normalizedEffort, ShowReasoning: *showReasoning}, nil
}

func probe(ctx context.Context, chatModel model.ToolCallingChatModel, modelName string) (probeResult, error) {
	if chatModel == nil {
		return probeResult{}, errors.New("chat model is nil")
	}
	probeTool, err := toolutils.InferTool(
		probeToolName,
		"MESGuard 模型兼容性探针；当用户要求能力检查时必须调用此工具",
		func(context.Context, probeInput) (string, error) { return "ok", nil },
	)
	if err != nil {
		return probeResult{}, fmt.Errorf("build probe tool: %w", err)
	}
	toolInfo, err := probeTool.Info(ctx)
	if err != nil {
		return probeResult{}, fmt.Errorf("read probe tool info: %w", err)
	}
	boundModel, err := chatModel.WithTools([]*schema.ToolInfo{toolInfo})
	if err != nil {
		return probeResult{}, fmt.Errorf("bind probe tool: %w", err)
	}
	startedAt := time.Now()
	messages := []*schema.Message{
		schema.SystemMessage("你正在执行模型能力检查。必须先调用提供的探针工具；获得工具结果后，用一句话说明检查结果。"),
		schema.UserMessage(`调用 mesguard_capability_probe，并将 value 设置为 "ok"。`),
	}
	toolCall, toolCallMetrics, err := probeStream(ctx, "tool_decision", boundModel, messages)
	if err != nil {
		return probeResult{}, fmt.Errorf("stream probe tool call: %w", err)
	}
	if len(toolCall.ToolCalls) == 0 || toolCall.ToolCalls[0].Function.Name != probeToolName {
		return probeResult{}, errors.New("model did not return the required probe tool call")
	}
	answer, answerMetrics, err := probeStream(ctx, "final_answer", boundModel, append(
		append([]*schema.Message(nil), messages...),
		toolCall,
		schema.ToolMessage("ok", toolCall.ToolCalls[0].ID),
	))
	if err != nil {
		return probeResult{}, fmt.Errorf("stream probe final answer: %w", err)
	}
	if strings.TrimSpace(answer.Content) == "" {
		return probeResult{}, errors.New("model returned no final probe answer")
	}
	result := probeResult{
		Model: modelName, Tool: toolCall.ToolCalls[0].Function.Name, Answer: answer.Content,
		Calls: []probeCallResult{toolCallMetrics, answerMetrics}, Total: time.Since(startedAt),
	}
	for _, call := range result.Calls {
		result.Usage.PromptTokens += call.PromptTokens
		result.Usage.CompletionTokens += call.CompletionTokens
		result.Usage.TotalTokens += call.TotalTokens
		result.Usage.CachedTokens += call.CachedTokens
		result.Usage.ReasoningTokens += call.ReasoningTokens
	}
	return result, nil
}

func probeStream(
	ctx context.Context,
	name string,
	chatModel model.ToolCallingChatModel,
	messages []*schema.Message,
) (*schema.Message, probeCallResult, error) {
	startedAt := time.Now()
	stream, err := chatModel.Stream(ctx, messages)
	metrics := probeCallResult{Name: name, StreamReady: time.Since(startedAt)}
	if err != nil {
		return nil, metrics, err
	}
	defer stream.Close()

	chunks := make([]*schema.Message, 0, 16)
	for {
		chunk, receiveErr := stream.Recv()
		if errors.Is(receiveErr, io.EOF) {
			break
		}
		if receiveErr != nil {
			return nil, metrics, receiveErr
		}
		if chunk == nil {
			continue
		}
		elapsed := time.Since(startedAt)
		metrics.Chunks++
		if metrics.FirstOutput == 0 && hasProbeOutput(chunk) {
			metrics.FirstOutput = elapsed
		}
		if metrics.FirstReasoning == 0 && chunk.ReasoningContent != "" {
			metrics.FirstReasoning = elapsed
		}
		if metrics.FirstContent == 0 && chunk.Content != "" {
			metrics.FirstContent = elapsed
		}
		chunks = append(chunks, chunk)
	}
	metrics.Total = time.Since(startedAt)
	if len(chunks) == 0 {
		return nil, metrics, errors.New("model stream returned no chunks")
	}
	message, err := schema.ConcatMessages(chunks)
	if err != nil {
		return nil, metrics, fmt.Errorf("concatenate model stream: %w", err)
	}
	metrics.Reasoning = message.ReasoningContent
	metrics.Content = message.Content
	if message.ResponseMeta != nil && message.ResponseMeta.Usage != nil {
		usage := message.ResponseMeta.Usage
		metrics.PromptTokens = usage.PromptTokens
		metrics.CompletionTokens = usage.CompletionTokens
		metrics.TotalTokens = usage.TotalTokens
		metrics.CachedTokens = usage.PromptTokenDetails.CachedTokens
		metrics.ReasoningTokens = usage.CompletionTokensDetails.ReasoningTokens
	}
	return message, metrics, nil
}

func hasProbeOutput(message *schema.Message) bool {
	return message.ReasoningContent != "" || message.Content != "" || len(message.ToolCalls) != 0
}

func formatOptionalDuration(value time.Duration) string {
	if value <= 0 {
		return "n/a"
	}
	return formatDuration(value)
}

func formatDuration(value time.Duration) string {
	return value.Round(time.Millisecond).String()
}

func formatTokenRate(call probeCallResult) string {
	if call.FirstOutput <= 0 || call.CompletionTokens <= 0 || call.Total <= call.FirstOutput {
		return "n/a"
	}
	rate := float64(call.CompletionTokens) / (call.Total - call.FirstOutput).Seconds()
	return fmt.Sprintf("%.1f", rate)
}
