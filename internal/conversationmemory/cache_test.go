package conversationmemory_test

import (
	"context"
	"errors"
	"testing"

	"github.com/chitandabb/GoAgent/internal/conversationmemory"

	"github.com/google/uuid"
)

type snapshotCacheStub struct {
	loaded      conversationmemory.Snapshot
	loadErr     error
	storeErr    error
	deleteErr   error
	loadCalls   int
	storeCalls  int
	deleteCalls int
	stored      conversationmemory.Snapshot
	deleted     uuid.UUID
}

func (s *snapshotCacheStub) Load(
	context.Context,
	uuid.UUID,
	uuid.UUID,
) (conversationmemory.Snapshot, error) {
	s.loadCalls++
	return s.loaded, s.loadErr
}

func (s *snapshotCacheStub) Store(_ context.Context, snapshot conversationmemory.Snapshot) error {
	s.storeCalls++
	s.stored = snapshot
	return s.storeErr
}

func (s *snapshotCacheStub) DeleteConversation(_ context.Context, conversationID uuid.UUID) error {
	s.deleteCalls++
	s.deleted = conversationID
	return s.deleteErr
}

type cacheAwareMemoryRepositoryStub struct {
	*activationMemoryRepositoryStub
	identity      conversationmemory.ActiveSnapshotIdentity
	identityErr   error
	identityCalls int
	activeCalls   int
}

func (s *cacheAwareMemoryRepositoryStub) ActiveIdentity(
	context.Context,
	uuid.UUID,
) (conversationmemory.ActiveSnapshotIdentity, error) {
	s.identityCalls++
	return s.identity, s.identityErr
}

func (s *cacheAwareMemoryRepositoryStub) Active(
	ctx context.Context,
	conversationID uuid.UUID,
) (*conversationmemory.Snapshot, error) {
	s.activeCalls++
	return s.activationMemoryRepositoryStub.Active(ctx, conversationID)
}

type cacheObserverStub struct {
	observations []conversationmemory.CacheObservation
}

func (s *cacheObserverStub) Observe(_ context.Context, observation conversationmemory.CacheObservation) {
	s.observations = append(s.observations, observation)
}

func TestConversationMemoryActiveUsesIdentityBoundCacheHit(t *testing.T) {
	conversationID := uuid.New()
	snapshot := activeSnapshotFixture(t, conversationID, 1, 4, nil)
	repository := cacheAwareRepository(snapshot)
	cache := &snapshotCacheStub{loaded: snapshot}
	observer := &cacheObserverStub{}
	service := newCacheAwareMemoryService(t, repository, cache, true, observer)

	active, err := service.Active(context.Background(), conversationID)
	if err != nil {
		t.Fatalf("Active() error = %v", err)
	}
	if active == nil || active.ID != snapshot.ID || active.PayloadSHA256 != snapshot.PayloadSHA256 {
		t.Fatalf("Active() = %+v, want cached snapshot %s", active, snapshot.ID)
	}
	if repository.identityCalls != 1 || repository.activeCalls != 0 || cache.loadCalls != 1 || cache.storeCalls != 0 {
		t.Fatalf("calls identity/active/load/store = %d/%d/%d/%d, want 1/0/1/0",
			repository.identityCalls, repository.activeCalls, cache.loadCalls, cache.storeCalls)
	}
	assertCacheObservation(t, observer, conversationmemory.CacheStatusHit, "")
}

func TestConversationMemoryActiveFallsBackAndFillsOnCacheMiss(t *testing.T) {
	conversationID := uuid.New()
	snapshot := activeSnapshotFixture(t, conversationID, 1, 4, nil)
	repository := cacheAwareRepository(snapshot)
	cache := &snapshotCacheStub{loadErr: conversationmemory.ErrSnapshotCacheMiss}
	observer := &cacheObserverStub{}
	service := newCacheAwareMemoryService(t, repository, cache, true, observer)

	active, err := service.Active(context.Background(), conversationID)
	if err != nil {
		t.Fatalf("Active() error = %v", err)
	}
	if active == nil || active.ID != snapshot.ID || cache.stored.ID != snapshot.ID {
		t.Fatalf("Active()/stored = %+v/%+v, want PostgreSQL snapshot %s", active, cache.stored, snapshot.ID)
	}
	if repository.identityCalls != 1 || repository.activeCalls != 1 || cache.storeCalls != 1 {
		t.Fatalf("calls identity/active/store = %d/%d/%d, want 1/1/1",
			repository.identityCalls, repository.activeCalls, cache.storeCalls)
	}
	assertCacheObservation(t, observer, conversationmemory.CacheStatusMiss, "")
}

