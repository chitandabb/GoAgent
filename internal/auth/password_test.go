package auth

import (
	"strings"
	"testing"
)

func TestArgon2idHasherHashAndVerify(t *testing.T) {
	hasher, err := NewArgon2idHasher(DefaultArgon2idParams())
	if err != nil {
		t.Fatalf("NewArgon2idHasher() error = %v", err)
	}

	encoded, err := hasher.Hash("correct horse battery staple")
	if err != nil {
		t.Fatalf("Hash() error = %v", err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=19$") {
		t.Fatalf("Hash() = %q, want PHC argon2id prefix", encoded)
	}

	matched, err := hasher.Verify("correct horse battery staple", encoded)
	if err != nil || !matched {
		t.Fatalf("Verify(correct) = %v, %v; want true, nil", matched, err)
	}
	matched, err = hasher.Verify("wrong password", encoded)
	if err != nil || matched {
		t.Fatalf("Verify(wrong) = %v, %v; want false, nil", matched, err)
	}
}

func TestArgon2idHasherUsesIndependentSalts(t *testing.T) {
	hasher, err := NewArgon2idHasher(DefaultArgon2idParams())
	if err != nil {
		t.Fatalf("NewArgon2idHasher() error = %v", err)
	}
	first, err := hasher.Hash("same password")
	if err != nil {
		t.Fatalf("first Hash() error = %v", err)
	}
	second, err := hasher.Hash("same password")
	if err != nil {
		t.Fatalf("second Hash() error = %v", err)
	}
	if first == second {
		t.Fatal("same password produced identical hashes; salts must be unique")
	}
}

func TestArgon2idHasherRejectsUnsafeEncodedCost(t *testing.T) {
	hasher, err := NewArgon2idHasher(DefaultArgon2idParams())
	if err != nil {
		t.Fatalf("NewArgon2idHasher() error = %v", err)
	}
	encoded := "$argon2id$v=19$m=1048576,t=1,p=4$MDEyMzQ1Njc4OWFiY2RlZg$MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY"
	if _, err := hasher.Verify("password", encoded); err == nil {
		t.Fatal("Verify() accepted an excessive memory cost")
	}
}

func TestArgon2idHasherNeedsRehash(t *testing.T) {
	oldParams := DefaultArgon2idParams()
	oldParams.Memory = 32 * 1024
	oldHasher, err := NewArgon2idHasher(oldParams)
	if err != nil {
		t.Fatalf("NewArgon2idHasher(old) error = %v", err)
	}
	encoded, err := oldHasher.Hash("password")
	if err != nil {
		t.Fatalf("Hash() error = %v", err)
	}
	current, err := NewArgon2idHasher(DefaultArgon2idParams())
	if err != nil {
		t.Fatalf("NewArgon2idHasher(current) error = %v", err)
	}
	needsRehash, err := current.NeedsRehash(encoded)
	if err != nil || !needsRehash {
		t.Fatalf("NeedsRehash() = %v, %v; want true, nil", needsRehash, err)
	}
}
