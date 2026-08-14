//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/conversationworker"
	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/semanticcache"
	"github.com/chitandabb/GoAgent/internal/semanticcache/contracttest"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestSemanticAnswerCacheAcrossConversationsAndGenerationInvalidation(t *testing.T) {
	dsn := os.Getenv("MESGUARD_TEST_POSTGRES_DSN")
	if dsn == "" {
		_ = godotenv.Load("../../../.env")
		t.Setenv("MESGUARD_CONFIG_FILE", "../../../config/mesguard.toml")
		cfg, configErr := config.Load()
		if configErr != nil {
			t.Skipf("load local PostgreSQL test config: %v", configErr)
		}
		var dsnErr error
		dsn, dsnErr = ConnectionString(cfg.Postgres)
		if dsnErr != nil {
			t.Skipf("build local PostgreSQL test DSN: %v", dsnErr)
		}
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
	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		t.Fatalf("begin fixture transaction: %v", tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })

	userID, documentID := uuid.New(), uuid.New()
	if err := tx.Exec(`
INSERT INTO users (id, username, display_name, password_hash, role, status, must_change_password)
VALUES (?, ?, 'Cache Owner', 'integration-hash', 'analyst', 'active', false)`,
		userID, "cache_owner_"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	knowledgeRepository := NewKnowledgeRepository(tx)
	if _, err := knowledgeRepository.CreateDocument(ctx, knowledge.CreateDocumentInput{
		ID: documentID, Scope: knowledge.ScopeGlobal, Title: "设备点检规范", CreatedBy: userID,
	}); err != nil {
		t.Fatalf("CreateDocument(): %v", err)
	}
	content := "设备点检周期为 30 天。"
	chunks, err := knowledge.ChunkMarkdown(content, knowledge.TextChunkOptions{MaxRunes: 128, OverlapRunes: 16})
	if err != nil {
		t.Fatal(err)
	}
	versionID := uuid.New()
	if _, err := knowledgeRepository.PublishVersion(ctx, publishKnowledgeVersionInput(
		versionID, documentID, userID, content, chunks,
	)); err != nil {
		t.Fatalf("PublishVersion(): %v", err)
	}
	var chunkRecord struct {
		ID            uuid.UUID `gorm:"column:id"`
		ContentSHA256 string    `gorm:"column:content_sha256"`
	}
	if err := tx.Raw(`
SELECT id, content_sha256 FROM knowledge_chunks
WHERE document_version_id = ? ORDER BY ordinal LIMIT 1`, versionID).Scan(&chunkRecord).Error; err != nil {
		t.Fatalf("load chunk: %v", err)
	}
	sourceRef := "knowledge:" + versionID.String() + "/" + chunkRecord.ID.String()
	agent := &semanticCacheIntegrationResponder{response: conversation.AgentResponse{
		Content: "设备点检周期为 30 天。[source:" + sourceRef + "]",
		Citations: []conversation.MessageCitation{{
			Position: 0, SourceType: conversation.CitationSourceKnowledgeChunk,
			SourceRef: sourceRef, ContentSHA256: chunkRecord.ContentSHA256,
		}},
		RunObservation: &conversation.AgentRunObservation{
			ModelProvider: "fixture", ModelID: "fixture-model", PromptVersion: "fixture-v1",
			ExecutionPath: conversation.AgentRunExecutionAgent, Outcome: conversation.AgentRunAnswered,
			AnswerCacheEligible: true, ToolCalls: 1,
			RetrievedSources: []conversation.AgentRunSource{{
				SourceType: conversation.CitationSourceKnowledgeChunk,
				SourceRef:  sourceRef, ContentSHA256: chunkRecord.ContentSHA256,
			}},
			Usage: conversation.AgentRunUsage{
				ModelCalls: 1, PromptTokens: 10, CompletionTokens: 3, TotalTokens: 13,
			},
		},
	}}
	conversationRepository := NewConversationRepository(tx)
	service, err := conversation.NewService(conversationRepository)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.WithAgentResponder(agent); err != nil {
		t.Fatal(err)
	}
	cacheRepository := NewSemanticAnswerCacheRepository(tx)
	if _, err := service.WithSemanticAnswerCache(
		cacheRepository,
		conversation.SemanticAnswerCacheConfig{
			TTL: time.Hour, LookupTimeout: time.Second, WriteTimeout: time.Second,
			MaxAnswerBytes: 16 * 1024, MaxCitations: 8,
		}, nil,
	); err != nil {
		t.Fatal(err)
	}

	question := "设备点检周期规范是什么？"
	var cachedTurnID uuid.UUID
	var sourceRunID uuid.UUID
	for index := 0; index < 2; index++ {
		created, err := service.Create(ctx, conversation.Actor{UserID: userID}, conversation.CreateInput{Title: "缓存会话"})
		if err != nil {
			t.Fatalf("Create conversation %d: %v", index, err)
		}
		result, err := service.ExecuteTurn(ctx, conversation.Actor{UserID: userID}, uuid.NewString(), conversation.AppendMessageInput{
			ConversationID: created.ID, Content: question,
		})
		if err != nil {
			t.Fatalf("ExecuteTurn %d: %v", index, err)
		}
		if result.Turn.AssistantMessage.Content != agent.response.Content ||
			len(result.Turn.AssistantMessage.Citations) != 1 {
			t.Fatalf("assistant %d = %+v", index, result.Turn.AssistantMessage)
		}
		if index == 1 {
			cachedTurnID = result.TurnID
		} else {
			sourceRunID = result.TurnID
		}
	}
	if agent.calls != 1 {
		t.Fatalf("agent calls after cross-conversation hit = %d, want 1", agent.calls)
	}
	wrongQuestionHash, err := semanticcache.ExactQuestionKey("另一个完全不同的问题？")
	if err != nil {
		t.Fatal(err)
	}
	if err := cacheRepository.Put(ctx, semanticcache.PutInput{
		QuestionHash: wrongQuestionHash, TTL: time.Hour,
		Answer: semanticcache.Answer{
			Content: agent.response.Content,
			Citations: []semanticcache.Source{{
				Position: 0, SourceType: string(conversation.CitationSourceKnowledgeChunk),
				SourceRef: sourceRef, ContentSHA256: chunkRecord.ContentSHA256,
			}},
			RetrievedSources: []semanticcache.Source{{
				SourceType: string(conversation.CitationSourceKnowledgeChunk),
				SourceRef:  sourceRef, ContentSHA256: chunkRecord.ContentSHA256,
			}},
			SourceRunID: sourceRunID, ModelProvider: "fixture", ModelID: "fixture-model",
			PromptVersion: "fixture-v1", CreatedAt: time.Now().UTC(),
		},
	}); !errors.Is(err, semanticcache.ErrInvalidRecord) {
		t.Fatalf("mismatched question/source run error = %v", err)
	}
	worker, err := conversationworker.New(conversationRepository, service, conversationworker.Config{
		WorkerID: "semantic-cache-integration", LeaseDuration: 10 * time.Second,
		RenewInterval: 2 * time.Second, MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	asyncHitConversation, err := service.Create(
		ctx, conversation.Actor{UserID: userID}, conversation.CreateInput{Title: "异步缓存命中"},
	)
	if err != nil {
		t.Fatal(err)
	}
	asyncHit, err := service.AcceptTurn(ctx, conversation.Actor{UserID: userID}, uuid.NewString(), conversation.AppendMessageInput{
		ConversationID: asyncHitConversation.ID, Content: question,
	})
	if err != nil {
		t.Fatalf("AcceptTurn(cache hit): %v", err)
	}
	processSemanticCacheTurn(t, ctx, worker, asyncHit.TurnID)
	asyncRecordedHit, err := conversationRepository.GetRecordedAgentRun(ctx, asyncHit.TurnID)
	if err != nil || asyncRecordedHit.Observation.ExecutionPath != conversation.AgentRunExecutionSemanticCacheHit || agent.calls != 1 {
		t.Fatalf("async cache hit run=%+v agent calls=%d err=%v", asyncRecordedHit, agent.calls, err)
	}
	questionHash, err := semanticcache.ExactQuestionKey(question)
	if err != nil {
		t.Fatal(err)
	}
	profile := activeSemanticCacheTestProfile(t, ctx, tx)
	vector := make([]float32, profile.Dimensions)
	vector[0] = 1
	if err := cacheRepository.IndexSemantic(ctx, semanticcache.SemanticIndexInput{
		QuestionHash: questionHash, Question: "另一个问题", Vector: vector,
		ProfileID: profile.ID, ProfileFingerprint: profile.Fingerprint,
		NormalizationVersion: semanticcache.SemanticNormalizationVersion, SourceRunID: sourceRunID,
	}); !errors.Is(err, semanticcache.ErrInvalidRecord) {
		t.Fatalf("mismatched semantic index question error = %v", err)
	}
	semanticIndexInput := semanticcache.SemanticIndexInput{
		QuestionHash: questionHash, Question: question, Vector: vector,
		ProfileID: profile.ID, ProfileFingerprint: profile.Fingerprint,
		NormalizationVersion: semanticcache.SemanticNormalizationVersion, SourceRunID: sourceRunID,
	}
	if err := cacheRepository.IndexSemantic(ctx, semanticIndexInput); err != nil {
		t.Fatalf("IndexSemantic(): %v", err)
	}
	semanticInput := semanticcache.SemanticLookupInput{
		Question: "设备点检需要遵循怎样的周期规范？", Vector: vector,
		ProfileID: profile.ID, ProfileFingerprint: profile.Fingerprint,
		NormalizationVersion: semanticcache.SemanticNormalizationVersion,
		MinimumSimilarity:    0.92, CandidateLimit: 5, Now: time.Now().UTC(),
	}
	contractAnswer, contractHit, contractErr := cacheRepository.Lookup(ctx, semanticcache.LookupInput{
		QuestionHash: questionHash, Now: time.Now().UTC(),
	})
	if contractErr != nil || !contractHit {
		t.Fatalf("load contract answer hit=%v err=%v", contractHit, contractErr)
	}
	contracttest.RunReadContract(t, contracttest.ReadContract{
		Provider:          cacheRepository,
		ExactInput:        semanticcache.LookupInput{QuestionHash: questionHash, Now: time.Now().UTC()},
		ExpiredExactInput: semanticcache.LookupInput{QuestionHash: questionHash, Now: contractAnswer.ExpiresAt.Add(time.Second)},
		SemanticInput:     semanticInput, SemanticIndexInput: semanticIndexInput,
		ConflictingQuestion: "设备点检周期是 60 天吗？",
		ExpectedSourceRunID: sourceRunID,
		ValidPut:            semanticcache.PutInput{QuestionHash: questionHash, Answer: contractAnswer, TTL: time.Hour},
	})
	if answer, hit, err := cacheRepository.LookupSemantic(ctx, semanticInput); err != nil || !hit ||
		answer.Layer != semanticcache.LayerSemantic || answer.SourceRunID != sourceRunID {
		t.Fatalf("semantic cache lookup hit=%v answer=%+v err=%v", hit, answer, err)
	}
	semanticInput.Question = "设备点检周期是 60 天吗？"
	if _, hit, err := cacheRepository.LookupSemantic(ctx, semanticInput); err != nil || hit {
		t.Fatalf("conflicting semantic cache lookup hit=%v err=%v", hit, err)
	}
	if err := tx.Exec(`
UPDATE semantic_answer_cache
SET citations = '[{"unknown":true}]'::jsonb
WHERE question_hash = ?`, questionHash).Error; err != nil {
		t.Fatalf("corrupt cache fixture: %v", err)
	}
	if _, hit, err := cacheRepository.Lookup(ctx, semanticcache.LookupInput{QuestionHash: questionHash, Now: time.Now().UTC()}); !errors.Is(err, semanticcache.ErrInvalidRecord) || hit {
		t.Fatalf("corrupt cache lookup hit=%v err=%v", hit, err)
	}
	if err := tx.Exec(`
UPDATE semantic_answer_cache
SET created_at = now() - interval '2 hours', expires_at = now() - interval '1 hour'
WHERE question_hash = ?`, questionHash).Error; err != nil {
		t.Fatalf("expire cache fixture: %v", err)
	}
	if _, hit, err := cacheRepository.Lookup(ctx, semanticcache.LookupInput{QuestionHash: questionHash, Now: time.Now().UTC()}); err != nil || hit {
		t.Fatalf("expired cache lookup hit=%v err=%v", hit, err)
	}
	var generation int64
	if err := tx.Raw("DELETE FROM global_knowledge_generation WHERE singleton = 1 RETURNING generation").Scan(&generation).Error; err != nil {
		t.Fatalf("remove generation fixture: %v", err)
	}
	if _, hit, err := cacheRepository.Lookup(ctx, semanticcache.LookupInput{QuestionHash: questionHash, Now: time.Now().UTC()}); err == nil || hit {
		t.Fatalf("missing generation lookup hit=%v err=%v", hit, err)
	}
	if err := tx.Exec(`
INSERT INTO global_knowledge_generation (singleton, generation, updated_at)
VALUES (1, ?, now())`, generation).Error; err != nil {
		t.Fatalf("restore generation fixture: %v", err)
	}
	recordedHit, err := conversationRepository.GetRecordedAgentRun(ctx, cachedTurnID)
	if err != nil {
		t.Fatalf("GetRecordedAgentRun(cache hit): %v", err)
	}
	if recordedHit.Observation.ExecutionPath != conversation.AgentRunExecutionSemanticCacheHit ||
		recordedHit.Observation.CacheLayer != conversation.AgentRunCacheLayerExact ||
		recordedHit.Observation.SourceRunID == uuid.Nil || recordedHit.Observation.ToolCalls != 0 ||
		recordedHit.Observation.Usage.ModelCalls != 0 {
		t.Fatalf("cache hit observation = %+v", recordedHit.Observation)
	}

	newContent := "设备点检周期调整为 20 天。"
	newChunks, err := knowledge.ChunkMarkdown(newContent, knowledge.TextChunkOptions{MaxRunes: 128, OverlapRunes: 16})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := knowledgeRepository.PublishVersion(ctx, publishKnowledgeVersionInput(
		uuid.New(), documentID, userID, newContent, newChunks,
	)); err != nil {
		t.Fatalf("PublishVersion(new): %v", err)
	}
	third, err := service.Create(ctx, conversation.Actor{UserID: userID}, conversation.CreateInput{Title: "失效后会话"})
	if err != nil {
		t.Fatal(err)
	}
	acceptedAfterGeneration, err := service.AcceptTurn(ctx, conversation.Actor{UserID: userID}, uuid.NewString(), conversation.AppendMessageInput{
		ConversationID: third.ID, Content: strings.TrimSpace(question),
	})
	if err != nil {
		t.Fatalf("AcceptTurn after generation change: %v", err)
	}
	processSemanticCacheTurn(t, ctx, worker, acceptedAfterGeneration.TurnID)
	if agent.calls != 2 {
		t.Fatalf("agent calls after generation change = %d, want 2", agent.calls)
	}
}

func activeSemanticCacheTestProfile(t *testing.T, ctx context.Context, db *gorm.DB) knowledge.EmbeddingProfile {
	t.Helper()
	var profile knowledge.EmbeddingProfile
	result := db.WithContext(ctx).Raw(`
SELECT id, profile_key AS key, provider, model, dimensions, distance_metric,
       query_input_type, document_input_type, normalized AS normalize,
       config_version, fingerprint
FROM knowledge_embedding_profiles
WHERE status = 'active'`).Scan(&profile)
	if result.Error != nil {
		t.Fatalf("load active embedding profile: %v", result.Error)
	}
	if result.RowsAffected == 0 {
		var err error
		profile, err = knowledge.NewEmbeddingProfile(
			"semantic-cache-integration", "fixture", "fixture-embedding", 1024, "cosine",
			knowledge.EmbeddingInputQuery, knowledge.EmbeddingInputDocument, true, "v1",
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := NewKnowledgeWorkerRepository(db).EnsureEmbeddingProfile(ctx, profile); err != nil {
			t.Fatalf("ensure fixture embedding profile: %v", err)
		}
	}
	if err := profile.Validate(); err != nil {
		t.Fatalf("active embedding profile is invalid: %+v err=%v", profile, err)
	}
	return profile
}

func processSemanticCacheTurn(
	t *testing.T,
	ctx context.Context,
	worker *conversationworker.Worker,
	turnID uuid.UUID,
) {
	t.Helper()
	messageID, correlationID := uuid.New(), uuid.New()
	envelope := map[string]any{
		"messageId": messageID.String(), "messageType": conversationworker.MessageType,
		"schemaVersion": conversationworker.SchemaVersion, "occurredAt": time.Now().UTC().Format(time.RFC3339Nano),
		"correlationId": correlationID.String(), "causationId": nil,
		"payload": map[string]any{"turnId": turnID.String()},
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	outcome := worker.Process(ctx, conversationworker.IncomingMessage{
		ContentType: "application/json", MessageID: messageID.String(),
		CorrelationID: correlationID.String(), Type: conversationworker.MessageType, Body: body,
	})
	if outcome.Action != conversationworker.ActionAck {
		t.Fatalf("worker outcome = %+v", outcome)
	}
}

type semanticCacheIntegrationResponder struct {
	response conversation.AgentResponse
	calls    int
}

func (s *semanticCacheIntegrationResponder) Respond(context.Context, conversation.AgentRequest) (conversation.AgentResponse, error) {
	s.calls++
	return s.response, nil
}
