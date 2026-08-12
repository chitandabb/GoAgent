package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/chitandabb/GoAgent/internal/contextgovernance"
	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/conversationmemory"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func (r *ConversationRunner) prepareSummaryTailPrompt(
	ctx context.Context,
	tools []tool.BaseTool,
	request conversation.AgentRequest,
) (conversationPromptProjection, *contextgovernance.PromptManifest, error) {
	active, err := r.contextPreflight.Memory.Active(ctx, request.Conversation.ID)
	if err != nil && !errors.Is(err, conversationmemory.ErrSnapshotNotFound) {
		return conversationPromptProjection{}, nil, fmt.Errorf("load active conversation memory: %w", err)
	}
	if errors.Is(err, conversationmemory.ErrSnapshotNotFound) {
		active = nil
	}

	pressureHistory := request.History
	if active != nil {
		pressureHistory = conversationMessagesAfter(request.History, active.ThroughSeq)
	}
	pressure, err := buildFullConversationPromptProjection(pressureHistory, request.UserMessage)
	if err != nil {
		return conversationPromptProjection{}, nil, err
	}
	if err := applyConversationSummary(&pressure, active); err != nil {
		return pressure, nil, err
	}
	pressureManifest, err := r.buildConversationPromptManifest(ctx, tools, pressure)
	if err != nil {
		return pressure, pressureManifest, err
	}
	hardTriggered := pressureManifest.HardThresholdReached
	if active == nil && !hardTriggered {
		return pressure, pressureManifest, nil
	}
	refreshNeeded := hardTriggered
	if active != nil && !hardTriggered {
		coverageComplete, coverageErr := r.summaryTailCoverageComplete(ctx, request, *active)
		if coverageErr != nil {
			return pressure, pressureManifest, coverageErr
		}
		refreshNeeded = !coverageComplete
	}
	refreshFailed := false
	if refreshNeeded {
		compactionCtx, cancel := context.WithTimeout(ctx, r.contextPreflight.SyncCompactionTimeout)
		prepared, prepareErr := r.contextPreflight.Memory.PrepareActive(compactionCtx, conversationmemory.PrepareActiveRequest{
			ConversationID:        request.Conversation.ID,
			CompletedMessages:     completedConversationMessages(request.History, request.UserMessage),
			KnownReportReferences: request.KnownReportReferences,
			ActivationGate:        conversationSummaryActivationGate{preflight: r.contextPreflight},
		})
		cancel()
		if prepareErr != nil {
			if active == nil {
				pressure.hardCompactionTriggered = true
				pressure.degradedReasons = append(pressure.degradedReasons, "summary_compaction_failed")
				failedManifest, _ := r.buildConversationPromptManifest(ctx, tools, pressure)
				return pressure, failedManifest, fmt.Errorf("prepare active conversation memory: %w", prepareErr)
			}
			refreshFailed = true
		} else {
			active = &prepared
		}
	}
	pressure.hardCompactionTriggered = hardTriggered
	if refreshFailed {
		pressure.degradedReasons = append(pressure.degradedReasons, "summary_refresh_failed")
	}
	if hardTriggered {
		pressureManifest, err = r.buildConversationPromptManifest(ctx, tools, pressure)
		if err != nil {
			return pressure, pressureManifest, err
		}
	}

	summaryContent, summaryFingerprint, summarySnapshotID, err := conversationSummaryProjection(active)
	if err != nil {
		return pressure, pressureManifest, err
	}
	tailBudget, err := r.summaryTailTokenBudget(ctx, summaryContent)
	if err != nil {
		return pressure, pressureManifest, err
	}
	var final conversationPromptProjection
	var manifest *contextgovernance.PromptManifest
	for attempt := 0; attempt < 6; attempt++ {
		final, err = r.buildConversationPromptProjectionWithTailBudget(
			ctx, request.History, request.UserMessage, tailBudget,
		)
		if err != nil {
			failed := pressure
			failed.hardCompactionTriggered = hardTriggered
			failed.degradedReasons = append(failed.degradedReasons, "tail_selection_failed")
			failedManifest, _ := r.buildConversationPromptManifest(ctx, tools, failed)
			return failed, failedManifest, fmt.Errorf("%w: %w", ErrConversationContextPreparationFailed, err)
		}
		final.summaryContent = summaryContent
		final.summaryFingerprint = summaryFingerprint
		final.summarySnapshotID = summarySnapshotID
		final.hardCompactionTriggered = hardTriggered
		if refreshFailed {
			final.degradedReasons = append(final.degradedReasons, "summary_refresh_failed")
		}
		if summaryContent != "" {
			final.messages = append([]*schema.Message{conversationSummaryPromptMessage(summaryContent)}, final.messages...)
		}
		manifest, err = r.buildConversationPromptManifest(ctx, tools, final)
		if err != nil {
			return final, manifest, err
		}
		if active != nil && final.tailFromSeq > active.ThroughSeq+1 {
			final.degradedReasons = append(final.degradedReasons, "summary_tail_coverage_gap")
			manifest, _ = r.buildConversationPromptManifest(ctx, tools, final)
			return final, manifest, ErrConversationContextPreparationFailed
		}
		if !manifest.ExceedsHardWindow {
			return final, manifest, nil
		}
		if final.tailFromSeq == request.UserMessage.Seq {
			break
		}
		overflow := manifest.EstimatedUpperBoundTokens - manifest.AvailableInputTokens
		nextBudget := tailBudget - overflow - 8
		if nextBudget >= tailBudget {
			nextBudget = tailBudget / 2
		}
		if nextBudget < 1 {
			break
		}
		tailBudget = nextBudget
	}
	if refreshFailed {
		return final, manifest, ErrConversationContextPreparationFailed
	}
	return final, manifest, nil
}

