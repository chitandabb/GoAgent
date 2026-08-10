package ragjudge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const (
	SchemaVersion          = "rag-judge-v2"
	maxJudgeSources        = 50
	maxJudgeSignals        = 50
	maxJudgeInputRunes     = 100_000
	maxJudgeResponseRunes  = 32_000
	maxJudgeReasonRunes    = 4_000
	maxJudgeClaimRunes     = 4_000
	maxJudgeSourceRunes    = 16_000
	maxJudgeQuestionRunes  = 4_096
	maxJudgeAnswerRunes    = 32_000
	maxJudgeSourceRefRunes = 2_048
)

var stableJudgeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type Generator interface {
	Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error)
}

type Evidence struct {
	CitationID    string `json:"citation_id"`
	SourceRef     string `json:"source_ref"`
	ContentSHA256 string `json:"content_sha256"`
	ContentText   string `json:"content_text"`
}

func (e Evidence) Validate() error {
	if !validBoundedText(e.CitationID, 1, 512) ||
		!validBoundedText(e.SourceRef, 1, maxJudgeSourceRefRunes) ||
		!validBoundedText(e.ContentText, 1, maxJudgeSourceRunes) ||
		e.ContentSHA256 != knowledge.SHA256Hex(e.ContentText) {
		return errors.New("RAG Judge evidence is invalid")
	}
	return nil
}

type Input struct {
	DatasetVersion  string     `json:"dataset_version"`
	CaseID          string     `json:"case_id"`
	AnswerProvider  string     `json:"answer_provider"`
	AnswerModel     string     `json:"answer_model"`
	Question        string     `json:"question"`
	Answerable      bool       `json:"answerable"`
	GoldFacts       []string   `json:"gold_facts"`
	AllowedSources  []Evidence `json:"allowed_sources"`
	CandidateAnswer string     `json:"candidate_answer"`
	CitedEvidence   []Evidence `json:"cited_evidence"`
}

func (i Input) Validate() error {
	if !stableJudgeID.MatchString(i.DatasetVersion) || !stableJudgeID.MatchString(i.CaseID) ||
		!stableJudgeID.MatchString(i.AnswerProvider) || !stableJudgeID.MatchString(i.AnswerModel) ||
		!validBoundedText(i.Question, 1, maxJudgeQuestionRunes) ||
		!validBoundedText(i.CandidateAnswer, 1, maxJudgeAnswerRunes) ||
		len(i.GoldFacts) > maxJudgeSignals || len(i.AllowedSources) > maxJudgeSources ||
		len(i.CitedEvidence) > maxJudgeSources ||
		(i.Answerable && (len(i.GoldFacts) == 0 || len(i.AllowedSources) == 0)) {
		return errors.New("RAG Judge input identity or dimensions are invalid")
	}
	for _, fact := range i.GoldFacts {
		if !validBoundedText(fact, 1, maxJudgeClaimRunes) {
			return errors.New("RAG Judge gold fact is invalid")
		}
	}
	allowed := make(map[string]Evidence, len(i.AllowedSources))
	for _, evidence := range i.AllowedSources {
		if err := evidence.Validate(); err != nil {
			return err
		}
		if _, exists := allowed[evidence.CitationID]; exists {
			return errors.New("RAG Judge allowed citation IDs must be unique")
		}
		allowed[evidence.CitationID] = evidence
	}
	cited := make(map[string]struct{}, len(i.CitedEvidence))
	for _, evidence := range i.CitedEvidence {
		if err := evidence.Validate(); err != nil {
			return err
		}
		if _, exists := cited[evidence.CitationID]; exists {
			return errors.New("RAG Judge cited evidence must be unique")
		}
		cited[evidence.CitationID] = struct{}{}
	}
	encoded, err := json.Marshal(i)
	if err != nil || len([]rune(string(encoded))) > maxJudgeInputRunes {
		return errors.New("RAG Judge input exceeds the bounded payload")
	}
	return nil
}

type Dimension struct {
	Score  int    `json:"score"`
	Reason string `json:"reason"`
}

func (d Dimension) Validate() error {
	if d.Score < 0 || d.Score > 4 || !validBoundedTextAllowEmpty(d.Reason, maxJudgeReasonRunes) {
		return errors.New("RAG Judge score dimension is invalid")
	}
	return nil
}

