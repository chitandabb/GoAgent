package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/bootstrap"
	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/externalcase"
	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/platform/chatmodel"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformembedding "github.com/chitandabb/GoAgent/internal/platform/dashscopeembedding"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type qualityChunkKey struct {
	documentKey   string
	ordinal       int
	contentSHA256 string
}

type qualityFixtureChunkRow struct {
	ChunkID           uuid.UUID `gorm:"column:chunk_id"`
	DocumentID        uuid.UUID `gorm:"column:document_id"`
	DocumentVersionID uuid.UUID `gorm:"column:document_version_id"`
	Ordinal           int       `gorm:"column:ordinal"`
	ContentSHA256     string    `gorm:"column:content_sha256"`
	ContentText       string    `gorm:"column:content_text"`
}

func selectAndValidateFixture(
	corpus knowledge.AdvancedRetrievalEvaluationCorpus,
	cases []qualityCaseDefinition,
) (knowledge.AdvancedRetrievalEvaluationCorpus, map[string][]knowledge.ChunkDraft, error) {
	allChunks, err := knowledge.BuildAdvancedRetrievalCorpusChunks(corpus)
	if err != nil {
		return knowledge.AdvancedRetrievalEvaluationCorpus{}, nil, err
	}
	requiredDocuments := make(map[string]struct{})
	for _, definition := range cases {
		for _, ref := range definition.RelevantChunks {
			chunks, exists := allChunks[ref.DocumentKey]
			if !exists || ref.Ordinal < 0 || ref.Ordinal >= len(chunks) ||
				chunks[ref.Ordinal].ContentSHA256 != ref.ContentSHA256 {
				return knowledge.AdvancedRetrievalEvaluationCorpus{}, nil,
					fmt.Errorf("case %s references an unpinned corpus chunk", definition.CaseID)
			}
			requiredDocuments[ref.DocumentKey] = struct{}{}
		}
	}
	fixture := corpus
	fixture.Documents = nil
	selectedChunks := make(map[string][]knowledge.ChunkDraft, len(requiredDocuments))
	for _, document := range corpus.Documents {
		if _, required := requiredDocuments[document.DocumentKey]; !required {
			continue
		}
		fixture.Documents = append(fixture.Documents, document)
		selectedChunks[document.DocumentKey] = allChunks[document.DocumentKey]
	}
	if len(fixture.Documents) != len(requiredDocuments) {
		return knowledge.AdvancedRetrievalEvaluationCorpus{}, nil, errors.New("quality fixture is missing a referenced document")
	}
	return fixture, selectedChunks, nil
}

