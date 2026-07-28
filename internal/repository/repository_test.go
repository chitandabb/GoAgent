package repository

import (
	"errors"
	"testing"
)

func TestWrapPreservesKindAndCause(t *testing.T) {
	cause := errors.New("duplicate username")
	err := Wrap(ErrConflict, cause)

	if !errors.Is(err, ErrConflict) {
		t.Fatalf("errors.Is(err, ErrConflict) = false, err = %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("errors.Is(err, cause) = false, err = %v", err)
	}
}

func TestWrapNilCauseReturnsNil(t *testing.T) {
	if err := Wrap(ErrConflict, nil); err != nil {
		t.Fatalf("Wrap(ErrConflict, nil) = %v, want nil", err)
	}
}
