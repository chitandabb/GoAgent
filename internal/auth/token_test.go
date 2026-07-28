package auth

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestTokenGeneratorGenerate(t *testing.T) {
	generator := NewTokenGenerator()
	first, err := generator.Generate()
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	second, err := generator.Generate()
	if err != nil {
		t.Fatalf("Generate() second error = %v", err)
	}

	decoded, err := base64.RawURLEncoding.DecodeString(first.Raw)
	if err != nil {
		t.Fatalf("decode token: %v", err)
	}
	if len(decoded) != tokenEntropyBytes {
		t.Fatalf("token entropy bytes = %d, want %d", len(decoded), tokenEntropyBytes)
	}
	if len(first.Hash) != 32 {
		t.Fatalf("token hash length = %d, want 32", len(first.Hash))
	}
	if bytes.Equal(first.Hash, second.Hash) {
		t.Fatal("independent token generations produced the same hash")
	}
	if !bytes.Equal(first.Hash, HashToken(first.Raw)) {
		t.Fatal("stored token hash does not match HashToken(raw)")
	}
}
