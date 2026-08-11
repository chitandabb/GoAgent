//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/conversationmemory"

	"github.com/google/uuid"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestConversationMemoryRepositoryAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("MESGUARD_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MESGUARD_TEST_POSTGRES_DSN is not configured")
	}
	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test postgres: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get test postgres sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		t.Fatalf("begin fixture transaction: %v", tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })

	userID := uuid.New()
	if err := tx.Exec(`
INSERT INTO users (id, username, display_name, password_hash, role, status, must_change_password)
VALUES (?, ?, 'Memory Owner', 'integration-hash', 'analyst', 'active', false)`,
		userID, "memory_owner_"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	conversationRepository := NewConversationRepository(tx)
	current, err := conversationRepository.Create(ctx, userID, conversation.CreateInput{Title: "结构化记忆"}, time.Now().UTC())
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	repository := NewConversationMemoryRepository(tx)
	firstCandidate := integrationMemoryCandidate(t, current.ID, nil, 1, 3)
	first, err := repository.Save(ctx, firstCandidate)
	if err != nil {
		t.Fatalf("Save(first): %v", err)
	}
	if first.Version != 1 || first.PayloadSHA256 != firstCandidate.PayloadSHA256 {
		t.Fatalf("first snapshot = %+v", first)
	}
	loaded, err := repository.Get(ctx, first.ID)
	if err != nil {
		t.Fatalf("Get(first): %v", err)
	}
	if loaded.ID != first.ID || loaded.Version != 1 || loaded.Payload.ConversationGoal == nil ||
		loaded.Payload.ConversationGoal.Content != "完成上下文治理" {
		t.Fatalf("loaded snapshot = %+v", loaded)
	}

	mutatedPayload := `{"conversationGoal":null,"facts":[],"decisions":[],"corrections":[],"evidenceReferences":[],"openQuestions":[],"todos":[],"taskReferences":[],"reportReferences":[]}`
	updateErr := tx.Transaction(func(savepoint *gorm.DB) error {
		return savepoint.Exec(`UPDATE conversation_memory_snapshots SET payload = ?::jsonb WHERE id = ?`, mutatedPayload, first.ID).Error
	})
	if updateErr == nil {
		t.Fatal("immutable snapshot payload update unexpectedly succeeded")
	}

	competingCandidate := integrationMemoryCandidate(t, current.ID, nil, 1, 4)
	competing, err := repository.Save(ctx, competingCandidate)
	if err != nil {
		t.Fatalf("Save(competing root): %v", err)
	}
	active, err := repository.Activate(ctx, conversationmemory.ActivationRequest{
		ConversationID: current.ID, CandidateSnapshotID: first.ID, ActivatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Activate(first): %v", err)
	}
	if active.ID != first.ID || active.Status != conversationmemory.SnapshotStatusActive || active.ActivatedAt == nil {
		t.Fatalf("active first snapshot = %+v", active)
	}
	if _, err := repository.Activate(ctx, conversationmemory.ActivationRequest{
		ConversationID: current.ID, CandidateSnapshotID: competing.ID, ActivatedAt: time.Now().UTC(),
	}); !errors.Is(err, conversationmemory.ErrSnapshotActivationConflict) {
		t.Fatalf("Activate(competing root) error = %v, want ErrSnapshotActivationConflict", err)
	}
	currentActive, err := repository.Active(ctx, current.ID)
	if err != nil || currentActive.ID != first.ID {
		t.Fatalf("Active() after root race = %+v, %v", currentActive, err)
	}
	identity, err := repository.ActiveIdentity(ctx, current.ID)
	if err != nil || identity.ConversationID != current.ID || identity.SnapshotID != first.ID ||
		identity.Version != first.Version || identity.PayloadSHA256 != first.PayloadSHA256 {
		t.Fatalf("ActiveIdentity() = %+v, %v", identity, err)
	}

	secondCandidate := integrationMemoryCandidate(t, current.ID, &first.ID, 1, 5)
	second, err := repository.Save(ctx, secondCandidate)
	if err != nil {
		t.Fatalf("Save(second): %v", err)
	}
	second, err = repository.Activate(ctx, conversationmemory.ActivationRequest{
		ConversationID: current.ID, CandidateSnapshotID: second.ID,
		ExpectedActiveSnapshotID: &first.ID, ActivatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Activate(second): %v", err)
	}
	latest, err := repository.Latest(ctx, current.ID)
	if err != nil {
		t.Fatalf("Latest(): %v", err)
	}
	if second.Version != 3 || latest.ID != second.ID || latest.SupersedesSnapshotID == nil ||
		*latest.SupersedesSnapshotID != first.ID || latest.FromSeq != 1 || latest.ThroughSeq != 5 {
		t.Fatalf("second/latest snapshots = %+v / %+v", second, latest)
	}
	staleCandidate := integrationMemoryCandidate(t, current.ID, &first.ID, 1, 7)
	stale, err := repository.Save(ctx, staleCandidate)
	if err != nil {
		t.Fatalf("Save(stale candidate for audit): %v", err)
	}
	if _, err := repository.Activate(ctx, conversationmemory.ActivationRequest{
		ConversationID: current.ID, CandidateSnapshotID: stale.ID,
		ExpectedActiveSnapshotID: &first.ID, ActivatedAt: time.Now().UTC(),
	}); !errors.Is(err, conversationmemory.ErrSnapshotActivationConflict) {
		t.Fatalf("Activate(stale candidate) error = %v, want ErrSnapshotActivationConflict", err)
	}
	currentActive, err = repository.Active(ctx, current.ID)
	if err != nil || currentActive.ID != second.ID {
		t.Fatalf("Active() after stale race = %+v, %v", currentActive, err)
	}
	identity, err = repository.ActiveIdentity(ctx, current.ID)
	if err != nil || identity.SnapshotID != second.ID || identity.Version != second.Version ||
		identity.PayloadSHA256 != second.PayloadSHA256 {
		t.Fatalf("ActiveIdentity() after replacement = %+v, %v", identity, err)
	}

	otherConversation, err := conversationRepository.Create(ctx, userID, conversation.CreateInput{Title: "其他会话"}, time.Now().UTC())
	if err != nil {
		t.Fatalf("create other conversation: %v", err)
	}
	invalidPredecessor := integrationMemoryCandidate(t, otherConversation.ID, &first.ID, 1, 7)
	if _, err := repository.Save(ctx, invalidPredecessor); !errors.Is(err, conversationmemory.ErrInvalidSnapshot) {
		t.Fatalf("Save(cross-conversation predecessor) error = %v, want ErrInvalidSnapshot", err)
	}

	active, err = repository.Get(ctx, second.ID)
	if err != nil {
		t.Fatalf("Get(active second): %v", err)
	}
	if active.Status != conversationmemory.SnapshotStatusActive || active.ActivatedAt == nil ||
		active.ActivatedAt.Before(active.CreatedAt) {
		t.Fatalf("active snapshot lifecycle = status %q, activatedAt %v", active.Status, active.ActivatedAt)
	}

	if err := tx.Exec(`DELETE FROM conversations WHERE id = ?`, current.ID).Error; err != nil {
		t.Fatalf("delete conversation: %v", err)
	}
	if _, err := repository.Get(ctx, second.ID); !errors.Is(err, conversationmemory.ErrSnapshotNotFound) {
		t.Fatalf("Get(after conversation delete) error = %v, want ErrSnapshotNotFound", err)
	}
}

func TestConversationMemoryRepositoryConcurrentCASAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("MESGUARD_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MESGUARD_TEST_POSTGRES_DSN is not configured")
	}
	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test postgres: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get test postgres sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	userID := uuid.New()
	if err := db.WithContext(ctx).Exec(`
INSERT INTO users (id, username, display_name, password_hash, role, status, must_change_password)
VALUES (?, ?, 'Memory Race Owner', 'integration-hash', 'analyst', 'active', false)`,
		userID, "memory_race_"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("insert concurrent CAS user: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_ = db.WithContext(cleanupCtx).Exec(`DELETE FROM users WHERE id = ?`, userID).Error
	})
	conversationRepository := NewConversationRepository(db)
	current, err := conversationRepository.Create(
		ctx, userID, conversation.CreateInput{Title: "并发 CAS"}, time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("create concurrent CAS conversation: %v", err)
	}
	repository := NewConversationMemoryRepository(db)

	roots := concurrentSaveMemoryCandidates(t, ctx, repository, []conversationmemory.CandidateSnapshot{
		integrationMemoryCandidate(t, current.ID, nil, 1, 3),
		integrationMemoryCandidate(t, current.ID, nil, 1, 4),
	})
	versions := []int64{roots[0].Version, roots[1].Version}
	slices.Sort(versions)
	if !slices.Equal(versions, []int64{1, 2}) {
		t.Fatalf("concurrent root versions = %v, want [1 2]", versions)
	}
	rootErrors := concurrentActivateMemoryCandidates(ctx, repository, current.ID, nil, roots)
	assertOneMemoryActivationWinner(t, rootErrors)
	rootActive, err := repository.Active(ctx, current.ID)
	if err != nil {
		t.Fatalf("Active() after concurrent root activation: %v", err)
	}
	assertSingleActiveMemorySnapshot(t, db, current.ID, rootActive.ID)

	successors := concurrentSaveMemoryCandidates(t, ctx, repository, []conversationmemory.CandidateSnapshot{
		integrationMemoryCandidate(t, current.ID, &rootActive.ID, 1, rootActive.ThroughSeq+1),
		integrationMemoryCandidate(t, current.ID, &rootActive.ID, 1, rootActive.ThroughSeq+2),
	})
	versions = []int64{successors[0].Version, successors[1].Version}
	slices.Sort(versions)
	if !slices.Equal(versions, []int64{3, 4}) {
		t.Fatalf("concurrent successor versions = %v, want [3 4]", versions)
	}
	successorErrors := concurrentActivateMemoryCandidates(ctx, repository, current.ID, &rootActive.ID, successors)
	assertOneMemoryActivationWinner(t, successorErrors)
	successorActive, err := repository.Active(ctx, current.ID)
	if err != nil {
		t.Fatalf("Active() after concurrent successor activation: %v", err)
	}
	if successorActive.ID == rootActive.ID || successorActive.SupersedesSnapshotID == nil ||
		*successorActive.SupersedesSnapshotID != rootActive.ID {
		t.Fatalf("concurrent successor Active = %+v", successorActive)
	}
	assertSingleActiveMemorySnapshot(t, db, current.ID, successorActive.ID)
}

