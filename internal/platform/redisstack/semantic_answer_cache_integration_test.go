//go:build integration

package redisstack

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/semanticcache"
	"github.com/chitandabb/GoAgent/internal/semanticcache/contracttest"
	"github.com/google/uuid"
	rediscli "github.com/redis/go-redis/v9"
)

func TestSemanticAnswerCacheContract(t *testing.T) {
	address := os.Getenv("MESGUARD_TEST_REDIS_STACK_ADDR")
	if address == "" {
		address = "127.0.0.1:6380"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := rediscli.NewClient(&rediscli.Options{Addr: address, Protocol: 2})
	t.Cleanup(func() { _ = client.Close() })
	runID := strings.ReplaceAll(uuid.NewString(), "-", "")
	indexName := "mesguard_semantic_cache_contract_" + runID
	keyPrefix := "contract:semantic-cache:" + runID + ":"
	t.Cleanup(func() {
		_ = client.Del(context.Background(), keyPrefix+"capacity").Err()
		_ = client.Do(context.Background(), "FT.DROPINDEX", indexName, "DD").Err()
	})
	authority := &fixtureAuthority{generation: 1}
	cache, err := NewSemanticAnswerCache(ctx, client, authority, Config{
		IndexName: indexName, KeyPrefix: keyPrefix,
		MaxRecords: 2, TTLJitterRatio: 0,
	})
	if err != nil {
		t.Fatalf("NewSemanticAnswerCache(): %v", err)
	}
	if _, err := NewSemanticAnswerCache(ctx, client, authority, Config{
		IndexName: indexName, KeyPrefix: "contract:wrong-prefix:",
		MaxRecords: 2, TTLJitterRatio: 0,
	}); err == nil {
		t.Fatal("existing index with an incompatible prefix was accepted")
	}

	question := "设备点检周期规范是什么？"
	input := fixturePutInput(t, question, "设备点检周期为 30 天。")
	if err := cache.Put(ctx, input); err != nil {
		t.Fatalf("Put(): %v", err)
	}
	answer, hit, err := cache.Lookup(ctx, semanticcache.LookupInput{
		QuestionHash: input.QuestionHash, Now: time.Now().UTC(),
	})
	if err != nil || !hit || answer.Content != input.Answer.Content || answer.Generation != 1 {
		t.Fatalf("exact lookup hit=%v answer=%+v err=%v", hit, answer, err)
	}
	if _, hit, err := cache.Lookup(ctx, semanticcache.LookupInput{
		QuestionHash: input.QuestionHash, Now: input.Answer.CreatedAt.Add(2 * time.Hour),
	}); err != nil || hit {
		t.Fatalf("logical expiry lookup hit=%v err=%v", hit, err)
	}

	vector := make([]float32, vectorDimensions)
	vector[0] = 1
	profileID := uuid.New()
	profileFingerprint := strings.Repeat("a", 64)
	semanticIndexInput := semanticcache.SemanticIndexInput{
		QuestionHash: input.QuestionHash, Question: question, Vector: vector,
		ProfileID: profileID, ProfileFingerprint: profileFingerprint,
		NormalizationVersion: semanticcache.SemanticNormalizationVersion,
		SourceRunID:          input.Answer.SourceRunID,
	}
	if err := cache.IndexSemantic(ctx, semanticIndexInput); err != nil {
		t.Fatalf("IndexSemantic(): %v", err)
	}
	semanticInput := semanticcache.SemanticLookupInput{
		Question: "设备点检需要遵循怎样的周期规范？", Vector: vector,
		ProfileID: profileID, ProfileFingerprint: profileFingerprint,
		NormalizationVersion: semanticcache.SemanticNormalizationVersion,
		MinimumSimilarity:    0.9, CandidateLimit: 5, Now: time.Now().UTC(),
	}
	contracttest.RunReadContract(t, contracttest.ReadContract{
		Provider:          cache,
		ExactInput:        semanticcache.LookupInput{QuestionHash: input.QuestionHash, Now: time.Now().UTC()},
		ExpiredExactInput: semanticcache.LookupInput{QuestionHash: input.QuestionHash, Now: input.Answer.CreatedAt.Add(2 * time.Hour)},
		SemanticInput:     semanticInput, SemanticIndexInput: semanticIndexInput,
		ConflictingQuestion: "设备点检周期是 60 天吗？",
		ExpectedSourceRunID: input.Answer.SourceRunID, ValidPut: input,
	})
	answer, hit, err = cache.LookupSemantic(ctx, semanticInput)
	if err != nil || !hit || answer.Layer != semanticcache.LayerSemantic || answer.Similarity < 0.99 {
		t.Fatalf("semantic lookup hit=%v answer=%+v err=%v", hit, answer, err)
	}
	semanticInput.Question = "设备点检周期是 60 天吗？"
	if _, hit, err := cache.LookupSemantic(ctx, semanticInput); err != nil || hit {
		t.Fatalf("conflicting semantic lookup hit=%v err=%v", hit, err)
	}
	if err := client.HSet(ctx, cache.answerKey(input.QuestionHash), "citations", `[{"unknown":true}]`).Err(); err != nil {
		t.Fatalf("corrupt cache record: %v", err)
	}
	if _, hit, err := cache.Lookup(ctx, semanticcache.LookupInput{
		QuestionHash: input.QuestionHash, Now: time.Now().UTC(),
	}); !errors.Is(err, semanticcache.ErrInvalidRecord) || hit {
		t.Fatalf("corrupt cache lookup hit=%v err=%v", hit, err)
	}
	if err := cache.Put(ctx, input); err != nil {
		t.Fatalf("restore cache record: %v", err)
	}

	authority.setGeneration(2)
	if _, hit, err := cache.Lookup(ctx, semanticcache.LookupInput{
		QuestionHash: input.QuestionHash, Now: time.Now().UTC(),
	}); err != nil || hit {
		t.Fatalf("generation invalidation hit=%v err=%v", hit, err)
	}
	capacityErrors := make(chan error, 12)
	var capacityWrites sync.WaitGroup
	for index := range 12 {
		put := fixturePutInput(t, "缓存容量问题"+strconv.Itoa(index)+"？", "capacity answer")
		put.Answer.CreatedAt = time.Now().UTC().Add(time.Duration(index) * time.Millisecond)
		capacityWrites.Add(1)
		go func() {
			defer capacityWrites.Done()
			capacityErrors <- cache.Put(ctx, put)
		}()
	}
	capacityWrites.Wait()
	close(capacityErrors)
	for err := range capacityErrors {
		if err != nil {
			t.Fatalf("concurrent capacity Put: %v", err)
		}
	}
	if count, err := client.ZCard(ctx, cache.capacityKey).Result(); err != nil || count != 2 {
		t.Fatalf("capacity count=%d err=%v", count, err)
	}
	keys, err := client.ZRange(ctx, cache.capacityKey, 0, -1).Result()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		if exists, existsErr := client.Exists(ctx, key).Result(); existsErr != nil || exists != 1 {
			t.Fatalf("capacity member %q exists=%d err=%v", key, exists, existsErr)
		}
	}

	invalid := fixturePutInput(t, "超限答案？", strings.Repeat("x", semanticcache.MaxAnswerBytes+1))
	if err := cache.Put(ctx, invalid); !errors.Is(err, semanticcache.ErrInvalidRecord) {
		t.Fatalf("oversized answer error = %v", err)
	}
}

