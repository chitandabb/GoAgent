// Command mesguard-embedding-smoke 对生产 Embedding 管线做单请求冒烟：
// 默认关闭，必须显式传 -allow-provider-calls；最多 1 个请求、固定短文本，
// 不输出向量与原文。进程内仍然经过 RPM/TPM/并发门禁与结构化错误分类；
// maxAttempts 强制为 1，保证最多一次真实 HTTP 请求。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformembedding "github.com/chitandabb/GoAgent/internal/platform/dashscopeembedding"
)

// smokeText 是固定短文本，刻意不包含任何业务或敏感内容。
const smokeText = "mesguard production embedding smoke"

type smokeOptions struct {
	allowProviderCalls bool
}

// runSmoke 通过依赖注入测试：注入 Embedder 后，默认（allowProviderCalls
// 为 false）零 Provider 调用；显式允许后恰好 1 次短文本请求，输出只包含
// bounded 汇总字段，绝不输出向量或原文。
func runSmoke(
	ctx context.Context,
	embedder knowledge.Embedder,
	profile knowledge.EmbeddingProfile,
	options smokeOptions,
	out io.Writer,
) error {
	if out == nil {
		out = io.Discard
	}
	if !options.allowProviderCalls {
		return errors.New("embedding smoke provider calls are disabled; add -allow-provider-calls to run")
	}
	if embedder == nil {
		return errors.New("embedding smoke embedder is unavailable")
	}
	result, err := embedder.Embed(ctx, knowledge.EmbeddingRequest{
		Texts: []string{smokeText}, InputType: knowledge.EmbeddingInputDocument,
	})
	if err != nil {
		return fmt.Errorf("embedding smoke request failed: %w", err)
	}
	fmt.Fprintf(out, "embedding_smoke model=%s dimensions=%d vectors=%d total_tokens=%d\n",
		profile.Model, profile.Dimensions, len(result.Vectors), result.Usage.TotalTokens)
	return nil
}

func run(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("mesguard-embedding-smoke", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	allowProviderCalls := flags.Bool("allow-provider-calls", false, "allow one real embedding request")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !*allowProviderCalls {
		return errors.New("embedding smoke provider calls are disabled; add -allow-provider-calls to run")
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !cfg.Models.Embedding.Enabled {
		return errors.New("embedding smoke requires models.embedding")
	}
	profile, err := cfg.Models.Embedding.Profile()
	if err != nil {
		return err
	}
	// 强制单次尝试：冒烟最多 1 个请求，不做重试。
	smokeConfig := cfg.Models.Embedding
	smokeConfig.MaxAttempts = 1
	client, err := platformembedding.NewClient(smokeConfig, nil)
	if err != nil {
		return fmt.Errorf("build embedding smoke client: %w", err)
	}
	return runSmoke(context.Background(), client, profile, smokeOptions{
		allowProviderCalls: *allowProviderCalls,
	}, out)
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