func TestConversationMemoryActiveRejectsStaleCachedIdentity(t *testing.T) {
	conversationID := uuid.New()
	stale := activeSnapshotFixture(t, conversationID, 1, 2, nil)
	current := activeSnapshotFixture(t, conversationID, 1, 4, &stale.ID)
	repository := cacheAwareRepository(current)
	cache := &snapshotCacheStub{loaded: stale}
	observer := &cacheObserverStub{}
	service := newCacheAwareMemoryService(t, repository, cache, true, observer)

	active, err := service.Active(context.Background(), conversationID)
	if err != nil {
		t.Fatalf("Active() error = %v", err)
	}
	if active == nil || active.ID != current.ID || cache.stored.ID != current.ID {
		t.Fatalf("Active()/stored = %+v/%+v, want current snapshot %s", active, cache.stored, current.ID)
	}
	assertCacheObservation(t, observer, conversationmemory.CacheStatusDegraded, conversationmemory.CacheReasonStale)
}

func TestConversationMemoryActiveFallsBackOnCacheTimeout(t *testing.T) {
	conversationID := uuid.New()
	snapshot := activeSnapshotFixture(t, conversationID, 1, 4, nil)
	repository := cacheAwareRepository(snapshot)
	cache := &snapshotCacheStub{loadErr: context.DeadlineExceeded}
	observer := &cacheObserverStub{}
	service := newCacheAwareMemoryService(t, repository, cache, true, observer)

	active, err := service.Active(context.Background(), conversationID)
	if err != nil || active == nil || active.ID != snapshot.ID {
		t.Fatalf("Active() = %+v, %v", active, err)
	}
	assertCacheObservation(t, observer, conversationmemory.CacheStatusDegraded, conversationmemory.CacheReasonTimeout)
}

func TestConversationMemoryActiveFallsBackOnInvalidCachedPayload(t *testing.T) {
	conversationID := uuid.New()
	snapshot := activeSnapshotFixture(t, conversationID, 1, 4, nil)
	invalid := snapshot
	invalid.PayloadSHA256 = "invalid"
	repository := cacheAwareRepository(snapshot)
	cache := &snapshotCacheStub{loaded: invalid}
	observer := &cacheObserverStub{}
	service := newCacheAwareMemoryService(t, repository, cache, true, observer)

	active, err := service.Active(context.Background(), conversationID)
	if err != nil || active == nil || active.ID != snapshot.ID {
		t.Fatalf("Active() = %+v, %v", active, err)
	}
	assertCacheObservation(t, observer, conversationmemory.CacheStatusDegraded, conversationmemory.CacheReasonInvalid)
}

func TestConversationMemoryActiveReturnsSourceWhenCacheFillFails(t *testing.T) {
	conversationID := uuid.New()
	snapshot := activeSnapshotFixture(t, conversationID, 1, 4, nil)
	repository := cacheAwareRepository(snapshot)
	cache := &snapshotCacheStub{
		loadErr:  conversationmemory.ErrSnapshotCacheMiss,
		storeErr: errors.New("redis write failed"),
	}
	observer := &cacheObserverStub{}
	service := newCacheAwareMemoryService(t, repository, cache, true, observer)

	active, err := service.Active(context.Background(), conversationID)
	if err != nil || active == nil || active.ID != snapshot.ID {
		t.Fatalf("Active() = %+v, %v", active, err)
	}
	assertCacheObservation(t, observer, conversationmemory.CacheStatusDegraded, conversationmemory.CacheReasonWriteFailed)
}

func TestConversationMemoryActiveReturnsSourceWhenExpectedCacheIsUnavailable(t *testing.T) {
	conversationID := uuid.New()
	snapshot := activeSnapshotFixture(t, conversationID, 1, 4, nil)
	repository := cacheAwareRepository(snapshot)
	observer := &cacheObserverStub{}
	service := newCacheAwareMemoryService(t, repository, nil, true, observer)

	active, err := service.Active(context.Background(), conversationID)
	if err != nil || active == nil || active.ID != snapshot.ID {
		t.Fatalf("Active() = %+v, %v", active, err)
	}
	if repository.identityCalls != 0 || repository.activeCalls != 1 {
		t.Fatalf("calls identity/active = %d/%d, want 0/1", repository.identityCalls, repository.activeCalls)
	}
	assertCacheObservation(t, observer, conversationmemory.CacheStatusDegraded, conversationmemory.CacheReasonUnavailable)
}

func TestConversationMemoryCacheDeletionIsBestEffort(t *testing.T) {
	conversationID := uuid.New()
	snapshot := activeSnapshotFixture(t, conversationID, 1, 4, nil)
	repository := cacheAwareRepository(snapshot)
	cache := &snapshotCacheStub{deleteErr: errors.New("redis delete failed")}
	observer := &cacheObserverStub{}
	service := newCacheAwareMemoryService(t, repository, cache, true, observer)

	service.DeleteConversationCache(context.Background(), conversationID)

	if cache.deleteCalls != 1 || cache.deleted != conversationID {
		t.Fatalf("DeleteConversation() calls/id = %d/%s", cache.deleteCalls, cache.deleted)
	}
	assertCacheObservation(t, observer, conversationmemory.CacheStatusDegraded, conversationmemory.CacheReasonDeleteFailed)
}

