// Command mesguard-conversation-quality-judge-export builds self-contained,
// provider-free RAG Judge inputs from a recorded Conversation quality run.
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
	"slices"
	"strings"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/ragjudge"
)

const defaultQualityRetrievalMaxResults = 3

type rawQualityCase struct {
	DatasetVersion         string                                  `json:"datasetVersion"`
	CaseID                 string                                  `json:"caseId"`
	UserQuery              string                                  `json:"userQuery"`
	RetrievalMaxResults    int                                     `json:"retrievalMaxResults,omitempty"`
	RelevantChunks         []knowledge.RetrievalEvaluationChunkRef `json:"relevantChunks"`
	RequiredCitationChunks []knowledge.RetrievalEvaluationChunkRef `json:"requiredCitationChunks,omitempty"`
	RequiredAnswerTerms    []string                                `json:"requiredAnswerTerms,omitempty"`
	ForbiddenAnswerTerms   []string                                `json:"forbiddenAnswerTerms,omitempty"`
	ExpectedOutcome        mesagent.ConversationQualityOutcome     `json:"expectedOutcome"`
	Tags                   []string                                `json:"tags,omitempty"`
}

func (c rawQualityCase) Validate() error {
	if strings.TrimSpace(c.DatasetVersion) == "" || strings.TrimSpace(c.CaseID) == "" ||
		strings.TrimSpace(c.UserQuery) == "" || c.UserQuery != strings.TrimSpace(c.UserQuery) ||
		len([]rune(c.UserQuery)) > 2_000 || c.RetrievalMaxResults < 0 || c.RetrievalMaxResults > 20 ||
		!c.ExpectedOutcome.Valid() || len(c.RelevantChunks) == 0 || len(c.RelevantChunks) > 20 {
		return errors.New("raw Conversation quality case is invalid")
	}
	seen := make(map[string]struct{}, len(c.RelevantChunks))
	for _, ref := range c.RelevantChunks {
		if err := ref.Validate(); err != nil {
			return err
		}
		key := rawChunkKey(ref)
		if _, exists := seen[key]; exists {
			return errors.New("raw Conversation quality relevant chunks must be unique")
		}
		seen[key] = struct{}{}
	}
	required := c.effectiveRequiredCitationChunks()
	if len(required) > len(c.RelevantChunks) {
		return errors.New("raw Conversation quality required citations exceed relevant chunks")
	}
	requiredSeen := make(map[string]struct{}, len(required))
	for _, ref := range required {
		if err := ref.Validate(); err != nil {
			return err
		}
		key := rawChunkKey(ref)
		if _, exists := seen[key]; !exists {
			return errors.New("raw Conversation quality required citation is not relevant")
		}
		if _, exists := requiredSeen[key]; exists {
			return errors.New("raw Conversation quality required citations must be unique")
		}
		requiredSeen[key] = struct{}{}
	}
	return nil
}

func (c rawQualityCase) effectiveRetrievalMaxResults() int {
	if c.RetrievalMaxResults > 0 {
		return c.RetrievalMaxResults
	}
	return defaultQualityRetrievalMaxResults
}

func (c rawQualityCase) effectiveRequiredCitationChunks() []knowledge.RetrievalEvaluationChunkRef {
	if len(c.RequiredCitationChunks) > 0 {
		return c.RequiredCitationChunks
	}
	return c.RelevantChunks
}

func rawChunkKey(ref knowledge.RetrievalEvaluationChunkRef) string {
	return fmt.Sprintf("%s/%d/%s", ref.DocumentKey, ref.Ordinal, ref.ContentSHA256)
}

type judgeFacts struct {
	DatasetVersion string   `json:"datasetVersion"`
	CaseID         string   `json:"caseId"`
	Answerable     bool     `json:"answerable"`
	GoldFacts      []string `json:"goldFacts"`
}

func (f judgeFacts) Validate() error {
	if strings.TrimSpace(f.DatasetVersion) == "" || strings.TrimSpace(f.CaseID) == "" ||
		len(f.GoldFacts) == 0 || len(f.GoldFacts) > 50 {
		return errors.New("Conversation quality Judge facts are invalid")
	}
	seen := make(map[string]struct{}, len(f.GoldFacts))
	for _, fact := range f.GoldFacts {
		if strings.TrimSpace(fact) == "" || fact != strings.TrimSpace(fact) || len([]rune(fact)) > 4_000 {
			return errors.New("Conversation quality Judge gold fact is invalid")
		}
		key := strings.ToLower(fact)
		if _, exists := seen[key]; exists {
			return errors.New("Conversation quality Judge gold facts must be unique")
		}
		seen[key] = struct{}{}
	}
	return nil
}

