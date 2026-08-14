package contracttest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/semanticcache"
	"github.com/google/uuid"
)

type ReadContract struct {
	Provider            semanticcache.SemanticProvider
	ExactInput          semanticcache.LookupInput
	ExpiredExactInput   semanticcache.LookupInput
	SemanticInput       semanticcache.SemanticLookupInput
	SemanticIndexInput  semanticcache.SemanticIndexInput
	ConflictingQuestion string
	ExpectedSourceRunID uuid.UUID
	ValidPut            semanticcache.PutInput
}

// RunReadContract applies the same externally visible read, cancellation and
// timeout assertions to every Semantic Answer Cache Provider.
func RunReadContract(t *testing.T, fixture ReadContract) {
	t.Helper()
	t.Run("exact hit", func(t *testing.T) {
		answer, hit, err := fixture.Provider.Lookup(context.Background(), fixture.ExactInput)
		if err != nil || !hit || answer.Layer != semanticcache.LayerExact || answer.SourceRunID != fixture.ExpectedSourceRunID {
			t.Fatalf("Lookup() hit=%v answer=%+v err=%v", hit, answer, err)
		}
	})
	t.Run("semantic hit", func(t *testing.T) {
		answer, hit, err := fixture.Provider.LookupSemantic(context.Background(), fixture.SemanticInput)
		if err != nil || !hit || answer.Layer != semanticcache.LayerSemantic || answer.SourceRunID != fixture.ExpectedSourceRunID {
			t.Fatalf("LookupSemantic() hit=%v answer=%+v err=%v", hit, answer, err)
		}
	})
	t.Run("semantic conflict", func(t *testing.T) {
		input := fixture.SemanticInput
		input.Question = fixture.ConflictingQuestion
		if _, hit, err := fixture.Provider.LookupSemantic(context.Background(), input); err != nil || hit {
			t.Fatalf("LookupSemantic(conflict) hit=%v err=%v", hit, err)
		}
	})
	t.Run("logical ttl", func(t *testing.T) {
		if _, hit, err := fixture.Provider.Lookup(context.Background(), fixture.ExpiredExactInput); err != nil || hit {
			t.Fatalf("Lookup(expired) hit=%v err=%v", hit, err)
		}
	})
	t.Run("size limit", func(t *testing.T) {
		oversized := fixture.ValidPut
		oversized.Answer.Content = string(make([]byte, semanticcache.MaxAnswerBytes+1))
		if err := fixture.Provider.Put(context.Background(), oversized); !errors.Is(err, semanticcache.ErrInvalidRecord) {
			t.Fatalf("Put(oversized) error=%v", err)
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, _, err := fixture.Provider.Lookup(ctx, fixture.ExactInput); !errors.Is(err, context.Canceled) {
			t.Fatalf("Lookup(canceled) error=%v", err)
		}
		if _, _, err := fixture.Provider.LookupSemantic(ctx, fixture.SemanticInput); !errors.Is(err, context.Canceled) {
			t.Fatalf("LookupSemantic(canceled) error=%v", err)
		}
		if err := fixture.Provider.Put(ctx, fixture.ValidPut); !errors.Is(err, context.Canceled) {
			t.Fatalf("Put(canceled) error=%v", err)
		}
		if err := fixture.Provider.IndexSemantic(ctx, fixture.SemanticIndexInput); !errors.Is(err, context.Canceled) {
			t.Fatalf("IndexSemantic(canceled) error=%v", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		if _, _, err := fixture.Provider.Lookup(ctx, fixture.ExactInput); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Lookup(timeout) error=%v", err)
		}
		if _, _, err := fixture.Provider.LookupSemantic(ctx, fixture.SemanticInput); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("LookupSemantic(timeout) error=%v", err)
		}
		if err := fixture.Provider.Put(ctx, fixture.ValidPut); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Put(timeout) error=%v", err)
		}
		if err := fixture.Provider.IndexSemantic(ctx, fixture.SemanticIndexInput); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("IndexSemantic(timeout) error=%v", err)
		}
	})
}
