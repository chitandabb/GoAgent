//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversation"
	repositorydomain "github.com/chitandabb/GoAgent/internal/repository"

	"github.com/google/uuid"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestConversationTurnRepositoryAgainstPostgres(t *testing.T) {
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
VALUES (?, ?, 'Conversation Owner', 'integration-hash', 'analyst', 'active', false)`,
		userID, "conversation_owner_"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	repository := NewConversationRepository(tx)
	current, err := repository.Create(ctx, userID, conversation.CreateInput{Title: "幂等回合"}, time.Now().UTC())
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}
	startedAt := time.Now().UTC()
	key := uuid.NewString()
	beginInput := conversation.BeginTurnInput{
		Message: conversation.AppendMessageInput{
			ConversationID: current.ID, Role: conversation.MessageRoleUser, Content: "知识库如何更新？",
		},
		IdempotencyKey: key, RequestFingerprint: strings.Repeat("a", 64),
		StartedAt: startedAt, LeaseExpiresAt: startedAt.Add(time.Minute),
	}
	first, err := repository.BeginTurn(ctx, userID, beginInput)
	if err != nil {
		t.Fatalf("BeginTurn(): %v", err)
	}
	if !first.Created || first.TurnID == uuid.Nil || first.UserMessage.Seq != 1 {
		t.Fatalf("first turn = %+v", first)
	}
	if _, err := repository.BeginTurn(ctx, userID, beginInput); !errors.Is(err, conversation.ErrTurnInProgress) {
		t.Fatalf("concurrent BeginTurn() error = %v, want ErrTurnInProgress", err)
	}
	if err := repository.FailTurn(ctx, userID, first.TurnID, startedAt.Add(time.Second)); err != nil {
		t.Fatalf("FailTurn(): %v", err)
	}
	retriedInput := beginInput
	retriedInput.StartedAt = startedAt.Add(2 * time.Second)
	retriedInput.LeaseExpiresAt = startedAt.Add(time.Minute + 2*time.Second)
	retried, err := repository.BeginTurn(ctx, userID, retriedInput)
	if err != nil {
		t.Fatalf("retry BeginTurn(): %v", err)
	}
	if retried.Created || retried.TurnID != first.TurnID || retried.UserMessage.ID != first.UserMessage.ID {
		t.Fatalf("retried turn = %+v, first = %+v", retried, first)
	}
	knowledgeSourceRef := "knowledge:" + uuid.NewString() + "/" + uuid.NewString()
	completed, err := repository.CompleteTurn(ctx, userID, first.TurnID, conversation.AgentResponse{
		Content: "采用不可变版本重新发布。[source:" + knowledgeSourceRef + "]",
		Citations: []conversation.MessageCitation{{
			Position: 0, SourceType: conversation.CitationSourceKnowledgeChunk,
			SourceRef: knowledgeSourceRef, ContentSHA256: strings.Repeat("d", 64),
		}},
		RunObservation: &conversation.AgentRunObservation{
			ModelProvider: "fixture", ModelID: "fixture-v1", PromptVersion: "conversation-test-v1",
			Outcome: conversation.AgentRunAnswered,
			RetrievedSources: []conversation.AgentRunSource{{
				SourceType: conversation.CitationSourceKnowledgeChunk,
				SourceRef:  knowledgeSourceRef, ContentSHA256: strings.Repeat("d", 64),
			}},
			Usage: conversation.AgentRunUsage{
				ModelCalls: 2, PromptTokens: 30, CompletionTokens: 10, TotalTokens: 40,
			},
			DurationMillis: 123,
		},
	}, startedAt.Add(3*time.Second))
	if err != nil {
		t.Fatalf("CompleteTurn(): %v", err)
	}
	if completed.AssistantMessage.Seq != 2 || completed.AssistantMessage.Role != conversation.MessageRoleAssistant ||
		len(completed.AssistantMessage.Citations) != 1 ||
		completed.AssistantMessage.Citations[0].SourceRef != knowledgeSourceRef {
		t.Fatalf("completed turn = %+v", completed)
	}
	replayed, err := repository.BeginTurn(ctx, userID, retriedInput)
	if err != nil {
		t.Fatalf("replay BeginTurn(): %v", err)
	}
	if replayed.AssistantMessage == nil || replayed.AssistantMessage.ID != completed.AssistantMessage.ID ||
		len(replayed.AssistantMessage.Citations) != 1 || replayed.AssistantMessage.Citations[0].SourceRef != knowledgeSourceRef {
		t.Fatalf("replayed turn = %+v", replayed)
	}
	var observationCount, retrievedSourceCount int64
	if err := tx.Raw(`SELECT COUNT(*) FROM conversation_turn_run_observations WHERE turn_id = ?`, first.TurnID).
		Scan(&observationCount).Error; err != nil {
		t.Fatalf("count run observations: %v", err)
	}
	if err := tx.Raw(`SELECT COUNT(*) FROM conversation_turn_retrieved_sources WHERE turn_id = ?`, first.TurnID).
		Scan(&retrievedSourceCount).Error; err != nil {
		t.Fatalf("count retrieved sources: %v", err)
	}
	if observationCount != 1 || retrievedSourceCount != 1 {
		t.Fatalf("observation/source counts = %d/%d, want 1/1", observationCount, retrievedSourceCount)
	}
	recordedRun, err := repository.GetRecordedAgentRun(ctx, first.TurnID)
	if err != nil {
		t.Fatalf("GetRecordedAgentRun(): %v", err)
	}
	if recordedRun.UserQuery != "知识库如何更新？" || recordedRun.Answer != completed.AssistantMessage.Content ||
		recordedRun.Observation.ModelProvider != "fixture" || recordedRun.Observation.Usage.TotalTokens != 40 ||
		len(recordedRun.Observation.RetrievedSources) != 1 ||
		recordedRun.Observation.RetrievedSources[0].SourceRef != knowledgeSourceRef ||
		len(recordedRun.Citations) != 1 || recordedRun.Citations[0].SourceRef != knowledgeSourceRef {
		t.Fatalf("recorded run = %+v", recordedRun)
	}
	conflictInput := retriedInput
	conflictInput.RequestFingerprint = strings.Repeat("b", 64)
	if _, err := repository.BeginTurn(ctx, userID, conflictInput); !errors.Is(err, conversation.ErrTurnIdempotencyConflict) {
		t.Fatalf("conflicting BeginTurn() error = %v, want ErrTurnIdempotencyConflict", err)
	}

	secondInput := beginInput
	secondInput.IdempotencyKey = uuid.NewString()
	secondInput.RequestFingerprint = strings.Repeat("c", 64)
	secondInput.StartedAt = startedAt.Add(4 * time.Second)
	secondInput.LeaseExpiresAt = startedAt.Add(5 * time.Second)
	second, err := repository.BeginTurn(ctx, userID, secondInput)
	if err != nil {
		t.Fatalf("second BeginTurn(): %v", err)
	}
	expiredRetry := secondInput
	expiredRetry.StartedAt = startedAt.Add(6 * time.Second)
	expiredRetry.LeaseExpiresAt = startedAt.Add(time.Minute + 6*time.Second)
	reclaimed, err := repository.BeginTurn(ctx, userID, expiredRetry)
	if err != nil {
		t.Fatalf("reclaim expired BeginTurn(): %v", err)
	}
	if reclaimed.Created || reclaimed.TurnID != second.TurnID || reclaimed.UserMessage.ID != second.UserMessage.ID {
		t.Fatalf("reclaimed turn = %+v, second = %+v", reclaimed, second)
	}
	if _, err := repository.AppendMessage(ctx, userID, conversation.AppendMessageInput{
		ConversationID: current.ID, Role: conversation.MessageRoleUser, Content: "并发追加",
	}, startedAt.Add(7*time.Second)); !errors.Is(err, conversation.ErrTurnInProgress) {
		t.Fatalf("AppendMessage during active turn error = %v, want ErrTurnInProgress", err)
	}
	if err := repository.FailTurn(ctx, userID, second.TurnID, startedAt.Add(8*time.Second)); err != nil {
		t.Fatalf("fail second turn: %v", err)
	}

	var turnCount, messageCount int64
	if err := tx.Raw("SELECT COUNT(*) FROM conversation_turns WHERE conversation_id = ?", current.ID).Scan(&turnCount).Error; err != nil {
		t.Fatalf("count turns: %v", err)
	}
	if err := tx.Raw("SELECT COUNT(*) FROM conversation_messages WHERE conversation_id = ?", current.ID).Scan(&messageCount).Error; err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if turnCount != 2 || messageCount != 3 {
		t.Fatalf("turn/message counts = %d/%d, want 2/3", turnCount, messageCount)
	}
}

func TestAsyncConversationTurnRepositoryAgainstPostgres(t *testing.T) {
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
VALUES (?, ?, 'Async Conversation Owner', 'integration-hash', 'admin', 'active', false)`,
		userID, "async_conversation_owner_"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	repository := NewConversationRepository(tx)
	current, err := repository.Create(ctx, userID, conversation.CreateInput{Title: "异步回合"}, time.Now().UTC())
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}
	// PostgreSQL TIMESTAMPTZ has microsecond precision; align the fixture clock so
	// exact lease/retry timestamp assertions do not depend on discarded nanoseconds.
	acceptedAt := time.Now().UTC().Truncate(time.Microsecond)
	key := uuid.NewString()
	correlationID := uuid.New()
	input := conversation.BeginTurnInput{
		Message: conversation.AppendMessageInput{
			ConversationID: current.ID, Role: conversation.MessageRoleUser, Content: "请判断是否需要创建诊断任务",
		},
		IdempotencyKey: key, RequestFingerprint: strings.Repeat("d", 64),
		StartedAt: acceptedAt, ExecutionMode: conversation.TurnExecutionAsynchronous,
		CorrelationID: correlationID,
	}
	accepted, err := repository.AcceptTurn(ctx, userID, input)
	if err != nil {
		t.Fatalf("AcceptTurn(): %v", err)
	}
	if !accepted.Created || accepted.Status != conversation.TurnStatusQueued || accepted.UserMessage.Seq != 1 {
		t.Fatalf("accepted turn = %+v", accepted)
	}
	replayedQueued, err := repository.AcceptTurn(ctx, userID, input)
	if err != nil {
		t.Fatalf("queued replay AcceptTurn(): %v", err)
	}
	if replayedQueued.Created || replayedQueued.TurnID != accepted.TurnID || replayedQueued.Status != conversation.TurnStatusQueued {
		t.Fatalf("queued replay = %+v", replayedQueued)
	}
	if _, err := repository.AppendMessage(ctx, userID, conversation.AppendMessageInput{
		ConversationID: current.ID, Role: conversation.MessageRoleUser, Content: "不应并发追加",
	}, acceptedAt.Add(time.Second)); !errors.Is(err, conversation.ErrTurnInProgress) {
		t.Fatalf("AppendMessage() error = %v, want ErrTurnInProgress", err)
	}

	workerOne, workerTwo := "conversation-worker-one", "conversation-worker-two"
	firstLeaseUntil := acceptedAt.Add(time.Minute)
	firstExecution, err := repository.ClaimTurn(ctx, accepted.TurnID, workerOne, acceptedAt.Add(2*time.Second), firstLeaseUntil)
	if err != nil {
		t.Fatalf("first ClaimTurn(): %v", err)
	}
	if firstExecution.AttemptCount != 1 || !firstExecution.Actor.IsAdmin || len(firstExecution.History) != 1 ||
		firstExecution.History[0].ID != accepted.UserMessage.ID {
		t.Fatalf("first execution = %+v", firstExecution)
	}
	if _, err := repository.ClaimTurn(
		ctx, accepted.TurnID, workerTwo, acceptedAt.Add(3*time.Second), acceptedAt.Add(2*time.Minute),
	); !errors.Is(err, conversation.ErrTurnInProgress) {
		t.Fatalf("held ClaimTurn() error = %v, want ErrTurnInProgress", err)
	}
	owned, err := repository.RenewTurnExecution(
		ctx, accepted.TurnID, workerOne, acceptedAt.Add(4*time.Second), acceptedAt.Add(70*time.Second),
	)
	if err != nil || !owned {
		t.Fatalf("RenewTurnExecution() owned=%v err=%v", owned, err)
	}
	owned, err = repository.RenewTurnExecution(
		ctx, accepted.TurnID, workerTwo, acceptedAt.Add(5*time.Second), acceptedAt.Add(80*time.Second),
	)
	if err != nil || owned {
		t.Fatalf("foreign RenewTurnExecution() owned=%v err=%v", owned, err)
	}

	reclaimAt := acceptedAt.Add(71 * time.Second)
	secondExecution, err := repository.ClaimTurn(
		ctx, accepted.TurnID, workerTwo, reclaimAt, reclaimAt.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("expired ClaimTurn(): %v", err)
	}
	if secondExecution.AttemptCount != 2 {
		t.Fatalf("second execution attempt = %d, want 2", secondExecution.AttemptCount)
	}
	if _, err := repository.CompleteTurnExecution(
		ctx, userID, accepted.TurnID, workerOne,
		conversation.AgentResponse{Content: "旧租约不能提交"}, reclaimAt.Add(time.Second),
	); !errors.Is(err, conversation.ErrTurnLeaseLost) {
		t.Fatalf("stale CompleteTurnExecution() error = %v, want ErrTurnLeaseLost", err)
	}
	completed, err := repository.CompleteTurnExecution(
		ctx, userID, accepted.TurnID, workerTwo,
		conversation.AgentResponse{
			Content: "已创建后台诊断任务。",
			RunObservation: &conversation.AgentRunObservation{
				ModelProvider: "fixture", ModelID: "fixture-v1", PromptVersion: "conversation-test-v1",
				Outcome: conversation.AgentRunAnswered,
				Usage: conversation.AgentRunUsage{
					ModelCalls: 1, PromptTokens: 20, CompletionTokens: 5, TotalTokens: 25,
				},
				DurationMillis: 50,
			},
		}, reclaimAt.Add(2*time.Second),
	)
	if err != nil {
		t.Fatalf("CompleteTurnExecution(): %v", err)
	}
	if completed.AssistantMessage.Seq != 2 || completed.AssistantMessage.Role != conversation.MessageRoleAssistant {
		t.Fatalf("completed turn = %+v", completed)
	}
	var asyncObservationCount int64
	if err := tx.Raw(`SELECT COUNT(*) FROM conversation_turn_run_observations WHERE turn_id = ?`, accepted.TurnID).
		Scan(&asyncObservationCount).Error; err != nil {
		t.Fatalf("count async run observations: %v", err)
	}
	if asyncObservationCount != 1 {
		t.Fatalf("async observation count = %d, want 1", asyncObservationCount)
	}
	completedReplay, err := repository.AcceptTurn(ctx, userID, input)
	if err != nil {
		t.Fatalf("completed replay AcceptTurn(): %v", err)
	}
	if completedReplay.Status != conversation.TurnStatusCompleted || completedReplay.AssistantMessage == nil ||
		completedReplay.AssistantMessage.ID != completed.AssistantMessage.ID {
		t.Fatalf("completed replay = %+v", completedReplay)
	}
	turnDetail, err := repository.GetTurn(ctx, userID, current.ID, accepted.TurnID)
	if err != nil {
		t.Fatalf("GetTurn(): %v", err)
	}
	if turnDetail.Status != conversation.TurnStatusCompleted || turnDetail.AttemptCount != 2 ||
		turnDetail.AssistantMessageID == nil || *turnDetail.AssistantMessageID != completed.AssistantMessage.ID {
		t.Fatalf("turn detail = %+v", turnDetail)
	}
	eventPage, err := repository.ListTurnEvents(ctx, userID, current.ID, accepted.TurnID, 0, 10)
	if err != nil {
		t.Fatalf("ListTurnEvents(): %v", err)
	}
	wantEvents := []conversation.TurnEventType{
		conversation.TurnEventQueued, conversation.TurnEventRunning,
		conversation.TurnEventRunning, conversation.TurnEventCompleted,
	}
	if len(eventPage.Items) != len(wantEvents) || eventPage.NextAfterSeq != int64(len(wantEvents)) || eventPage.HasMore {
		t.Fatalf("event page = %+v", eventPage)
	}
	for index, eventType := range wantEvents {
		if eventPage.Items[index].Seq != int64(index+1) || eventPage.Items[index].EventType != eventType {
			t.Fatalf("event[%d] = %+v, want %q", index, eventPage.Items[index], eventType)
		}
	}
	if _, err := repository.GetTurn(ctx, uuid.New(), current.ID, accepted.TurnID); !errors.Is(err, repositorydomain.ErrNotFound) {
		t.Fatalf("foreign GetTurn() error = %v, want repository.ErrNotFound", err)
	}

	var facts struct {
		TurnCount    int64  `gorm:"column:turn_count"`
		MessageCount int64  `gorm:"column:message_count"`
		OutboxCount  int64  `gorm:"column:outbox_count"`
		EventType    string `gorm:"column:event_type"`
		Payload      string `gorm:"column:payload"`
	}
	if err := tx.Raw(`
SELECT
    (SELECT COUNT(*) FROM conversation_turns WHERE conversation_id = ?) AS turn_count,
    (SELECT COUNT(*) FROM conversation_messages WHERE conversation_id = ?) AS message_count,
    (SELECT COUNT(*) FROM outbox_events WHERE aggregate_id = ?) AS outbox_count,
    (SELECT event_type FROM outbox_events WHERE aggregate_id = ? LIMIT 1) AS event_type,
    (SELECT payload::text FROM outbox_events WHERE aggregate_id = ? LIMIT 1) AS payload`,
		current.ID, current.ID, accepted.TurnID, accepted.TurnID, accepted.TurnID).Scan(&facts).Error; err != nil {
		t.Fatalf("load async turn facts: %v", err)
	}
	if facts.TurnCount != 1 || facts.MessageCount != 2 || facts.OutboxCount != 1 ||
		facts.EventType != "conversation.turn.execute" || !strings.Contains(facts.Payload, accepted.TurnID.String()) {
		t.Fatalf("async turn facts = %+v", facts)
	}

	retryConversation, err := repository.Create(ctx, userID, conversation.CreateInput{Title: "失败重试"}, reclaimAt.Add(3*time.Second))
	if err != nil {
		t.Fatalf("create retry conversation: %v", err)
	}
	retryInput := input
	retryInput.Message.ConversationID = retryConversation.ID
	retryInput.IdempotencyKey = uuid.NewString()
	retryInput.RequestFingerprint = strings.Repeat("e", 64)
	retryInput.CorrelationID = uuid.New()
	retryInput.StartedAt = reclaimAt.Add(4 * time.Second)
	firstRetry, err := repository.AcceptTurn(ctx, userID, retryInput)
	if err != nil {
		t.Fatalf("accept retry turn: %v", err)
	}
	retryExecution, err := repository.ClaimTurn(
		ctx, firstRetry.TurnID, workerOne, reclaimAt.Add(5*time.Second), reclaimAt.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("claim retry turn: %v", err)
	}
	if err := repository.FailTurnExecution(
		ctx, userID, firstRetry.TurnID, workerOne, nil, reclaimAt.Add(6*time.Second),
	); err != nil {
		t.Fatalf("fail retry turn: %v", err)
	}
	retryInput.StartedAt = reclaimAt.Add(7 * time.Second)
	retryInput.CorrelationID = uuid.New()
	requeued, err := repository.AcceptTurn(ctx, userID, retryInput)
	if err != nil {
		t.Fatalf("requeue failed turn: %v", err)
	}
	if requeued.Created || requeued.Status != conversation.TurnStatusQueued || requeued.UserMessage.ID != firstRetry.UserMessage.ID {
		t.Fatalf("requeued turn = %+v", requeued)
	}
	secondRetry, err := repository.ClaimTurn(
		ctx, firstRetry.TurnID, workerTwo, reclaimAt.Add(8*time.Second), reclaimAt.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("claim requeued turn: %v", err)
	}
	if secondRetry.AttemptCount != retryExecution.AttemptCount+1 {
		t.Fatalf("requeued attempt = %d, want %d", secondRetry.AttemptCount, retryExecution.AttemptCount+1)
	}
	retryScheduledAt := reclaimAt.Add(9 * time.Second)
	retryAt := retryScheduledAt.Add(30 * time.Second)
	if err := repository.QueueTurnRetry(
		ctx, userID, firstRetry.TurnID, workerTwo, retryScheduledAt, retryAt,
	); err != nil {
		t.Fatalf("QueueTurnRetry(): %v", err)
	}
	retryDetail, err := repository.GetTurn(ctx, userID, retryConversation.ID, firstRetry.TurnID)
	if err != nil {
		t.Fatalf("GetTurn(retry queued): %v", err)
	}
	if retryDetail.Status != conversation.TurnStatusQueued || retryDetail.RetryAt == nil || !retryDetail.RetryAt.Equal(retryAt) ||
		retryDetail.FailureSummary == "" {
		t.Fatalf("retry detail = %+v", retryDetail)
	}
	if _, err := repository.ClaimTurn(
		ctx, firstRetry.TurnID, workerOne, retryScheduledAt.Add(time.Second), retryAt.Add(time.Minute),
	); !errors.Is(err, conversation.ErrTurnInProgress) {
		t.Fatalf("early retry ClaimTurn() error = %v, want ErrTurnInProgress", err)
	}
	thirdRetry, err := repository.ClaimTurn(
		ctx, firstRetry.TurnID, workerOne, retryAt, retryAt.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("scheduled retry ClaimTurn(): %v", err)
	}
	if thirdRetry.AttemptCount != secondRetry.AttemptCount+1 {
		t.Fatalf("scheduled retry attempt = %d, want %d", thirdRetry.AttemptCount, secondRetry.AttemptCount+1)
	}
	retryEvents, err := repository.ListTurnEvents(ctx, userID, retryConversation.ID, firstRetry.TurnID, 0, 20)
	if err != nil {
		t.Fatalf("ListTurnEvents(retry): %v", err)
	}
	if len(retryEvents.Items) < 6 || retryEvents.Items[len(retryEvents.Items)-2].EventType != conversation.TurnEventRetryScheduled ||
		retryEvents.Items[len(retryEvents.Items)-1].EventType != conversation.TurnEventRunning {
		t.Fatalf("retry events = %+v", retryEvents.Items)
	}
	var retryOutboxCount int64
	if err := tx.Raw("SELECT COUNT(*) FROM outbox_events WHERE aggregate_id = ?", firstRetry.TurnID).Scan(&retryOutboxCount).Error; err != nil {
		t.Fatalf("count retry outbox: %v", err)
	}
	if retryOutboxCount != 2 {
		t.Fatalf("retry outbox count = %d, want 2", retryOutboxCount)
	}
	failureSourceRef := "https://go.dev/doc/go1.25"
	failure := &conversation.AgentRunFailureRecord{
		Observation: conversation.AgentRunObservation{
			ModelProvider: "fixture", ModelID: "fixture-v1", PromptVersion: "conversation-test-v1",
			Outcome: conversation.AgentRunFailed,
			RetrievedSources: []conversation.AgentRunSource{{
				SourceType: conversation.CitationSourceWeb, SourceRef: failureSourceRef,
				ContentSHA256: strings.Repeat("f", 64),
			}},
			DurationMillis: 321,
		},
		ErrorType: "agent_timeout",
	}
	failedAt := retryAt.Add(time.Second)
	if err := repository.FailTurnExecution(
		ctx, userID, firstRetry.TurnID, workerOne, failure, failedAt,
	); err != nil {
		t.Fatalf("FailTurnExecution(recorded failure): %v", err)
	}
	failedRun, err := repository.GetRecordedAgentRun(ctx, firstRetry.TurnID)
	if err != nil {
		t.Fatalf("GetRecordedAgentRun(failed): %v", err)
	}
	if failedRun.AssistantMessageID != nil || failedRun.Answer != "" || len(failedRun.Citations) != 0 ||
		failedRun.CompletedAt != nil || failedRun.ErrorType != "agent_timeout" ||
		failedRun.Observation.Outcome != conversation.AgentRunFailed ||
		failedRun.Observation.Usage.ModelCalls != 0 || len(failedRun.Observation.RetrievedSources) != 1 ||
		failedRun.Observation.RetrievedSources[0].SourceRef != failureSourceRef ||
		!failedRun.ObservedAt.Equal(failedAt) {
		t.Fatalf("failed recorded run = %+v", failedRun)
	}
	var failureFacts struct {
		FailureCode string `gorm:"column:failure_code"`
		ErrorType   string `gorm:"column:error_type"`
		SourceCount int64  `gorm:"column:source_count"`
	}
	if err := tx.Raw(`
SELECT turn.failure_code, observation.error_type,
       (SELECT COUNT(*) FROM conversation_turn_retrieved_sources source WHERE source.turn_id = turn.id) AS source_count
FROM conversation_turns turn
JOIN conversation_turn_run_observations observation ON observation.turn_id = turn.id
WHERE turn.id = ?`, firstRetry.TurnID).Scan(&failureFacts).Error; err != nil {
		t.Fatalf("load failed run facts: %v", err)
	}
	if failureFacts.FailureCode != "agent_timeout" || failureFacts.ErrorType != "agent_timeout" || failureFacts.SourceCount != 1 {
		t.Fatalf("failure facts = %+v", failureFacts)
	}
	retryInput.StartedAt = failedAt.Add(time.Second)
	retryInput.CorrelationID = uuid.New()
	reopenedFailure, err := repository.AcceptTurn(ctx, userID, retryInput)
	if err != nil {
		t.Fatalf("reopen recorded failure: %v", err)
	}
	if reopenedFailure.Status != conversation.TurnStatusQueued || reopenedFailure.Created {
		t.Fatalf("reopened recorded failure = %+v", reopenedFailure)
	}
	var clearedObservationCount int64
	if err := tx.Raw(`SELECT COUNT(*) FROM conversation_turn_run_observations WHERE turn_id = ?`, firstRetry.TurnID).
		Scan(&clearedObservationCount).Error; err != nil {
		t.Fatalf("count cleared failed observation: %v", err)
	}
	if clearedObservationCount != 0 {
		t.Fatalf("cleared failed observation count = %d, want 0", clearedObservationCount)
	}

	expiredConversation, err := repository.Create(ctx, userID, conversation.CreateInput{Title: "过期恢复"}, reclaimAt.Add(9*time.Second))
	if err != nil {
		t.Fatalf("create expired conversation: %v", err)
	}
	expiredInput := input
	expiredInput.Message.ConversationID = expiredConversation.ID
	expiredInput.IdempotencyKey = uuid.NewString()
	expiredInput.RequestFingerprint = strings.Repeat("f", 64)
	expiredInput.CorrelationID = uuid.New()
	expiredInput.StartedAt = reclaimAt.Add(10 * time.Second)
	expiredTurn, err := repository.AcceptTurn(ctx, userID, expiredInput)
	if err != nil {
		t.Fatalf("accept expired turn fixture: %v", err)
	}
	if _, err := repository.ClaimTurn(
		ctx, expiredTurn.TurnID, workerOne, reclaimAt.Add(11*time.Second), reclaimAt.Add(12*time.Second),
	); err != nil {
		t.Fatalf("claim expired turn fixture: %v", err)
	}
	expiredInput.StartedAt = reclaimAt.Add(13 * time.Second)
	expiredInput.CorrelationID = uuid.New()
	recoveredExpired, err := repository.AcceptTurn(ctx, userID, expiredInput)
	if err != nil {
		t.Fatalf("recover expired turn through AcceptTurn(): %v", err)
	}
	if recoveredExpired.Status != conversation.TurnStatusQueued || recoveredExpired.Created ||
		recoveredExpired.UserMessage.ID != expiredTurn.UserMessage.ID {
		t.Fatalf("recovered expired turn = %+v", recoveredExpired)
	}
	var expiredOutboxCount int64
	if err := tx.Raw("SELECT COUNT(*) FROM outbox_events WHERE aggregate_id = ?", expiredTurn.TurnID).Scan(&expiredOutboxCount).Error; err != nil {
		t.Fatalf("count expired recovery outbox: %v", err)
	}
	if expiredOutboxCount != 2 {
		t.Fatalf("expired recovery outbox count = %d, want 2", expiredOutboxCount)
	}
}
