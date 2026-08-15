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

	provideropenai "github.com/cloudwego/eino-ext/components/model/openai"
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
	UsageProvided    bool
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CachedTokens     int
	ReasoningTokens  int
}

type probeUsage struct {
	// Provided 标记 Provider 流是否真的返回了 usage 对象。eino 流式转换只在
	// 响应块携带 usage 时才设置 ResponseMeta.Usage；为 false 时输出必须显示
	// "未提供"（n/a），不能把缺失字段包装成零消耗。
	Provided         bool
	Complete         bool
	ProvidedCalls    int
	TotalCalls       int
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CachedTokens     int
	ReasoningTokens  int
}

type runOptions struct {
	Profile            string
	AllowProviderCalls bool
	ReasoningEffort    string
	ShowReasoning      bool
}

type providerSmokeError struct {
	Kind string
	Err  error
}

func (e *providerSmokeError) Error() string {
	return fmt.Sprintf("provider_error_type=%s: %v", e.Kind, e.Err)
}

func (e *providerSmokeError) Unwrap() error { return e.Err }

type probeProtocolError struct{ Err error }

func (e *probeProtocolError) Error() string { return e.Err.Error() }

func (e *probeProtocolError) Unwrap() error { return e.Err }

// modelFactory 构建命名 Profile 的模型实例；生产路径使用 platformchatmodel，
// 离线测试注入桩 Factory 以证明成本护栏先于 Provider 创建。
type modelFactory func(context.Context, string, config.ChatModelProfileConfig) (*platformchatmodel.Instance, error)

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
	return runWith(ctx, args, cfg, defaultModelFactory)
}

func defaultModelFactory(
	ctx context.Context,
	profileName string,
	profile config.ChatModelProfileConfig,
) (*platformchatmodel.Instance, error) {
	return platformchatmodel.New(ctx, profileName, profile)
}

func runWith(ctx context.Context, args []string, cfg config.Config, newModel modelFactory) error {
	opts, err := parseRunOptions(args, "")
	if err != nil {
		return err
	}
	profileName := strings.TrimSpace(opts.Profile)
	var profile config.ChatModelProfileConfig
	if profileName == "" {
		profileName = strings.TrimSpace(cfg.Models.Chat.ActiveProfileName)
		profile, err = cfg.Models.Chat.ActiveProfile()
	} else {
		profile, err = cfg.Models.Chat.Profile(profileName)
	}
	if err != nil {
		return err
	}
	if opts.ReasoningEffort == "" {
		opts.ReasoningEffort = strings.ToLower(strings.TrimSpace(profile.ReasoningEffort))
	}
	if !opts.AllowProviderCalls {
		return errors.New("provider calls are not allowed; pass -allow-provider-calls to run this smoke against a real model")
	}
	profile.ReasoningEffort = opts.ReasoningEffort
	instance, err := newModel(ctx, profileName, profile)
	if err != nil {
		return fmt.Errorf("build chat model: %w", err)
	}
	result, err := probe(ctx, instance.Model, instance.Identity.ModelID)
	if err != nil {
		return classifyProviderSmokeError(err)
	}
	fmt.Printf("profile=%s provider=%s model=%s tool=%s calls=%d total=%s answerRunes=%d\n",
		instance.Identity.Profile, instance.Identity.Provider, result.Model, result.Tool,
		len(result.Calls), formatDuration(result.Total), utf8.RuneCountInString(result.Answer),
	)
	for index, call := range result.Calls {
		fmt.Printf(
			"call=%d stage=%s streamReady=%s ttft=%s reasoningTTFT=%s answerTTFT=%s total=%s chunks=%d reasoningRunes=%d answerRunes=%d completionTPS=%s promptTokens=%s completionTokens=%s totalTokens=%s cachedTokens=%s reasoningTokens=%s\n",
			index+1, call.Name, formatDuration(call.StreamReady), formatOptionalDuration(call.FirstOutput),
			formatOptionalDuration(call.FirstReasoning), formatOptionalDuration(call.FirstContent),
			formatDuration(call.Total), call.Chunks, utf8.RuneCountInString(call.Reasoning),
			utf8.RuneCountInString(call.Content), formatTokenRate(call),
			tokenOrNA(call.UsageProvided, call.PromptTokens), tokenOrNA(call.UsageProvided, call.CompletionTokens),
			tokenOrNA(call.UsageProvided, call.TotalTokens), tokenOrNA(call.UsageProvided, call.CachedTokens),
			tokenOrNA(call.UsageProvided, call.ReasoningTokens),
		)
		if opts.ShowReasoning && strings.TrimSpace(call.Reasoning) != "" {
			fmt.Printf("reasoning[%s]:\n%s\n", call.Name, call.Reasoning)
		}
	}
	if result.Usage.Complete {
		fmt.Printf(
			"usage=provided providedCalls=%d totalCalls=%d promptTokens=%d completionTokens=%d totalTokens=%d cachedTokens=%d reasoningTokens=%d\n",
			result.Usage.ProvidedCalls, result.Usage.TotalCalls,
			result.Usage.PromptTokens, result.Usage.CompletionTokens, result.Usage.TotalTokens,
			result.Usage.CachedTokens, result.Usage.ReasoningTokens,
		)
	} else if result.Usage.Provided {
		fmt.Printf(
			"usage=partial providedCalls=%d totalCalls=%d promptTokens=%d completionTokens=%d totalTokens=%d cachedTokens=%d reasoningTokens=%d\n",
			result.Usage.ProvidedCalls, result.Usage.TotalCalls,
			result.Usage.PromptTokens, result.Usage.CompletionTokens, result.Usage.TotalTokens,
			result.Usage.CachedTokens, result.Usage.ReasoningTokens,
		)
	} else {
		fmt.Printf(
			"usage=not-provided providedCalls=0 totalCalls=%d promptTokens=n/a completionTokens=n/a totalTokens=n/a cachedTokens=n/a reasoningTokens=n/a\n",
			result.Usage.TotalCalls,
		)
	}
	return nil
}

