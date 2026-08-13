//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/conversationmemory"
	"github.com/chitandabb/GoAgent/internal/conversationmemoryworker"

	"github.com/google/uuid"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestConversationMemoryJobRepositoryLeaseFencingCASAndRetryAgainstPostgres(t *testing.T) {
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
	userID, conversationID, sourceTurnID, throughSeq := createConversationMemoryJobFixture(t, ctx, db)
	t.Cleanup(func() {
		cleanup := db.WithContext(context.Background())
		_ = cleanup.Exec("DELETE FROM outbox_events WHERE correlation_id = ?", sourceTurnID).Error
		_ = cleanup.Exec("DELETE FROM conversations WHERE id = ?", conversationID).Error
		_ = cleanup.Exec("DELETE FROM users WHERE id = ?", userID).Error
	})

	repository := NewConversationMemoryJobRepository(db)
	memoryRepository := NewConversationMemoryRepository(db)
	baseTime := time.Now().UTC().Truncate(time.Microsecond).Add(time.Second)

	staleJobID := insertIntegrationMemoryJob(t, db, conversationID, sourceTurnID, throughSeq, 3, baseTime)
	firstClaim, err := repository.Claim(
		ctx, staleJobID, conversationID, "memory-worker-stale", baseTime, baseTime.Add(100*time.Millisecond),
	)
	if err != nil || firstClaim.Disposition != conversationmemoryworker.ClaimAcquired || firstClaim.Lease == nil {
		t.Fatalf("first Claim() = %+v, %v", firstClaim, err)
	}
	renewedUntil := baseTime.Add(200 * time.Millisecond)
	renewed, err := repository.Renew(
		ctx, *firstClaim.Lease, baseTime.Add(20*time.Millisecond), renewedUntil,
	)
	if err != nil || !renewed {
		t.Fatalf("Renew(first lease) = %v, %v", renewed, err)
	}
	held, err := repository.Claim(
		ctx, staleJobID, conversationID, "memory-worker-held", baseTime.Add(10*time.Millisecond), baseTime.Add(time.Second),
	)
	if err != nil || held.Disposition != conversationmemoryworker.ClaimLeaseHeld {
		t.Fatalf("held Claim() = %+v, %v", held, err)
	}
	staleCandidate, err := memoryRepository.Save(ctx, integrationMemoryCandidate(t, conversationID, nil, 1, throughSeq))
	if err != nil {
		t.Fatalf("Save(stale candidate): %v", err)
	}
	reclaimAt := renewedUntil.Add(time.Millisecond)
	reclaimed, err := repository.Claim(
		ctx, staleJobID, conversationID, "memory-worker-winner", reclaimAt, reclaimAt.Add(time.Second),
	)
	if err != nil || reclaimed.Disposition != conversationmemoryworker.ClaimAcquired || reclaimed.Lease == nil ||
		reclaimed.Lease.FencingToken <= firstClaim.Lease.FencingToken {
		t.Fatalf("reclaimed Claim() = %+v, %v", reclaimed, err)
	}
	staleRenewed, err := repository.Renew(
		ctx, *firstClaim.Lease, reclaimAt.Add(time.Millisecond), reclaimAt.Add(time.Second),
	)
	if err != nil || staleRenewed {
		t.Fatalf("Renew(stale fencing token) = %v, %v", staleRenewed, err)
	}
	if _, err := repository.Complete(ctx, *firstClaim.Lease, conversationmemoryworker.ExecutionResult{
		CurrentSnapshotID: staleCandidate.ID, ThroughSeq: throughSeq,
	}, reclaimAt.Add(time.Millisecond)); !errors.Is(err, conversationmemoryworker.ErrLeaseLost) {
		t.Fatalf("stale Complete() error = %v, want ErrLeaseLost", err)
	}
	winnerCandidate, err := memoryRepository.Save(ctx, integrationMemoryCandidate(t, conversationID, nil, 1, throughSeq))
	if err != nil {
		t.Fatalf("Save(winner candidate): %v", err)
	}
	winnerCurrent, err := memoryRepository.Activate(ctx, conversationmemory.ActivationRequest{
		ConversationID: conversationID, CandidateSnapshotID: winnerCandidate.ID, ActivatedAt: reclaimAt.Add(time.Millisecond),
	})
	if err != nil {
		t.Fatalf("Activate(winner): %v", err)
	}
	completed, err := repository.Complete(ctx, *reclaimed.Lease, conversationmemoryworker.ExecutionResult{
		CurrentSnapshotID: winnerCurrent.ID, ThroughSeq: throughSeq,
	}, reclaimAt.Add(2*time.Millisecond))
	if err != nil || !completed.Committed || completed.ActivationResult != conversationmemoryworker.ActivationAlreadyCurrent {
		t.Fatalf("winner Complete() = %+v, %v", completed, err)
	}
	terminal, err := repository.Claim(
		ctx, staleJobID, conversationID, "memory-worker-redelivery",
		reclaimAt.Add(3*time.Millisecond), reclaimAt.Add(time.Second),
	)
	if err != nil || terminal.Disposition != conversationmemoryworker.ClaimTerminal ||
		terminal.Status != conversationmemoryworker.JobSucceeded {
		t.Fatalf("Claim(succeeded redelivery) = %+v, %v", terminal, err)
	}
	retained, err := memoryRepository.Get(ctx, staleCandidate.ID)
	if err != nil || retained.Status != conversationmemory.SnapshotStatusCandidate {
		t.Fatalf("stale candidate audit status = %+v, %v", retained, err)
	}

	if err := db.WithContext(ctx).Exec(`
INSERT INTO conversation_messages (id, conversation_id, seq, role, content, content_schema_version, created_at)
SELECT gen_random_uuid(), ?, sequence,
       CASE WHEN sequence % 2 = 1 THEN 'user' ELSE 'assistant' END,
       'memory-history-' || sequence, 1, ?
FROM generate_series(3, 6) AS sequence`, conversationID, reclaimAt.Add(time.Second)).Error; err != nil {
		t.Fatalf("append memory fixture messages: %v", err)
	}

	concurrentJobID := insertIntegrationMemoryJob(t, db, conversationID, sourceTurnID, 3, 3, reclaimAt.Add(time.Second))
	claimResults := make(chan conversationmemoryworker.ClaimResult, 2)
	claimErrors := make(chan error, 2)
	var claimGroup sync.WaitGroup
	for _, workerID := range []string{"memory-worker-a", "memory-worker-b"} {
		claimGroup.Add(1)
		go func(id string) {
			defer claimGroup.Done()
			claim, claimErr := repository.Claim(
				ctx, concurrentJobID, conversationID, id, reclaimAt.Add(2*time.Second), reclaimAt.Add(3*time.Second),
			)
			claimResults <- claim
			claimErrors <- claimErr
		}(workerID)
	}
	claimGroup.Wait()
	close(claimResults)
	close(claimErrors)
	for claimErr := range claimErrors {
		if claimErr != nil {
			t.Fatalf("concurrent Claim() error = %v", claimErr)
		}
	}
	acquired, leaseHeld := 0, 0
	var concurrentLease *conversationmemoryworker.Lease
	for claim := range claimResults {
		switch claim.Disposition {
		case conversationmemoryworker.ClaimAcquired:
			acquired++
			concurrentLease = claim.Lease
		case conversationmemoryworker.ClaimLeaseHeld:
			leaseHeld++
		}
	}
	if acquired != 1 || leaseHeld != 1 || concurrentLease == nil {
		t.Fatalf("concurrent Claim dispositions acquired=%d held=%d", acquired, leaseHeld)
	}
	if failed, err := repository.Fail(ctx, *concurrentLease, "integration_cleanup", "claim race verified", reclaimAt.Add(2*time.Second+time.Millisecond)); err != nil || !failed {
		t.Fatalf("Fail(concurrent winner) = %v, %v", failed, err)
	}

	active := winnerCurrent
	jobThroughFour := insertIntegrationMemoryJob(t, db, conversationID, sourceTurnID, 4, 3, reclaimAt.Add(3*time.Second))
	jobThroughSix := insertIntegrationMemoryJob(t, db, conversationID, sourceTurnID, 6, 3, reclaimAt.Add(3*time.Second))
	leaseFour := mustClaimMemoryJob(t, ctx, repository, jobThroughFour, conversationID, "memory-worker-four", reclaimAt.Add(4*time.Second))
	leaseSix := mustClaimMemoryJob(t, ctx, repository, jobThroughSix, conversationID, "memory-worker-six", reclaimAt.Add(4*time.Second))
	candidateFour, err := memoryRepository.Save(ctx, integrationMemoryCandidate(t, conversationID, &active.ID, 1, 4))
	if err != nil {
		t.Fatalf("Save(candidate four): %v", err)
	}
	candidateSix, err := memoryRepository.Save(ctx, integrationMemoryCandidate(t, conversationID, &active.ID, 1, 6))
	if err != nil {
		t.Fatalf("Save(candidate six): %v", err)
	}
	currentSix, err := memoryRepository.Activate(ctx, conversationmemory.ActivationRequest{
		ConversationID: conversationID, CandidateSnapshotID: candidateSix.ID,
		ExpectedActiveSnapshotID: &active.ID, ActivatedAt: reclaimAt.Add(4 * time.Second),
	})
	if err != nil {
		t.Fatalf("Activate(through six): %v", err)
	}
	if _, err := repository.Complete(ctx, leaseSix, conversationmemoryworker.ExecutionResult{
		CurrentSnapshotID: currentSix.ID, ThroughSeq: 6,
	}, reclaimAt.Add(4*time.Second+time.Millisecond)); err != nil {
		t.Fatalf("Complete(through six): %v", err)
	}
	casWinner, err := repository.Complete(ctx, leaseFour, conversationmemoryworker.ExecutionResult{
		CurrentSnapshotID: candidateFour.ID, ThroughSeq: 4,
	}, reclaimAt.Add(4*time.Second+2*time.Millisecond))
	if err != nil || casWinner.ActivationResult != conversationmemoryworker.ActivationAlreadyCurrent ||
		casWinner.ActiveSnapshotID != candidateSix.ID {
		t.Fatalf("Complete(overlap) = %+v, %v", casWinner, err)
	}

	retryJobID := insertIntegrationMemoryJob(t, db, conversationID, sourceTurnID, 5, 3, reclaimAt.Add(5*time.Second))
	retryLease := mustClaimMemoryJob(t, ctx, repository, retryJobID, conversationID, "memory-worker-retry", reclaimAt.Add(6*time.Second))
	retryAt := reclaimAt.Add(7 * time.Second)
	released, err := repository.ReleaseForRetry(
		ctx, retryLease, "provider_unavailable", "summary provider unavailable",
		reclaimAt.Add(6*time.Second+time.Millisecond), retryAt,
	)
	if err != nil || !released {
		t.Fatalf("ReleaseForRetry() = %v, %v", released, err)
	}
	var retryFacts struct {
		Status      string    `gorm:"column:status"`
		AvailableAt time.Time `gorm:"column:available_at"`
		OutboxCount int64     `gorm:"column:outbox_count"`
	}
	if err := db.WithContext(ctx).Raw(`
SELECT job.status, job.available_at,
       (SELECT COUNT(*) FROM outbox_events outbox
        WHERE outbox.aggregate_id = job.id AND outbox.event_type = 'conversation.memory.compact') AS outbox_count
FROM conversation_memory_jobs job WHERE job.id = ?`, retryJobID).Scan(&retryFacts).Error; err != nil {
		t.Fatalf("load retry facts: %v", err)
	}
	if retryFacts.Status != "retry_wait" || !retryFacts.AvailableAt.Equal(retryAt) || retryFacts.OutboxCount != 1 {
		t.Fatalf("retry facts = %+v", retryFacts)
	}
	delayed, err := repository.Claim(
		ctx, retryJobID, conversationID, "memory-worker-too-early", retryAt.Add(-time.Millisecond), retryAt.Add(time.Second),
	)
	if err != nil || delayed.Disposition != conversationmemoryworker.ClaimDelayed {
		t.Fatalf("delayed Claim() = %+v, %v", delayed, err)
	}
	secondRetryClaim := mustClaimMemoryJob(
		t, ctx, repository, retryJobID, conversationID, "memory-worker-retry-two", retryAt,
	)
	finalRetryAt := retryAt.Add(2 * time.Second)
	released, err = repository.ReleaseForRetry(
		ctx, secondRetryClaim, "provider_unavailable", "summary provider still unavailable",
		retryAt.Add(time.Millisecond), finalRetryAt,
	)
	if err != nil || !released {
		t.Fatalf("ReleaseForRetry(second attempt) = %v, %v", released, err)
	}
	finalLease := mustClaimMemoryJob(
		t, ctx, repository, retryJobID, conversationID, "memory-worker-retry-final", finalRetryAt,
	)
	if finalLease.AttemptCount != finalLease.MaxAttempts || finalLease.AttemptCount != 3 {
		t.Fatalf("final retry lease = %+v", finalLease)
	}
	failed, err := repository.Fail(
		ctx, finalLease, "memory_compaction_retry_exhausted", "summary provider unavailable after three attempts",
		finalRetryAt.Add(time.Millisecond),
	)
	if err != nil || !failed {
		t.Fatalf("Fail(final attempt) = %v, %v", failed, err)
	}
	failedRedelivery, err := repository.Claim(
		ctx, retryJobID, conversationID, "memory-worker-failed-redelivery",
		finalRetryAt.Add(2*time.Millisecond), finalRetryAt.Add(time.Second),
	)
	if err != nil || failedRedelivery.Disposition != conversationmemoryworker.ClaimTerminal ||
		failedRedelivery.Status != conversationmemoryworker.JobFailed {
		t.Fatalf("Claim(failed redelivery) = %+v, %v", failedRedelivery, err)
	}
	activeAfterFailure, err := memoryRepository.Active(ctx, conversationID)
	if err != nil || activeAfterFailure.ID != candidateSix.ID {
		t.Fatalf("Active() after retry exhaustion = %+v, %v", activeAfterFailure, err)
	}

	if err := db.WithContext(ctx).Exec("DELETE FROM outbox_events WHERE correlation_id = ?", sourceTurnID).Error; err != nil {
		t.Fatalf("delete memory fixture outbox: %v", err)
	}
	if err := db.WithContext(ctx).Exec("DELETE FROM conversations WHERE id = ?", conversationID).Error; err != nil {
		t.Fatalf("delete conversation with memory jobs: %v", err)
	}
	var remaining struct {
		Jobs      int64 `gorm:"column:jobs"`
		Snapshots int64 `gorm:"column:snapshots"`
	}
	if err := db.WithContext(ctx).Raw(`
SELECT
    (SELECT COUNT(*) FROM conversation_memory_jobs WHERE conversation_id = ?) AS jobs,
    (SELECT COUNT(*) FROM conversation_memory_snapshots WHERE conversation_id = ?) AS snapshots`,
		conversationID, conversationID).Scan(&remaining).Error; err != nil {
		t.Fatalf("count cascaded memory facts: %v", err)
	}
	if remaining.Jobs != 0 || remaining.Snapshots != 0 {
		t.Fatalf("remaining memory facts after conversation delete = %+v", remaining)
	}
}

