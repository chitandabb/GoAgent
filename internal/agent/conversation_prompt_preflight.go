package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/contextgovernance"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type ConversationContextPreflightConfig struct {
	Enabled                 bool
	Planner                 contextgovernance.TokenBudgetPlanner
	TailSelector            *contextgovernance.ContinuousTailSelector
	ModelProfile            contextgovernance.ModelProfile
	ContinuousTailEnabled   bool
	TailMaxRatio            float64
	SoftThresholdRatio      float64
	HardThresholdRatio      float64
	ToolGrowthReserveTokens int
	PreflightTimeout        time.Duration
	PreloadedSkill          string
	SummaryFingerprint      string
}

const defaultConversationPreflightTimeout = 250 * time.Millisecond

func (c ConversationContextPreflightConfig) validate(modelProvider, modelID string) error {
	if c.ContinuousTailEnabled && !c.Enabled {
		return errors.New("conversation continuous Tail requires context preflight")
	}
	if !c.Enabled {
		return nil
	}
	availableInput := c.ModelProfile.ContextWindowTokens - c.ModelProfile.MaxOutputTokens - c.ModelProfile.SafetyMarginTokens
	if c.Planner == nil || c.ModelProfile.Validate() != nil ||
		c.ModelProfile.Provider != modelProvider || c.ModelProfile.ModelID != modelID ||
		math.IsNaN(c.SoftThresholdRatio) || math.IsInf(c.SoftThresholdRatio, 0) ||
		math.IsNaN(c.HardThresholdRatio) || math.IsInf(c.HardThresholdRatio, 0) ||
		c.SoftThresholdRatio <= 0 || c.HardThresholdRatio <= c.SoftThresholdRatio ||
		c.HardThresholdRatio >= 1 || c.ToolGrowthReserveTokens < 1 ||
		c.ToolGrowthReserveTokens >= availableInput ||
		(c.PreflightTimeout != 0 && (c.PreflightTimeout < 5*time.Millisecond || c.PreflightTimeout > 5*time.Second)) ||
		len(c.PreloadedSkill) > 256*1024 {
		return errors.New("conversation context preflight configuration is invalid")
	}
	if c.SummaryFingerprint != "" && !contextgovernance.IsSHA256Hex(c.SummaryFingerprint) {
		return errors.New("conversation context preflight summary fingerprint is invalid")
	}
	if c.ContinuousTailEnabled && (c.TailSelector == nil ||
		!contextgovernance.ValidTailWindowRatio(c.TailMaxRatio)) {
		return errors.New("conversation continuous Tail configuration is invalid")
	}
	return nil
}

func (c ConversationContextPreflightConfig) effectiveTimeout() time.Duration {
	if c.PreflightTimeout > 0 {
		return c.PreflightTimeout
	}
	return defaultConversationPreflightTimeout
}

func (c ConversationContextPreflightConfig) plan(
	ctx context.Context,
	segments []contextgovernance.PromptSegment,
) (contextgovernance.TokenBudgetPlan, error) {
	profile := c.ModelProfile
	return c.Planner.Plan(ctx, contextgovernance.TokenBudgetRequest{
		ContextWindowTokens: profile.ContextWindowTokens,
		MaxOutputTokens:     profile.MaxOutputTokens,
		SafetyMarginTokens:  profile.SafetyMarginTokens,
		SoftThresholdRatio:  c.SoftThresholdRatio,
		HardThresholdRatio:  c.HardThresholdRatio,
		Prompt: contextgovernance.PromptInput{
			Profile: profile.Name, Segments: segments,
		},
	})
}

