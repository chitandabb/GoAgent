package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/cloudwego/eino/components/tool"
)

var ErrToolNotAllowed = errors.New("tool is not registered or allowed")

// ToolRegistry 是应用层 Tool 白名单的事实来源。
// MCP Server 即使意外暴露了更多工具，也只有注册到这里且被 Skill 引用的工具才能进入 ReAct。
type ToolRegistry struct {
	tools map[string]tool.BaseTool
}

func NewToolRegistry(ctx context.Context, tools ...tool.BaseTool) (*ToolRegistry, error) {
	if len(tools) == 0 {
		return nil, errors.New("at least one tool is required")
	}
	registry := &ToolRegistry{tools: make(map[string]tool.BaseTool, len(tools))}
	for _, current := range tools {
		if current == nil {
			return nil, errors.New("tool is nil")
		}
		info, err := current.Info(ctx)
		if err != nil {
			return nil, fmt.Errorf("read tool info: %w", err)
		}
		if info == nil || !toolNamePattern.MatchString(info.Name) {
			return nil, fmt.Errorf("tool has invalid name %q", info.Name)
		}
		if _, exists := registry.tools[info.Name]; exists {
			return nil, fmt.Errorf("duplicate tool %q", info.Name)
		}
		registry.tools[info.Name] = current
	}
	return registry, nil
}

func (r *ToolRegistry) Resolve(names []string) ([]tool.BaseTool, error) {
	if r == nil {
		return nil, ErrToolNotAllowed
	}
	resolved := make([]tool.BaseTool, 0, len(names))
	for _, name := range names {
		current, exists := r.tools[name]
		if !exists {
			return nil, fmt.Errorf("%w: %s", ErrToolNotAllowed, name)
		}
		resolved = append(resolved, current)
	}
	return resolved, nil
}

func (r *ToolRegistry) Names() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// SchemaBytes 返回序列化 ToolInfo 的字节数，只作为接入真实模型前的静态代理指标。
// 简历中的 Token 降幅必须使用供应商返回的 input token usage 重新测量。
func (r *ToolRegistry) SchemaBytes(ctx context.Context, names []string) (int, error) {
	tools, err := r.Resolve(names)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, current := range tools {
		info, infoErr := current.Info(ctx)
		if infoErr != nil {
			return 0, fmt.Errorf("read tool info: %w", infoErr)
		}
		encoded, marshalErr := json.Marshal(info)
		if marshalErr != nil {
			return 0, fmt.Errorf("marshal tool info: %w", marshalErr)
		}
		total += len(encoded)
	}
	return total, nil
}
