package bootstrap

import (
	"fmt"
	"strings"

	"github.com/chitandabb/GoAgent/internal/contextgovernance"
	"github.com/chitandabb/GoAgent/internal/platform/config"
)

type conversationTokenBudgetRuntime struct {
	estimator contextgovernance.TokenEstimator
	planner   contextgovernance.TokenBudgetPlanner
	profile   contextgovernance.ModelProfile
}

func buildConversationTokenBudgetRuntime(cfg config.Config) (conversationTokenBudgetRuntime, error) {
	profile, err := cfg.Models.Chat.ActiveProfile()
	if err != nil {
		return conversationTokenBudgetRuntime{}, fmt.Errorf("resolve conversation context profile: %w", err)
	}
	estimator, err := contextgovernance.NewLocalTokenEstimator(
		contextgovernance.EstimationMethod(profile.TokenizerStrategy), nil,
	)
	if err != nil {
		return conversationTokenBudgetRuntime{}, fmt.Errorf("build conversation TokenEstimator: %w", err)
	}
	planner, err := contextgovernance.NewTokenBudgetPlanner(estimator)
	if err != nil {
		return conversationTokenBudgetRuntime{}, fmt.Errorf("build conversation TokenBudgetPlanner: %w", err)
	}
	return conversationTokenBudgetRuntime{
		estimator: estimator,
		planner:   planner,
		profile: contextgovernance.ModelProfile{
			Name:     strings.TrimSpace(cfg.Models.Chat.ActiveProfileName),
			Provider: strings.ToLower(strings.TrimSpace(profile.Provider)), ModelID: strings.TrimSpace(profile.Model),
			ContextWindowTokens: profile.ContextWindowTokens, MaxOutputTokens: profile.MaxOutputTokens,
			SafetyMarginTokens: profile.EffectivePromptSafetyMarginTokens(),
		},
	}, nil
}
