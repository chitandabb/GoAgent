package agent

import (
	"context"
	"errors"
	"sync"

	"github.com/cloudwego/eino/components/tool"
)

var ErrAgentToolRunLimitExhausted = errors.New("agent tool run limit exhausted")

type agentToolRunPolicyContextKey struct{}

type agentToolRunPolicy struct {
	mu      sync.Mutex
	blocked map[string]struct{}
	limits  map[string]int
	used    map[string]int
}

func newAgentToolRunPolicy(blocked []string, limits map[string]int) *agentToolRunPolicy {
	policy := &agentToolRunPolicy{
		blocked: make(map[string]struct{}, len(blocked)),
		limits:  make(map[string]int, len(limits)),
		used:    make(map[string]int, len(limits)),
	}
	for _, name := range blocked {
		if name != "" {
			policy.blocked[name] = struct{}{}
		}
	}
	for name, limit := range limits {
		if name != "" && limit >= 0 {
			policy.limits[name] = limit
		}
	}
	return policy
}

func withAgentToolRunPolicy(ctx context.Context, policy *agentToolRunPolicy) context.Context {
	if policy == nil {
		return ctx
	}
	return context.WithValue(ctx, agentToolRunPolicyContextKey{}, policy)
}

func agentToolRunPolicyFromContext(ctx context.Context) *agentToolRunPolicy {
	policy, _ := ctx.Value(agentToolRunPolicyContextKey{}).(*agentToolRunPolicy)
	return policy
}

func (p *agentToolRunPolicy) reserve(name string) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, blocked := p.blocked[name]; blocked {
		return ErrAgentToolRunLimitExhausted
	}
	limit, limited := p.limits[name]
	if !limited {
		return nil
	}
	if p.used[name] >= limit {
		return ErrAgentToolRunLimitExhausted
	}
	p.used[name]++
	return nil
}

func (p *agentToolRunPolicy) remaining(name string) (int, bool) {
	if p == nil {
		return 0, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, blocked := p.blocked[name]; blocked {
		return 0, true
	}
	limit, limited := p.limits[name]
	if !limited {
		return 0, false
	}
	return max(0, limit-p.used[name]), true
}

func filterAgentToolsForRun(ctx context.Context, tools []tool.BaseTool) ([]tool.BaseTool, error) {
	policy := agentToolRunPolicyFromContext(ctx)
	if policy == nil || len(policy.blocked) == 0 {
		return append([]tool.BaseTool(nil), tools...), nil
	}
	filtered := make([]tool.BaseTool, 0, len(tools))
	for _, current := range tools {
		if current == nil {
			return nil, errors.New("agent tool provider returned a nil tool")
		}
		info, err := current.Info(ctx)
		if err != nil {
			return nil, err
		}
		if info == nil {
			return nil, errors.New("agent tool provider returned nil tool info")
		}
		if _, blocked := policy.blocked[info.Name]; blocked {
			continue
		}
		filtered = append(filtered, current)
	}
	return filtered, nil
}
