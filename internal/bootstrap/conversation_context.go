package bootstrap

import (
	"fmt"
	"strings"

	"github.com/chitandabb/GoAgent/internal/contextgovernance"
	"github.com/chitandabb/GoAgent/internal/platform/config"
)

type ConversationTokenBudgetRuntime struct {
	Estimator contextgovernance.TokenEstimator
	Planner   contextgovernance.TokenBudgetPlanner
	Profile   contextgovernance.ModelProfile
}

func BuildConversationTokenBudgetRuntime(cfg config.Config) (ConversationTokenBudgetRuntime, error) {
	profile, err := cfg.Models.Chat.ActiveProfile()
	if err != nil {
		return ConversationTokenBudgetRuntime{}, fmt.Errorf("resolve conversation context profile: %w", err)
	}
	estimator, err := contextgovernance.NewLocalTokenEstimator(
		contextgovernance.EstimationMethod(profile.TokenizerStrategy), nil,
	)
	if err != nil {
		return ConversationTokenBudgetRuntime{}, fmt.Errorf("build conversation TokenEstimator: %w", err)
	}
	planner, err := contextgovernance.NewTokenBudgetPlanner(estimator)
	if err != nil {
		return ConversationTokenBudgetRuntime{}, fmt.Errorf("build conversation TokenBudgetPlanner: %w", err)
	}
	return ConversationTokenBudgetRuntime{
		Estimator: estimator,
		Planner:   planner,
		Profile: contextgovernance.ModelProfile{
			Name:     strings.TrimSpace(cfg.Models.Chat.ActiveProfileName),
			Provider: strings.ToLower(strings.TrimSpace(profile.Provider)), ModelID: strings.TrimSpace(profile.Model),
			ContextWindowTokens: profile.ContextWindowTokens, MaxOutputTokens: profile.MaxOutputTokens,
			SafetyMarginTokens: profile.EffectivePromptSafetyMarginTokens(),
		},
	}, nil
}

func buildConversationTokenBudgetRuntime(cfg config.Config) (ConversationTokenBudgetRuntime, error) {
	return BuildConversationTokenBudgetRuntime(cfg)
}
