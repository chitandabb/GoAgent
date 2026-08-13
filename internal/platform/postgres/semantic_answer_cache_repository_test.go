package postgres

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/semanticcache"
	"github.com/google/uuid"
)

func TestParseKnowledgeSourceRefRequiresVersionAndChunkUUIDs(t *testing.T) {
	versionID, chunkID := uuid.New(), uuid.New()
	parsedVersion, parsedChunk, ok := parseKnowledgeSourceRef(
		"knowledge:" + versionID.String() + "/" + chunkID.String(),
	)
	if !ok || parsedVersion != versionID || parsedChunk != chunkID {
		t.Fatalf("parsed version=%s chunk=%s ok=%v", parsedVersion, parsedChunk, ok)
	}
	for _, invalid := range []string{
		"knowledge:" + versionID.String(),
		"knowledge:" + versionID.String() + "/not-a-uuid",
		"attachment:" + versionID.String() + "/" + chunkID.String(),
	} {
		if _, _, valid := parseKnowledgeSourceRef(invalid); valid {
			t.Fatalf("invalid source ref accepted: %q", invalid)
		}
	}
}

func TestSemanticCacheTTLJitterIsDeterministicAndBounded(t *testing.T) {
	ttl := 24 * time.Hour
	first := semanticCacheTTL(ttl, 0.1, strings.Repeat("a", 64))
	second := semanticCacheTTL(ttl, 0.1, strings.Repeat("a", 64))
	if first != second || first < 21*time.Hour+36*time.Minute || first > 26*time.Hour+24*time.Minute {
		t.Fatalf("jittered ttl = %s, second = %s", first, second)
	}
	if got := semanticCacheTTL(ttl, 0, strings.Repeat("b", 64)); got != ttl {
		t.Fatalf("zero jitter ttl = %s", got)
	}
}

func TestDecodeSemanticCacheJSONRejectsUnknownFieldsAndTrailingValues(t *testing.T) {
	valid := `[{"position":0,"sourceType":"knowledge_chunk","sourceRef":"knowledge:a/b","contentSha256":"` + strings.Repeat("a", 64) + `"}]`
	var sources []semanticcache.Source
	if err := decodeSemanticCacheJSON([]byte(valid), &sources); err != nil || len(sources) != 1 {
		t.Fatalf("decode valid sources: len=%d err=%v", len(sources), err)
	}
	for _, invalid := range []string{
		`[{"position":0,"unknown":true}]`,
		valid + `{}`,
	} {
		if err := decodeSemanticCacheJSON([]byte(invalid), &sources); !errors.Is(err, semanticcache.ErrInvalidRecord) {
			t.Fatalf("decode error = %v, want ErrInvalidRecord", err)
		}
	}
}
