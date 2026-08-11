package redis

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversationmemory"

	"github.com/google/uuid"
	rediscli "github.com/redis/go-redis/v9"
)

type conversationMemoryCommandsStub struct {
	values          map[string]string
	getErr          error
	storeErr        error
	deleteErr       error
	loadedKey       string
	storedKey       string
	storedIndexKey  string
	storedTTL       time.Duration
	storedIndexTTL  time.Duration
	deletedIndexKey string
	deadlineSeen    bool
}

func (s *conversationMemoryCommandsStub) Get(ctx context.Context, key string) (string, error) {
	_, s.deadlineSeen = ctx.Deadline()
	s.loadedKey = key
	if s.getErr != nil {
		return "", s.getErr
	}
	value, ok := s.values[key]
	if !ok {
		return "", rediscli.Nil
	}
	return value, nil
}

func (s *conversationMemoryCommandsStub) Store(
	ctx context.Context,
	key, indexKey, value string,
	ttl, indexTTL time.Duration,
) error {
	_, s.deadlineSeen = ctx.Deadline()
	s.storedKey = key
	s.storedIndexKey = indexKey
	s.storedTTL = ttl
	s.storedIndexTTL = indexTTL
	if s.storeErr != nil {
		return s.storeErr
	}
	if s.values == nil {
		s.values = make(map[string]string)
	}
	s.values[key] = value
	return nil
}

func (s *conversationMemoryCommandsStub) DeleteIndexed(ctx context.Context, indexKey, _ string) error {
	_, s.deadlineSeen = ctx.Deadline()
	s.deletedIndexKey = indexKey
	return s.deleteErr
}

func TestConversationMemoryCacheStoresIdentityBoundSnapshotWithJitteredTTL(t *testing.T) {
	snapshot := redisActiveSnapshotFixture(t)
	commands := &conversationMemoryCommandsStub{}
	cache, err := newConversationMemoryCache(commands, ConversationMemoryCacheConfig{
		TTL: 2 * time.Hour, JitterRatio: 0.10, OperationTimeout: 50 * time.Millisecond,
		randomFloat64: func() float64 { return 0 },
	})
	if err != nil {
		t.Fatalf("newConversationMemoryCache() error = %v", err)
	}

	if err := cache.Store(context.Background(), snapshot); err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	if !strings.Contains(commands.storedKey, snapshot.ConversationID.String()) ||
		!strings.Contains(commands.storedKey, snapshot.ID.String()) {
		t.Fatalf("stored key %q does not bind conversation and snapshot", commands.storedKey)
	}
	if commands.storedTTL != 108*time.Minute || commands.storedIndexTTL != 132*time.Minute {
		t.Fatalf("stored ttl/index ttl = %s/%s, want 108m/132m", commands.storedTTL, commands.storedIndexTTL)
	}
	if commands.storedIndexKey == "" || !commands.deadlineSeen {
		t.Fatalf("index/deadline = %q/%t", commands.storedIndexKey, commands.deadlineSeen)
	}

	loaded, err := cache.Load(context.Background(), snapshot.ConversationID, snapshot.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.ID != snapshot.ID || loaded.Version != snapshot.Version ||
		loaded.PayloadSHA256 != snapshot.PayloadSHA256 {
		t.Fatalf("Load() = %+v, want snapshot %s", loaded, snapshot.ID)
	}
}

func TestConversationMemoryCacheMapsRedisMissAndRejectsInvalidPayload(t *testing.T) {
	snapshot := redisActiveSnapshotFixture(t)
	commands := &conversationMemoryCommandsStub{getErr: rediscli.Nil}
	cache := mustConversationMemoryCache(t, commands)

	if _, err := cache.Load(context.Background(), snapshot.ConversationID, snapshot.ID); !errors.Is(err, conversationmemory.ErrSnapshotCacheMiss) {
		t.Fatalf("Load() miss error = %v", err)
	}

	commands.getErr = nil
	commands.values = map[string]string{conversationMemorySnapshotKey(snapshot.ConversationID, snapshot.ID): `{"schemaVersion":1,"snapshot":`}
	if _, err := cache.Load(context.Background(), snapshot.ConversationID, snapshot.ID); !errors.Is(err, conversationmemory.ErrSnapshotCacheInvalid) {
		t.Fatalf("Load() invalid error = %v", err)
	}
}

func TestConversationMemoryCacheRejectsMismatchedEnvelopeIdentity(t *testing.T) {
	snapshot := redisActiveSnapshotFixture(t)
	commands := &conversationMemoryCommandsStub{}
	cache := mustConversationMemoryCache(t, commands)
	if err := cache.Store(context.Background(), snapshot); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	if _, err := cache.Load(context.Background(), snapshot.ConversationID, uuid.New()); !errors.Is(err, conversationmemory.ErrSnapshotCacheMiss) {
		t.Fatalf("Load() wrong snapshot key error = %v", err)
	}

	otherConversation := uuid.New()
	commands.values[conversationMemorySnapshotKey(otherConversation, snapshot.ID)] = commands.values[commands.storedKey]
	if _, err := cache.Load(context.Background(), otherConversation, snapshot.ID); !errors.Is(err, conversationmemory.ErrSnapshotCacheInvalid) {
		t.Fatalf("Load() mismatched envelope error = %v", err)
	}
}

func TestConversationMemoryCacheDeleteUsesConversationIndexAndPropagatesFailure(t *testing.T) {
	snapshot := redisActiveSnapshotFixture(t)
	commands := &conversationMemoryCommandsStub{deleteErr: errors.New("unlink failed")}
	cache := mustConversationMemoryCache(t, commands)

	err := cache.DeleteConversation(context.Background(), snapshot.ConversationID)
	if err == nil || commands.deletedIndexKey != conversationMemoryIndexKey(snapshot.ConversationID) || !commands.deadlineSeen {
		t.Fatalf("DeleteConversation() = %v, index/deadline = %q/%t",
			err, commands.deletedIndexKey, commands.deadlineSeen)
	}
}

func TestConversationMemoryCacheConfigValidation(t *testing.T) {
	commands := &conversationMemoryCommandsStub{}
	valid := ConversationMemoryCacheConfig{
		TTL: 2 * time.Hour, JitterRatio: 0.10, OperationTimeout: 50 * time.Millisecond,
	}
	for name, mutate := range map[string]func(*ConversationMemoryCacheConfig){
		"ttl too short":     func(cfg *ConversationMemoryCacheConfig) { cfg.TTL = 0 },
		"ttl too long":      func(cfg *ConversationMemoryCacheConfig) { cfg.TTL = 8 * 24 * time.Hour },
		"negative jitter":   func(cfg *ConversationMemoryCacheConfig) { cfg.JitterRatio = -0.01 },
		"excess jitter":     func(cfg *ConversationMemoryCacheConfig) { cfg.JitterRatio = 0.51 },
		"timeout too short": func(cfg *ConversationMemoryCacheConfig) { cfg.OperationTimeout = 0 },
		"timeout too long":  func(cfg *ConversationMemoryCacheConfig) { cfg.OperationTimeout = 2 * time.Second },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if _, err := newConversationMemoryCache(commands, candidate); err == nil {
				t.Fatal("newConversationMemoryCache() accepted invalid config")
			}
		})
	}
}

