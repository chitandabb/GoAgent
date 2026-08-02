// Package githubmcp 封装官方 GitHub MCP Server 的只读连接和代码参数策略。
package githubmcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/platform/config"

	einomcp "github.com/cloudwego/eino-ext/components/tool/mcp"
	"github.com/cloudwego/eino/components/tool"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

type Connection struct {
	Tools []tool.BaseTool
	cli   client.MCPClient
}

func (c *Connection) Close() error {
	if c == nil || c.cli == nil {
		return nil
	}
	return c.cli.Close()
}

// Connect 使用 Streamable HTTP 连接 GitHub 官方远端 MCP。
// 三层限制分别是：GitHub read-only、精确 MCP tools、应用内 Skill/参数白名单。
func Connect(ctx context.Context, cfg config.GitHubMCPConfig, log *zap.Logger) (*Connection, error) {
	if log == nil {
		return nil, errors.New("github MCP logger is nil")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, errors.New("github MCP is disabled")
	}
	token, err := cfg.Token()
	if err != nil {
		return nil, err
	}
	headers := map[string]string{
		"Authorization":  "Bearer " + token,
		"X-MCP-Readonly": "true",
		"X-MCP-Tools":    strings.Join(mesagent.GitHubReadOnlyTools, ","),
	}
	cli, err := client.NewStreamableHttpClient(
		cfg.Endpoint,
		transport.WithHTTPHeaders(headers),
		transport.WithHTTPBasicClient(&http.Client{Timeout: time.Duration(cfg.TimeoutMillis) * time.Millisecond}),
	)
	if err != nil {
		return nil, fmt.Errorf("create github MCP client: %w", err)
	}
	cleanup := func() { _ = cli.Close() }
	if err = cli.Start(ctx); err != nil {
		cleanup()
		return nil, fmt.Errorf("start github MCP client: %w", err)
	}
	initialize := mcp.InitializeRequest{}
	initialize.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initialize.Params.ClientInfo = mcp.Implementation{Name: "mesguard", Version: "0.1.0"}
	if _, err = cli.Initialize(ctx, initialize); err != nil {
		cleanup()
		return nil, fmt.Errorf("initialize github MCP client: %w", err)
	}
	tools, err := einomcp.GetTools(ctx, &einomcp.Config{
		Cli: cli, ToolNameList: mesagent.GitHubReadOnlyTools,
	})
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("load github MCP tools: %w", err)
	}
	wrappedTools := make([]tool.BaseTool, 0, len(tools))
	for _, current := range tools {
		wrapped, wrapErr := wrapGitHubTool(ctx, current)
		if wrapErr != nil {
			cleanup()
			return nil, fmt.Errorf("wrap github MCP tool: %w", wrapErr)
		}
		wrappedTools = append(wrappedTools, wrapped)
	}
	if err = verifyTools(ctx, wrappedTools); err != nil {
		cleanup()
		return nil, err
	}
	log.Info("GitHub MCP connected in read-only mode",
		zap.String("endpoint", cfg.Endpoint),
		zap.Int("tools", len(wrappedTools)),
	)
	return &Connection{Tools: wrappedTools, cli: cli}, nil
}

func verifyTools(ctx context.Context, tools []tool.BaseTool) error {
	found := make(map[string]struct{}, len(tools))
	for _, current := range tools {
		if current == nil {
			return errors.New("github MCP returned a nil tool")
		}
		info, err := current.Info(ctx)
		if err != nil {
			return fmt.Errorf("read github MCP tool info: %w", err)
		}
		if info == nil || info.Name == "" {
			return errors.New("github MCP returned a tool without metadata")
		}
		found[info.Name] = struct{}{}
	}
	for _, required := range mesagent.GitHubReadOnlyTools {
		if _, ok := found[required]; !ok {
			return fmt.Errorf("github MCP required tool %q is unavailable", required)
		}
	}
	if len(found) != len(mesagent.GitHubReadOnlyTools) {
		return fmt.Errorf("github MCP returned unexpected tools")
	}
	return nil
}
