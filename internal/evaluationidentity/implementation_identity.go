// Package evaluationidentity provides fail-closed implementation identity
// resolution shared by the evaluation commands
// (mesguard-agent-paired-eval, mesguard-tool-selection-eval,
// mesguard-text2sql-eval). It was extracted from the duplicated per-command
// copies so every command resolves, evaluates and records the implementation
// revision and working-tree state with exactly the same semantics.
package evaluationidentity

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime/debug"
	"strings"
	"time"
)

// Identity 记录评测运行时的实现身份：Revision 来自 vcs 元数据（build info 的
// vcs.revision 优先，git 兜底），Dirty 表示工作树有未提交修改。dirty/unknown
// 的观测只能用于本地 smoke，不是正式指标。
type Identity struct {
	Revision string
	Dirty    bool
}

const (
	gitRevParseTimeout = 10 * time.Second
	gitStatusTimeout   = 10 * time.Second
)

// git 命令执行 seam：评测命令默认走真实 git；测试替换为桩函数，避免依赖
// 真实 git 工作树状态或故障注入。所有使用本包的命令共享同一组 seam，
// 每个命令的测试二进制相互独立，互不干扰。
var (
	GitRevParseShortHead = func(ctx context.Context) (string, error) {
		output, err := exec.CommandContext(ctx, "git", "rev-parse", "--short", "HEAD").Output()
		return string(output), err
	}
	GitStatusPorcelain = func(ctx context.Context) (string, error) {
		output, err := exec.CommandContext(ctx, "git", "status", "--porcelain").Output()
		return string(output), err
	}
)

// ResolveImplementationIdentity fail-closed 地解析实现身份：优先读取 Go
// build info 的 VCS 元数据，且必须同时确认 revision 与 modified 状态；
// BuildInfo 缺失或不完整时回退到带独立超时的 git 命令。git status 失败时
// 不能默认 clean——无法确认工作树状态一律返回 error，且 identity 保留
// GitFallbackIdentity 已经取得的已知 revision + dirty=true，不得覆盖成
// unknown。任何无法确认 clean/dirty 的情况都以 error 返回。
func ResolveImplementationIdentity() (Identity, error) {
	if info, ok := debug.ReadBuildInfo(); ok {
		var revision, modified string
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				revision = setting.Value
			case "vcs.modified":
				modified = setting.Value
			}
		}
		if revision != "" && modified != "" {
			return Identity{Revision: revision, Dirty: modified == "true"}, nil
		}
	}
	// BuildInfo 缺失或不完整（revision/modified 缺一）时回退 git。
	// git 返回 error 时原样保留它已取得的 identity（已知 revision 或
	// unknown），不覆盖为 unknown。
	identity, err := gitFallbackIdentity()
	if err != nil {
		return identity, err
	}
	return identity, nil
}

func gitFallbackIdentity() (Identity, error) {
	revCtx, cancelRev := context.WithTimeout(context.Background(), gitRevParseTimeout)
	defer cancelRev()
	revisionOutput, err := GitRevParseShortHead(revCtx)
	if err != nil {
		return Identity{Revision: "unknown", Dirty: true}, fmt.Errorf("git rev-parse failed: %w", err)
	}
	revision := strings.TrimSpace(revisionOutput)
	if revision == "" {
		return Identity{Revision: "unknown", Dirty: true}, errors.New("git rev-parse returned an empty revision")
	}
	statusCtx, cancelStatus := context.WithTimeout(context.Background(), gitStatusTimeout)
	defer cancelStatus()
	statusOutput, err := GitStatusPorcelain(statusCtx)
	if err != nil {
		// git status 失败不能默认 dirty=false：无法确认工作树状态。
		return Identity{Revision: revision, Dirty: true}, fmt.Errorf("git status failed: %w", err)
	}
	return Identity{
		Revision: revision, Dirty: len(bytes.TrimSpace([]byte(statusOutput))) > 0,
	}, nil
}

// EvaluateImplementationIdentity 决定身份是否可用于正式评测。formal 模式
// 下 dirty/unknown 直接拒绝；-allow-dirty 模式接受并强制记录 dirty=true，
// 结果仅用于本地 smoke。
func EvaluateImplementationIdentity(identity Identity, allowDirty bool) (Identity, error) {
	if identity.Revision == "unknown" || identity.Dirty {
		if !allowDirty {
			return Identity{}, errors.New("implementation revision is dirty or unknown; refuse formal evaluation (pass -allow-dirty for local smoke)")
		}
		return Identity{Revision: identity.Revision, Dirty: true}, nil
	}
	return identity, nil
}