func (r *ConversationRunner) buildConversationPromptManifest(
	ctx context.Context,
	tools []tool.BaseTool,
	projection conversationPromptProjection,
) (*contextgovernance.PromptManifest, error) {
	if !r.contextPreflight.Enabled {
		return nil, nil
	}
	startedAt := time.Now()
	preflightCtx, cancel := context.WithTimeout(ctx, r.contextPreflight.effectiveTimeout())
	defer cancel()
	contract, err := canonicalConversationToolContract(preflightCtx, tools)
	if err != nil {
		return r.failedConversationPromptManifest(projection, startedAt, nil, "tool_schema_failed"), err
	}
	identity, err := contextgovernance.BuildPromptIdentity(contextgovernance.PromptIdentityInput{
		ModelProfile:          r.contextPreflight.ModelProfile.Name,
		ModelProvider:         r.modelProvider,
		ModelID:               r.modelID,
		SystemPromptVersion:   r.promptVersion,
		SystemPrompt:          r.systemInstruction,
		ToolSchemaFingerprint: contract.Fingerprint,
		PreloadedSkill:        r.contextPreflight.PreloadedSkill,
		SummaryFingerprint:    r.contextPreflight.SummaryFingerprint,
	})
	if err != nil {
		return r.failedConversationPromptManifest(projection, startedAt, nil, "prompt_identity_failed"), err
	}
	history, dynamicReferences, currentUser := conversationPromptSegments(projection)
	segments := []contextgovernance.PromptSegment{
		{Kind: contextgovernance.PromptSegmentSystem, Content: r.systemInstruction},
		{Kind: contextgovernance.PromptSegmentToolSchema, Content: contract.ModelVisibleJSON},
		{Kind: contextgovernance.PromptSegmentPreloadedSkill, Content: r.contextPreflight.PreloadedSkill},
		{Kind: contextgovernance.PromptSegmentSummary},
		{Kind: contextgovernance.PromptSegmentHistory, Content: history},
		{Kind: contextgovernance.PromptSegmentDynamicReferences, Content: dynamicReferences},
		{Kind: contextgovernance.PromptSegmentCurrentUser, Content: currentUser},
		{Kind: contextgovernance.PromptSegmentToolGrowthReserve, ReservedTokens: r.contextPreflight.ToolGrowthReserveTokens},
	}
	profile := r.contextPreflight.ModelProfile
	plan, err := r.contextPreflight.plan(preflightCtx, segments)
	if err != nil {
		return r.failedConversationPromptManifest(projection, startedAt, &identity, "token_estimation_failed"), err
	}
	degradedReasons := append([]string(nil), plan.EstimatorDegradedReasons...)
	contextDegraded := !projection.tailContinuous
	if contextDegraded {
		degradedReasons = append(degradedReasons, "non_continuous_tail")
	}
	manifest := &contextgovernance.PromptManifest{
		SchemaVersion:             1,
		PreflightStatus:           contextgovernance.PreflightStatusSucceeded,
		PromptIdentityAvailable:   true,
		EstimateAvailable:         true,
		PromptEpochID:             identity.PromptEpochID,
		StablePrefixFingerprint:   identity.StablePrefixFingerprint,
		ModelProfile:              profile.Name,
		ModelProfileFingerprint:   identity.ModelProfileFingerprint,
		SystemPromptVersion:       r.promptVersion,
		SystemPromptFingerprint:   identity.SystemPromptFingerprint,
		ToolSchemaFingerprint:     identity.ToolSchemaFingerprint,
		SkillPromptFingerprint:    identity.SkillPromptFingerprint,
		SummaryFingerprint:        identity.SummaryFingerprint,
		TailFromSeq:               projection.tailFromSeq,
		TailThroughSeq:            projection.tailThroughSeq,
		AvailableInputTokens:      plan.AvailableInputTokens,
		EstimatedPromptTokens:     plan.EstimatedPromptTokens,
		EstimatedUpperBoundTokens: plan.EstimatedUpperBoundTokens,
		ToolGrowthReserveTokens:   plan.ReservedTokens,
		EstimationMethod:          plan.EstimationMethod,
		SoftThresholdRatio:        r.contextPreflight.SoftThresholdRatio,
		HardThresholdRatio:        r.contextPreflight.HardThresholdRatio,
		SoftThresholdReached:      plan.SoftThresholdReached,
		HardThresholdReached:      plan.HardThresholdReached,
		ExceedsHardWindow:         plan.ExceedsHardWindow,
		PreflightDurationMicros:   time.Since(startedAt).Microseconds(),
		ContextDegraded:           contextDegraded,
		DegradedReasons:           degradedReasons,
	}
	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("validate conversation prompt manifest: %w", err)
	}
	return manifest, nil
}