func parseRunOptions(args []string, defaultEffort string) (runOptions, error) {
	flags := flag.NewFlagSet("mesguard-model-smoke", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	effort := flags.String("reasoning-effort", defaultEffort, "provider-supported effort; empty keeps thinking control only")
	showReasoning := flags.Bool("show-reasoning", false, "print raw reasoning in this local probe")
	profileName := flags.String("profile", "", "named chat profile; empty uses activeProfile")
	allowProviderCalls := flags.Bool("allow-provider-calls", false, "allow real provider calls; must be explicit")
	if err := flags.Parse(args); err != nil {
		return runOptions{}, fmt.Errorf("usage: mesguard-model-smoke [-reasoning-effort provider-value] [-profile name] [-allow-provider-calls] [-show-reasoning]: %w", err)
	}
	if flags.NArg() != 0 {
		return runOptions{}, errors.New("usage: mesguard-model-smoke [-reasoning-effort provider-value] [-profile name] [-allow-provider-calls] [-show-reasoning]")
	}
	normalizedEffort := strings.ToLower(strings.TrimSpace(*effort))
	switch normalizedEffort {
	case "", "low", "medium", "high", "xhigh", "max":
	default:
		return runOptions{}, errors.New("reasoning-effort must be empty, low, medium, high, xhigh, or max")
	}
	return runOptions{
		Profile: strings.TrimSpace(*profileName), AllowProviderCalls: *allowProviderCalls,
		ReasoningEffort: normalizedEffort, ShowReasoning: *showReasoning,
	}, nil
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
		return probeResult{}, &probeProtocolError{Err: errors.New("model did not return the required probe tool call")}
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
		return probeResult{}, &probeProtocolError{Err: errors.New("model returned no final probe answer")}
	}
	result := probeResult{
		Model: modelName, Tool: toolCall.ToolCalls[0].Function.Name, Answer: answer.Content,
		Calls: []probeCallResult{toolCallMetrics, answerMetrics}, Total: time.Since(startedAt),
	}
	result.Usage.TotalCalls = len(result.Calls)
	for _, call := range result.Calls {
		if !call.UsageProvided {
			continue
		}
		result.Usage.Provided = true
		result.Usage.ProvidedCalls++
		result.Usage.PromptTokens += call.PromptTokens
		result.Usage.CompletionTokens += call.CompletionTokens
		result.Usage.TotalTokens += call.TotalTokens
		result.Usage.CachedTokens += call.CachedTokens
		result.Usage.ReasoningTokens += call.ReasoningTokens
	}
	result.Usage.Complete = result.Usage.TotalCalls > 0 && result.Usage.ProvidedCalls == result.Usage.TotalCalls
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
		return nil, metrics, &probeProtocolError{Err: errors.New("model stream returned no chunks")}
	}
	message, err := schema.ConcatMessages(chunks)
	if err != nil {
		return nil, metrics, &probeProtocolError{Err: fmt.Errorf("concatenate model stream: %w", err)}
	}
	metrics.Reasoning = message.ReasoningContent
	metrics.Content = message.Content
	if message.ResponseMeta != nil && message.ResponseMeta.Usage != nil {
		usage := message.ResponseMeta.Usage
		metrics.UsageProvided = true
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

// tokenOrNA 区分"Provider 未提供 usage"（n/a）与"真实为 0"（0），避免把缺失
// 字段包装成零消耗。eino 流式转换仅在响应块携带 usage 时才设置
// ResponseMeta.Usage，因此 UsageProvided=false 即 Provider 未返回 usage 对象。
func tokenOrNA(provided bool, value int) string {
	if !provided {
		return "n/a"
	}
	return fmt.Sprintf("%d", value)
}

func classifyProviderSmokeError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *provideropenai.APIError
	if errors.As(err, &apiErr) && apiErr.HTTPStatusCode == 429 {
		return &providerSmokeError{Kind: "rate_limited", Err: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &providerSmokeError{Kind: "timeout", Err: err}
	}
	var protocolErr *probeProtocolError
	if errors.As(err, &protocolErr) || errors.Is(err, io.ErrUnexpectedEOF) {
		return &providerSmokeError{Kind: "protocol", Err: err}
	}
	return &providerSmokeError{Kind: "provider", Err: err}
}
