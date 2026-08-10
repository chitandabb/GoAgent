// Command mesguard-conversation-quality-export converts persisted Conversation
// run ledgers into strict recorded_run JSONL observations. It performs no model,
// retrieval, OCR, VLM, rerank, or web-provider calls.
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
	"path/filepath"
	"strings"
	"time"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type commandOptions struct {
	datasetPath   string
	selectionPath string
	outputPath    string
	validateOnly  bool
	timeout       time.Duration
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	options, err := parseOptions(args)
	if err != nil {
		return err
	}
	cases, err := readStrictJSONL(options.datasetPath, func() mesagent.ConversationQualityCase {
		return mesagent.ConversationQualityCase{}
	})
	if err != nil {
		return fmt.Errorf("read dataset: %w", err)
	}
	selections, err := readStrictJSONL(options.selectionPath, func() mesagent.ConversationQualityRecordedRunSelection {
		return mesagent.ConversationQualityRecordedRunSelection{}
	})
	if err != nil {
		return fmt.Errorf("read selections: %w", err)
	}
	ordered, err := alignSelections(cases, selections)
	if err != nil {
		return err
	}
	if options.validateOnly {
		fmt.Printf("dataset=%s cases=%d selections=%d validation=passed\n", cases[0].DatasetVersion, len(cases), len(ordered))
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), options.timeout)
	defer cancel()
	db, closeDB, err := platformpostgres.Open(ctx, cfg.Postgres, zap.NewNop())
	if err != nil {
		return fmt.Errorf("open postgres: %w", err)
	}
	defer closeDB()
	repository := platformpostgres.NewConversationRepository(db)
	observations := make([]mesagent.ConversationQualityObservation, 0, len(cases))
	for index, definition := range cases {
		turnID, _ := uuid.Parse(ordered[index].TurnID)
		recorded, err := repository.GetRecordedAgentRun(ctx, turnID)
		if err != nil {
			return fmt.Errorf("load recorded run for case %q: %w", definition.CaseID, err)
		}
		observation, err := mesagent.BuildRecordedConversationQualityObservation(
			definition, recorded, ordered[index],
		)
		if err != nil {
			return fmt.Errorf("build observation for case %q: %w", definition.CaseID, err)
		}
		observations = append(observations, observation)
	}
	if err := writeStrictJSONL(options.outputPath, observations); err != nil {
		return err
	}
	fmt.Printf("dataset=%s recorded_runs=%d output=%s\n", cases[0].DatasetVersion, len(observations), options.outputPath)
	return nil
}

func parseOptions(args []string) (commandOptions, error) {
	flags := flag.NewFlagSet("mesguard-conversation-quality-export", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := commandOptions{}
	flags.StringVar(&options.datasetPath, "dataset", "testdata/conversation-quality-v1.jsonl", "versioned quality case JSONL")
	flags.StringVar(&options.selectionPath, "selections", "", "caseId-to-turnId selection JSONL")
	flags.StringVar(&options.outputPath, "output", "", "new recorded_run observation JSONL path")
	flags.BoolVar(&options.validateOnly, "validate-only", false, "validate dataset and selection mapping without database access")
	flags.DurationVar(&options.timeout, "timeout", 30*time.Second, "database export timeout")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return commandOptions{}, errors.New("usage: mesguard-conversation-quality-export -selections path [-dataset path] [-output path] [-validate-only] [-timeout duration]")
	}
	options.datasetPath = strings.TrimSpace(options.datasetPath)
	options.selectionPath = strings.TrimSpace(options.selectionPath)
	options.outputPath = strings.TrimSpace(options.outputPath)
	if options.datasetPath == "" || options.selectionPath == "" ||
		(!options.validateOnly && options.outputPath == "") || options.timeout <= 0 || options.timeout > 5*time.Minute {
		return commandOptions{}, errors.New("dataset, selections, output, or timeout is invalid")
	}
	return options, nil
}

func alignSelections(
	cases []mesagent.ConversationQualityCase,
	selections []mesagent.ConversationQualityRecordedRunSelection,
) ([]mesagent.ConversationQualityRecordedRunSelection, error) {
	if len(cases) == 0 || len(cases) != len(selections) {
		return nil, errors.New("recorded-run export requires exactly one selection per dataset case")
	}
	byCase := make(map[string]mesagent.ConversationQualityRecordedRunSelection, len(selections))
	turns := make(map[string]struct{}, len(selections))
	for _, selection := range selections {
		if err := selection.Validate(); err != nil {
			return nil, err
		}
		if _, exists := byCase[selection.CaseID]; exists {
			return nil, fmt.Errorf("duplicate selection for case %q", selection.CaseID)
		}
		if _, exists := turns[selection.TurnID]; exists {
			return nil, fmt.Errorf("turn %q is selected more than once", selection.TurnID)
		}
		byCase[selection.CaseID] = selection
		turns[selection.TurnID] = struct{}{}
	}
	ordered := make([]mesagent.ConversationQualityRecordedRunSelection, 0, len(cases))
	seenCases := make(map[string]struct{}, len(cases))
	for _, definition := range cases {
		if err := definition.Validate(); err != nil {
			return nil, err
		}
		if _, exists := seenCases[definition.CaseID]; exists {
			return nil, fmt.Errorf("duplicate dataset case %q", definition.CaseID)
		}
		seenCases[definition.CaseID] = struct{}{}
		selection, exists := byCase[definition.CaseID]
		if !exists {
			return nil, fmt.Errorf("case %q has no recorded turn selection", definition.CaseID)
		}
		ordered = append(ordered, selection)
	}
	return ordered, nil
}

func readStrictJSONL[T any](path string, factory func() T) ([]T, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	values := make([]T, 0)
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
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			if err == nil {
				err = errors.New("multiple JSON values on one line")
			}
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		values = append(values, value)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, errors.New("JSONL contains no items")
	}
	return values, nil
}

func writeStrictJSONL[T any](path string, values []T) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	succeeded := false
	defer func() {
		_ = file.Close()
		if !succeeded {
			_ = os.Remove(path)
		}
	}()
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	for _, value := range values {
		if err := encoder.Encode(value); err != nil {
			return fmt.Errorf("write output: %w", err)
		}
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync output: %w", err)
	}
	succeeded = true
	return nil
}
