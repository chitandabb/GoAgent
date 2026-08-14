package postgres

import (
	"context"
	"errors"

	"github.com/chitandabb/GoAgent/internal/semanticcache"
	"gorm.io/gorm"
)

// SemanticAnswerCacheAuthority keeps PostgreSQL authoritative when disposable
// cache records are stored by another Provider such as Redis Stack.
type SemanticAnswerCacheAuthority struct {
	db *gorm.DB
}

func NewSemanticAnswerCacheAuthority(db *gorm.DB) (*SemanticAnswerCacheAuthority, error) {
	if db == nil {
		return nil, errors.New("semantic answer cache authority is unavailable")
	}
	return &SemanticAnswerCacheAuthority{db: db}, nil
}

func (a *SemanticAnswerCacheAuthority) CurrentGeneration(ctx context.Context) (int64, error) {
	if a == nil || a.db == nil {
		return 0, errors.New("semantic answer cache authority is unavailable")
	}
	var generation int64
	result := ResolveDB(ctx, a.db).Raw(`
SELECT generation FROM global_knowledge_generation WHERE singleton = 1`).Scan(&generation)
	if result.Error != nil {
		return 0, TranslateError(result.Error)
	}
	if result.RowsAffected != 1 || generation < 1 {
		return 0, errors.New("global knowledge generation is unavailable")
	}
	return generation, nil
}

func (a *SemanticAnswerCacheAuthority) AuthorizePut(
	ctx context.Context,
	input semanticcache.PutInput,
) (int64, error) {
	if a == nil || a.db == nil {
		return 0, errors.New("semantic answer cache authority is unavailable")
	}
	var generation int64
	err := ResolveDB(ctx, a.db).Transaction(func(tx *gorm.DB) error {
		var err error
		generation, err = authorizeSemanticCachePut(tx, input)
		return err
	})
	return generation, TranslateError(err)
}

func (a *SemanticAnswerCacheAuthority) AuthorizeSemanticIndex(
	ctx context.Context,
	input semanticcache.SemanticIndexInput,
) (int64, error) {
	if a == nil || a.db == nil {
		return 0, errors.New("semantic answer cache authority is unavailable")
	}
	var generation int64
	err := ResolveDB(ctx, a.db).Transaction(func(tx *gorm.DB) error {
		var err error
		generation, err = authorizeSemanticCacheIndex(tx, input)
		return err
	})
	return generation, TranslateError(err)
}

func authorizeSemanticCachePut(tx *gorm.DB, input semanticcache.PutInput) (int64, error) {
	var generation int64
	result := tx.Raw(`
SELECT generation FROM global_knowledge_generation WHERE singleton = 1 FOR UPDATE`).Scan(&generation)
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected != 1 || generation < 1 {
		return 0, errors.New("global knowledge generation is unavailable")
	}
	if err := validateSemanticCacheSourceRun(tx, input.QuestionHash, input.Answer); err != nil {
		return 0, err
	}
	for _, source := range append(append([]semanticcache.Source(nil), input.Answer.Citations...), input.Answer.RetrievedSources...) {
		if err := validateCurrentGlobalKnowledgeSource(tx, source); err != nil {
			return 0, err
		}
	}
	return generation, nil
}

func authorizeSemanticCacheIndex(tx *gorm.DB, input semanticcache.SemanticIndexInput) (int64, error) {
	var generation int64
	result := tx.Raw(`
SELECT generation FROM global_knowledge_generation WHERE singleton = 1 FOR UPDATE`).Scan(&generation)
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected != 1 || generation < 1 {
		return 0, errors.New("global knowledge generation is unavailable")
	}
	var profileMatches bool
	result = tx.Raw(`
SELECT true FROM knowledge_embedding_profiles
WHERE id = ? AND fingerprint = ? AND status = 'active' AND dimensions = 1024
  AND distance_metric = 'cosine' AND normalized = true`, input.ProfileID, input.ProfileFingerprint).Scan(&profileMatches)
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected != 1 || !profileMatches {
		return 0, semanticcache.ErrInvalidRecord
	}
	return generation, nil
}
