// Command mesguard-model-smoke 验证真实 ChatModel 的 Tool Calling 和 usage 回传。
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

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
	Model            string
	Tool             string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CachedTokens     int
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
	if len(args) != 0 {
		return errors.New("usage: mesguard-model-smoke")
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	chatModel, err := platformchatmodel.NewStepFun(ctx, cfg.Models.Chat)
	if err != nil {
		return fmt.Errorf("build StepFun model: %w", err)
	}
	result, err := probe(ctx, chatModel, cfg.Models.Chat.Model)
	if err != nil {
		return err
	}
	fmt.Printf(
		"model=%s tool=%s promptTokens=%d completionTokens=%d totalTokens=%d cachedTokens=%d\n",
		result.Model, result.Tool, result.PromptTokens, result.CompletionTokens,
		result.TotalTokens, result.CachedTokens,
	)
	return nil
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
	message, err := boundModel.Generate(ctx, []*schema.Message{
		schema.SystemMessage("你正在执行模型能力检查。必须调用提供的探针工具，不要直接回答。"),
		schema.UserMessage(`调用 mesguard_capability_probe，并将 value 设置为 "ok"。`),
	})
	if err != nil {
		return probeResult{}, fmt.Errorf("generate probe tool call: %w", err)
	}
	if len(message.ToolCalls) == 0 || message.ToolCalls[0].Function.Name != probeToolName {
		return probeResult{}, errors.New("model did not return the required probe tool call")
	}
	if message.ResponseMeta == nil || message.ResponseMeta.Usage == nil {
		return probeResult{}, errors.New("model response did not include token usage")
	}
	usage := message.ResponseMeta.Usage
	return probeResult{
		Model: modelName, Tool: message.ToolCalls[0].Function.Name,
		PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
		TotalTokens: usage.TotalTokens, CachedTokens: usage.PromptTokenDetails.CachedTokens,
	}, nil
}
