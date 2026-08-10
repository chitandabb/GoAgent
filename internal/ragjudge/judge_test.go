package ragjudge

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type generatorFunc func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error)

func (f generatorFunc) Generate(
	ctx context.Context,
	messages []*schema.Message,
	options ...model.Option,
) (*schema.Message, error) {
	return f(ctx, messages, options...)
}

func TestJudgeEvaluatesStrictResponseAndRecordsIdentity(t *testing.T) {
	input := validInput()
	model := generatorFunc(func(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.Message, error) {
		if len(messages) != 2 || messages[0].Role != schema.System || messages[1].Role != schema.User ||
			!strings.Contains(messages[1].Content, input.CaseID) {
			t.Fatalf("messages = %+v", messages)
		}
		message := schema.AssistantMessage(validResponseJSON(), nil)
		message.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{
			PromptTokens: 120, CompletionTokens: 80, TotalTokens: 200,
		}}
		return message, nil
	})
	judge, err := New(model, "judge prompt", SchemaVersion, "dashscope", "qwen3-max", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := judge.Evaluate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if observation.CaseID != input.CaseID || observation.Provider != "dashscope" ||
		observation.RequestModel != "qwen3-max" || observation.PromptVersion != SchemaVersion ||
		observation.PromptSHA256 != knowledge.SHA256Hex("judge prompt") ||
		observation.Usage.TotalTokens != 200 || observation.Scores.Faithfulness.Score != 3 ||
		observation.Scores.AnswerRelevance.Score != 2 || len(observation.Scores.UnsupportedClaims) != 1 {
		t.Fatalf("observation = %+v", observation)
	}
}

func TestJudgeRejectsInvalidInputAndNonStrictOutput(t *testing.T) {
	input := validInput()
	invalid := cloneInput(input)
	invalid.CitedEvidence[0].ContentText = "stale"
	if err := invalid.Validate(); err == nil {
		t.Fatal("Validate accepted cited evidence with a stale content hash")
	}
	disallowed := cloneInput(input)
	disallowed.CitedEvidence[0].CitationID = "source-outside-gold"
	disallowed.CitedEvidence[0].SourceRef = "knowledge:version/outside-gold"
	if err := disallowed.Validate(); err != nil {
		t.Fatalf("Validate rejected bounded cited evidence outside the gold source set: %v", err)
	}

	for _, response := range []string{
		"```json\n" + validResponseJSON() + "\n```",
		strings.Replace(validResponseJSON(), `"verdict":"partial"`, `"verdict":"partial","extra":true`, 1),
		validResponseJSON() + `{}`,
	} {
		model := generatorFunc(func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
			message := schema.AssistantMessage(response, nil)
			message.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{TotalTokens: 0}}
			return message, nil
		})
		judge, err := New(model, "judge prompt", SchemaVersion, "dashscope", "qwen3-max", 10*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := judge.Evaluate(context.Background(), input); err == nil {
			t.Fatalf("Judge accepted non-strict output %q", response)
		}
	}
}

func TestJudgeRejectsAnswerModelAsJudge(t *testing.T) {
	input := validInput()
	judge, err := New(
		generatorFunc(func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
			t.Fatal("answer model must be rejected before a Provider call")
			return nil, nil
		}),
		"judge prompt", SchemaVersion, input.AnswerProvider, input.AnswerModel, 10*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := judge.Evaluate(context.Background(), input); err == nil ||
		!strings.Contains(err.Error(), "must differ") {
		t.Fatalf("Evaluate error = %v", err)
	}
}

func cloneInput(input Input) Input {
	result := input
	result.GoldFacts = append([]string(nil), input.GoldFacts...)
	result.AllowedSources = append([]Evidence(nil), input.AllowedSources...)
	result.CitedEvidence = append([]Evidence(nil), input.CitedEvidence...)
	return result
}

func validInput() Input {
	content := "A bounded pool makes new operations wait and can cause a deadlock."
	evidence := Evidence{
		CitationID: "source-1", SourceRef: "knowledge:version/chunk",
		ContentSHA256: knowledge.SHA256Hex(content), ContentText: content,
	}
	return Input{
		DatasetVersion: "conversation-quality-v1", CaseID: "pool-wait",
		AnswerProvider: "stepfun", AnswerModel: "step-3.7-flash",
		Question: "Why does the query wait?", Answerable: true,
		GoldFacts:      []string{"New operations wait for a connection.", "The application can deadlock."},
		AllowedSources: []Evidence{evidence}, CandidateAnswer: "It waits and can deadlock [source-1].",
		CitedEvidence: []Evidence{evidence},
	}
}

func validResponseJSON() string {
	return `{"schema_version":"rag-judge-v2","verdict":"partial",` +
		`"answer_correctness":{"score":3,"reason":"core facts covered"},` +
		`"faithfulness":{"score":3,"reason":"one claim is unsupported"},` +
		`"answer_relevance":{"score":2,"reason":"contains extra advice"},` +
		`"citation_correctness":{"score":4,"reason":"citation supports its claim"},` +
		`"refusal_correctness":{"score":4,"reason":"answerable and answered"},` +
		`"unsupported_claims":[{"claim":"extra risk","reason":"not in evidence"}],` +
		`"missing_key_facts":[],"citation_issues":[]}`
}
