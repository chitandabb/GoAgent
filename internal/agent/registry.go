package agent

import (
	"errors"
	"fmt"
	"sort"
)

var ErrSkillNotFound = errors.New("skill not found")

// Registry 在启动时一次性注册 Skill，运行期间只读。
// 这样可以避免请求并发时热更新 Prompt 或 Tool 白名单导致行为漂移。
type Registry struct {
	definitions map[SkillID]SkillDefinition
}

func NewRegistry(definitions ...SkillDefinition) (*Registry, error) {
	if len(definitions) == 0 {
		return nil, errors.New("at least one skill definition is required")
	}
	registry := &Registry{definitions: make(map[SkillID]SkillDefinition, len(definitions))}
	for _, definition := range definitions {
		if err := definition.Validate(); err != nil {
			return nil, err
		}
		if _, exists := registry.definitions[definition.ID]; exists {
			return nil, fmt.Errorf("duplicate skill %q", definition.ID)
		}
		definition.AllowedTools = append([]string(nil), definition.AllowedTools...)
		registry.definitions[definition.ID] = definition
	}
	return registry, nil
}

func (r *Registry) Get(id SkillID) (SkillDefinition, error) {
	if r == nil {
		return SkillDefinition{}, ErrSkillNotFound
	}
	definition, exists := r.definitions[id]
	if !exists {
		return SkillDefinition{}, fmt.Errorf("%w: %s", ErrSkillNotFound, id)
	}
	definition.AllowedTools = append([]string(nil), definition.AllowedTools...)
	return definition, nil
}

func (r *Registry) IDs() []SkillID {
	if r == nil {
		return nil
	}
	ids := make([]SkillID, 0, len(r.definitions))
	for id := range r.definitions {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