func (r *ConversationRunner) summaryTailCoverageComplete(
	ctx context.Context,
	request conversation.AgentRequest,
	active conversationmemory.Snapshot,
) (bool, error) {
	summaryContent, _, _, err := conversationSummaryProjection(&active)
	if err != nil {
		return false, err
	}
	tailBudget, err := r.summaryTailTokenBudget(ctx, summaryContent)
	if err != nil {
		return false, err
	}
	projection, err := r.buildConversationPromptProjectionWithTailBudget(
		ctx, request.History, request.UserMessage, tailBudget,
	)
	if err != nil {
		return false, err
	}
	return projection.tailFromSeq <= active.ThroughSeq+1, nil
}

type conversationSummaryActivationGate struct {
	preflight ConversationContextPreflightConfig
}

// NewConversationMemoryActivationGate exposes the exact Summary budget gate
// used by the synchronous Conversation Runner without requiring an async
// memory worker to construct the full main-model Agent runtime.
func NewConversationMemoryActivationGate(
	preflight ConversationContextPreflightConfig,
) (conversationmemory.ActivationGate, error) {
	if !preflight.Enabled || !preflight.SummaryTailEnabled || preflight.Planner == nil ||
		preflight.ModelProfile.Validate() != nil ||
		math.IsNaN(preflight.MemoryMaxRatio) || math.IsInf(preflight.MemoryMaxRatio, 0) ||
		math.IsNaN(preflight.SummaryMaxRatio) || math.IsInf(preflight.SummaryMaxRatio, 0) ||
		math.IsNaN(preflight.TailMaxRatio) || math.IsInf(preflight.TailMaxRatio, 0) ||
		math.IsNaN(preflight.SoftThresholdRatio) || math.IsInf(preflight.SoftThresholdRatio, 0) ||
		math.IsNaN(preflight.HardThresholdRatio) || math.IsInf(preflight.HardThresholdRatio, 0) ||
		preflight.MemoryMaxRatio <= 0 || preflight.MemoryMaxRatio > contextgovernance.MaxTailWindowRatio ||
		preflight.SummaryMaxRatio <= 0 || preflight.SummaryMaxRatio > 0.05 ||
		!contextgovernance.ValidTailWindowRatio(preflight.TailMaxRatio) ||
		preflight.SoftThresholdRatio <= 0 || preflight.HardThresholdRatio <= preflight.SoftThresholdRatio ||
		preflight.HardThresholdRatio >= 1 ||
		preflight.SummaryMaxRatio+preflight.TailMaxRatio > preflight.MemoryMaxRatio+1e-12 {
		return nil, errors.New("conversation memory activation gate configuration is invalid")
	}
	return conversationSummaryActivationGate{preflight: preflight}, nil
}

func (g conversationSummaryActivationGate) ValidateForActivation(
	ctx context.Context,
	snapshot conversationmemory.Snapshot,
) error {
	if g.preflight.Planner == nil || (snapshot.Status != conversationmemory.SnapshotStatusCandidate &&
		snapshot.Status != conversationmemory.SnapshotStatusActive) {
		return ErrConversationContextPreparationFailed
	}
	content, _, _, err := renderConversationSummary(&snapshot)
	if err != nil {
		return err
	}
	_, err = summaryTailTokenBudgetForPreflight(ctx, g.preflight, content)
	return err
}

func (r *ConversationRunner) summaryTailTokenBudget(ctx context.Context, summaryContent string) (int, error) {
	return summaryTailTokenBudgetForPreflight(ctx, r.contextPreflight, summaryContent)
}