type commandOptions struct {
	corpusPath       string
	rawDatasetPath   string
	resolvedPath     string
	observationsPath string
	factsPath        string
	outputPath       string
	overwrite        bool
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	opts, err := parseOptions(args)
	if err != nil {
		return err
	}
	corpus, err := readStrictJSON[knowledge.AdvancedRetrievalEvaluationCorpus](opts.corpusPath)
	if err != nil {
		return err
	}
	rawCases, err := readStrictJSONL(opts.rawDatasetPath, func(value rawQualityCase) error { return value.Validate() })
	if err != nil {
		return err
	}
	facts, err := readStrictJSONL(opts.factsPath, func(value judgeFacts) error { return value.Validate() })
	if err != nil {
		return err
	}
	resolved, err := readStrictJSONL(opts.resolvedPath, func(value mesagent.ConversationQualityCase) error { return value.Validate() })
	if err != nil {
		return err
	}
	observations, err := readStrictJSONL(opts.observationsPath, func(value mesagent.ConversationQualityObservation) error { return value.Validate() })
	if err != nil {
		return err
	}
	inputs, err := buildJudgeInputs(corpus, rawCases, facts, resolved, observations)
	if err != nil {
		return err
	}
	if err := writeJSONL(opts.outputPath, inputs, opts.overwrite); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "conversation_quality_judge_export cases=%d provider_calls=0 output=%s\n", len(inputs), opts.outputPath)
	return nil
}

func parseOptions(args []string) (commandOptions, error) {
	flags := flag.NewFlagSet("mesguard-conversation-quality-judge-export", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	opts := commandOptions{}
	flags.StringVar(&opts.corpusPath, "corpus", "testdata/rag-advanced-v1.corpus.json", "pinned source corpus")
	flags.StringVar(&opts.rawDatasetPath, "raw-dataset", "testdata/conversation-quality-recorded-v1.jsonl", "raw Chunk-pinned quality cases")
	flags.StringVar(&opts.resolvedPath, "dataset", "output/evaluation/conversation-quality-recorded-v1.dataset.jsonl", "resolved quality cases")
	flags.StringVar(&opts.observationsPath, "input", "output/evaluation/conversation-quality-recorded-v1.observations.jsonl", "recorded quality observations")
	flags.StringVar(&opts.factsPath, "facts", "testdata/conversation-quality-judge-facts-v1.jsonl", "human gold-fact annotations")
	flags.StringVar(&opts.outputPath, "output", "output/evaluation/conversation-quality-recorded-v1.judge-inputs.jsonl", "self-contained Judge inputs")
	flags.BoolVar(&opts.overwrite, "overwrite", false, "replace an existing output file")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return commandOptions{}, errors.New("usage: mesguard-conversation-quality-judge-export [-overwrite]")
	}
	for _, path := range []string{
		opts.corpusPath, opts.rawDatasetPath, opts.resolvedPath,
		opts.observationsPath, opts.factsPath, opts.outputPath,
	} {
		if strings.TrimSpace(path) == "" {
			return commandOptions{}, errors.New("Conversation quality Judge export paths are required")
		}
	}
	return opts, nil
}

