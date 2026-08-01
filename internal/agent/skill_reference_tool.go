package agent

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
)

const (
	ToolReadSkillReference    = "read_skill_reference"
	defaultReferenceLineLimit = 120
	maxReferenceLineLimit     = 200
	maxReferenceResultBytes   = 24 * 1024
)

type readSkillReferenceInput struct {
	Skill  string `json:"skill" jsonschema:"required,description=Skill 名称，例如 ticket-diagnosis"`
	Path   string `json:"path" jsonschema:"required,description=references 下的一层 Markdown 路径，例如 references/evidence-rules.md"`
	Offset int    `json:"offset,omitempty" jsonschema:"description=从第几行开始读取，默认 1"`
	Limit  int    `json:"limit,omitempty" jsonschema:"description=最多读取多少行，默认 120，最大 200"`
}

type SkillReferenceResult struct {
	Skill     string `json:"skill"`
	Path      string `json:"path"`
	Offset    int    `json:"offset"`
	LineCount int    `json:"lineCount"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

func NewReadSkillReferenceTool(backend *ReadOnlySkillFilesystem) (tool.InvokableTool, error) {
	if backend == nil {
		return nil, errors.New("Skill reference filesystem is required")
	}
	return toolutils.InferTool(
		ToolReadSkillReference,
		"按需读取已加载 Skill 的只读参考资料；只能访问该 Skill 的 references/*.md，禁止脚本和任意文件访问",
		func(ctx context.Context, input readSkillReferenceInput) (SkillReferenceResult, error) {
			return readSkillReference(ctx, backend, input)
		},
	)
}

func readSkillReference(
	ctx context.Context,
	backend *ReadOnlySkillFilesystem,
	input readSkillReferenceInput,
) (SkillReferenceResult, error) {
	skillName := strings.TrimSpace(input.Skill)
	if skillName != input.Skill || !skillIDPattern.MatchString(skillName) {
		return SkillReferenceResult{}, fmt.Errorf("invalid Skill name %q", input.Skill)
	}
	referencePath, err := validateReferencePath(input.Path)
	if err != nil {
		return SkillReferenceResult{}, err
	}
	offset := input.Offset
	if offset < 1 {
		offset = 1
	}
	limit := input.Limit
	if limit < 1 {
		limit = defaultReferenceLineLimit
	}
	if limit > maxReferenceLineLimit {
		limit = maxReferenceLineLimit
	}

	// 先确认 Skill 本体存在，避免把 root 下的任意 references 目录伪装成 Skill。
	if _, err = backend.Read(ctx, &filesystem.ReadRequest{
		FilePath: path.Join(nativeSkillBaseDir, skillName, "SKILL.md"),
		Offset:   1,
		Limit:    1,
	}); err != nil {
		return SkillReferenceResult{}, fmt.Errorf("validate Skill package: %w", err)
	}
	content, err := backend.Read(ctx, &filesystem.ReadRequest{
		FilePath: path.Join(nativeSkillBaseDir, skillName, referencePath),
		Offset:   offset,
		Limit:    limit + 1,
	})
	if err != nil {
		return SkillReferenceResult{}, fmt.Errorf("read Skill reference: %w", err)
	}
	lines := splitReferenceLines(content.Content)
	truncated := len(lines) > limit
	if truncated {
		lines = lines[:limit]
	}
	joined := strings.Join(lines, "\n")
	if len(joined) > maxReferenceResultBytes {
		joined = truncateValidUTF8(joined, maxReferenceResultBytes)
		truncated = true
	}
	return SkillReferenceResult{
		Skill:     skillName,
		Path:      referencePath,
		Offset:    offset,
		LineCount: len(lines),
		Content:   joined,
		Truncated: truncated,
	}, nil
}

func validateReferencePath(raw string) (string, error) {
	if raw == "" || raw != strings.TrimSpace(raw) || filepath.VolumeName(raw) != "" {
		return "", errors.New("Skill reference path is required and must be relative")
	}
	normalized := strings.ReplaceAll(raw, `\`, "/")
	if path.IsAbs(normalized) || strings.HasPrefix(normalized, "//") || containsParentSegment(normalized) {
		return "", fmt.Errorf("%w: Skill reference path traversal is not allowed", ErrUnsafeSkillPath)
	}
	clean := path.Clean(normalized)
	if path.Dir(clean) != "references" || path.Ext(clean) != ".md" || path.Base(clean) == ".md" {
		return "", errors.New("Skill reference must be a one-level references/*.md file")
	}
	return clean, nil
}

func splitReferenceLines(content string) []string {
	if content == "" {
		return nil
	}
	return strings.Split(content, "\n")
}

func truncateValidUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]
}