func mustConversationMemoryCache(
	t *testing.T,
	commands conversationMemoryCommands,
) *ConversationMemoryCache {
	t.Helper()
	cache, err := newConversationMemoryCache(commands, ConversationMemoryCacheConfig{
		TTL: 2 * time.Hour, JitterRatio: 0.10, OperationTimeout: 50 * time.Millisecond,
		randomFloat64: func() float64 { return 0.5 },
	})
	if err != nil {
		t.Fatalf("newConversationMemoryCache() error = %v", err)
	}
	return cache
}

func redisActiveSnapshotFixture(t *testing.T) conversationmemory.Snapshot {
	t.Helper()
	createdAt := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	candidate, err := conversationmemory.NewCandidateSnapshot(conversationmemory.NewCandidateSnapshotInput{
		ID: uuid.New(), ConversationID: uuid.New(), FromSeq: 1, ThroughSeq: 3,
		SchemaVersion: conversationmemory.CurrentSchemaVersion,
		Provenance: conversationmemory.SummaryProvenance{
			ModelProfile: "conversation-memory", ModelProvider: "dashscope",
			ModelID: "qwen3.6-flash", PromptVersion: "conversation-memory-v1",
		},
		Payload: conversationmemory.Payload{
			ConversationGoal: &conversationmemory.Entry{
				EntryID: "goal", Content: "完成上下文治理", SourceMessageSeqs: []int64{1},
				Status: conversationmemory.EntryStatusActive,
			},
			Facts: []conversationmemory.Entry{}, Decisions: []conversationmemory.Entry{},
			Corrections: []conversationmemory.Entry{}, EvidenceReferences: []conversationmemory.ReferenceEntry{},
			OpenQuestions: []conversationmemory.Entry{}, Todos: []conversationmemory.Entry{},
			TaskReferences: []conversationmemory.ReferenceEntry{}, ReportReferences: []conversationmemory.ReferenceEntry{},
		},
		Usage: conversationmemory.SummaryUsage{
			PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120,
		},
		CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatalf("NewCandidateSnapshot() error = %v", err)
	}
	activatedAt := createdAt.Add(time.Minute)
	snapshot := conversationmemory.Snapshot{CandidateSnapshot: candidate, Version: 2}
	snapshot.Status = conversationmemory.SnapshotStatusActive
	snapshot.ActivatedAt = &activatedAt
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("snapshot fixture Validate() error = %v", err)
	}
	return snapshot
}
