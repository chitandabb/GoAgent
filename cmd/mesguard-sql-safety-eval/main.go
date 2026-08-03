// Command mesguard-sql-safety-eval runs the fixed, model-free QueryGuard safety evaluation.
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

	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/platform/sqlserver"
	"github.com/google/uuid"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("mesguard-sql-safety-eval", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	datasetPath := flags.String("dataset", "testdata/sql-safety-v1.jsonl", "versioned JSONL safety cases")
	outputPath := flags.String("output", "testdata/sql-safety-v1.observations.jsonl", "observation JSONL output")
	summaryPath := flags.String("summary", "testdata/sql-safety-v1.summary.json", "summary JSON output")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("usage: mesguard-sql-safety-eval [-dataset path] [-output path] [-summary path]")
	}
	cases, err := readQueryGuardCases(*datasetPath)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !cfg.SQLServer.Enabled {
		return errors.New("SQL Server must be enabled to load the production QueryGuard policy")
	}
	guard, err := sqlserver.NewReadonlyQueryGuard(
		cfg.SQLServer.Investigation.AllowedSchemas,
		cfg.SQLServer.Investigation.MaxQueryBytes,
	)
	if err != nil {
		return fmt.Errorf("build QueryGuard: %w", err)
	}
	observations, summary, err := sqlserver.EvaluateQueryGuard(guard, cases)
	if err != nil {
		return err
	}
	return writeEvaluationFiles(*outputPath, *summaryPath, observations, summary)
}

func readQueryGuardCases(path string) ([]sqlserver.QueryGuardEvaluationCase, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open dataset: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var result []sqlserver.QueryGuardEvaluationCase
	for line := 1; scanner.Scan(); line++ {
		contents := bytes.TrimSpace(scanner.Bytes())
		if len(contents) == 0 {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(contents))
		decoder.DisallowUnknownFields()
		var current sqlserver.QueryGuardEvaluationCase
		if err := decoder.Decode(&current); err != nil {
			return nil, fmt.Errorf("decode line %d: %w", line, err)
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			if err == nil {
				err = errors.New("multiple JSON values")
			}
			return nil, fmt.Errorf("decode line %d: %w", line, err)
		}
		if err := current.Validate(); err != nil {
			return nil, fmt.Errorf("validate line %d: %w", line, err)
		}
		result = append(result, current)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, errors.New("dataset contains no cases")
	}
	return result, nil
}

func writeEvaluationFiles(
	outputPath, summaryPath string,
	observations []sqlserver.QueryGuardEvaluationObservation,
	summary sqlserver.QueryGuardEvaluationSummary,
) error {
	outputTemp := outputPath + ".tmp-" + uuid.NewString()
	summaryTemp := summaryPath + ".tmp-" + uuid.NewString()
	defer os.Remove(outputTemp)
	defer os.Remove(summaryTemp)
	file, err := os.OpenFile(outputTemp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	for _, observation := range observations {
		if err := encoder.Encode(observation); err != nil {
			_ = file.Close()
			return err
		}
	}
	if err := file.Close(); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(summaryTemp, append(encoded, '\n'), 0o600); err != nil {
		return err
	}
	if err := replaceFile(outputTemp, outputPath); err != nil {
		return err
	}
	return replaceFile(summaryTemp, summaryPath)
}

func replaceFile(source, target string) error {
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(source, target)
}
