// Command mesguard-conversation-quality-eval aggregates versioned conversation
// answer, retrieval, citation, preview, degradation, latency, Token, and cost observations.
// Seeded contract observations and recorded model runs cannot be mixed.
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
	"github.com/chitandabb/GoAgent/internal/ragjudge"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("mesguard-conversation-quality-eval", flag.ContinueOnError)
	flags.SetOutput(stderr)
	datasetPath := flags.String("dataset", "", "versioned JSONL conversation quality cases")
	inputPath := flags.String("input", "", "JSONL conversation quality observations")
	judgePath := flags.String("judge", "", "optional JSONL independent RAG Judge observations")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if stringsTrimmedEmpty(*datasetPath) || stringsTrimmedEmpty(*inputPath) {
		fmt.Fprintln(stderr, "-dataset and -input are required")
		return 2
	}
	cases, err := readConversationQualityFile(*datasetPath, "dataset cases", func() mesagent.ConversationQualityCase {
		return mesagent.ConversationQualityCase{}
	})
	if err != nil {
		fmt.Fprintf(stderr, "read dataset: %v\n", err)
		return 1
	}
	observations, err := readConversationQualityFile(*inputPath, "observations", func() mesagent.ConversationQualityObservation {
		return mesagent.ConversationQualityObservation{}
	})
	if err != nil {
		fmt.Fprintf(stderr, "read observations: %v\n", err)
		return 1
	}
	if !stringsTrimmedEmpty(*judgePath) {
		judges, readErr := readConversationQualityFile(*judgePath, "Judge observations", func() ragjudge.Observation {
			return ragjudge.Observation{}
		})
		if readErr != nil {
			fmt.Fprintf(stderr, "read Judge observations: %v\n", readErr)
			return 1
		}
		observations, err = mergeJudgeObservations(observations, judges)
		if err != nil {
			fmt.Fprintf(stderr, "merge Judge observations: %v\n", err)
			return 1
		}
	}
	summary, err := mesagent.EvaluateConversationQuality(cases, observations)
	if err != nil {
		fmt.Fprintf(stderr, "evaluate conversation quality: %v\n", err)
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(summary); err != nil {
		fmt.Fprintf(stderr, "write summary: %v\n", err)
		return 1
	}
	return 0
}

func mergeJudgeObservations(
	observations []mesagent.ConversationQualityObservation,
	judges []ragjudge.Observation,
) ([]mesagent.ConversationQualityObservation, error) {
	indexed := make(map[string]int, len(observations))
	for index, observation := range observations {
		indexed[observation.DatasetVersion+"/"+observation.CaseID] = index
	}
	result := append([]mesagent.ConversationQualityObservation(nil), observations...)
	seen := make(map[string]struct{}, len(judges))
	for _, judge := range judges {
		if err := judge.Validate(); err != nil {
			return nil, err
		}
		key := judge.DatasetVersion + "/" + judge.CaseID
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("duplicate Judge observation %q", key)
		}
		seen[key] = struct{}{}
		index, exists := indexed[key]
		if !exists {
			return nil, fmt.Errorf("Judge observation %q has no recorded run", key)
		}
		if result[index].Judge != nil {
			return nil, fmt.Errorf("recorded run %q already contains a Judge", key)
		}
		result[index].Judge = &mesagent.ConversationQualityJudgeObservation{
			Method: "llm", JudgeID: judge.Provider + "/" + judge.RequestModel,
			RubricVersion:     judge.PromptVersion,
			Faithfulness:      float64(judge.Scores.Faithfulness.Score) / 4,
			AnswerRelevance:   float64(judge.Scores.AnswerRelevance.Score) / 4,
			CitationAlignment: float64(judge.Scores.CitationCorrectness.Score) / 4,
		}
	}
	return result, nil
}

func readConversationQualityFile[T any](path, kind string, factory func() T) ([]T, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readConversationQualityJSONLines(file, kind, factory)
}

func readConversationQualityJSONLines[T any](reader io.Reader, kind string, factory func() T) ([]T, error) {
	scanner := bufio.NewScanner(reader)
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
			return nil, fmt.Errorf("%s line %d: %w", kind, line, err)
		}
		if err := conversationQualityDecoderEOF(decoder); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", kind, line, err)
		}
		values = append(values, value)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("%s contains no items", kind)
	}
	return values, nil
}

func conversationQualityDecoderEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("multiple JSON values on one line")
}

func stringsTrimmedEmpty(value string) bool {
	return len(bytes.TrimSpace([]byte(value))) == 0
}
