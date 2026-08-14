package main

import (
	"strings"
	"testing"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/knowledge"

	"github.com/google/uuid"
)

func TestBuildJudgeInputsKeepsActualCitationOutsideGoldSources(t *testing.T) {
	corpus, err := readStrictJSON[knowledge.AdvancedRetrievalEvaluationCorpus](
		"../../../testdata/rag-advanced-v1.corpus.json",
	)
	if err != nil {
		t.Fatal(err)
	}
	rawCases, err := readStrictJSONL(
		"../../../testdata/conversation-quality-recorded-v1.jsonl",
		func(value rawQualityCase) error { return value.Validate() },
	)
	if err != nil {
		t.Fatal(err)
	}
	raw := rawCases[0]
	chunksByDocument, err := knowledge.BuildAdvancedRetrievalCorpusChunks(corpus)
	if err != nil {
		t.Fatal(err)
	}

	definition := mesagent.ConversationQualityCase{
		DatasetVersion: raw.DatasetVersion, CaseID: raw.CaseID, UserQuery: raw.UserQuery,
		RetrievalMaxResults:  raw.effectiveRetrievalMaxResults(),
		RequiredAnswerTerms:  append([]string(nil), raw.RequiredAnswerTerms...),
		ForbiddenAnswerTerms: append([]string(nil), raw.ForbiddenAnswerTerms...),
		ExpectedOutcome:      raw.ExpectedOutcome, Tags: append([]string(nil), raw.Tags...),
	}
	sourceByChunk := make(map[string]mesagent.ConversationQualitySource, len(raw.RelevantChunks))
	versionID := uuid.NewString()
	for _, ref := range raw.RelevantChunks {
		source := qualityTestSource(versionID, ref.ContentSHA256)
		definition.RelevantSources = append(definition.RelevantSources, source)
		sourceByChunk[rawChunkKey(ref)] = source
	}
	for _, ref := range raw.effectiveRequiredCitationChunks() {
		definition.RequiredCitationRefs = append(definition.RequiredCitationRefs, sourceByChunk[rawChunkKey(ref)].SourceRef)
	}
	if err := definition.Validate(); err != nil {
		t.Fatal(err)
	}

	relevantHashes := make(map[string]struct{}, len(raw.RelevantChunks))
	for _, ref := range raw.RelevantChunks {
		relevantHashes[ref.ContentSHA256] = struct{}{}
	}
	var extraChunk knowledge.ChunkDraft
	foundExtra := false
	for _, chunks := range chunksByDocument {
		for _, chunk := range chunks {
			if _, relevant := relevantHashes[chunk.ContentSHA256]; relevant {
				continue
			}
			extraChunk, foundExtra = chunk, true
			break
		}
		if foundExtra {
			break
		}
	}
	if !foundExtra {
		t.Fatal("fixture has no non-gold chunk")
	}
	extraSource := qualityTestSource(versionID, extraChunk.ContentSHA256)
	observation := mesagent.ConversationQualityObservation{
		DatasetVersion: raw.DatasetVersion, CaseID: raw.CaseID, RunID: "judge-export-run",
		ObservationKind: mesagent.ConversationQualityRecordedRun,
		Model:           "dashscope", ModelVersion: "qwen3.6-flash", PromptVersion: "conversation-v4",
		Answer: "The answer cites one gold source and one additional source.", Outcome: raw.ExpectedOutcome,
		RetrievedSources: append(append([]mesagent.ConversationQualitySource(nil), definition.RelevantSources...), extraSource),
		Citations: []mesagent.ConversationQualityCitation{
			qualityTestCitation(definition.RelevantSources[0]), qualityTestCitation(extraSource),
		},
		Usage:          mesagent.ModelUsage{ModelCalls: 1, PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		DurationMillis: 10,
	}
	if err := observation.Validate(); err != nil {
		t.Fatal(err)
	}
	facts := judgeFacts{
		DatasetVersion: raw.DatasetVersion, CaseID: raw.CaseID, Answerable: true,
		GoldFacts: []string{"The connection pool makes a new operation wait."},
	}

	inputs, err := buildJudgeInputs(
		corpus, []rawQualityCase{raw}, []judgeFacts{facts},
		[]mesagent.ConversationQualityCase{definition}, []mesagent.ConversationQualityObservation{observation},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 1 || len(inputs[0].AllowedSources) != len(raw.RelevantChunks) ||
		len(inputs[0].CitedEvidence) != 2 || inputs[0].AnswerProvider != observation.Model ||
		inputs[0].AnswerModel != observation.ModelVersion {
		t.Fatalf("inputs = %+v", inputs)
	}
	extraCitationID := inputs[0].CitedEvidence[1].CitationID
	for _, allowed := range inputs[0].AllowedSources {
		if allowed.CitationID == extraCitationID {
			t.Fatalf("extra citation %q was laundered into gold sources", extraCitationID)
		}
	}
}

func TestRawQualityCaseRejectsRequiredCitationOutsideRelevant(t *testing.T) {
	raw := rawQualityCase{
		DatasetVersion: "dataset-v1", CaseID: "case-v1", UserQuery: "question",
		RelevantChunks: []knowledge.RetrievalEvaluationChunkRef{{
			DocumentKey: "doc-a", Ordinal: 0, ContentSHA256: strings.Repeat("a", 64),
		}},
		ExpectedOutcome: mesagent.ConversationQualityAnswered,
	}
	if err := raw.Validate(); err != nil {
		t.Fatal(err)
	}
	raw.RequiredCitationChunks = []knowledge.RetrievalEvaluationChunkRef{{
		DocumentKey: "doc-b", Ordinal: 0, ContentSHA256: strings.Repeat("b", 64),
	}}
	if err := raw.Validate(); err == nil || !strings.Contains(err.Error(), "not relevant") {
		t.Fatalf("Validate error = %v", err)
	}
}

func qualityTestSource(versionID, contentSHA256 string) mesagent.ConversationQualitySource {
	return mesagent.ConversationQualitySource{
		SourceType:    mesagent.EvidenceSourceKnowledgeChunk,
		SourceRef:     "knowledge:" + versionID + "/" + uuid.NewString(),
		ContentSHA256: contentSHA256, PreviewRequired: true,
	}
}

func qualityTestCitation(source mesagent.ConversationQualitySource) mesagent.ConversationQualityCitation {
	return mesagent.ConversationQualityCitation{
		SourceType: source.SourceType, SourceRef: source.SourceRef,
		ContentSHA256: source.ContentSHA256, PreviewContentSHA256: source.ContentSHA256,
	}
}
