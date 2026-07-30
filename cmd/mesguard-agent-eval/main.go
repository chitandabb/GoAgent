// Command mesguard-agent-eval 汇总固定评测集的 Agent 路由、工具选择和 Token 指标。
// 输入是 JSONL；其中 Token 必须来自模型供应商 usage，而不是本地字符数估算。
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("mesguard-agent-eval", flag.ContinueOnError)
	flags.SetOutput(stderr)
	inputPath := flags.String("input", "", "JSONL evaluation observations")
	skillsDirectory := flags.String("skills-dir", "config/skills", "directory containing Skill packages")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *inputPath == "" {
		fmt.Fprintln(stderr, "-input is required")
		return 2
	}
	file, err := os.Open(*inputPath)
	if err != nil {
		fmt.Fprintf(stderr, "open input: %v\n", err)
		return 1
	}
	defer file.Close()

	observations, err := readObservations(file)
	if err != nil {
		fmt.Fprintf(stderr, "read observations: %v\n", err)
		return 1
	}
	definitions, err := mesagent.LoadSkillDefinitions(*skillsDirectory)
	if err != nil {
		fmt.Fprintf(stderr, "load skills: %v\n", err)
		return 1
	}
	registry, err := mesagent.NewRegistry(definitions...)
	if err != nil {
		fmt.Fprintf(stderr, "build skill registry: %v\n", err)
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(mesagent.SummarizeEvaluation(observations, registry)); err != nil {
		fmt.Fprintf(stderr, "write summary: %v\n", err)
		return 1
	}
	return 0
}

func readObservations(reader io.Reader) ([]mesagent.EvaluationObservation, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var observations []mesagent.EvaluationObservation
	line := 0
	for scanner.Scan() {
		line++
		var observation mesagent.EvaluationObservation
		if err := json.Unmarshal(scanner.Bytes(), &observation); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		observations = append(observations, observation)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(observations) == 0 {
		return nil, fmt.Errorf("input contains no observations")
	}
	return observations, nil
}
