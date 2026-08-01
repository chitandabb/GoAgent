package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/components/tool"
)

func TestNativeSkillRuntimeDisclosesInstructionsAndReferencesProgressively(t *testing.T) {
	root := t.TempDir()
	writeNativeSkillPackage(
		t, root, string(SkillTicketDiagnosis), string(SkillTicketDiagnosis),
		"读取测试工单", "FULL_SKILL_SENTINEL\n需要时读取 references/evidence-rules.md。",
	)
	referenceDir := filepath.Join(root, string(SkillTicketDiagnosis), "references")
	if err := os.MkdirAll(referenceDir, 0o755); err != nil {
		t.Fatalf("create references directory: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(referenceDir, "evidence-rules.md"),
		[]byte("REFERENCE_SENTINEL\nsecond line\n"),
		0o600,
	); err != nil {
		t.Fatalf("write reference: %v", err)
	}

	runtimeResources, err := NewNativeSkillRuntime(context.Background(), root)
	if err != nil {
		t.Fatalf("NewNativeSkillRuntime: %v", err)
	}
	_, runCtx, err := runtimeResources.Middleware.BeforeAgent(
		context.Background(),
		&adk.ChatModelAgentContext{},
	)
	if err != nil {
		t.Fatalf("BeforeAgent: %v", err)
	}
	skillTool := findToolForTest(t, runCtx.Tools, "skill")
	info, err := skillTool.Info(context.Background())
	if err != nil {
		t.Fatalf("Skill Tool Info: %v", err)
	}
	if !strings.Contains(info.Desc, "读取测试工单") {
		t.Fatalf("initial Skill metadata missing: %q", info.Desc)
	}
	if strings.Contains(info.Desc, "FULL_SKILL_SENTINEL") || strings.Contains(info.Desc, "REFERENCE_SENTINEL") {
		t.Fatalf("initial Tool description leaked Skill body/reference: %q", info.Desc)
	}

	skillResult, err := skillTool.InvokableRun(
		context.Background(),
		`{"skill":"ticket-diagnosis"}`,
	)
	if err != nil {
		t.Fatalf("invoke Skill Tool: %v", err)
	}
	if !strings.Contains(skillResult, "FULL_SKILL_SENTINEL") {
		t.Fatalf("Skill instructions were not loaded: %q", skillResult)
	}
	if strings.Contains(skillResult, "REFERENCE_SENTINEL") {
		t.Fatalf("Skill load eagerly included reference content: %q", skillResult)
	}

	referenceResult, err := runtimeResources.ReferenceTool.InvokableRun(
		context.Background(),
		`{"skill":"ticket-diagnosis","path":"references/evidence-rules.md"}`,
	)
	if err != nil {
		t.Fatalf("invoke reference Tool: %v", err)
	}
	if !strings.Contains(referenceResult, "REFERENCE_SENTINEL") {
		t.Fatalf("reference content was not loaded: %q", referenceResult)
	}
}

func TestReadOnlySkillFilesystemSupportsWindowsPathsAndRejectsWrites(t *testing.T) {
	root := t.TempDir()
	writeNativeSkillPackage(
		t, root, string(SkillTicketDiagnosis), string(SkillTicketDiagnosis), "test skill", "line one\nline two",
	)
	backend, err := NewReadOnlySkillFilesystem(root)
	if err != nil {
		t.Fatalf("NewReadOnlySkillFilesystem: %v", err)
	}
	content, err := backend.Read(context.Background(), &filesystem.ReadRequest{
		FilePath: `\skills\ticket-diagnosis\SKILL.md`,
		Offset:   2,
		Limit:    2,
	})
	if err != nil {
		t.Fatalf("Read Windows-style path: %v", err)
	}
	if !strings.Contains(content.Content, "name: ticket-diagnosis") {
		t.Fatalf("line selection content = %q", content.Content)
	}
	entries, err := backend.GlobInfo(context.Background(), &filesystem.GlobInfoRequest{
		Path: nativeSkillBaseDir, Pattern: "*/SKILL.md",
	})
	if err != nil {
		t.Fatalf("GlobInfo: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "ticket-diagnosis/SKILL.md" {
		t.Fatalf("glob entries = %+v", entries)
	}
	if err = backend.Write(context.Background(), &filesystem.WriteRequest{}); !errors.Is(err, ErrSkillFilesystemReadOnly) {
		t.Fatalf("Write error = %v", err)
	}
	if err = backend.Edit(context.Background(), &filesystem.EditRequest{}); !errors.Is(err, ErrSkillFilesystemReadOnly) {
		t.Fatalf("Edit error = %v", err)
	}
}

func TestSkillResourcesRejectTraversalAndSymbolicLinks(t *testing.T) {
	root := t.TempDir()
	writeNativeSkillPackage(
		t, root, string(SkillTicketDiagnosis), string(SkillTicketDiagnosis), "test skill", "prompt",
	)
	backend, err := NewReadOnlySkillFilesystem(root)
	if err != nil {
		t.Fatalf("NewReadOnlySkillFilesystem: %v", err)
	}
	unsafePaths := []string{
		"/skills/../outside.md",
		"../outside.md",
		`C:\outside.md`,
		`\\server\share\outside.md`,
	}
	for _, unsafePath := range unsafePaths {
		_, err = backend.Read(context.Background(), &filesystem.ReadRequest{FilePath: unsafePath})
		if !errors.Is(err, ErrUnsafeSkillPath) {
			t.Errorf("Read(%q) error = %v, want ErrUnsafeSkillPath", unsafePath, err)
		}
	}
	for _, unsafeReference := range []string{
		"../outside.md",
		"references/../SKILL.md",
		"references/nested/rules.md",
		"scripts/run.md",
		`C:\outside.md`,
	} {
		if _, err = validateReferencePath(unsafeReference); err == nil {
			t.Errorf("validateReferencePath(%q) succeeded", unsafeReference)
		}
	}

	outsideDirectory := t.TempDir()
	outside := filepath.Join(outsideDirectory, "outside.md")
	if err = os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	link := filepath.Join(root, string(SkillTicketDiagnosis), "references-link")
	if err = os.Symlink(outsideDirectory, link); err != nil {
		if runtime.GOOS != "windows" {
			t.Fatalf("create symlink: %v", err)
		}
		// Windows 创建普通 symlink 可能需要 Developer Mode；目录 Junction 不需要管理员权限，
		// 同样属于必须拒绝的 reparse point。
		if output, junctionErr := exec.Command("cmd.exe", "/c", "mklink", "/J", link, outsideDirectory).CombinedOutput(); junctionErr != nil {
			t.Fatalf("create Windows junction: %v: %s", junctionErr, output)
		}
	}
	_, err = backend.Read(context.Background(), &filesystem.ReadRequest{
		FilePath: fmt.Sprintf("/skills/%s/references-link/outside.md", SkillTicketDiagnosis),
	})
	if !errors.Is(err, ErrUnsafeSkillPath) {
		t.Fatalf("read symlink error = %v, want ErrUnsafeSkillPath", err)
	}
	_, err = backend.GlobInfo(context.Background(), &filesystem.GlobInfoRequest{
		Path: nativeSkillBaseDir, Pattern: "*/SKILL.md",
	})
	if !errors.Is(err, ErrUnsafeSkillPath) {
		t.Fatalf("glob with symlink error = %v, want ErrUnsafeSkillPath", err)
	}
}

func TestReadSkillReferenceEnforcesLineBudget(t *testing.T) {
	root := t.TempDir()
	writeNativeSkillPackage(
		t, root, string(SkillTicketDiagnosis), string(SkillTicketDiagnosis), "test skill", "prompt",
	)
	referenceDir := filepath.Join(root, string(SkillTicketDiagnosis), "references")
	if err := os.MkdirAll(referenceDir, 0o755); err != nil {
		t.Fatalf("create references directory: %v", err)
	}
	lines := make([]string, 250)
	for index := range lines {
		lines[index] = fmt.Sprintf("line-%03d", index+1)
	}
	if err := os.WriteFile(filepath.Join(referenceDir, "large.md"), []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatalf("write large reference: %v", err)
	}
	backend, err := NewReadOnlySkillFilesystem(root)
	if err != nil {
		t.Fatalf("NewReadOnlySkillFilesystem: %v", err)
	}
	result, err := readSkillReference(context.Background(), backend, readSkillReferenceInput{
		Skill: string(SkillTicketDiagnosis), Path: "references/large.md", Limit: 999,
	})
	if err != nil {
		t.Fatalf("readSkillReference: %v", err)
	}
	if result.LineCount != maxReferenceLineLimit || !result.Truncated {
		t.Fatalf("reference result = %+v", result)
	}
}

func findToolForTest(t *testing.T, tools []tool.BaseTool, name string) tool.InvokableTool {
	t.Helper()
	for _, current := range tools {
		info, err := current.Info(context.Background())
		if err != nil {
			t.Fatalf("Tool.Info: %v", err)
		}
		if info.Name != name {
			continue
		}
		invokable, ok := current.(tool.InvokableTool)
		if !ok {
			t.Fatalf("Tool %q is not invokable", name)
		}
		return invokable
	}
	t.Fatalf("Tool %q not found", name)
	return nil
}

func writeNativeSkillPackage(t *testing.T, root, directory, id, description, content string) {
	t.Helper()
	dir := filepath.Join(root, directory)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create Skill directory: %v", err)
	}
	frontMatter := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n", id, description)
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(frontMatter+content), 0o600); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}
