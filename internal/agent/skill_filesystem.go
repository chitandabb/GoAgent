package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/cloudwego/eino/adk/filesystem"
)

const (
	nativeSkillBaseDir       = "/skills"
	maxSkillResourceBytes    = 256 * 1024
	maxSkillFilesystemResult = 256
)

var (
	ErrSkillFilesystemReadOnly = errors.New("skill filesystem is read-only")
	ErrUnsafeSkillPath         = errors.New("unsafe skill path")
)

// ReadOnlySkillFilesystem 把本地 Skill 目录映射为 Eino 使用的虚拟 /skills 目录。
// 所有读操作都拒绝目录逃逸和符号链接；Write/Edit 始终关闭。
type ReadOnlySkillFilesystem struct {
	root string
}

func NewReadOnlySkillFilesystem(root string) (*ReadOnlySkillFilesystem, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("skill directory is required")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve skill directory: %w", err)
	}
	info, err := os.Lstat(rootAbs)
	if err != nil {
		return nil, fmt.Errorf("inspect skill directory: %w", err)
	}
	if err = rejectSkillLink(rootAbs, info); err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("skill root must be a directory")
	}
	return &ReadOnlySkillFilesystem{root: filepath.Clean(rootAbs)}, nil
}

func (b *ReadOnlySkillFilesystem) LsInfo(
	ctx context.Context,
	req *filesystem.LsInfoRequest,
) ([]filesystem.FileInfo, error) {
	if req == nil {
		return nil, errors.New("skill ls request is nil")
	}
	physical, virtual, err := b.resolve(req.Path)
	if err != nil {
		return nil, err
	}
	if err = contextErr(ctx); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(physical)
	if err != nil {
		return nil, fmt.Errorf("list skill directory: %w", err)
	}
	if len(entries) > maxSkillFilesystemResult {
		return nil, fmt.Errorf("skill directory contains more than %d entries", maxSkillFilesystemResult)
	}
	result := make([]filesystem.FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil, fmt.Errorf("inspect skill entry %q: %w", entry.Name(), infoErr)
		}
		if linkErr := rejectSkillLink(filepath.Join(physical, entry.Name()), info); linkErr != nil {
			return nil, linkErr
		}
		result = append(result, newSkillFileInfo(path.Join(virtual, entry.Name()), info))
	}
	return result, nil
}

func (b *ReadOnlySkillFilesystem) Read(
	ctx context.Context,
	req *filesystem.ReadRequest,
) (*filesystem.FileContent, error) {
	if req == nil {
		return nil, errors.New("skill read request is nil")
	}
	physical, _, err := b.resolve(req.FilePath)
	if err != nil {
		return nil, err
	}
	if err = contextErr(ctx); err != nil {
		return nil, err
	}
	data, err := readSkillRegularFile(physical)
	if err != nil {
		return nil, err
	}
	content := selectSkillLines(string(data), req.Offset, req.Limit)
	return &filesystem.FileContent{Content: content}, nil
}

