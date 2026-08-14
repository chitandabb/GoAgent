// Command mesguard-agent-eval 汇总固定评测集的 Agent 路由、工具选择和 Token 指标。
// 输入是 JSONL；其中 Token 必须来自模型供应商 usage，而不是本地字符数估算。
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
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
	datasetPath := flags.String("dataset", "", "versioned JSONL evaluation cases")
	inputPath := flags.String("input", "", "JSONL evaluation observations")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *datasetPath == "" || *inputPath == "" {
		fmt.Fprintln(stderr, "-dataset and -input are required")
		return 2
	}
	datasetFile, err := os.Open(*datasetPath)
	if err != nil {
		fmt.Fprintf(stderr, "open dataset: %v\n", err)
		return 1
	}
	defer datasetFile.Close()
	cases, err := readCases(datasetFile)
	if err != nil {
		fmt.Fprintf(stderr, "read dataset: %v\n", err)
		return 1
	}

	observationFile, err := os.Open(*inputPath)
	if err != nil {
		fmt.Fprintf(stderr, "open input: %v\n", err)
		return 1
	}
	defer observationFile.Close()
	observations, err := readObservations(observationFile)
	if err != nil {
		fmt.Fprintf(stderr, "read observations: %v\n", err)
		return 1
	}
	summary, err := mesagent.EvaluateDataset(cases, observations)
	if err != nil {
		fmt.Fprintf(stderr, "evaluate dataset: %v\n", err)
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(summary); err != nil {
		fmt.Fprintf(stderr, "write summary: %v\n", err)
		return 1
	}
	return 0
}

func readObservations(reader io.Reader) ([]mesagent.EvaluationObservation, error) {
	values, err := readJSONLines(reader, "observations", func() mesagent.EvaluationObservation {
		return mesagent.EvaluationObservation{}
	})
	if err != nil {
		return nil, err
	}
	for index, value := range values {
		if err := value.Validate(); err != nil {
			return nil, fmt.Errorf("observations item %d: %w", index, err)
		}
	}
	return values, nil
}

func readCases(reader io.Reader) ([]mesagent.EvaluationCase, error) {
	values, err := readJSONLines(reader, "dataset cases", func() mesagent.EvaluationCase {
		return mesagent.EvaluationCase{}
	})
	if err != nil {
		return nil, err
	}
	for index, value := range values {
		if err := value.Validate(); err != nil {
			return nil, fmt.Errorf("dataset cases item %d: %w", index, err)
		}
	}
	return values, nil
}

func readJSONLines[T any](reader io.Reader, kind string, factory func() T) ([]T, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var values []T
	line := 0
	for scanner.Scan() {
		line++
		contents := bytes.TrimSpace(scanner.Bytes())
		if len(contents) == 0 {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(contents))
		decoder.DisallowUnknownFields()
		value := factory()
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", kind, line, err)
		}
		if err := ensureDecoderEOF(decoder); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", kind, line, err)
		}
		values = append(values, value)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("%s contains no observations", kind)
	}
	return values, nil
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
