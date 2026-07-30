package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var ErrSkillUnavailable = errors.New("skill is unavailable")

type RunRequest struct {
	UserQuery      string  `json:"userQuery"`
	ExternalCaseID string  `json:"externalCaseId,omitempty"`
	RequestedSkill SkillID `json:"requestedSkill,omitempty"`
}

func (r RunRequest) Validate() error {
	if strings.TrimSpace(r.UserQuery) == "" {
		return errors.New("user query is required")
	}
	return nil
}

type RouteDecision struct {
	SkillID    SkillID `json:"skillId"`
	Reason     string  `json:"reason"`
	Confidence float64 `json:"confidence"`
}

type Router interface {
	Route(ctx context.Context, request RunRequest) (RouteDecision, error)
}

// RuleRouter 是语义路由上线前的安全回退。
// 明确指定 Skill 时优先使用；携带工单时先进入工单诊断；只有纯代码问题才直接进入代码调查。
type RuleRouter struct {
	registry *Registry
}

func NewRuleRouter(registry *Registry) (*RuleRouter, error) {
	if registry == nil {
		return nil, errors.New("skill registry is nil")
	}
	if _, err := registry.Get(SkillTicketDiagnosis); err != nil {
		return nil, fmt.Errorf("rule router requires %q: %w", SkillTicketDiagnosis, err)
	}
	return &RuleRouter{registry: registry}, nil
}

func (r *RuleRouter) Route(_ context.Context, request RunRequest) (RouteDecision, error) {
	if err := request.Validate(); err != nil {
		return RouteDecision{}, err
	}
	if request.RequestedSkill != "" {
		if _, err := r.registry.Get(request.RequestedSkill); err != nil {
			return RouteDecision{}, err
		}
		return RouteDecision{SkillID: request.RequestedSkill, Reason: "explicit_skill", Confidence: 1}, nil
	}
	if strings.TrimSpace(request.ExternalCaseID) != "" {
		return RouteDecision{SkillID: SkillTicketDiagnosis, Reason: "external_case_present", Confidence: 1}, nil
	}

	query := strings.ToLower(request.UserQuery)
	for _, keyword := range []string{"代码", "源码", "函数", "提交", "commit", "github", "repository", "repo"} {
		if strings.Contains(query, keyword) {
			if _, err := r.registry.Get(SkillCodeInvestigation); err == nil {
				return RouteDecision{SkillID: SkillCodeInvestigation, Reason: "code_keyword", Confidence: 0.8}, nil
			}
			return RouteDecision{}, fmt.Errorf("%w: %s", ErrSkillUnavailable, SkillCodeInvestigation)
		}
	}
	return RouteDecision{SkillID: SkillTicketDiagnosis, Reason: "safe_default", Confidence: 0.5}, nil
}
