// Command mesguard-conversation-memory-smoke validates the configured
// Conversation Memory structured-output contract with one bounded model call.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/contextgovernance"
	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/conversationmemory"
	"github.com/chitandabb/GoAgent/internal/platform/chatmodel"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/platform/memorycompactor"
	modelopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

const (
	defaultTimeout      = 60 * time.Second
	maxProbeInputRunes  = 256
	maxEstimatedTokens  = 3_000
	probeOutputMaxBytes = 64 * 1024
	probePrompt         = `Return only JSON matching the response schema. Keep only user-stated facts. Every entry must cite an existing sourceMessageSeqs value. Use null or [] for empty fields. Invent nothing.`
)

type options struct {
	executeProvider bool
	timeout         time.Duration
}

type result struct {
	Profile              string `json:"profile"`
	Provider             string `json:"provider"`
	ModelID              string `json:"modelId"`
	ResponseFormat       string `json:"responseFormat"`
	ResponseSchema       string `json:"responseSchema"`
	StrictSchema         bool   `json:"strictSchema"`
	ProviderCalls        int    `json:"providerCalls"`
	EstimatedInputTokens int    `json:"estimatedInputTokens"`
	PromptTokens         int    `json:"promptTokens"`
	CompletionTokens     int    `json:"completionTokens"`
	TotalTokens          int    `json:"totalTokens"`
	CachedTokens         int    `json:"cachedTokens"`
	DurationMillis       int64  `json:"durationMillis"`
	PayloadBytes         int    `json:"payloadBytes"`
	DomainValidated      bool   `json:"domainValidated"`
	FailureCode          string `json:"failureCode,omitempty"`
}

type measuredGenerator struct {
	inner model.ToolCallingChatModel
	calls int
	usage conversationmemory.SummaryUsage
}

func (g *measuredGenerator) Generate(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if g == nil || g.inner == nil || g.calls != 0 {
		return nil, errors.New("conversation memory smoke permits exactly one provider call")
	}
	g.calls++
	response, err := g.inner.Generate(ctx, messages, opts...)
	if response != nil && response.ResponseMeta != nil && response.ResponseMeta.Usage != nil {
		u := response.ResponseMeta.Usage
		g.usage = conversationmemory.SummaryUsage{
			PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens,
			TotalTokens: u.TotalTokens, CachedTokens: u.PromptTokenDetails.CachedTokens,
		}
	}
	return response, err
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	opts, err := parseOptions(args)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	profileName := strings.TrimSpace(cfg.Models.Chat.ConversationMemoryProfileName)
	profile, err := cfg.Models.Chat.ConversationMemoryProfile()
	if err != nil {
		return err
	}
	conversationID := uuid.New()
	messages := probeMessages(conversationID)
	estimatedTokens, err := estimateInputTokens(probePrompt, messages)
	if err != nil {
		return err
	}
	preflight := result{
		Profile: profileName, Provider: strings.ToLower(strings.TrimSpace(profile.Provider)), ModelID: strings.TrimSpace(profile.Model),
		ResponseFormat: strings.ToLower(strings.TrimSpace(profile.ResponseFormat)), ResponseSchema: strings.TrimSpace(profile.ResponseSchema),
		StrictSchema:         profile.ResponseFormat == "json_schema" && profile.ResponseSchema == conversationmemory.ResponseSchemaName,
		EstimatedInputTokens: estimatedTokens,
	}
	if preflight.Provider != "stepfun" || !preflight.StrictSchema {
		return errors.New("conversation memory smoke requires the StepFun strict Schema profile")
	}
	if !opts.executeProvider {
		return json.NewEncoder(stdout).Encode(preflight)
	}
	instance, err := chatmodel.NewProfileWithResponseSchema(ctx, cfg.Models.Chat, profileName, chatmodel.ResponseSchema{
		Name: conversationmemory.ResponseSchemaName, Description: "MESGuard structured conversation memory snapshot",
		Schema: conversationmemory.PayloadJSONSchema(), Strict: true,
	})
	if err != nil {
		return fmt.Errorf("build conversation memory model: %w", err)
	}
	measured := &measuredGenerator{inner: instance.Model}
	compactor, err := memorycompactor.New(memorycompactor.Config{
		Generator: measured, Prompt: probePrompt, PromptVersion: cfg.Agent.ContextMemory.Summary.PromptVersion,
		Timeout: opts.timeout, MaxOutputBytes: probeOutputMaxBytes,
	})
	if err != nil {
		return err
	}
	started := time.Now()
	output, err := compactor.Compact(ctx, conversationmemory.CompactionInput{
		ConversationID: conversationID, FromSeq: 1, ThroughSeq: int64(len(messages)),
		NewMessages: messages, Attempt: 1,
	})
	preflight.ProviderCalls = measured.calls
	preflight.PromptTokens, preflight.CompletionTokens = measured.usage.PromptTokens, measured.usage.CompletionTokens
	preflight.TotalTokens, preflight.CachedTokens = measured.usage.TotalTokens, measured.usage.CachedTokens
	preflight.DurationMillis = time.Since(started).Milliseconds()
	if err != nil {
		preflight.FailureCode = smokeFailureCode(err)
		_ = json.NewEncoder(stdout).Encode(preflight)
		return fmt.Errorf("compact conversation memory: %w", err)
	}
	encoded, err := json.Marshal(output.Payload)
	if err != nil {
		return err
	}
	preflight.PayloadBytes = len(encoded)
	if err := conversationmemory.ValidatePayload(output.Payload, conversationmemory.ValidationContext{
		FromSeq: 1, ThroughSeq: int64(len(messages)), MaxPayloadBytes: probeOutputMaxBytes,
		MessageRoles: map[int64]conversation.MessageRole{
			1: conversation.MessageRoleUser, 2: conversation.MessageRoleAssistant,
		},
		KnownEvidenceReferences: map[string]conversationmemory.EvidenceReferenceIdentity{},
		KnownTaskReferences:     map[string]conversationmemory.StableReferenceIdentity{},
		KnownReportReferences:   map[string]conversationmemory.StableReferenceIdentity{},
	}); err != nil {
		preflight.FailureCode = smokeFailureCode(err)
		_ = json.NewEncoder(stdout).Encode(preflight)
		return fmt.Errorf("validate conversation memory payload: %w", err)
	}
	preflight.DomainValidated = true
	return json.NewEncoder(stdout).Encode(preflight)
}

