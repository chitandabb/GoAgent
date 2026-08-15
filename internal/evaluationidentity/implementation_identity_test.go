package evaluationidentity

import (
	"context"
	"errors"
	"testing"
)

func withGitSeams(
	t *testing.T,
	revParse func(context.Context) (string, error),
	status func(context.Context) (string, error),
) {
	t.Helper()
	originalRevParse := GitRevParseShortHead
	originalStatus := GitStatusPorcelain
	GitRevParseShortHead = revParse
	GitStatusPorcelain = status
	t.Cleanup(func() {
		GitRevParseShortHead = originalRevParse
		GitStatusPorcelain = originalStatus
	})
}

// TestGitFallbackIdentityDetectsDirtyStatus：rev-parse 成功、status 显示修改
// -> dirty。
func TestGitFallbackIdentityDetectsDirtyStatus(t *testing.T) {
	withGitSeams(t,
		func(context.Context) (string, error) { return "abc123", nil },
		func(context.Context) (string, error) { return " M internal/agent/runner.go\n", nil },
	)
	identity, err := gitFallbackIdentity()
	if err != nil {
		t.Fatalf("GitFallbackIdentity(): %v", err)
	}
	if identity.Revision != "abc123" || !identity.Dirty {
		t.Fatalf("identity = %+v, want revision abc123 and dirty", identity)
	}
}

// TestGitFallbackIdentityStatusFailureIsNotClean：rev-parse 成功、status 失败
// -> error，绝不能认为 clean。
func TestGitFallbackIdentityStatusFailureIsNotClean(t *testing.T) {
	withGitSeams(t,
		func(context.Context) (string, error) { return "abc123", nil },
		func(context.Context) (string, error) { return "", errors.New("git status failed") },
	)
	identity, err := gitFallbackIdentity()
	if err == nil {
		t.Fatal("git status failure must return an error")
	}
	if !identity.Dirty {
		t.Fatalf("status failure must not be considered clean, identity = %+v", identity)
	}
	if identity.Revision != "abc123" {
		t.Fatalf("known revision must be preserved, got %q", identity.Revision)
	}
}

// TestGitStatusFailureAllowDirtyKeepsKnownRevision 验证完整链路：git status
// 失败时 revision 保留已知值、dirty=true、error 非空；ResolveImplementationIdentity
// 不再把 GitFallbackIdentity 已取得的 identity 覆盖成 unknown，
// -allow-dirty 路径仍记录已知 revision。
func TestGitStatusFailureAllowDirtyKeepsKnownRevision(t *testing.T) {
	withGitSeams(t,
		func(context.Context) (string, error) { return "abc123", nil },
		func(context.Context) (string, error) { return "", errors.New("git status failed") },
	)
	identity, err := gitFallbackIdentity()
	if err == nil {
		t.Fatal("git status failure must return an error")
	}
	if identity.Revision != "abc123" || !identity.Dirty {
		t.Fatalf("status failure identity = %+v, want known revision abc123 with dirty=true", identity)
	}
	// ResolveImplementationIdentity 的 git 回退路径原样返回该 identity；
	// -allow-dirty 决策继续保留已知 revision，不变成 unknown。
	identity, err = ResolveImplementationIdentity()
	if err == nil {
		t.Fatal("ResolveImplementationIdentity must surface the git status failure")
	}
	if identity.Revision != "abc123" || !identity.Dirty {
		t.Fatalf("resolve must keep the known revision, got %+v", identity)
	}
	identity, err = EvaluateImplementationIdentity(identity, true)
	if err != nil {
		t.Fatalf("allow-dirty must accept the identity: %v", err)
	}
	if identity.Revision != "abc123" || !identity.Dirty {
		t.Fatalf("allow-dirty must keep the known revision, got %+v", identity)
	}
}

// TestGitFallbackIdentityCleanTree：rev-parse 与 status 都成功且 status 为空
// -> clean。
func TestGitFallbackIdentityCleanTree(t *testing.T) {
	withGitSeams(t,
		func(context.Context) (string, error) { return "abc123", nil },
		func(context.Context) (string, error) { return "", nil },
	)
	identity, err := gitFallbackIdentity()
	if err != nil {
		t.Fatalf("GitFallbackIdentity(): %v", err)
	}
	if identity.Revision != "abc123" || identity.Dirty {
		t.Fatalf("identity = %+v, want clean revision abc123", identity)
	}
}

// TestGitFallbackIdentityRevParseFailureIsUnknown：rev-parse 失败 -> unknown
// + error。
func TestGitFallbackIdentityRevParseFailureIsUnknown(t *testing.T) {
	withGitSeams(t,
		func(context.Context) (string, error) { return "", errors.New("git rev-parse failed") },
		func(context.Context) (string, error) { return "", nil },
	)
	identity, err := gitFallbackIdentity()
	if err == nil {
		t.Fatal("rev-parse failure must return an error")
	}
	if identity.Revision != "unknown" || !identity.Dirty {
		t.Fatalf("identity = %+v, want revision unknown and dirty", identity)
	}
}

// TestEvaluateImplementationIdentityFormalRejectsUnknown：unknown/dirty 在正式
// 模式被拒绝，clean 通过。
func TestEvaluateImplementationIdentityFormalRejectsUnknown(t *testing.T) {
	if _, err := EvaluateImplementationIdentity(
		Identity{Revision: "unknown", Dirty: true}, false,
	); err == nil {
		t.Fatal("formal mode must reject an unknown revision")
	}
	if _, err := EvaluateImplementationIdentity(
		Identity{Revision: "abc123", Dirty: true}, false,
	); err == nil {
		t.Fatal("formal mode must reject a dirty revision")
	}
	if _, err := EvaluateImplementationIdentity(
		Identity{Revision: "abc123", Dirty: false}, false,
	); err != nil {
		t.Fatalf("formal mode must accept a clean revision: %v", err)
	}
}

// TestEvaluateImplementationIdentityAllowDirtyAcceptsUnknown：allow-dirty 接受
// unknown/dirty，但记录 dirty=true（仅本地 smoke）。
func TestEvaluateImplementationIdentityAllowDirtyAcceptsUnknown(t *testing.T) {
	identity, err := EvaluateImplementationIdentity(
		Identity{Revision: "unknown", Dirty: true}, true,
	)
	if err != nil {
		t.Fatalf("allow-dirty must accept unknown: %v", err)
	}
	if !identity.Dirty || identity.Revision != "unknown" {
		t.Fatalf("allow-dirty identity = %+v, want revision unknown with dirty=true", identity)
	}
	identity, err = EvaluateImplementationIdentity(
		Identity{Revision: "abc123", Dirty: true}, true,
	)
	if err != nil {
		t.Fatalf("allow-dirty must accept dirty: %v", err)
	}
	if !identity.Dirty || identity.Revision != "abc123" {
		t.Fatalf("allow-dirty identity = %+v, want known revision with dirty=true", identity)
	}
}