func (r *ConversationRunner) failedConversationPromptManifest(
	projection conversationPromptProjection,
	startedAt time.Time,
	identity *contextgovernance.PromptIdentity,
	reason string,
) *contextgovernance.PromptManifest {
	profile := r.contextPreflight.ModelProfile
	available := profile.ContextWindowTokens - profile.MaxOutputTokens - profile.SafetyMarginTokens
	degradedReasons := []string{"preflight_failed", reason}
	if !projection.tailContinuous {
		degradedReasons = append(degradedReasons, "non_continuous_tail")
	}
	manifest := &contextgovernance.PromptManifest{
		SchemaVersion: 1, PreflightStatus: contextgovernance.PreflightStatusFailed,
		FailureStage: reason, PromptIdentityAvailable: identity != nil,
		ModelProfile: profile.Name, SystemPromptVersion: r.promptVersion,
		TailFromSeq: projection.tailFromSeq, TailThroughSeq: projection.tailThroughSeq,
		AvailableInputTokens:    available,
		ToolGrowthReserveTokens: r.contextPreflight.ToolGrowthReserveTokens,
		SoftThresholdRatio:      r.contextPreflight.SoftThresholdRatio,
		HardThresholdRatio:      r.contextPreflight.HardThresholdRatio,
		PreflightDurationMicros: time.Since(startedAt).Microseconds(),
		ContextDegraded:         true, DegradedReasons: degradedReasons,
	}
	if identity != nil {
		manifest.PromptEpochID = identity.PromptEpochID
		manifest.StablePrefixFingerprint = identity.StablePrefixFingerprint
		manifest.ModelProfileFingerprint = identity.ModelProfileFingerprint
		manifest.SystemPromptFingerprint = identity.SystemPromptFingerprint
		manifest.ToolSchemaFingerprint = identity.ToolSchemaFingerprint
		manifest.SkillPromptFingerprint = identity.SkillPromptFingerprint
		manifest.SummaryFingerprint = identity.SummaryFingerprint
	}
	if manifest.Validate() != nil {
		return nil
	}
	return manifest
}

func canonicalConversationToolContract(
	ctx context.Context,
	tools []tool.BaseTool,
) (contextgovernance.CanonicalToolContract, error) {
	infos := make([]*schema.ToolInfo, 0, len(tools))
	for _, current := range tools {
		if current == nil {
			return contextgovernance.CanonicalToolContract{}, errors.New("conversation Tool is nil")
		}
		info, err := current.Info(ctx)
		if err != nil {
			return contextgovernance.CanonicalToolContract{}, fmt.Errorf("read conversation Tool schema: %w", err)
		}
		if info == nil {
			return contextgovernance.CanonicalToolContract{}, errors.New("conversation Tool schema is nil")
		}
		infos = append(infos, info)
	}
	return canonicalConversationToolInfoContract(infos)
}

func canonicalConversationToolInfoContract(
	infos []*schema.ToolInfo,
) (contextgovernance.CanonicalToolContract, error) {
	definitions := make([]contextgovernance.ToolDefinition, 0, len(infos))
	for _, info := range infos {
		if info == nil {
			return contextgovernance.CanonicalToolContract{}, errors.New("conversation Tool schema is nil")
		}
		parameters, err := info.ParamsOneOf.ToJSONSchema()
		if err != nil {
			return contextgovernance.CanonicalToolContract{}, fmt.Errorf("convert conversation Tool parameters: %w", err)
		}
		var encoded json.RawMessage
		if parameters != nil {
			encoded, err = json.Marshal(parameters)
			if err != nil {
				return contextgovernance.CanonicalToolContract{}, fmt.Errorf("encode conversation Tool parameters: %w", err)
			}
		}
		// Eino's OpenAI-compatible adapter exposes Name, Desc and JSON Schema;
		// ToolInfo.Extra is not model-visible and is intentionally excluded.
		definitions = append(definitions, contextgovernance.ToolDefinition{
			Name: info.Name, Description: info.Desc, Parameters: encoded,
		})
	}
	return contextgovernance.NewCanonicalToolContract(definitions)
}

func conversationPromptSegments(projection conversationPromptProjection) (history, dynamicReferences, currentUser string) {
	var historyBuilder strings.Builder
	var referenceBuilder strings.Builder
	for _, message := range projection.selected {
		references := conversationMessageReferencePrompt(message)
		if references != "" {
			fmt.Fprintf(&referenceBuilder, "message_seq=%d\n%s", message.Seq, references)
		}
		if message.ID == projection.currentMessageID {
			currentUser = strings.TrimSpace(message.Content)
			continue
		}
		fmt.Fprintf(&historyBuilder, "role=%s seq=%d\n%s\n", message.Role, message.Seq, strings.TrimSpace(message.Content))
	}
	return historyBuilder.String(), referenceBuilder.String(), currentUser
}
