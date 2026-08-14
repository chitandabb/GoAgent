// Command mesguard-github-search-eval 运行不依赖模型的 GitHub 分层检索评测。
// 它只读取评测样本、GitHub MCP 仓库树和文件，不执行写操作，也不创建本地缓存。
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/platform/githubmcp"
	"go.uber.org/zap"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("mesguard-github-search-eval", flag.ContinueOnError)
	flags.SetOutput(stderr)
	casesPath := flags.String("cases", "", "JSONL GitHub code-search evaluation cases")
	timeout := flags.Duration("timeout", 3*time.Minute, "total evaluation timeout")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *casesPath == "" {
		fmt.Fprintln(stderr, "-cases is required")
		return 2
	}
	if *timeout <= 0 {
		fmt.Fprintln(stderr, "-timeout must be positive")
		return 2
	}

	file, err := os.Open(*casesPath)
	if err != nil {
		fmt.Fprintf(stderr, "open cases: %v\n", err)
		return 1
	}
	cases, err := readCases(file)
	_ = file.Close()
	if err != nil {
		fmt.Fprintf(stderr, "read cases: %v\n", err)
		return 1
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "load config: %v\n", err)
		return 1
	}
	if !cfg.GitHubMCP.Enabled {
		fmt.Fprintln(stderr, "GitHub MCP is disabled")
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	connection, err := githubmcp.Connect(ctx, cfg.GitHubMCP, zap.NewNop())
	if err != nil {
		fmt.Fprintf(stderr, "connect GitHub MCP: %v\n", err)
		return 1
	}
	defer connection.Close()

	evaluator, err := githubmcp.NewCodeSearchStabilityEvaluator(ctx, connection.Tools)
	if err != nil {
		fmt.Fprintf(stderr, "build evaluator: %v\n", err)
		return 1
	}
	summary, err := evaluator.Evaluate(ctx, cases)
	if err != nil {
		if summary.Cases > 0 {
			if writeErr := writeSummary(stdout, summary); writeErr != nil {
				fmt.Fprintf(stderr, "write partial summary: %v\n", writeErr)
			}
		}
		fmt.Fprintf(stderr, "evaluate cases: %v\n", err)
		return 1
	}
	if err := writeSummary(stdout, summary); err != nil {
		fmt.Fprintf(stderr, "write summary: %v\n", err)
		return 1
	}
	return 0
}

func writeSummary(writer io.Writer, summary githubmcp.CodeSearchEvaluationSummary) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(summary)
}

func readCases(reader io.Reader) ([]githubmcp.CodeSearchEvaluationCase, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var cases []githubmcp.CodeSearchEvaluationCase
	line := 0
	for scanner.Scan() {
		line++
		contents := bytes.TrimSpace(scanner.Bytes())
		if len(contents) == 0 {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(contents))
		decoder.DisallowUnknownFields()
		var current githubmcp.CodeSearchEvaluationCase
		if err := decoder.Decode(&current); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if err := ensureDecoderEOF(decoder); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if err := current.Validate(); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		cases = append(cases, current)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(cases) == 0 {
		return nil, errors.New("cases contains no evaluation cases")
	}
	return cases, nil
}

func ensureDecoderEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("multiple JSON values on one line")
}