func TestCacheObservationRejectsUnknownAndImpossibleEnumCombinations(t *testing.T) {
	conversationID := uuid.New()
	snapshotID := uuid.New()
	valid := []conversationmemory.CacheObservation{
		{Operation: conversationmemory.CacheOperationActiveLoad, Status: conversationmemory.CacheStatusHit,
			ConversationID: conversationID, SnapshotID: snapshotID},
		{Operation: conversationmemory.CacheOperationActiveLoad, Status: conversationmemory.CacheStatusMiss,
			ConversationID: conversationID, SnapshotID: snapshotID},
		{Operation: conversationmemory.CacheOperationActiveLoad, Status: conversationmemory.CacheStatusDegraded,
			Reason: conversationmemory.CacheReasonTimeout, ConversationID: conversationID, SnapshotID: snapshotID},
		{Operation: conversationmemory.CacheOperationDelete, Status: conversationmemory.CacheStatusSucceeded,
			ConversationID: conversationID},
		{Operation: conversationmemory.CacheOperationDelete, Status: conversationmemory.CacheStatusDegraded,
			Reason: conversationmemory.CacheReasonDeleteFailed, ConversationID: conversationID},
	}
	for _, observation := range valid {
		if err := observation.Validate(); err != nil {
			t.Fatalf("valid observation %+v rejected: %v", observation, err)
		}
	}

	invalid := []conversationmemory.CacheObservation{
		{Operation: "unknown", Status: conversationmemory.CacheStatusHit,
			ConversationID: conversationID, SnapshotID: snapshotID},
		{Operation: conversationmemory.CacheOperationActiveLoad, Status: "unknown",
			ConversationID: conversationID, SnapshotID: snapshotID},
		{Operation: conversationmemory.CacheOperationActiveLoad, Status: conversationmemory.CacheStatusSucceeded,
			ConversationID: conversationID, SnapshotID: snapshotID},
		{Operation: conversationmemory.CacheOperationDelete, Status: conversationmemory.CacheStatusMiss,
			ConversationID: conversationID},
		{Operation: conversationmemory.CacheOperationDelete, Status: conversationmemory.CacheStatusDegraded,
			Reason: conversationmemory.CacheReasonWriteFailed, ConversationID: conversationID},
		{Operation: conversationmemory.CacheOperationActiveLoad, Status: conversationmemory.CacheStatusHit,
			Reason: conversationmemory.CacheReasonTimeout, ConversationID: conversationID, SnapshotID: snapshotID},
	}
	for _, observation := range invalid {
		if err := observation.Validate(); err == nil {
			t.Fatalf("invalid observation accepted: %+v", observation)
		}
	}
}

func cacheAwareRepository(snapshot conversationmemory.Snapshot) *cacheAwareMemoryRepositoryStub {
	repository := &cacheAwareMemoryRepositoryStub{
		activationMemoryRepositoryStub: &activationMemoryRepositoryStub{
			memoryRepositoryStub: &memoryRepositoryStub{latest: &snapshot},
			active:               &snapshot,
		},
		identity: conversationmemory.ActiveSnapshotIdentity{
			ConversationID: snapshot.ConversationID,
			SnapshotID:     snapshot.ID,
			Version:        snapshot.Version,
			PayloadSHA256:  snapshot.PayloadSHA256,
		},
	}
	return repository
}

func newCacheAwareMemoryService(
	t *testing.T,
	repository conversationmemory.ActivationRepository,
	cache conversationmemory.SnapshotCache,
	cacheExpected bool,
	observer conversationmemory.CacheObserver,
) *conversationmemory.Service {
	t.Helper()
	service, err := conversationmemory.NewService(conversationmemory.ServiceConfig{
		Repository: repository,
		Compactor: compactorFunc(func(context.Context, conversationmemory.CompactionInput) (conversationmemory.CompactionOutput, error) {
			return conversationmemory.CompactionOutput{}, errors.New("unexpected compaction")
		}),
		SchemaVersion:   conversationmemory.CurrentSchemaVersion,
		MaxPayloadBytes: 64 * 1024,
		Provenance: conversationmemory.SummaryProvenance{
			ModelProfile: "conversation-memory", ModelProvider: "dashscope",
			ModelID: "qwen3.6-flash", PromptVersion: "conversation-memory-v1",
		},
		MaxAttempts: 1, Cache: cache, CacheExpected: cacheExpected, CacheObserver: observer,
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

func assertCacheObservation(
	t *testing.T,
	observer *cacheObserverStub,
	status conversationmemory.CacheStatus,
	reason conversationmemory.CacheReason,
) {
	t.Helper()
	if len(observer.observations) != 1 {
		t.Fatalf("observations = %+v, want exactly one", observer.observations)
	}
	observation := observer.observations[0]
	if observation.Status != status || observation.Reason != reason || observation.Duration < 0 {
		t.Fatalf("observation = %+v, want status/reason %s/%s", observation, status, reason)
	}
}