func (b *ReadOnlySkillFilesystem) GrepRaw(
	ctx context.Context,
	req *filesystem.GrepRequest,
) ([]filesystem.GrepMatch, error) {
	if req == nil {
		return nil, errors.New("skill grep request is nil")
	}
	if strings.TrimSpace(req.Pattern) == "" {
		return nil, errors.New("skill grep pattern is required")
	}
	if req.EnableMultiline {
		return nil, errors.New("multiline skill grep is not supported")
	}
	physical, virtual, err := b.resolve(defaultSkillPath(req.Path))
	if err != nil {
		return nil, err
	}
	pattern := req.Pattern
	if req.CaseInsensitive {
		pattern = "(?i)" + pattern
	}
	expression, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("compile skill grep pattern: %w", err)
	}
	if req.Glob != "" && !doublestar.ValidatePattern(filepath.ToSlash(req.Glob)) {
		return nil, errors.New("invalid skill grep glob")
	}

	result := make([]filesystem.GrepMatch, 0)
	err = filepath.WalkDir(physical, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := contextErr(ctx); err != nil {
			return err
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if linkErr := rejectSkillLink(current, info); linkErr != nil {
			return linkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: non-regular resource %q", ErrUnsafeSkillPath, current)
		}
		rel, relErr := filepath.Rel(physical, current)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if req.Glob != "" {
			matched, matchErr := doublestar.Match(filepath.ToSlash(req.Glob), rel)
			if matchErr != nil {
				return matchErr
			}
			if !matched {
				return nil
			}
		}
		if req.FileType != "" && !strings.EqualFold(path.Ext(rel), "."+strings.TrimPrefix(req.FileType, ".")) {
			return nil
		}
		data, readErr := readSkillRegularFile(current)
		if readErr != nil {
			return readErr
		}
		if strings.IndexByte(string(data), 0) >= 0 {
			return nil
		}
		for index, line := range strings.Split(string(data), "\n") {
			if !expression.MatchString(strings.TrimSuffix(line, "\r")) {
				continue
			}
			result = append(result, filesystem.GrepMatch{
				Content: strings.TrimSuffix(line, "\r"),
				Path:    path.Join(virtual, rel),
				Line:    index + 1,
			})
			if len(result) >= maxSkillFilesystemResult {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("grep skill resources: %w", err)
	}
	return result, nil
}

func (b *ReadOnlySkillFilesystem) GlobInfo(
	ctx context.Context,
	req *filesystem.GlobInfoRequest,
) ([]filesystem.FileInfo, error) {
	if req == nil {
		return nil, errors.New("skill glob request is nil")
	}
	pattern := filepath.ToSlash(strings.TrimSpace(req.Pattern))
	if pattern == "" || path.IsAbs(pattern) || containsParentSegment(pattern) || !doublestar.ValidatePattern(pattern) {
		return nil, errors.New("invalid skill glob pattern")
	}
	physical, _, err := b.resolve(defaultSkillPath(req.Path))
	if err != nil {
		return nil, err
	}
	result := make([]filesystem.FileInfo, 0)
	err = filepath.WalkDir(physical, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := contextErr(ctx); err != nil {
			return err
		}
		if current == physical {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if linkErr := rejectSkillLink(current, info); linkErr != nil {
			return linkErr
		}
		rel, relErr := filepath.Rel(physical, current)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		matched, matchErr := doublestar.Match(pattern, rel)
		if matchErr != nil {
			return matchErr
		}
		if !matched {
			return nil
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("%w: non-regular resource %q", ErrUnsafeSkillPath, current)
		}
		// 返回相对 req.Path 的 slash 路径，规避 Eino v0.9.13 在 Windows 下混用 filepath/path。
		result = append(result, newSkillFileInfo(rel, info))
		if len(result) > maxSkillFilesystemResult {
			return fmt.Errorf("skill glob matched more than %d entries", maxSkillFilesystemResult)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("glob skill resources: %w", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func (b *ReadOnlySkillFilesystem) Write(context.Context, *filesystem.WriteRequest) error {
	return ErrSkillFilesystemReadOnly
}

func (b *ReadOnlySkillFilesystem) Edit(context.Context, *filesystem.EditRequest) error {
	return ErrSkillFilesystemReadOnly
}

func (b *ReadOnlySkillFilesystem) resolve(requested string) (physical string, virtual string, err error) {
	if b == nil || b.root == "" {
		return "", "", errors.New("skill filesystem is nil")
	}
	requested = strings.TrimSpace(requested)
	if requested == "" || filepath.VolumeName(requested) != "" {
		return "", "", fmt.Errorf("%w: path must be under %s", ErrUnsafeSkillPath, nativeSkillBaseDir)
	}
	virtual = strings.ReplaceAll(requested, `\`, "/")
	if strings.HasPrefix(virtual, "//") || containsParentSegment(virtual) {
		return "", "", fmt.Errorf("%w: path traversal is not allowed", ErrUnsafeSkillPath)
	}
	if !strings.HasPrefix(virtual, "/") {
		virtual = path.Join(nativeSkillBaseDir, virtual)
	}
	virtual = path.Clean(virtual)
	if virtual != nativeSkillBaseDir && !strings.HasPrefix(virtual, nativeSkillBaseDir+"/") {
		return "", "", fmt.Errorf("%w: path must be under %s", ErrUnsafeSkillPath, nativeSkillBaseDir)
	}
	rel := strings.TrimPrefix(virtual, nativeSkillBaseDir)
	rel = strings.TrimPrefix(rel, "/")
	physical = b.root
	if rel != "" {
		physical = filepath.Join(b.root, filepath.FromSlash(rel))
	}
	within, relErr := filepath.Rel(b.root, physical)
	if relErr != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("%w: resolved path escapes skill root", ErrUnsafeSkillPath)
	}
	if err = rejectSkillSymlinks(b.root, physical); err != nil {
		return "", "", err
	}
	return physical, virtual, nil
}

func rejectSkillSymlinks(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return fmt.Errorf("resolve skill resource: %w", err)
	}
	current := root
	parts := strings.Split(rel, string(filepath.Separator))
	if rel == "." {
		parts = nil
	}
	for _, part := range parts {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			return fmt.Errorf("inspect skill resource: %w", statErr)
		}
		if err = rejectSkillLink(current, info); err != nil {
			return err
		}
	}
	return nil
}

func readSkillRegularFile(filePath string) ([]byte, error) {
	info, err := os.Lstat(filePath)
	if err != nil {
		return nil, fmt.Errorf("inspect skill resource: %w", err)
	}
	if err = rejectSkillLink(filePath, info); err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: skill resource must be a regular file", ErrUnsafeSkillPath)
	}
	if info.Size() > maxSkillResourceBytes {
		return nil, fmt.Errorf("skill resource exceeds %d bytes", maxSkillResourceBytes)
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read skill resource: %w", err)
	}
	return data, nil
}

func rejectSkillLink(filePath string, info os.FileInfo) error {
	if info == nil {
		return fmt.Errorf("%w: missing file information for %q", ErrUnsafeSkillPath, filePath)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: symbolic link %q is not allowed", ErrUnsafeSkillPath, filePath)
	}
	reparse, err := isSkillReparsePoint(filePath)
	if err != nil {
		return fmt.Errorf("inspect Skill reparse point %q: %w", filePath, err)
	}
	if reparse {
		return fmt.Errorf("%w: reparse point %q is not allowed", ErrUnsafeSkillPath, filePath)
	}
	return nil
}

func selectSkillLines(content string, offset, limit int) string {
	lines := strings.Split(content, "\n")
	if offset < 1 {
		offset = 1
	}
	start := offset - 1
	if start >= len(lines) {
		return ""
	}
	end := len(lines)
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	return strings.Join(lines[start:end], "\n")
}

func containsParentSegment(value string) bool {
	for _, segment := range strings.Split(strings.ReplaceAll(value, `\`, "/"), "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

func defaultSkillPath(value string) string {
	if strings.TrimSpace(value) == "" {
		return nativeSkillBaseDir
	}
	return value
}

func newSkillFileInfo(filePath string, info os.FileInfo) filesystem.FileInfo {
	return filesystem.FileInfo{
		Path:       filepath.ToSlash(filePath),
		IsDir:      info.IsDir(),
		Size:       info.Size(),
		ModifiedAt: info.ModTime().UTC().Format(time.RFC3339Nano),
	}
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