func smokeFailureCode(err error) string {
	if err == nil {
		return ""
	}
	var apiErr *modelopenai.APIError
	if errors.As(err, &apiErr) && apiErr != nil && apiErr.HTTPStatusCode > 0 {
		return fmt.Sprintf("provider_http_%d", apiErr.HTTPStatusCode)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "provider_timeout"
	}
	if code := conversationmemory.FailureCode(err); code != "" {
		return code
	}
	return "unknown"
}

func parseOptions(args []string) (options, error) {
	flags := flag.NewFlagSet("mesguard-conversation-memory-smoke", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var result options
	flags.BoolVar(&result.executeProvider, "execute-provider", false, "perform exactly one configured Provider call")
	flags.DurationVar(&result.timeout, "timeout", defaultTimeout, "single Provider-call timeout")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || result.timeout < time.Second || result.timeout > 2*time.Minute {
		return options{}, errors.New("usage: mesguard-conversation-memory-smoke [-execute-provider] [-timeout 1s..2m]")
	}
	return result, nil
}

func probeMessages(conversationID uuid.UUID) []conversation.Message {
	return []conversation.Message{
		{ID: uuid.New(), ConversationID: conversationID, Seq: 1, Role: conversation.MessageRoleUser, Content: "本次会话目标是确认结构化会话记忆兼容性。"},
		{ID: uuid.New(), ConversationID: conversationID, Seq: 2, Role: conversation.MessageRoleAssistant, Content: "已记录该目标。"},
	}
}

func estimateInputTokens(prompt string, messages []conversation.Message) (int, error) {
	request := struct {
		Prompt   string                 `json:"prompt"`
		Schema   any                    `json:"schema"`
		Messages []conversation.Message `json:"messages"`
	}{Prompt: prompt, Schema: conversationmemory.PayloadJSONSchema(), Messages: messages}
	encoded, err := json.Marshal(request)
	if err != nil {
		return 0, err
	}
	for _, message := range messages {
		if len([]rune(message.Content)) > maxProbeInputRunes {
			return 0, errors.New("conversation memory smoke input exceeds the rune limit")
		}
	}
	estimator, err := contextgovernance.NewLocalTokenEstimator(contextgovernance.EstimationMethodConservativeHeuristic, nil)
	if err != nil {
		return 0, err
	}
	estimate, err := estimator.Estimate(context.Background(), contextgovernance.PromptInput{
		Profile:  "conversation-memory-smoke",
		Segments: []contextgovernance.PromptSegment{{Kind: contextgovernance.PromptSegmentSystem, Content: string(encoded)}},
	})
	if err != nil {
		return 0, err
	}
	if estimate.UpperBoundTokens > maxEstimatedTokens {
		return 0, fmt.Errorf("conversation memory smoke estimated upper bound %d exceeds %d tokens", estimate.UpperBoundTokens, maxEstimatedTokens)
	}
	return estimate.UpperBoundTokens, nil
}
