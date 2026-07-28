package postgres

import (
	"errors"
	"fmt"
	"testing"

	"github.com/chitandabb/GoAgent/internal/repository"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

func TestTranslateError(t *testing.T) {
	other := errors.New("connection closed")
	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "nil", err: nil, want: nil},
		{name: "not found", err: gorm.ErrRecordNotFound, want: repository.ErrNotFound},
		{name: "unique violation", err: &pgconn.PgError{Code: "23505"}, want: repository.ErrConflict},
		{name: "unknown database error", err: other, want: other},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TranslateError(tt.err)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("TranslateError() = %v, want nil", got)
				}
				return
			}
			if !errors.Is(got, tt.want) {
				t.Fatalf("TranslateError() = %v, want errors.Is(..., %v)", got, tt.want)
			}
		})
	}
}

func TestTranslateError_PreservesPgErrorChain(t *testing.T) {
	// 模拟 UseCase 层包装后的错误：fmt.Errorf("create user: %w", pgErr)
	inner := &pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint"}
	wrapped := fmt.Errorf("create user: %w", inner)

	got := TranslateError(wrapped)

	// TranslateError 应将其识别为 ErrConflict
	if !errors.Is(got, repository.ErrConflict) {
		t.Fatalf("TranslateError() error kind = %v, want repository.ErrConflict", got)
	}

	// 即使经过 TranslateError 包装，errors.As 仍应能在错误链中找到原始的 *pgconn.PgError
	var pgErr *pgconn.PgError
	if !errors.As(got, &pgErr) {
		t.Fatal("errors.As cannot find *pgconn.PgError in wrapped error chain")
	}
	if pgErr.Code != "23505" {
		t.Fatalf("pgErr.Code = %q, want %q", pgErr.Code, "23505")
	}
	if pgErr.Message != inner.Message {
		t.Fatalf("pgErr.Message = %q, want %q", pgErr.Message, inner.Message)
	}
}