func buildJudgeInputs(
	corpus knowledge.AdvancedRetrievalEvaluationCorpus,
	rawCases []rawQualityCase,
	facts []judgeFacts,
	resolved []mesagent.ConversationQualityCase,
	observations []mesagent.ConversationQualityObservation,
) ([]ragjudge.Input, error) {
	chunksByDocument, err := knowledge.BuildAdvancedRetrievalCorpusChunks(corpus)
	if err != nil {
		return nil, err
	}
	for index, value := range rawCases {
		if err := value.Validate(); err != nil {
			return nil, fmt.Errorf("raw Judge case %d: %w", index, err)
		}
	}
	for index, value := range facts {
		if err := value.Validate(); err != nil {
			return nil, fmt.Errorf("Judge facts %d: %w", index, err)
		}
	}
	for index, value := range resolved {
		if err := value.Validate(); err != nil {
			return nil, fmt.Errorf("resolved Judge case %d: %w", index, err)
		}
	}
	for index, value := range observations {
		if err := value.Validate(); err != nil {
			return nil, fmt.Errorf("Judge observation %d: %w", index, err)
		}
	}
	contentByHash := make(map[string]string)
	for _, chunks := range chunksByDocument {
		for _, chunk := range chunks {
			if existing, exists := contentByHash[chunk.ContentSHA256]; exists && existing != chunk.ContentText {
				return nil, errors.New("Judge corpus contains a content-hash collision")
			}
			contentByHash[chunk.ContentSHA256] = chunk.ContentText
		}
	}
	rawByCase, err := indexUnique(rawCases, func(value rawQualityCase) string { return value.DatasetVersion + "/" + value.CaseID })
	if err != nil {
		return nil, err
	}
	factsByCase, err := indexUnique(facts, func(value judgeFacts) string { return value.DatasetVersion + "/" + value.CaseID })
	if err != nil {
		return nil, err
	}
	resolvedByCase, err := indexUnique(resolved, func(value mesagent.ConversationQualityCase) string { return value.DatasetVersion + "/" + value.CaseID })
	if err != nil {
		return nil, err
	}
	if len(observations) == 0 {
		return nil, errors.New("Conversation quality Judge export has no observations")
	}
	inputs := make([]ragjudge.Input, 0, len(observations))
	seenObservations := make(map[string]struct{}, len(observations))
	for _, observation := range observations {
		key := observation.DatasetVersion + "/" + observation.CaseID
		if _, exists := seenObservations[key]; exists {
			return nil, fmt.Errorf("duplicate Conversation quality observation %q", key)
		}
		seenObservations[key] = struct{}{}
		raw, rawExists := rawByCase[key]
		gold, factsExist := factsByCase[key]
		definition, resolvedExists := resolvedByCase[key]
		if !rawExists || !factsExist || !resolvedExists {
			return nil, fmt.Errorf("Judge export case %q is missing raw, fact, or resolved data", key)
		}
		if observation.Outcome == mesagent.ConversationQualityFailed || strings.TrimSpace(observation.Answer) == "" ||
			len(raw.RelevantChunks) != len(definition.RelevantSources) {
			return nil, fmt.Errorf("Judge export case %q is not a complete answered run", key)
		}
		if raw.UserQuery != definition.UserQuery || raw.effectiveRetrievalMaxResults() != definition.RetrievalMaxResults ||
			raw.ExpectedOutcome != definition.ExpectedOutcome ||
			!slices.Equal(raw.RequiredAnswerTerms, definition.RequiredAnswerTerms) ||
			!slices.Equal(raw.ForbiddenAnswerTerms, definition.ForbiddenAnswerTerms) ||
			!slices.Equal(raw.Tags, definition.Tags) {
			return nil, fmt.Errorf("Judge export case %q has drifted raw and resolved contracts", key)
		}
		input := ragjudge.Input{
			DatasetVersion: observation.DatasetVersion, CaseID: observation.CaseID,
			AnswerProvider: observation.Model, AnswerModel: observation.ModelVersion,
			Question: raw.UserQuery, Answerable: gold.Answerable,
			GoldFacts: append([]string(nil), gold.GoldFacts...), CandidateAnswer: observation.Answer,
		}
		sourceByChunk := make(map[string]mesagent.ConversationQualitySource, len(raw.RelevantChunks))
		for index, ref := range raw.RelevantChunks {
			source := definition.RelevantSources[index]
			content, exists := contentByHash[ref.ContentSHA256]
			if !exists || source.ContentSHA256 != ref.ContentSHA256 {
				return nil, fmt.Errorf("Judge export case %q has stale relevant content", key)
			}
			sourceByChunk[rawChunkKey(ref)] = source
			input.AllowedSources = append(input.AllowedSources, ragjudge.Evidence{
				CitationID: source.SourceRef, SourceRef: source.SourceRef,
				ContentSHA256: ref.ContentSHA256, ContentText: content,
			})
		}
		requiredChunks := raw.effectiveRequiredCitationChunks()
		if len(requiredChunks) != len(definition.RequiredCitationRefs) {
			return nil, fmt.Errorf("Judge export case %q has drifted required citations", key)
		}
		for index, ref := range requiredChunks {
			source, exists := sourceByChunk[rawChunkKey(ref)]
			if !exists || source.SourceRef != definition.RequiredCitationRefs[index] {
				return nil, fmt.Errorf("Judge export case %q has stale required citation mapping", key)
			}
		}
		for _, citation := range observation.Citations {
			content, exists := contentByHash[citation.ContentSHA256]
			if !exists {
				return nil, fmt.Errorf("Judge export case %q cannot resolve cited content", key)
			}
			input.CitedEvidence = append(input.CitedEvidence, ragjudge.Evidence{
				CitationID: citation.SourceRef, SourceRef: citation.SourceRef,
				ContentSHA256: citation.ContentSHA256, ContentText: content,
			})
		}
		if err := input.Validate(); err != nil {
			return nil, fmt.Errorf("Judge export case %q: %w", key, err)
		}
		inputs = append(inputs, input)
	}
	return inputs, nil
}

func indexUnique[T any](values []T, key func(T) string) (map[string]T, error) {
	result := make(map[string]T, len(values))
	for _, value := range values {
		current := key(value)
		if _, exists := result[current]; exists {
			return nil, fmt.Errorf("duplicate Judge export item %q", current)
		}
		result[current] = value
	}
	return result, nil
}

func readStrictJSON[T interface{ Validate() error }](path string) (T, error) {
	var value T
	contents, err := os.ReadFile(path)
	if err != nil {
		return value, err
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return value, errors.New("JSON file contains trailing content")
	}
	if err := value.Validate(); err != nil {
		return value, err
	}
	return value, nil
}

func readStrictJSONL[T any](path string, validate func(T) error) ([]T, error) {
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
		var value T
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode JSONL line %d: %w", line, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("decode JSONL line %d: trailing content", line)
		}
		if err := validate(value); err != nil {
			return nil, fmt.Errorf("validate JSONL line %d: %w", line, err)
		}
		values = append(values, value)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, errors.New("JSONL file contains no items")
	}
	return values, nil
}

func writeJSONL(path string, values []ragjudge.Input, overwrite bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	flags := os.O_WRONLY | os.O_CREATE
	if overwrite {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	for _, value := range values {
		if err := encoder.Encode(value); err != nil {
			_ = file.Close()
			return err
		}
	}
	return file.Close()
}