type UnsupportedClaim struct {
	Claim  string `json:"claim"`
	Reason string `json:"reason"`
}

type CitationIssue struct {
	CitationID string `json:"citation_id"`
	Reason     string `json:"reason"`
}

type Response struct {
	SchemaVersion       string             `json:"schema_version"`
	Verdict             string             `json:"verdict"`
	AnswerCorrectness   Dimension          `json:"answer_correctness"`
	Faithfulness        Dimension          `json:"faithfulness"`
	AnswerRelevance     Dimension          `json:"answer_relevance"`
	CitationCorrectness Dimension          `json:"citation_correctness"`
	RefusalCorrectness  Dimension          `json:"refusal_correctness"`
	UnsupportedClaims   []UnsupportedClaim `json:"unsupported_claims"`
	MissingKeyFacts     []string           `json:"missing_key_facts"`
	CitationIssues      []CitationIssue    `json:"citation_issues"`
}

func (r Response) Validate() error {
	if r.SchemaVersion != SchemaVersion ||
		(r.Verdict != "pass" && r.Verdict != "partial" && r.Verdict != "fail") ||
		len(r.UnsupportedClaims) > maxJudgeSignals || len(r.MissingKeyFacts) > maxJudgeSignals ||
		len(r.CitationIssues) > maxJudgeSignals {
		return errors.New("RAG Judge response identity or dimensions are invalid")
	}
	for _, dimension := range []Dimension{
		r.AnswerCorrectness, r.Faithfulness, r.AnswerRelevance,
		r.CitationCorrectness, r.RefusalCorrectness,
	} {
		if err := dimension.Validate(); err != nil {
			return err
		}
	}
	for _, item := range r.UnsupportedClaims {
		if !validBoundedText(item.Claim, 1, maxJudgeClaimRunes) ||
			!validBoundedText(item.Reason, 1, maxJudgeReasonRunes) {
			return errors.New("RAG Judge unsupported claim is invalid")
		}
	}
	for _, fact := range r.MissingKeyFacts {
		if !validBoundedText(fact, 1, maxJudgeClaimRunes) {
			return errors.New("RAG Judge missing fact is invalid")
		}
	}
	for _, item := range r.CitationIssues {
		if !validBoundedText(item.CitationID, 1, 512) ||
			!validBoundedText(item.Reason, 1, maxJudgeReasonRunes) {
			return errors.New("RAG Judge citation issue is invalid")
		}
	}
	return nil
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (u Usage) Validate() error {
	if u.PromptTokens < 0 || u.CompletionTokens < 0 || u.TotalTokens < 0 ||
		u.TotalTokens != u.PromptTokens+u.CompletionTokens {
		return errors.New("RAG Judge usage is invalid")
	}
	return nil
}

type Observation struct {
	DatasetVersion   string   `json:"dataset_version"`
	CaseID           string   `json:"case_id"`
	Provider         string   `json:"provider"`
	RequestModel     string   `json:"request_model"`
	PromptVersion    string   `json:"prompt_version"`
	PromptSHA256     string   `json:"prompt_sha256"`
	DurationMillis   int64    `json:"duration_millis"`
	Usage            Usage    `json:"usage"`
	EstimatedCostCNY float64  `json:"estimated_cost_cny"`
	Scores           Response `json:"scores"`
}

func (o Observation) Validate() error {
	if !stableJudgeID.MatchString(o.DatasetVersion) || !stableJudgeID.MatchString(o.CaseID) ||
		!stableJudgeID.MatchString(o.Provider) || !stableJudgeID.MatchString(o.RequestModel) ||
		!stableJudgeID.MatchString(o.PromptVersion) || o.PromptVersion != SchemaVersion ||
		len(o.PromptSHA256) != 64 || o.DurationMillis < 0 || o.DurationMillis > 10*60*1000 ||
		math.IsNaN(o.EstimatedCostCNY) || math.IsInf(o.EstimatedCostCNY, 0) ||
		o.EstimatedCostCNY < 0 || o.EstimatedCostCNY > 1_000 {
		return errors.New("RAG Judge observation identity or runtime is invalid")
	}
	for _, current := range o.PromptSHA256 {
		if !strings.ContainsRune("0123456789abcdef", current) {
			return errors.New("RAG Judge prompt hash is invalid")
		}
	}
	if err := o.Usage.Validate(); err != nil {
		return err
	}
	return o.Scores.Validate()
}

type Judge struct {
	generator     Generator
	prompt        string
	promptVersion string
	promptSHA256  string
	provider      string
	modelID       string
	timeout       time.Duration
}

func New(
	generator Generator,
	prompt, promptVersion, provider, modelID string,
	timeout time.Duration,
) (*Judge, error) {
	if generator == nil || !validBoundedText(prompt, 1, 32_000) ||
		!stableJudgeID.MatchString(promptVersion) || !stableJudgeID.MatchString(provider) ||
		!stableJudgeID.MatchString(modelID) || timeout < time.Second || timeout > 5*time.Minute {
		return nil, errors.New("RAG Judge config is invalid")
	}
	return &Judge{
		generator: generator, prompt: prompt, promptVersion: promptVersion,
		promptSHA256: knowledge.SHA256Hex(prompt), provider: provider, modelID: modelID, timeout: timeout,
	}, nil
}

func (j *Judge) Evaluate(ctx context.Context, input Input) (Observation, error) {
	if j == nil || j.generator == nil {
		return Observation{}, errors.New("RAG Judge is unavailable")
	}
	if err := input.Validate(); err != nil {
		return Observation{}, err
	}
	if strings.EqualFold(input.AnswerProvider, j.provider) && strings.EqualFold(input.AnswerModel, j.modelID) {
		return Observation{}, errors.New("RAG Judge model must differ from the answer model")
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return Observation{}, errors.New("encode RAG Judge input")
	}
	runCtx, cancel := context.WithTimeout(ctx, j.timeout)
	defer cancel()
	started := time.Now()
	message, err := j.generator.Generate(runCtx, []*schema.Message{
		schema.SystemMessage(j.prompt), schema.UserMessage(string(payload)),
	})
	duration := time.Since(started)
	if err != nil {
		return Observation{}, fmt.Errorf("RAG Judge model request: %w", err)
	}
	if message == nil || message.Role != schema.Assistant ||
		!validBoundedText(message.Content, 1, maxJudgeResponseRunes) {
		return Observation{}, errors.New("RAG Judge model output is invalid")
	}
	scores, err := decodeResponse(message.Content)
	if err != nil {
		return Observation{}, err
	}
	if message.ResponseMeta == nil || message.ResponseMeta.Usage == nil {
		return Observation{}, errors.New("RAG Judge model usage is required")
	}
	usage := Usage{
		PromptTokens:     message.ResponseMeta.Usage.PromptTokens,
		CompletionTokens: message.ResponseMeta.Usage.CompletionTokens,
		TotalTokens:      message.ResponseMeta.Usage.TotalTokens,
	}
	if err := usage.Validate(); err != nil {
		return Observation{}, err
	}
	observation := Observation{
		DatasetVersion: input.DatasetVersion, CaseID: input.CaseID,
		Provider: j.provider, RequestModel: j.modelID,
		PromptVersion: j.promptVersion, PromptSHA256: j.promptSHA256,
		DurationMillis: duration.Milliseconds(), Usage: usage, Scores: scores,
	}
	if err := observation.Validate(); err != nil {
		return Observation{}, err
	}
	return observation, nil
}

func decodeResponse(content string) (Response, error) {
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(content)))
	decoder.DisallowUnknownFields()
	var response Response
	if err := decoder.Decode(&response); err != nil {
		return Response{}, fmt.Errorf("decode RAG Judge response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Response{}, errors.New("decode RAG Judge response: trailing content")
	}
	if err := response.Validate(); err != nil {
		return Response{}, err
	}
	return response, nil
}

func validBoundedText(value string, minRunes, maxRunes int) bool {
	return value == strings.TrimSpace(value) && !strings.ContainsRune(value, 0) &&
		len([]rune(value)) >= minRunes && len([]rune(value)) <= maxRunes
}

func validBoundedTextAllowEmpty(value string, maxRunes int) bool {
	return value == strings.TrimSpace(value) && !strings.ContainsRune(value, 0) && len([]rune(value)) <= maxRunes
}
