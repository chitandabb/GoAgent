package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversation"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const (
	maxConversationCitationRepairEvidenceBytes = 64 * 1024
	maxConversationCitationRepairInputRunes    = 64_000
)

type ConversationCitationRepairRequest struct {
	UserQuery string
	Draft     string
	Evidence  []string
	Sources   []conversation.MessageCitation
}

type ConversationCitationRepairResult struct {
	Answer string
	Usage  ModelUsage
}

type ConversationCitationRepairer interface {
	Repair(context.Context, ConversationCitationRepairRequest) (ConversationCitationRepairResult, error)
}

type ModelConversationCitationRepairerConfig struct {
	ChatModel       model.ToolCallingChatModel
	Instruction     string
	PromptVersion   string
	Timeout         time.Duration
	MaxOutputTokens int
}

type modelConversationCitationRepairer struct {
	chatModel       model.ToolCallingChatModel
	instruction     string
	promptVersion   string
	timeout         time.Duration
	maxOutputTokens int
}

func NewModelConversationCitationRepairer(
	cfg ModelConversationCitationRepairerConfig,
) (ConversationCitationRepairer, error) {
	cfg.Instruction = strings.TrimSpace(cfg.Instruction)
	cfg.PromptVersion = strings.TrimSpace(cfg.PromptVersion)
	if cfg.ChatModel == nil || cfg.Instruction == "" || cfg.PromptVersion == "" {
		return nil, errors.New("conversation citation repair model, instruction, and prompt version are required")
	}
	if cfg.Timeout < time.Second || cfg.Timeout > 60*time.Second {
		return nil, errors.New("conversation citation repair timeout must be between 1 and 60 seconds")
	}
	if cfg.MaxOutputTokens < 128 || cfg.MaxOutputTokens > 2048 {
		return nil, errors.New("conversation citation repair max output tokens must be between 128 and 2048")
	}
	return &modelConversationCitationRepairer{
		chatModel: cfg.ChatModel, instruction: cfg.Instruction, promptVersion: cfg.PromptVersion,
		timeout: cfg.Timeout, maxOutputTokens: cfg.MaxOutputTokens,
	}, nil
}

func (r *modelConversationCitationRepairer) Repair(
	ctx context.Context,
	request ConversationCitationRepairRequest,
) (ConversationCitationRepairResult, error) {
	if r == nil {
		return ConversationCitationRepairResult{}, errors.New("conversation citation repairer is unavailable")
	}
	payload, err := prepareConversationCitationRepairRequest(request)
	if err != nil {
		return ConversationCitationRepairResult{}, err
	}
	repairCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	response, err := r.chatModel.Generate(
		repairCtx,
		[]*schema.Message{
			schema.SystemMessage(r.instruction),
			schema.UserMessage("<citation_repair_request>\n" + string(payload) + "\n</citation_repair_request>"),
		},
		model.WithTemperature(0), model.WithMaxTokens(r.maxOutputTokens),
		model.WithTools(nil), model.WithToolChoice(schema.ToolChoiceForbidden),
	)
	result := ConversationCitationRepairResult{Usage: conversationCitationRepairUsage(response)}
	if err != nil {
		return result, fmt.Errorf("generate conversation citation repair: %w", err)
	}
	if response == nil || len(response.ToolCalls) > 0 || response.ResponseMeta != nil &&
		strings.EqualFold(strings.TrimSpace(response.ResponseMeta.FinishReason), "length") {
		return result, errors.New("conversation citation repair response is incomplete")
	}
	answer, err := decodeConversationCitationRepairAnswer(response.Content)
	if err != nil {
		return result, err
	}
	citations, err := conversation.ResolveAnswerCitations(answer, request.Sources)
	if err != nil || len(citations) == 0 {
		return result, errors.New("conversation citation repair answer has no valid citation")
	}
	result.Answer = answer
	return result, nil
}

func prepareConversationCitationRepairRequest(request ConversationCitationRepairRequest) ([]byte, error) {
	request.UserQuery = strings.TrimSpace(request.UserQuery)
	request.Draft = strings.TrimSpace(request.Draft)
	if request.UserQuery == "" || request.Draft == "" || len(request.Evidence) == 0 || len(request.Sources) == 0 ||
		len(request.Sources) > conversation.MaxCitationSourcesPerRun {
		return nil, errors.New("conversation citation repair request is incomplete")
	}
	totalRunes := len([]rune(request.UserQuery)) + len([]rune(request.Draft))
	evidence := make([]json.RawMessage, 0, len(request.Evidence))
	for _, snapshot := range request.Evidence {
		snapshot = strings.TrimSpace(snapshot)
		if snapshot == "" || !json.Valid([]byte(snapshot)) {
			return nil, errors.New("conversation citation repair evidence is invalid")
		}
		totalRunes += len([]rune(snapshot))
		evidence = append(evidence, json.RawMessage(snapshot))
	}
	if totalRunes > maxConversationCitationRepairInputRunes {
		return nil, errors.New("conversation citation repair input exceeds the rune budget")
	}
	markers := make([]string, 0, len(request.Sources))
	for _, source := range request.Sources {
		marker, err := conversation.FormatAnswerCitationMarker(source)
		if err != nil {
			return nil, errors.New("conversation citation repair source is invalid")
		}
		markers = append(markers, marker)
	}
	return json.Marshal(struct {
		UserQuery     string            `json:"userQuery"`
		DraftAnswer   string            `json:"draftAnswer"`
		Evidence      []json.RawMessage `json:"evidence"`
		AllowedMarker []string          `json:"allowedMarkers"`
	}{
		UserQuery: request.UserQuery, DraftAnswer: request.Draft,
		Evidence: evidence, AllowedMarker: markers,
	})
}

func decodeConversationCitationRepairAnswer(content string) (string, error) {
	content = strings.TrimSpace(content)
	if content == "" || strings.HasPrefix(content, "```") {
		return "", errors.New("conversation citation repair response is not strict JSON")
	}
	var payload struct {
		Answer string `json:"answer"`
	}
	decoder := json.NewDecoder(bytes.NewBufferString(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return "", fmt.Errorf("decode conversation citation repair response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return "", errors.New("conversation citation repair response has trailing content")
	}
	payload.Answer = strings.TrimSpace(payload.Answer)
	if payload.Answer == "" || len([]rune(payload.Answer)) > conversation.MaxContentRunes {
		return "", errors.New("conversation citation repair answer is outside the content boundary")
	}
	return payload.Answer, nil
}

func conversationCitationRepairUsage(response *schema.Message) ModelUsage {
	if response == nil {
		return ModelUsage{}
	}
	if response.ResponseMeta == nil || response.ResponseMeta.Usage == nil {
		return ModelUsage{ModelCalls: 1}
	}
	usage := response.ResponseMeta.Usage
	total := usage.TotalTokens
	if minimum := usage.PromptTokens + usage.CompletionTokens; total < minimum {
		total = minimum
	}
	return ModelUsage{
		ModelCalls: 1, PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
		TotalTokens: total, CachedTokens: usage.PromptTokenDetails.CachedTokens,
		ReasoningTokens: usage.CompletionTokensDetails.ReasoningTokens,
	}
}