func summaryTailTokenBudgetForPreflight(
	ctx context.Context,
	preflight ConversationContextPreflightConfig,
	summaryContent string,
) (int, error) {
	profile := preflight.ModelProfile
	summaryTokens := 0
	if summaryContent != "" {
		plan, err := preflight.plan(ctx, []contextgovernance.PromptSegment{{
			Kind: contextgovernance.PromptSegmentSummary, Content: summaryContent,
		}})
		if err != nil {
			return 0, fmt.Errorf("estimate conversation Summary: %w", err)
		}
		summaryTokens = plan.EstimatedUpperBoundTokens
		maxSummaryTokens := int(math.Floor(float64(profile.ContextWindowTokens) * preflight.SummaryMaxRatio))
		if summaryTokens > maxSummaryTokens {
			return 0, fmt.Errorf("%w: active Summary exceeds configured Token budget", ErrConversationContextPreparationFailed)
		}
	}
	memoryBudget := int(math.Floor(float64(profile.ContextWindowTokens) * preflight.MemoryMaxRatio))
	tailBudget := int(math.Floor(float64(profile.ContextWindowTokens) * preflight.TailMaxRatio))
	if remaining := memoryBudget - summaryTokens; tailBudget > remaining {
		tailBudget = remaining
	}
	if tailBudget < 1 {
		return 0, ErrConversationContextPreparationFailed
	}
	return tailBudget, nil
}

func buildFullConversationPromptProjection(
	history []conversation.Message,
	current conversation.Message,
) (conversationPromptProjection, error) {
	candidates, err := continuousConversationCandidates(history, current)
	if err != nil {
		return conversationPromptProjection{}, err
	}
	messages := make([]*schema.Message, 0, len(candidates))
	for _, item := range candidates {
		content := conversationMessagePrompt(item)
		if item.Role == conversation.MessageRoleUser {
			messages = append(messages, schema.UserMessage(content))
		} else {
			messages = append(messages, schema.AssistantMessage(content, nil))
		}
	}
	return conversationPromptProjection{
		messages: messages, selected: candidates, currentMessageID: current.ID,
		tailFromSeq: candidates[0].Seq, tailThroughSeq: candidates[len(candidates)-1].Seq,
		tailContinuous: true,
	}, nil
}

func applyConversationSummary(projection *conversationPromptProjection, snapshot *conversationmemory.Snapshot) error {
	content, fingerprint, snapshotID, err := conversationSummaryProjection(snapshot)
	if err != nil {
		return err
	}
	projection.summaryContent = content
	projection.summaryFingerprint = fingerprint
	projection.summarySnapshotID = snapshotID
	if content != "" {
		projection.messages = append([]*schema.Message{conversationSummaryPromptMessage(content)}, projection.messages...)
	}
	return nil
}

func conversationSummaryProjection(snapshot *conversationmemory.Snapshot) (string, string, string, error) {
	if snapshot == nil {
		return "", "", "", nil
	}
	if snapshot.Status != conversationmemory.SnapshotStatusActive || snapshot.Validate() != nil {
		return "", "", "", ErrConversationContextPreparationFailed
	}
	return renderConversationSummary(snapshot)
}

func renderConversationSummary(snapshot *conversationmemory.Snapshot) (string, string, string, error) {
	if snapshot == nil || snapshot.Validate() != nil {
		return "", "", "", ErrConversationContextPreparationFailed
	}
	payload, err := json.Marshal(snapshot.Payload)
	if err != nil {
		return "", "", "", fmt.Errorf("encode active conversation Summary: %w", err)
	}
	content := fmt.Sprintf(
		"<conversation_memory snapshot_id=%q version=%q through_seq=%q>\n%s\n</conversation_memory>",
		snapshot.ID.String(), fmt.Sprint(snapshot.Version), fmt.Sprint(snapshot.ThroughSeq), payload,
	)
	return content, contextgovernance.SHA256Hex(string(schema.User) + "\x00" + content), snapshot.ID.String(), nil
}

// conversationSummaryPromptMessage deliberately keeps model-generated memory
// at the same trust level as conversation input. The stable System instruction
// remains the only authority-bearing prompt segment.
func conversationSummaryPromptMessage(content string) *schema.Message {
	return schema.UserMessage(content)
}

func conversationMessagesAfter(messages []conversation.Message, throughSeq int64) []conversation.Message {
	result := make([]conversation.Message, 0, len(messages))
	for _, message := range messages {
		if message.Seq > throughSeq {
			result = append(result, message)
		}
	}
	return result
}

func completedConversationMessages(history []conversation.Message, current conversation.Message) []conversation.Message {
	ordered := conversationHistoryThroughCurrent(history, current)
	completed := make([]conversation.Message, 0, len(ordered))
	for _, message := range ordered {
		if message.ID == current.ID || message.Seq >= current.Seq ||
			(message.Role != conversation.MessageRoleUser && message.Role != conversation.MessageRoleAssistant) ||
			strings.TrimSpace(message.Content) == "" {
			continue
		}
		completed = append(completed, message)
	}
	return completed
}
