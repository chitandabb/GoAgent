package main

import (
	"math"
	"strings"
	"testing"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/knowledge"
)

func TestParseOptionsKeepsProviderExecutionExplicitAndBounded(t *testing.T) {
	options, err := parseOptions([]string{
		"-estimate-only", "-max-cases", "5", "-chat-profile", "qwen-qa-eval",
	})
	if err != nil {
		t.Fatalf("parseOptions: %v", err)
	}
	if !options.estimateOnly || options.executeProvider || options.maxCases != 5 ||
		options.maxProviderCostCNY != defaultMaxProviderCostCNY || options.chatProfile != "qwen-qa-eval" {
		t.Fatalf("options = %+v", options)
	}
	for _, args := range [][]string{
		{"-estimate-only", "-execute-provider"},
		{"-max-cases", "9"},
		{"-max-provider-cost-cny", "0"},
		{"-max-chat-tokens-per-case", "999"},
		{"-chat-profile", "bad/profile"},
		{"-chat-profile", "包含中文"},
	} {
		if _, err := parseOptions(args); err == nil {
			t.Fatalf("parseOptions(%v) accepted an unsafe option set", args)
		}
	}
}

func TestValidEvaluationProfileName(t *testing.T) {
	for _, value := range []string{"", "stepfun-main", "qwen_qa.eval", "A1"} {
		if !validEvaluationProfileName(value) {
			t.Fatalf("validEvaluationProfileName(%q) = false", value)
		}
	}
	for _, value := range []string{"bad/profile", "two words", strings.Repeat("x", 65)} {
		if validEvaluationProfileName(value) {
			t.Fatalf("validEvaluationProfileName(%q) = true", value)
		}
	}
}

func TestCheckedInConversationQualityFixtureIsPinnedAndBudgeted(t *testing.T) {
	corpus, err := readStrictJSON[knowledge.AdvancedRetrievalEvaluationCorpus]("../../testdata/rag-advanced-v1.corpus.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	definitions, err := readStrictJSONL("../../testdata/conversation-quality-recorded-v1.jsonl", func(value qualityCaseDefinition) error {
		return value.Validate()
	})
	if err != nil {
		t.Fatalf("read cases: %v", err)
	}
	if err := validateQualityCaseSet(definitions); err != nil {
		t.Fatalf("validate cases: %v", err)
	}
	fixture, chunks, err := selectAndValidateFixture(corpus, definitions)
	if err != nil {
		t.Fatalf("select fixture: %v", err)
	}
	if len(definitions) != 5 || len(fixture.Documents) != 4 {
		t.Fatalf("cases=%d documents=%d", len(definitions), len(fixture.Documents))
	}
	if got := definitions[0].effectiveRequiredCitationChunks(); len(got) != 2 ||
		got[0].Ordinal != 3 || got[1].Ordinal != 4 {
		t.Fatalf("pool-limit required citations = %+v", got)
	}
	if got := definitions[0].effectiveRetrievalMaxResults(); got != 3 {
		t.Fatalf("pool-limit retrieval max results = %d", got)
	}
	genericTuningChunk := chunks["go-managing-connections"][0]
	if genericTuningChunk.ContentSHA256 != "f82607d49ddf5fc3709d915fddf89b0d8e78124ea1a4e5724ae0b71d4e7a784f" ||
		strings.Contains(genericTuningChunk.ContentText, "SetMaxOpenConns") ||
		strings.Contains(genericTuningChunk.ContentText, "SetMaxIdleConns") {
		t.Fatalf("unexpected generic tuning chunk: %+v", genericTuningChunk)
	}
	options, err := parseOptions([]string{"-estimate-only", "-max-cases", "5"})
	if err != nil {
		t.Fatal(err)
	}
	plan := buildProviderPlan(fixture, chunks, definitions, options)
	if plan.Chunks != 21 || plan.EstimatedPlanningCostCNY <= options.maxProviderCostCNY {
		t.Fatalf("plan = %+v", plan)
	}
	oneFixture, oneChunks, err := selectAndValidateFixture(corpus, definitions[:1])
	if err != nil {
		t.Fatal(err)
	}
	onePlan := buildProviderPlan(oneFixture, oneChunks, definitions[:1], options)
	if onePlan.EstimatedPlanningCostCNY > options.maxProviderCostCNY || math.IsNaN(onePlan.EstimatedPlanningCostCNY) {
		t.Fatalf("single-case plan = %+v", onePlan)
	}
}

func TestQualityCaseDefinitionRequiresCitationChunksToBeRelevant(t *testing.T) {
	ref := knowledge.RetrievalEvaluationChunkRef{
		DocumentKey: "doc-a", Ordinal: 1,
		ContentSHA256: strings.Repeat("a", 64),
	}
	definition := qualityCaseDefinition{
		DatasetVersion: defaultDatasetVersion, CaseID: "case-a", UserQuery: "query",
		RetrievalMaxResults: 3, RelevantChunks: []knowledge.RetrievalEvaluationChunkRef{ref},
		ExpectedOutcome: mesagent.ConversationQualityAnswered,
	}
	if err := definition.Validate(); err != nil {
		t.Fatalf("backward-compatible required citations: %v", err)
	}
	if got := definition.effectiveRequiredCitationChunks(); len(got) != 1 || got[0] != ref {
		t.Fatalf("effective required citations = %+v", got)
	}
	definition.RequiredCitationChunks = []knowledge.RetrievalEvaluationChunkRef{{
		DocumentKey: "doc-a", Ordinal: 2, ContentSHA256: strings.Repeat("b", 64),
	}}
	if err := definition.Validate(); err == nil {
		t.Fatal("accepted a required citation chunk outside the relevant set")
	}
}

func TestStableFixtureUUIDIsRepeatable(t *testing.T) {
	first := stableFixtureUUID("conversation-quality:chunk:1")
	if first != stableFixtureUUID("conversation-quality:chunk:1") ||
		first == stableFixtureUUID("conversation-quality:chunk:2") {
		t.Fatal("stable fixture UUID mapping is not deterministic")
	}
}
