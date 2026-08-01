package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cloudwego/eino/adk"
	skillmiddleware "github.com/cloudwego/eino/adk/middlewares/skill"
	"github.com/cloudwego/eino/components/tool"
)

// NativeSkillRuntime 是 P2 的原生 Skill 装配结果。
// Backend 交给 Eino 按需加载 SKILL.md，ReferenceTool 只允许读取 references 下的 Markdown。
type NativeSkillRuntime struct {
	Filesystem    *ReadOnlySkillFilesystem
	Backend       skillmiddleware.Backend
	Middleware    adk.ChatModelAgentMiddleware
	ReferenceTool tool.InvokableTool
	skills        map[SkillID]skillmiddleware.Skill
}

func NewNativeSkillRuntime(ctx context.Context, root string) (*NativeSkillRuntime, error) {
	filesystemBackend, backend, err := newNativeSkillBackend(ctx, root)
	if err != nil {
		return nil, err
	}
	loadedSkills, err := loadValidatedNativeSkills(ctx, backend)
	if err != nil {
		return nil, err
	}
	middleware, err := skillmiddleware.NewMiddleware(ctx, &skillmiddleware.Config{Backend: backend})
	if err != nil {
		return nil, fmt.Errorf("build Eino Skill Middleware: %w", err)
	}
	referenceTool, err := NewReadSkillReferenceTool(filesystemBackend)
	if err != nil {
		return nil, fmt.Errorf("build Skill reference Tool: %w", err)
	}
	skills := make(map[SkillID]skillmiddleware.Skill, len(loadedSkills))
	for _, current := range loadedSkills {
		skills[SkillID(current.Name)] = current
	}
	return &NativeSkillRuntime{
		Filesystem:    filesystemBackend,
		Backend:       backend,
		Middleware:    middleware,
		ReferenceTool: referenceTool,
		skills:        skills,
	}, nil
}

// Instruction 返回入口 Skill 的完整 SOP。只有入口 Skill 会被应用预加载，
// 其他 Skill 仍由模型通过 Eino 的 skill Tool 渐进读取。
func (r *NativeSkillRuntime) Instruction(_ context.Context, id SkillID) (string, error) {
	if r == nil {
		return "", errors.New("native Skill runtime is nil")
	}
	current, ok := r.skills[id]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrSkillUnavailable, id)
	}
	return current.Content, nil
}

func (r *NativeSkillRuntime) HasSkill(id SkillID) bool {
	if r == nil {
		return false
	}
	_, ok := r.skills[id]
	return ok
}

func newNativeSkillBackend(
	ctx context.Context,
	root string,
) (*ReadOnlySkillFilesystem, skillmiddleware.Backend, error) {
	filesystemBackend, err := NewReadOnlySkillFilesystem(root)
	if err != nil {
		return nil, nil, err
	}
	backend, err := skillmiddleware.NewBackendFromFilesystem(ctx, &skillmiddleware.BackendFromFilesystemConfig{
		Backend: filesystemBackend,
		BaseDir: nativeSkillBaseDir,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("build Eino Skill filesystem backend: %w", err)
	}
	return filesystemBackend, backend, nil
}

func loadValidatedNativeSkills(
	ctx context.Context,
	backend skillmiddleware.Backend,
) ([]skillmiddleware.Skill, error) {
	if backend == nil {
		return nil, errors.New("native Skill backend is required")
	}
	metadata, err := backend.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list native Skills: %w", err)
	}
	if len(metadata) == 0 {
		return nil, errors.New("skill directory contains no SKILL.md packages")
	}
	sort.Slice(metadata, func(i, j int) bool { return metadata[i].Name < metadata[j].Name })
	seen := make(map[string]struct{}, len(metadata))
	result := make([]skillmiddleware.Skill, 0, len(metadata))
	for _, current := range metadata {
		name := strings.TrimSpace(current.Name)
		if !skillIDPattern.MatchString(name) {
			return nil, fmt.Errorf("invalid native Skill name %q", current.Name)
		}
		if name != current.Name || strings.TrimSpace(current.Description) == "" {
			return nil, fmt.Errorf("native Skill %q requires a trimmed description", name)
		}
		if current.Context != "" || current.Agent != "" || current.Model != "" {
			return nil, fmt.Errorf("native Skill %q must use inline context without agent/model overrides", name)
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("duplicate native Skill %q", name)
		}
		seen[name] = struct{}{}
		loaded, loadErr := backend.Get(ctx, name)
		if loadErr != nil {
			return nil, fmt.Errorf("load native Skill %q: %w", name, loadErr)
		}
		if strings.TrimSpace(loaded.Content) == "" {
			return nil, fmt.Errorf("native Skill %q has empty instructions", name)
		}
		if directory := filepath.Base(filepath.Clean(loaded.BaseDirectory)); directory != name {
			return nil, fmt.Errorf("native Skill %q must be stored in a directory with the same name", name)
		}
		result = append(result, loaded)
	}
	return result, nil
}
