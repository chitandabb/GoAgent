package redisstack

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/semanticcache"
	"github.com/google/uuid"
)

func TestEncodeDecodeAnswerRoundTrip(t *testing.T) {
	source := semanticcache.Source{
		Position: 0, SourceType: "knowledge_chunk", SourceRef: "knowledge:a/b",
		ContentSHA256: strings.Repeat("a", 64),
	}
	input := semanticcache.PutInput{
		QuestionHash: strings.Repeat("b", 64), TTL: time.Hour,
		Answer: semanticcache.Answer{
			Content: "answer", Citations: []semanticcache.Source{source},
			RetrievedSources: []semanticcache.Source{source}, SourceRunID: uuid.New(),
			ModelProvider: "fixture", ModelID: "model", PromptVersion: "v1",
			CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		},
	}
	encoded, expiresAt, err := encodeAnswer(input, 7, 0.1)
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]string, len(encoded))
	for key, value := range encoded {
		switch current := value.(type) {
		case string:
			values[key] = current
		case []byte:
			values[key] = string(current)
		case int64:
			values[key] = stringInt(current)
		default:
			t.Fatalf("unsupported encoded type %T", value)
		}
	}
	answer, _, err := decodeAnswer(values, semanticcache.LayerExact)
	if err != nil {
		t.Fatal(err)
	}
	if answer.Content != input.Answer.Content || answer.Generation != 7 ||
		answer.SourceRunID != input.Answer.SourceRunID || !answer.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("decoded answer = %+v", answer)
	}
}

func TestParseSearchResultRejectsMalformedPayload(t *testing.T) {
	valid := []any{int64(1), "key", []any{"distance", "0.125", "generation", "7"}}
	records, err := parseSearchResult(valid)
	if err != nil || len(records) != 1 || records[0].distance != 0.125 {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	if _, err := parseSearchResult([]any{int64(1), "key", []any{"distance"}}); !errors.Is(err, semanticcache.ErrInvalidRecord) {
		t.Fatalf("malformed result error = %v", err)
	}
	for _, malformed := range []any{
		[]any{int64(1)},
		[]any{int64(1), "key"},
		[]any{"not-a-count"},
		[]any{int64(0), "key", []any{"distance", "0.1"}},
	} {
		if _, err := parseSearchResult(malformed); !errors.Is(err, semanticcache.ErrInvalidRecord) {
			t.Fatalf("malformed result %#v error = %v", malformed, err)
		}
	}
}

func TestEscapeTagEscapesRedisSearchPunctuation(t *testing.T) {
	if got := escapeTag("semantic-question-v1"); got != `semantic\-question\-v1` {
		t.Fatalf("escapeTag() = %q", got)
	}
}

func stringInt(value int64) string {
	return strconv.FormatInt(value, 10)
}
