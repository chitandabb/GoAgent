package knowledge

import (
	"math"
	"testing"
)

func TestEmbeddingProfileStableIdentity(t *testing.T) {
	first, err := NewEmbeddingProfile(
		"knowledge-v1", "dashscope", "text-embedding-v4", 1024, "cosine",
		EmbeddingInputQuery, EmbeddingInputDocument, true, "embedding-v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewEmbeddingProfile(
		"knowledge-v1", "dashscope", "text-embedding-v4", 1024, "cosine",
		EmbeddingInputQuery, EmbeddingInputDocument, true, "embedding-v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.Fingerprint != second.Fingerprint {
		t.Fatal("equivalent embedding profiles must have stable identities")
	}
	if err := first.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateEmbeddingVector(t *testing.T) {
	if err := ValidateEmbeddingVector([]float32{0.6, 0.8}, 2, true); err != nil {
		t.Fatal(err)
	}
	for name, vector := range map[string][]float32{
		"wrong dimensions": {1},
		"zero norm":        {0, 0},
		"not normalized":   {1, 1},
		"non finite":       {float32(math.Inf(1)), 0},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateEmbeddingVector(vector, 2, true); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
