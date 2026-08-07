// Command mesguard-ingestion-throughput-eval validates and summarizes paired
// knowledge-ingestion throughput observations without calling infrastructure or models.
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

	"github.com/chitandabb/GoAgent/internal/knowledgeingestion"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("mesguard-ingestion-throughput-eval", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	inputPath := flags.String("input", "", "paired throughput observation JSONL")
	outputPath := flags.String("output", "output/evaluation/rag-ingestion-v1.summary.json", "summary JSON output")
	target := flags.Float64("target-increase-percent", 40, "median throughput increase acceptance target")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("usage: mesguard-ingestion-throughput-eval -input path [-output path] [-target-increase-percent n]")
	}
	if *inputPath == "" {
		return errors.New("input path is required")
	}
	if filepath.Clean(*inputPath) == filepath.Clean(*outputPath) {
		return errors.New("input and output paths must be different")
	}
	observations, err := readObservations(*inputPath)
	if err != nil {
		return err
	}
	summary, err := knowledgeingestion.EvaluateThroughput(observations, *target)
	if err != nil {
		return err
	}
	if err := writeSummary(*outputPath, summary); err != nil {
		return err
	}
	fmt.Printf(
		"dataset=%s pairs=%d eligible=%t integrity=%t median_throughput_increase=%.2f%% duration_reduction=%.2f%% meets_target=%t\n",
		summary.DatasetVersion, summary.Pairs, summary.AcceptanceEligible, summary.IntegrityPreserved,
		summary.MedianThroughputIncreasePercent, summary.MedianDurationReductionPercent, summary.MeetsTarget,
	)
	return nil
}

func readObservations(path string) ([]knowledgeingestion.ThroughputObservation, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open observations: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var observations []knowledgeingestion.ThroughputObservation
	for line := 1; scanner.Scan(); line++ {
		contents := bytes.TrimSpace(scanner.Bytes())
		if len(contents) == 0 {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(contents))
		decoder.DisallowUnknownFields()
		var observation knowledgeingestion.ThroughputObservation
		if err := decoder.Decode(&observation); err != nil {
			return nil, fmt.Errorf("decode observations line %d: %w", line, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			if err == nil {
				err = errors.New("multiple JSON values")
			}
			return nil, fmt.Errorf("decode observations line %d: %w", line, err)
		}
		if err := observation.Validate(); err != nil {
			return nil, fmt.Errorf("validate observations line %d: %w", line, err)
		}
		observations = append(observations, observation)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(observations) == 0 {
		return nil, errors.New("observations contain no records")
	}
	return observations, nil
}

func writeSummary(path string, summary knowledgeingestion.ThroughputEvaluationSummary) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return err
	}
	temp, err := os.CreateTemp(directory, ".ingestion-throughput-*.tmp")
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