type concurrentMemorySaveResult struct {
	snapshot conversationmemory.Snapshot
	err      error
}

func concurrentSaveMemoryCandidates(
	t *testing.T,
	ctx context.Context,
	repository *ConversationMemoryRepository,
	candidates []conversationmemory.CandidateSnapshot,
) []conversationmemory.Snapshot {
	t.Helper()
	start := make(chan struct{})
	results := make(chan concurrentMemorySaveResult, len(candidates))
	for _, candidate := range candidates {
		candidate := candidate
		go func() {
			<-start
			snapshot, err := repository.Save(ctx, candidate)
			results <- concurrentMemorySaveResult{snapshot: snapshot, err: err}
		}()
	}
	close(start)
	snapshots := make([]conversationmemory.Snapshot, 0, len(candidates))
	for range candidates {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent conversation memory Save failed: %v", result.err)
		}
		snapshots = append(snapshots, result.snapshot)
	}
	return snapshots
}

func concurrentActivateMemoryCandidates(
	ctx context.Context,
	repository *ConversationMemoryRepository,
	conversationID uuid.UUID,
	expectedActive *uuid.UUID,
	candidates []conversationmemory.Snapshot,
) []error {
	start := make(chan struct{})
	results := make(chan error, len(candidates))
	for _, candidate := range candidates {
		candidate := candidate
		go func() {
			<-start
			_, err := repository.Activate(ctx, conversationmemory.ActivationRequest{
				ConversationID: conversationID, CandidateSnapshotID: candidate.ID,
				ExpectedActiveSnapshotID: expectedActive, ActivatedAt: time.Now().UTC(),
			})
			results <- err
		}()
	}
	close(start)
	errors := make([]error, 0, len(candidates))
	for range candidates {
		errors = append(errors, <-results)
	}
	return errors
}

