// Command mesguard-context-governance-pilot validates and aggregates the M3
// Context Governance Pilot. It never calls a Provider; execution is an
// explicit upstream step that writes bounded JSONL observations.
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
	"strings"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("mesguard-context-governance-pilot", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	fixturePath := flags.String("fixture", "", "optional exported Pilot fixture JSON; empty uses the pinned fixture")
	fixtureOutput := flags.String("fixture-output", "", "optional path to export the pinned fixture JSON")
	inputPath := flags.String("input", "", "optional strict JSONL Pilot observations")
	outputPath := flags.String("output", "output/evaluation/context-governance-pilot-v1.summary.json", "summary JSON output")
	validateOnly := flags.Bool("validate-only", false, "validate/export the fixture without estimating or reading observations")
	estimateOnly := flags.Bool("estimate-only", false, "print the bounded Provider call and cost plan without executing")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("usage: mesguard-context-governance-pilot [-validate-only|-estimate-only] [-fixture path] [-fixture-output path] [-input path] [-output path]")
	}
	if *validateOnly && *estimateOnly {
		return errors.New("validate-only and estimate-only are mutually exclusive")
	}
	dataset := mesagent.ContextGovernancePilotFixture()
	if strings.TrimSpace(*fixturePath) != "" {
		if err := readStrictJSON(*fixturePath, &dataset); err != nil {
			return fmt.Errorf("read Pilot fixture: %w", err)
		}
	}
	if err := dataset.Validate(); err != nil {
		return fmt.Errorf("validate Pilot fixture: %w", err)
	}
	if strings.TrimSpace(*fixtureOutput) != "" {
		if err := writeJSON(*fixtureOutput, dataset); err != nil {
			return fmt.Errorf("write Pilot fixture: %w", err)
		}
	}
	if *validateOnly {
		return writeJSONTo(stdout, struct {
			DatasetVersion string `json:"datasetVersion"`
			FixtureVersion string `json:"fixtureVersion"`
			Scenarios      int    `json:"scenarios"`
			Checkpoints    int    `json:"checkpoints"`
		}{dataset.DatasetVersion, dataset.FixtureVersion, len(dataset.Scenarios), len(dataset.Scenarios) * 3})
	}
	planOptions := mesagent.DefaultContextGovernancePilotPlanOptions()
	plan, err := mesagent.BuildContextGovernancePilotPlan(dataset, planOptions)
	if err != nil {
		return err
	}
	if *estimateOnly || strings.TrimSpace(*inputPath) == "" {
		return writeJSONTo(stdout, plan)
	}
	observations, err := readPilotObservations(*inputPath)
	if err != nil {
		return err
	}
	report, err := mesagent.EvaluateContextGovernancePilot(dataset, observations, planOptions.Pricing)
	if err != nil {
		return fmt.Errorf("evaluate Pilot: %w", err)
	}
	if err := writeJSON(*outputPath, report); err != nil {
		return err
	}
	return writeJSONTo(stdout, report)
}

func readPilotObservations(path string) ([]mesagent.ContextGovernancePilotObservation, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open Pilot observations: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	var result []mesagent.ContextGovernancePilotObservation
	for line := 1; scanner.Scan(); line++ {
		contents := bytes.TrimSpace(scanner.Bytes())
		if len(contents) == 0 {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(contents))
		decoder.DisallowUnknownFields()
		var observation mesagent.ContextGovernancePilotObservation
		if err := decoder.Decode(&observation); err != nil {
			return nil, fmt.Errorf("decode Pilot observation line %d: %w", line, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			if err == nil {
				err = errors.New("multiple JSON values")
			}
			return nil, fmt.Errorf("decode Pilot observation line %d: %w", line, err)
		}
		result = append(result, observation)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, errors.New("Pilot observations contain no records")
	}
	return result, nil
}

func readStrictJSON(path string, target any) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func writeJSON(path string, value any) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return err
	}
	temp, err := os.CreateTemp(directory, ".context-governance-pilot-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if err := writeJSONTo(temp, value); err != nil {
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

func writeJSONTo(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