func fixturePutInput(t *testing.T, question, answer string) semanticcache.PutInput {
	t.Helper()
	hash, err := semanticcache.ExactQuestionKey(question)
	if err != nil {
		t.Fatal(err)
	}
	source := semanticcache.Source{
		Position: 0, SourceType: "knowledge_chunk", SourceRef: "knowledge:" + uuid.NewString() + "/" + uuid.NewString(),
		ContentSHA256: strings.Repeat("b", 64),
	}
	return semanticcache.PutInput{
		QuestionHash: hash, TTL: time.Hour,
		Answer: semanticcache.Answer{
			Content: answer, Citations: []semanticcache.Source{source}, RetrievedSources: []semanticcache.Source{source},
			SourceRunID: uuid.New(), ModelProvider: "fixture", ModelID: "fixture-model",
			PromptVersion: "fixture-v1", CreatedAt: time.Now().UTC(),
		},
	}
}

type fixtureAuthority struct {
	mu         sync.RWMutex
	generation int64
}

func (a *fixtureAuthority) CurrentGeneration(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.generation, nil
}

func (a *fixtureAuthority) AuthorizePut(ctx context.Context, _ semanticcache.PutInput) (int64, error) {
	return a.CurrentGeneration(ctx)
}

func (a *fixtureAuthority) AuthorizeSemanticIndex(ctx context.Context, _ semanticcache.SemanticIndexInput) (int64, error) {
	return a.CurrentGeneration(ctx)
}

func (a *fixtureAuthority) setGeneration(generation int64) {
	a.mu.Lock()
	a.generation = generation
	a.mu.Unlock()
}
