// Command mesguard-rag-paired-eval validates and summarizes paired Advanced RAG observations.
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
	"path/filepath"

	"github.com/chitandabb/GoAgent/internal/knowledge"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("mesguard-rag-paired-eval", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	datasetPath := flags.String("dataset", "", "versioned Advanced RAG cases")
	inputPath := flags.String("input", "", "paired observation JSONL")
	outputPath := flags.String("output", "output/evaluation/rag-advanced-v1.summary.json", "summary JSON output")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("usage: mesguard-rag-paired-eval [-dataset path] [-input path] [-output path]")
	}
	if *datasetPath == "" || *inputPath == "" {
		return errors.New("dataset and input paths are required")
	}
	cases, err := readStrictJSONL(*datasetPath, func(value knowledge.AdvancedRetrievalEvaluationCase) error {
		return value.Validate()
	})
	if err != nil {
		return err
	}
	observations, err := readStrictJSONL(*inputPath, func(value knowledge.AdvancedRetrievalObservation) error {
		return value.Validate()
	})
	if err != nil {
		return err
	}
	summary, err := knowledge.EvaluateAdvancedRetrieval(cases, observations)
	if err != nil {
		return err
	}
	if err := writeSummary(*outputPath, summary); err != nil {
		return err
	}
	fmt.Printf(
		"dataset=%s pairs=%d hit_rate_delta=%.4f document_recall_delta=%.4f mrr_delta=%.4f context_precision_delta=%.4f context_recall_delta=%.4f query_amplification=%.4f\n",
		summary.DatasetVersion, summary.PairedCases, summary.Delta.HitRateAtK, summary.Delta.RecallAtK, summary.Delta.MeanReciprocalRank,
		summary.Delta.ContextPrecision, summary.Delta.ContextRecall, summary.Delta.QueryAmplificationRatio,
	)
	return nil
}

func readStrictJSONL[T any](path string, validate func(T) error) ([]T, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	var result []T
	for line := 1; scanner.Scan(); line++ {
		contents := bytes.TrimSpace(scanner.Bytes())
		if len(contents) == 0 {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(contents))
		decoder.DisallowUnknownFields()
		var value T
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode %s line %d: %w", path, line, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			if err == nil {
				err = errors.New("multiple JSON values")
			}
			return nil, fmt.Errorf("decode %s line %d: %w", path, line, err)
		}
		if err := validate(value); err != nil {
			return nil, fmt.Errorf("validate %s line %d: %w", path, line, err)
		}
		result = append(result, value)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%s contains no records", path)
	}
	return result, nil
}

func writeSummary(path string, summary knowledge.AdvancedRetrievalEvaluationSummary) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return err
	}
	temp, err := os.CreateTemp(directory, ".rag-paired-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	encoder := json.NewEncoder(temp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(summary); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(tempPath, path)
}