func assertOneMemoryActivationWinner(t *testing.T, activationErrors []error) {
	t.Helper()
	winners, conflicts := 0, 0
	for _, err := range activationErrors {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, conversationmemory.ErrSnapshotActivationConflict):
			conflicts++
		default:
			t.Fatalf("concurrent activation error = %v", err)
		}
	}
	if winners != 1 || conflicts != len(activationErrors)-1 {
		t.Fatalf("concurrent activation winners/conflicts = %d/%d", winners, conflicts)
	}
}

func assertSingleActiveMemorySnapshot(t *testing.T, db *gorm.DB, conversationID, activeID uuid.UUID) {
	t.Helper()
	var facts struct {
		Count int64 `gorm:"column:count"`
	}
	if err := db.Raw(`
SELECT COUNT(*) AS count
FROM conversation_memory_snapshots
WHERE conversation_id = ? AND status = 'active'`, conversationID).Scan(&facts).Error; err != nil {
		t.Fatalf("query Active snapshot count: %v", err)
	}
	if facts.Count != 1 {
		t.Fatalf("Active snapshot facts = %+v, want count=1 id=%s", facts, activeID)
	}
}

func integrationMemoryCandidate(
	t *testing.T,
	conversationID uuid.UUID,
	predecessor *uuid.UUID,
	fromSeq, throughSeq int64,
) conversationmemory.CandidateSnapshot {
	t.Helper()
	payload := conversationmemory.Payload{
		ConversationGoal: &conversationmemory.Entry{
			EntryID: "goal_context", Content: "完成上下文治理", SourceMessageSeqs: []int64{1},
			Status: conversationmemory.EntryStatusActive,
		},
		Facts: []conversationmemory.Entry{}, Decisions: []conversationmemory.Entry{},
		Corrections: []conversationmemory.Entry{}, EvidenceReferences: []conversationmemory.ReferenceEntry{},
		OpenQuestions: []conversationmemory.Entry{}, Todos: []conversationmemory.Entry{},
		TaskReferences: []conversationmemory.ReferenceEntry{}, ReportReferences: []conversationmemory.ReferenceEntry{},
	}
	candidate, err := conversationmemory.NewCandidateSnapshot(conversationmemory.NewCandidateSnapshotInput{
		ID: uuid.New(), ConversationID: conversationID, SupersedesSnapshotID: predecessor,
		FromSeq: fromSeq, ThroughSeq: throughSeq, SchemaVersion: conversationmemory.CurrentSchemaVersion,
		Provenance: conversationmemory.SummaryProvenance{
			ModelProfile: "conversation-memory", ModelProvider: "dashscope",
			ModelID: "qwen3.6-flash", PromptVersion: "conversation-memory-v1",
		},
		Payload:   payload,
		Usage:     conversationmemory.SummaryUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120, CachedTokens: 10},
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	})
	if err != nil {
		t.Fatalf("NewCandidateSnapshot(): %v", err)
	}
	return candidate
}