func createConversationMemoryJobFixture(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
) (uuid.UUID, uuid.UUID, uuid.UUID, int64) {
	t.Helper()
	userID := uuid.New()
	if err := db.WithContext(ctx).Exec(`
INSERT INTO users (id, username, display_name, password_hash, role, status, must_change_password)
VALUES (?, ?, 'Memory Job Owner', 'integration-hash', 'admin', 'active', false)`,
		userID, "memory_job_owner_"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("insert memory job user: %v", err)
	}
	repository := NewConversationRepository(db)
	current, err := repository.Create(ctx, userID, conversation.CreateInput{Title: "异步记忆任务"}, time.Now().UTC())
	if err != nil {
		t.Fatalf("create memory job conversation: %v", err)
	}
	acceptedAt := time.Now().UTC().Truncate(time.Microsecond)
	accepted, err := repository.AcceptTurn(ctx, userID, conversation.BeginTurnInput{
		Message: conversation.AppendMessageInput{
			ConversationID: current.ID, Role: conversation.MessageRoleUser, Content: "请记录会话决策",
		},
		IdempotencyKey: uuid.NewString(), RequestFingerprint: strings.Repeat("a", 64),
		StartedAt: acceptedAt, ExecutionMode: conversation.TurnExecutionAsynchronous,
		CorrelationID: uuid.New(),
	})
	if err != nil {
		t.Fatalf("AcceptTurn(memory fixture): %v", err)
	}
	workerID := "conversation-worker-memory-fixture"
	if _, err := repository.ClaimTurn(ctx, accepted.TurnID, workerID, acceptedAt.Add(time.Second), acceptedAt.Add(time.Minute)); err != nil {
		t.Fatalf("ClaimTurn(memory fixture): %v", err)
	}
	completed, err := repository.CompleteTurnExecution(ctx, userID, accepted.TurnID, workerID, conversation.AgentResponse{
		Content: "已记录会话决策。",
		RunObservation: &conversation.AgentRunObservation{
			ModelProvider: "fixture", ModelID: "fixture-v1", PromptVersion: "conversation-test-v1",
			Outcome:        conversation.AgentRunAnswered,
			Usage:          conversation.AgentRunUsage{ModelCalls: 1, PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
			DurationMillis: 10,
		},
	}, acceptedAt.Add(2*time.Second))
	if err != nil {
		t.Fatalf("CompleteTurnExecution(memory fixture): %v", err)
	}
	return userID, current.ID, accepted.TurnID, completed.AssistantMessage.Seq
}

func insertIntegrationMemoryJob(
	t *testing.T,
	db *gorm.DB,
	conversationID, sourceTurnID uuid.UUID,
	throughSeq int64,
	maxAttempts int,
	createdAt time.Time,
) uuid.UUID {
	t.Helper()
	jobID := uuid.New()
	if err := db.Exec(`
INSERT INTO conversation_memory_jobs (
    id, conversation_id, source_turn_id, requested_through_seq,
    status, attempt_count, max_attempts, fencing_token, available_at, created_at, updated_at
)
VALUES (?, ?, ?, ?, 'pending', 0, ?, 0, ?, ?, ?)`,
		jobID, conversationID, sourceTurnID, throughSeq, maxAttempts,
		createdAt.UTC(), createdAt.UTC(), createdAt.UTC()).Error; err != nil {
		t.Fatalf("insert memory job: %v", err)
	}
	return jobID
}

func mustClaimMemoryJob(
	t *testing.T,
	ctx context.Context,
	repository *ConversationMemoryJobRepository,
	jobID, conversationID uuid.UUID,
	workerID string,
	now time.Time,
) conversationmemoryworker.Lease {
	t.Helper()
	claim, err := repository.Claim(ctx, jobID, conversationID, workerID, now, now.Add(time.Second))
	if err != nil || claim.Disposition != conversationmemoryworker.ClaimAcquired || claim.Lease == nil {
		t.Fatalf("Claim(%s) = %+v, %v", workerID, claim, err)
	}
	return *claim.Lease
}