func seedConversationQualityFixture(
	ctx context.Context,
	tx *gorm.DB,
	cfg config.Config,
	corpus knowledge.AdvancedRetrievalEvaluationCorpus,
	chunksByDocument map[string][]knowledge.ChunkDraft,
) (uuid.UUID, map[qualityChunkKey]mesagent.ConversationQualitySource, int, error) {
	actorID := uuid.New()
	username := "conversation_quality_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	if err := tx.WithContext(ctx).Exec(`
INSERT INTO users (id, username, display_name, password_hash, role, status, must_change_password)
VALUES (?, ?, 'Conversation Quality Evaluation', 'evaluation-only-not-a-login-secret', 'analyst', 'active', false)`,
		actorID, username,
	).Error; err != nil {
		return uuid.Nil, nil, 0, err
	}

	repository := platformpostgres.NewKnowledgeRepository(tx)
	documentKeyByID := make(map[uuid.UUID]string, len(corpus.Documents))
	versionIDs := make([]uuid.UUID, 0, len(corpus.Documents))
	for _, document := range corpus.Documents {
		documentID := stableFixtureUUID(defaultDatasetVersion + ":document:" + document.DocumentKey)
		versionID := stableFixtureUUID(defaultDatasetVersion + ":version:" + document.DocumentKey + ":" + document.ContentSHA256)
		if _, err := repository.CreateDocument(ctx, knowledge.CreateDocumentInput{
			ID: documentID, Scope: knowledge.ScopeGlobal, Title: document.Title, CreatedBy: actorID,
		}); err != nil {
			return uuid.Nil, nil, 0, err
		}
		if _, err := repository.PublishVersion(ctx, knowledge.PublishVersionInput{
			ID: versionID, DocumentID: documentID, SourceMediaType: document.MediaType,
			SourceSizeBytes: int64(len([]byte(document.Content))), SourceSHA256: document.ContentSHA256,
			ParserVersion: corpus.ChunkerVersion, CreatedBy: actorID, Chunks: chunksByDocument[document.DocumentKey],
		}); err != nil {
			return uuid.Nil, nil, 0, err
		}
		documentKeyByID[documentID] = document.DocumentKey
		versionIDs = append(versionIDs, versionID)
	}

	var rows []qualityFixtureChunkRow
	if err := tx.WithContext(ctx).Raw(`
SELECT c.id AS chunk_id, v.document_id, c.document_version_id, c.ordinal,
       c.content_sha256, c.content_text
FROM knowledge_chunks c
JOIN knowledge_document_versions v ON v.id = c.document_version_id
WHERE c.document_version_id IN ?
ORDER BY v.document_id, c.ordinal`, versionIDs).Scan(&rows).Error; err != nil {
		return uuid.Nil, nil, 0, err
	}
	if len(rows) == 0 {
		return uuid.Nil, nil, 0, errors.New("Conversation quality fixture contains no chunks")
	}
	sourceByKey := make(map[qualityChunkKey]mesagent.ConversationQualitySource, len(rows))
	for index := range rows {
		row := &rows[index]
		documentKey, exists := documentKeyByID[row.DocumentID]
		if !exists {
			return uuid.Nil, nil, 0, errors.New("Conversation quality fixture document identity is missing")
		}
		stableChunkID := stableFixtureUUID(fmt.Sprintf(
			"%s:chunk:%s:%d:%s", defaultDatasetVersion, documentKey, row.Ordinal, row.ContentSHA256,
		))
		if err := tx.WithContext(ctx).Exec(
			"UPDATE knowledge_chunks SET id = ? WHERE id = ?", stableChunkID, row.ChunkID,
		).Error; err != nil {
			return uuid.Nil, nil, 0, err
		}
		row.ChunkID = stableChunkID
		sourceByKey[qualityChunkKey{
			documentKey: documentKey, ordinal: row.Ordinal, contentSHA256: row.ContentSHA256,
		}] = mesagent.ConversationQualitySource{
			SourceType:    mesagent.EvidenceSourceKnowledgeChunk,
			SourceRef:     "knowledge:" + row.DocumentVersionID.String() + "/" + row.ChunkID.String(),
			ContentSHA256: row.ContentSHA256, PreviewRequired: true,
		}
	}

	profile, err := cfg.Models.Embedding.Profile()
	if err != nil {
		return uuid.Nil, nil, 0, err
	}
	if err := platformpostgres.NewKnowledgeWorkerRepository(tx).EnsureEmbeddingProfile(ctx, profile); err != nil {
		return uuid.Nil, nil, 0, err
	}
	embedder, err := platformembedding.NewClient(cfg.Models.Embedding, nil)
	if err != nil {
		return uuid.Nil, nil, 0, err
	}
	embeddingTokens, err := indexConversationQualityEmbeddings(ctx, tx, profile, embedder, rows, cfg.Models.Embedding.BatchSize)
	if err != nil {
		return uuid.Nil, nil, 0, err
	}
	return actorID, sourceByKey, embeddingTokens, nil
}

func indexConversationQualityEmbeddings(
	ctx context.Context,
	tx *gorm.DB,
	profile knowledge.EmbeddingProfile,
	embedder knowledge.Embedder,
	rows []qualityFixtureChunkRow,
	batchSize int,
) (int, error) {
	if batchSize < 1 || batchSize > 10 {
		batchSize = 10
	}
	totalTokens := 0
	for start := 0; start < len(rows); start += batchSize {
		end := min(start+batchSize, len(rows))
		texts := make([]string, end-start)
		for index := start; index < end; index++ {
			texts[index-start] = rows[index].ContentText
		}
		result, err := embedder.Embed(ctx, knowledge.EmbeddingRequest{
			Texts: texts, InputType: profile.DocumentInputType,
		})
		if err != nil {
			return 0, fmt.Errorf("embed Conversation quality fixture: %w", err)
		}
		if err := result.Validate(len(texts), profile.Dimensions, profile.Normalize); err != nil {
			return 0, err
		}
		totalTokens += result.Usage.TotalTokens
		for offset, vector := range result.Vectors {
			row := rows[start+offset]
			if err := tx.WithContext(ctx).Exec(`
INSERT INTO knowledge_chunk_embeddings (chunk_id, profile_id, content_sha256, embedding)
VALUES (?, ?, ?, ?)`, row.ChunkID, profile.ID, row.ContentSHA256, pgvector.NewVector(vector)).Error; err != nil {
				return 0, err
			}
		}
	}
	return totalTokens, nil
}

func resolveQualityCases(
	definitions []qualityCaseDefinition,
	sourceByKey map[qualityChunkKey]mesagent.ConversationQualitySource,
) ([]mesagent.ConversationQualityCase, error) {
	resolved := make([]mesagent.ConversationQualityCase, 0, len(definitions))
	for _, definition := range definitions {
		item := mesagent.ConversationQualityCase{
			DatasetVersion: definition.DatasetVersion, CaseID: definition.CaseID,
			UserQuery: definition.UserQuery, RetrievalMaxResults: definition.effectiveRetrievalMaxResults(),
			RequiredAnswerTerms:  append([]string(nil), definition.RequiredAnswerTerms...),
			ForbiddenAnswerTerms: append([]string(nil), definition.ForbiddenAnswerTerms...),
			ExpectedOutcome:      definition.ExpectedOutcome, Tags: append([]string(nil), definition.Tags...),
		}
		for _, ref := range definition.RelevantChunks {
			source, exists := sourceByKey[qualityChunkKey{
				documentKey: ref.DocumentKey, ordinal: ref.Ordinal, contentSHA256: ref.ContentSHA256,
			}]
			if !exists {
				return nil, fmt.Errorf("case %s could not resolve a relevant source", definition.CaseID)
			}
			item.RelevantSources = append(item.RelevantSources, source)
		}
		for _, ref := range definition.effectiveRequiredCitationChunks() {
			source, exists := sourceByKey[qualityChunkKey{
				documentKey: ref.DocumentKey, ordinal: ref.Ordinal, contentSHA256: ref.ContentSHA256,
			}]
			if !exists {
				return nil, fmt.Errorf("case %s could not resolve a required citation source", definition.CaseID)
			}
			item.RequiredCitationRefs = append(item.RequiredCitationRefs, source.SourceRef)
		}
		if err := item.Validate(); err != nil {
			return nil, fmt.Errorf("resolve case %s: %w", definition.CaseID, err)
		}
		resolved = append(resolved, item)
	}
	return resolved, nil
}

func buildConversationQualityRunner(
	ctx context.Context,
	tx *gorm.DB,
	cfg config.Config,
	options commandOptions,
	retrievalMaxResults int,
) (*mesagent.ConversationRunner, *knowledge.CitationService, *qualityModelDiagnostics, error) {
	searchService, err := buildQualitySearchService(ctx, tx, cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	searchTool, err := mesagent.NewSearchKnowledgeTool(searchService)
	if err != nil {
		return nil, nil, nil, err
	}
	searchTool, err = newBoundedQualitySearchTool(searchTool, retrievalMaxResults)
	if err != nil {
		return nil, nil, nil, err
	}
	catalog, err := mesagent.NewDefaultToolCatalog(ctx, mesagent.DefaultToolCatalogDependencies{
		ExternalCases: unavailableExternalCaseGetter{}, KnowledgeSearch: searchTool,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	var modelInstance *chatmodel.Instance
	if options.chatProfile == "" {
		modelInstance, err = chatmodel.NewActive(ctx, cfg.Models.Chat)
	} else {
		modelInstance, err = chatmodel.NewProfile(ctx, cfg.Models.Chat, options.chatProfile)
	}
	if err != nil {
		return nil, nil, nil, err
	}
	prompts, err := cfg.Agent.LoadPrompts()
	if err != nil {
		return nil, nil, nil, err
	}
	qualityModel := newSingleSearchQualityModel(modelInstance.Model)
	var citationRepairer mesagent.ConversationCitationRepairer
	if cfg.Agent.ConversationCitationRepairEnabled {
		citationRepairer, err = mesagent.NewModelConversationCitationRepairer(
			mesagent.ModelConversationCitationRepairerConfig{
				ChatModel:       modelInstance.Model,
				Instruction:     prompts.ConversationCitationRepairInstruction,
				PromptVersion:   cfg.Agent.ConversationCitationRepairPromptVersion,
				Timeout:         time.Duration(cfg.Agent.ConversationCitationRepairTimeoutMillis) * time.Millisecond,
				MaxOutputTokens: cfg.Agent.ConversationCitationRepairMaxOutputTokens,
			},
		)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	runner, err := mesagent.NewConversationRunner(mesagent.ConversationRunnerConfig{
		ChatModel: qualityModel, CitationRepairer: citationRepairer, ToolCatalog: catalog,
		SystemInstruction: prompts.ConversationInstruction,
		ModelProvider:     modelInstance.Identity.Provider, ModelID: modelInstance.Identity.ModelID,
		PromptVersion:         cfg.Agent.ConversationPromptVersion,
		AvailableDependencies: []mesagent.ToolDependency{mesagent.ToolDependencyKnowledge},
		Logger:                zap.NewNop(), MaxIterations: min(cfg.Agent.ConversationMaxIterations, 3),
		MaxToolCalls: 2, MaxTotalTokens: options.maxChatTokensPerCase,
		MaxContextRunes: cfg.Agent.ConversationMaxContextRunes,
		Timeout:         time.Duration(cfg.Agent.ConversationTimeoutMillis) * time.Millisecond,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	preview, err := knowledge.NewCitationService(platformpostgres.NewKnowledgeCitationRepository(tx))
	if err != nil {
		return nil, nil, nil, err
	}
	return runner, preview, qualityModel.diagnostics, nil
}

func buildQualitySearchService(ctx context.Context, tx *gorm.DB, cfg config.Config) (*knowledge.SearchService, error) {
	// BuildKnowledgeSearchService is intentionally called through a tiny local
	// wrapper so the observer and production runtime share the same chain.
	return bootstrap.BuildKnowledgeSearchService(ctx, tx, cfg, nil, zap.NewNop())
}

func observeQualityCase(
	ctx context.Context,
	runner *mesagent.ConversationRunner,
	preview *knowledge.CitationService,
	modelDiagnostics *qualityModelDiagnostics,
	actorID uuid.UUID,
	definition mesagent.ConversationQualityCase,
	options commandOptions,
) (mesagent.ConversationQualityObservation, float64, error) {
	conversationID, messageID := uuid.New(), uuid.New()
	now := time.Now().UTC()
	current := conversation.Message{
		ID: messageID, ConversationID: conversationID, Seq: 1,
		Role: conversation.MessageRoleUser, Content: definition.UserQuery,
		ContentSchemaVersion: 1, CreatedAt: now,
	}
	request := conversation.AgentRequest{
		Conversation: conversation.Conversation{
			ID: conversationID, UserID: actorID, Title: "Conversation quality: " + definition.CaseID,
			Status: conversation.StatusActive, CreatedAt: now, UpdatedAt: now,
		},
		UserMessage: current,
	}
	runCtx := conversation.WithCommandContext(ctx, conversation.CommandContext{
		ConversationID: conversationID, UserMessageID: messageID,
		Actor: conversation.Actor{UserID: actorID},
	})
	diagnosticStart := modelDiagnostics.count()
	response, runErr := runner.Respond(runCtx, request)
	if err := printQualityModelDiagnostics(
		definition.CaseID, conversationRunErrorType(runErr), modelDiagnostics.snapshotFrom(diagnosticStart),
	); err != nil {
		return mesagent.ConversationQualityObservation{}, 0, err
	}
	recorded, err := newRecordedRun(definition, response, runErr)
	if err != nil {
		return mesagent.ConversationQualityObservation{}, 0, err
	}
	cost := onlineRunCost(recorded, definition.UserQuery, options)
	previews := make(map[string]string, len(recorded.Citations))
	for _, citation := range recorded.Citations {
		chunkID, err := parseKnowledgeChunkSourceRef(citation.SourceRef)
		if err != nil {
			return mesagent.ConversationQualityObservation{}, 0, err
		}
		item, err := preview.Get(runCtx, actorID, chunkID)
		if err != nil {
			return mesagent.ConversationQualityObservation{}, 0, err
		}
		if item.ContentSHA256 != citation.ContentSHA256 {
			return mesagent.ConversationQualityObservation{}, 0, errors.New("citation preview hash drifted from the Agent response")
		}
		previews[citation.SourceRef] = item.ContentSHA256
	}
	observation, err := mesagent.BuildRecordedConversationQualityObservation(
		definition, recorded, mesagent.ConversationQualityRecordedRunSelection{
			CaseID: definition.CaseID, TurnID: recorded.TurnID.String(),
			EstimatedCostCNY: cost, PreviewContentSHA256ByRef: previews,
		},
	)
	return observation, cost, err
}

func conversationRunErrorType(err error) string {
	if err == nil {
		return ""
	}
	if failure, ok := conversation.AgentRunFailureRecordFrom(err); ok {
		return failure.ErrorType
	}
	return boundedDiagnosticLabel(fmt.Sprintf("%T", err))
}

func printQualityModelDiagnostics(
	caseID string,
	terminalErrorType string,
	calls []qualityModelCallDiagnostic,
) error {
	payload := struct {
		CaseID            string                       `json:"caseId"`
		TerminalErrorType string                       `json:"terminalErrorType,omitempty"`
		Calls             []qualityModelCallDiagnostic `json:"calls"`
	}{
		CaseID: boundedDiagnosticLabel(caseID), TerminalErrorType: boundedDiagnosticLabel(terminalErrorType),
		Calls: calls,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return errors.New("encode Conversation quality model diagnostics")
	}
	fmt.Printf("conversation_quality_model_shape %s\n", encoded)
	return nil
}

func parseKnowledgeChunkSourceRef(sourceRef string) (uuid.UUID, error) {
	if !strings.HasPrefix(sourceRef, "knowledge:") {
		return uuid.Nil, errors.New("Conversation quality run cited a non-knowledge source")
	}
	parts := strings.Split(strings.TrimPrefix(sourceRef, "knowledge:"), "/")
	if len(parts) != 2 {
		return uuid.Nil, errors.New("Conversation quality run returned an invalid knowledge source ref")
	}
	chunkID, err := uuid.Parse(parts[1])
	if err != nil || chunkID == uuid.Nil {
		return uuid.Nil, errors.New("Conversation quality run returned an invalid chunk id")
	}
	return chunkID, nil
}

func stableFixtureUUID(value string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(value))
}

type unavailableExternalCaseGetter struct{}

func (unavailableExternalCaseGetter) Get(context.Context, uuid.UUID) (*externalcase.ExternalCase, error) {
	return nil, errors.New("external cases are unavailable in the knowledge-only quality observer")
}

func uniqueDocumentKeys(cases []qualityCaseDefinition) []string {
	seen := make(map[string]struct{})
	for _, definition := range cases {
		for _, ref := range definition.RelevantChunks {
			seen[ref.DocumentKey] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for key := range seen {
		result = append(result, key)
	}
	slices.Sort(result)
	return result
}
