package httptransport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/apperror"
	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/repository"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type conversationUseCase interface {
	Create(ctx context.Context, actor conversation.Actor, input conversation.CreateInput) (conversation.Conversation, error)
	Get(ctx context.Context, actor conversation.Actor, conversationID uuid.UUID) (conversation.Conversation, error)
	List(ctx context.Context, actor conversation.Actor, query conversation.ListQuery) (conversation.ListResult, error)
	ListMessages(ctx context.Context, actor conversation.Actor, conversationID uuid.UUID, query conversation.MessageQuery) (conversation.MessagePage, error)
	AppendUserMessage(ctx context.Context, actor conversation.Actor, input conversation.AppendMessageInput) (conversation.Message, error)
	AcceptTurn(ctx context.Context, actor conversation.Actor, idempotencyKey string, input conversation.AppendMessageInput) (conversation.ConversationTurnResult, error)
	GetTurn(ctx context.Context, actor conversation.Actor, conversationID, turnID uuid.UUID) (conversation.TurnDetail, error)
	ListTurnEvents(ctx context.Context, actor conversation.Actor, conversationID, turnID uuid.UUID, afterSeq int64, limit int) (conversation.TurnEventPage, error)
	OpenTurnEventStream(ctx context.Context, actor conversation.Actor, conversationID, turnID uuid.UUID) (conversation.TurnEventStream, error)
}

type ConversationRoutes struct {
	useCase              conversationUseCase
	auth                 gin.HandlerFunc
	csrf                 gin.HandlerFunc
	lifecycle            context.Context
	ssePollInterval      time.Duration
	sseHeartbeatInterval time.Duration
}

func NewConversationRoutes(lifecycle context.Context, useCase conversationUseCase, auth, csrf gin.HandlerFunc) (*ConversationRoutes, error) {
	if lifecycle == nil || useCase == nil || auth == nil || csrf == nil {
		return nil, errors.New("conversation route dependencies are nil")
	}
	return &ConversationRoutes{
		useCase: useCase, auth: auth, csrf: csrf, lifecycle: lifecycle,
		ssePollInterval: defaultSSEPollInterval, sseHeartbeatInterval: defaultSSEHeartbeatInterval,
	}, nil
}

func (r *ConversationRoutes) Register(api *gin.RouterGroup) {
	protected := api.Group("/conversations")
	protected.Use(r.auth)
	protected.GET("", r.list)
	protected.GET("/:conversationId", r.get)
	protected.GET("/:conversationId/messages", r.listMessages)
	protected.GET("/:conversationId/turns/:turnId", r.getTurn)
	protected.GET("/:conversationId/turns/:turnId/events", r.listTurnEvents)

	commands := protected.Group("")
	commands.Use(r.csrf)
	commands.POST("", r.create)
	commands.POST("/:conversationId/messages", r.appendMessage)
	commands.POST("/:conversationId/turns", r.appendTurn)
}

type conversationCreateRequest struct {
	Title string `json:"title" binding:"max=200"`
}

type conversationMessageRequest struct {
	Content        string                             `json:"content" binding:"required,max=20000"`
	CaseReferences []conversationCaseReferenceRequest `json:"caseReferences"`
	TaskReferences []conversationTaskReferenceRequest `json:"taskReferences"`
	Attachments    []conversationAttachmentRequest    `json:"attachments"`
}

type conversationCaseReferenceRequest struct {
	ExternalCaseID string `json:"externalCaseId" binding:"required,uuid"`
	Kind           string `json:"kind" binding:"omitempty,oneof=selected mentioned"`
}

type conversationTaskReferenceRequest struct {
	TaskID string `json:"taskId" binding:"required,uuid"`
	Kind   string `json:"kind" binding:"omitempty,oneof=created referenced"`
}

type conversationAttachmentRequest struct {
	AttachmentID string `json:"attachmentId" binding:"required,uuid"`
	Purpose      string `json:"purpose" binding:"omitempty,max=64"`
}

type conversationResponse struct {
	ID               string              `json:"id"`
	Title            string              `json:"title"`
	FirstUserMessage string              `json:"firstUserMessage,omitempty"`
	Status           conversation.Status `json:"status"`
	CreatedAt        time.Time           `json:"createdAt"`
	UpdatedAt        time.Time           `json:"updatedAt"`
	LastMessageAt    *time.Time          `json:"lastMessageAt,omitempty"`
}

type conversationMessageResponse struct {
	ID                   string                            `json:"id"`
	ConversationID       string                            `json:"conversationId"`
	Seq                  int64                             `json:"seq"`
	Role                 conversation.MessageRole          `json:"role"`
	Content              string                            `json:"content"`
	ContentSchemaVersion int                               `json:"contentSchemaVersion"`
	CaseReferences       []conversationCaseReferenceResp   `json:"caseReferences"`
	TaskReferences       []conversationTaskReferenceResp   `json:"taskReferences"`
	Attachments          []conversationAttachmentResp      `json:"attachments"`
	Citations            []conversationCitationResp        `json:"citations"`
	TurnID               *string                           `json:"turnId,omitempty"`
	Provenance           *conversationAnswerProvenanceResp `json:"provenance,omitempty"`
	CreatedAt            time.Time                         `json:"createdAt"`
}

type conversationAnswerSourceCountResp struct {
	SourceType conversation.CitationSourceType `json:"sourceType"`
	Count      int                             `json:"count"`
}

type conversationAnswerProvenanceResp struct {
	ExecutionPath  conversation.AgentRunExecutionPath  `json:"executionPath"`
	CacheLayer     conversation.AgentRunCacheLayer     `json:"cacheLayer,omitempty"`
	Outcome        conversation.AgentRunOutcome        `json:"outcome"`
	ToolCalls      int                                 `json:"toolCalls"`
	DurationMillis int64                               `json:"durationMillis"`
	Sources        []conversationAnswerSourceCountResp `json:"sources"`
}

type conversationTurnResponse struct {
	TurnID           string                       `json:"turnId"`
	Status           conversation.TurnStatus      `json:"status"`
	UserMessage      conversationMessageResponse  `json:"userMessage"`
	AssistantMessage *conversationMessageResponse `json:"assistantMessage,omitempty"`
	Replayed         bool                         `json:"replayed"`
}

type conversationTurnDetailResponse struct {
	TurnID             string                  `json:"turnId"`
	ConversationID     string                  `json:"conversationId"`
	Status             conversation.TurnStatus `json:"status"`
	UserMessageID      string                  `json:"userMessageId"`
	AssistantMessageID *string                 `json:"assistantMessageId,omitempty"`
	AttemptCount       int                     `json:"attemptCount"`
	FailureSummary     string                  `json:"failureSummary,omitempty"`
	RetryAt            *time.Time              `json:"retryAt,omitempty"`
	CreatedAt          time.Time               `json:"createdAt"`
	UpdatedAt          time.Time               `json:"updatedAt"`
	CompletedAt        *time.Time              `json:"completedAt,omitempty"`
}

type conversationTurnEventResponse struct {
	Seq                  int64          `json:"seq"`
	EventType            string         `json:"eventType"`
	Payload              map[string]any `json:"payload"`
	PayloadSchemaVersion int            `json:"payloadSchemaVersion"`
	CreatedAt            string         `json:"createdAt"`
}

type conversationTurnEventsResponse struct {
	Items        []conversationTurnEventResponse `json:"items"`
	AfterSeq     int64                           `json:"afterSeq"`
	NextAfterSeq int64                           `json:"nextAfterSeq"`
	HasMore      bool                            `json:"hasMore"`
}

type conversationCaseReferenceResp struct {
	ExternalCaseID string                     `json:"externalCaseId"`
	Kind           conversation.ReferenceKind `json:"kind"`
}

type conversationTaskReferenceResp struct {
	TaskID string                     `json:"taskId"`
	Kind   conversation.ReferenceKind `json:"kind"`
}

type conversationAttachmentResp struct {
	AttachmentID  string `json:"attachmentId"`
	Position      int    `json:"position"`
	Purpose       string `json:"purpose"`
	OriginalName  string `json:"originalName"`
	MediaType     string `json:"mediaType"`
	SizeBytes     int64  `json:"sizeBytes"`
	ContentSHA256 string `json:"contentSha256"`
	Status        string `json:"status"`
}

type conversationCitationResp struct {
	Position      int                             `json:"position"`
	SourceType    conversation.CitationSourceType `json:"sourceType"`
	SourceRef     string                          `json:"sourceRef"`
	ContentSHA256 string                          `json:"contentSha256"`
}

func (r *ConversationRoutes) list(c *gin.Context) {
	request, ok := BindQuery[conversationListRequest](c)
	if !ok {
		return
	}
	request.PageQuery.Normalize()
	identity, ok := identityFromContext(c)
	if !ok {
		AbortWithError(c, apperror.New(apperror.CodeUnauthorized))
		return
	}
	result, err := r.useCase.List(c.Request.Context(), conversationActor(identity), conversation.ListQuery{
		Page: request.Page, PageSize: request.PageSize,
	})
	if err != nil {
		AbortWithError(c, translateConversationError("list conversations", err))
		return
	}
	items := make([]conversationResponse, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, conversationResponseFrom(item))
	}
	WriteSuccess(c, PageData[conversationResponse]{
		Items: items, Page: request.Page, PageSize: request.PageSize, Total: result.Total,
	})
}

type conversationListRequest struct{ PageQuery }

func (r *ConversationRoutes) create(c *gin.Context) {
	request, ok := BindJSON[conversationCreateRequest](c)
	if !ok {
		return
	}
	identity, ok := identityFromContext(c)
	if !ok {
		AbortWithError(c, apperror.New(apperror.CodeUnauthorized))
		return
	}
	item, err := r.useCase.Create(c.Request.Context(), conversationActor(identity), conversation.CreateInput{
		Title: strings.TrimSpace(request.Title),
	})
	if err != nil {
		AbortWithError(c, translateConversationError("create conversation", err))
		return
	}
	c.Header("Location", "/api/v1/conversations/"+item.ID.String())
	WriteSuccessWithStatus(c, http.StatusCreated, conversationResponseFrom(item))
}

func (r *ConversationRoutes) get(c *gin.Context) {
	conversationID, ok := parseConversationID(c)
	if !ok {
		return
	}
	identity, ok := identityFromContext(c)
	if !ok {
		AbortWithError(c, apperror.New(apperror.CodeUnauthorized))
		return
	}
	item, err := r.useCase.Get(c.Request.Context(), conversationActor(identity), conversationID)
	if err != nil {
		AbortWithError(c, translateConversationError("get conversation", err))
		return
	}
	WriteSuccess(c, conversationResponseFrom(item))
}

type conversationMessageListRequest struct {
	AfterSeq int64 `form:"afterSeq"`
	Limit    int   `form:"limit"`
}

func (r *ConversationRoutes) listMessages(c *gin.Context) {
	conversationID, ok := parseConversationID(c)
	if !ok {
		return
	}
	request, ok := BindQuery[conversationMessageListRequest](c)
	if !ok {
		return
	}
	identity, ok := identityFromContext(c)
	if !ok {
		AbortWithError(c, apperror.New(apperror.CodeUnauthorized))
		return
	}
	page, err := r.useCase.ListMessages(c.Request.Context(), conversationActor(identity), conversationID, conversation.MessageQuery{
		AfterSeq: request.AfterSeq, Limit: request.Limit,
	})
	if err != nil {
		AbortWithError(c, translateConversationError("list conversation messages", err))
		return
	}
	responses := make([]conversationMessageResponse, 0, len(page.Items))
	for _, item := range page.Items {
		responses = append(responses, conversationMessageResponseFrom(item))
	}
	WriteSuccess(c, gin.H{"items": responses, "afterSeq": page.AfterSeq, "nextAfterSeq": page.NextAfterSeq, "hasMore": page.HasMore})
}

func (r *ConversationRoutes) getTurn(c *gin.Context) {
	conversationID, ok := parseConversationID(c)
	if !ok {
		return
	}
	turnID, err := uuid.Parse(c.Param("turnId"))
	if err != nil {
		AbortWithError(c, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
			Field: "turnId", Reason: "必须是合法的 UUID",
		}}))
		return
	}
	identity, ok := identityFromContext(c)
	if !ok {
		AbortWithError(c, apperror.New(apperror.CodeUnauthorized))
		return
	}
	turn, err := r.useCase.GetTurn(c.Request.Context(), conversationActor(identity), conversationID, turnID)
	if err != nil {
		AbortWithError(c, translateConversationError("get conversation turn", err))
		return
	}
	WriteSuccess(c, conversationTurnDetailResponseFrom(turn))
}

func (r *ConversationRoutes) listTurnEvents(c *gin.Context) {
	conversationID, ok := parseConversationID(c)
	if !ok {
		return
	}
	turnID, err := uuid.Parse(c.Param("turnId"))
	if err != nil {
		AbortWithError(c, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
			Field: "turnId", Reason: "必须是合法的 UUID",
		}}))
		return
	}
	query, ok := BindQuery[conversationTurnEventsQuery](c)
	if !ok {
		return
	}
	identity, ok := identityFromContext(c)
	if !ok {
		AbortWithError(c, apperror.New(apperror.CodeUnauthorized))
		return
	}
	actor := conversationActor(identity)
	if acceptsEventStream(c.GetHeader("Accept")) {
		afterSeq, cursorErr := taskEventStreamAfterSeq(c, query.AfterSeq)
		if cursorErr != nil {
			AbortWithError(c, cursorErr)
			return
		}
		batchLimit := query.Limit
		if batchLimit == 0 {
			batchLimit = conversation.DefaultTurnEventLimit
		}
		r.streamTurnEvents(c, actor, conversationID, turnID, afterSeq, batchLimit)
		return
	}
	page, err := r.useCase.ListTurnEvents(c.Request.Context(), actor, conversationID, turnID, query.AfterSeq, query.Limit)
	if err != nil {
		AbortWithError(c, translateConversationError("list conversation turn events", err))
		return
	}
	WriteSuccess(c, conversationTurnEventsResponseFrom(page))
}

type conversationTurnEventsQuery struct {
	AfterSeq int64 `form:"afterSeq" binding:"omitempty,min=0"`
	Limit    int   `form:"limit" binding:"omitempty,min=1,max=200"`
}

func (r *ConversationRoutes) appendMessage(c *gin.Context) {
	conversationID, ok := parseConversationID(c)
	if !ok {
		return
	}
	request, ok := BindJSON[conversationMessageRequest](c)
	if !ok {
		return
	}
	identity, ok := identityFromContext(c)
	if !ok {
		AbortWithError(c, apperror.New(apperror.CodeUnauthorized))
		return
	}
	input, err := conversationMessageInput(conversationID, request)
	if err != nil {
		AbortWithError(c, err)
		return
	}
	item, err := r.useCase.AppendUserMessage(c.Request.Context(), conversationActor(identity), input)
	if err != nil {
		AbortWithError(c, translateConversationError("append conversation message", err))
		return
	}
	WriteSuccessWithStatus(c, http.StatusCreated, conversationMessageResponseFrom(item))
}

func (r *ConversationRoutes) appendTurn(c *gin.Context) {
	conversationID, ok := parseConversationID(c)
	if !ok {
		return
	}
	request, ok := BindJSON[conversationMessageRequest](c)
	if !ok {
		return
	}
	identity, ok := identityFromContext(c)
	if !ok {
		AbortWithError(c, apperror.New(apperror.CodeUnauthorized))
		return
	}
	input, err := conversationMessageInput(conversationID, request)
	if err != nil {
		AbortWithError(c, err)
		return
	}
	idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if _, err := uuid.Parse(idempotencyKey); err != nil {
		AbortWithError(c, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
			Field: "Idempotency-Key", Reason: "必须是合法的 UUID",
		}}))
		return
	}
	result, err := r.useCase.AcceptTurn(c.Request.Context(), conversationActor(identity), idempotencyKey, input)
	if err != nil {
		AbortWithError(c, translateConversationError("append conversation turn", err))
		return
	}
	status := http.StatusAccepted
	var assistantMessage *conversationMessageResponse
	if result.Status == conversation.TurnStatusCompleted {
		status = http.StatusOK
		value := conversationMessageResponseFrom(result.Turn.AssistantMessage)
		assistantMessage = &value
	}
	WriteSuccessWithStatus(c, status, conversationTurnResponse{
		TurnID:           result.TurnID.String(),
		Status:           result.Status,
		UserMessage:      conversationMessageResponseFrom(result.Turn.UserMessage),
		AssistantMessage: assistantMessage,
		Replayed:         result.Replayed,
	})
}

func conversationMessageInput(conversationID uuid.UUID, request conversationMessageRequest) (conversation.AppendMessageInput, error) {
	caseReferences, err := parseConversationCaseReferences(request.CaseReferences)
	if err != nil {
		return conversation.AppendMessageInput{}, err
	}
	taskReferences, err := parseConversationTaskReferences(request.TaskReferences)
	if err != nil {
		return conversation.AppendMessageInput{}, err
	}
	attachments, err := parseConversationAttachments(request.Attachments)
	if err != nil {
		return conversation.AppendMessageInput{}, err
	}
	return conversation.AppendMessageInput{
		ConversationID: conversationID,
		Content:        request.Content,
		CaseReferences: caseReferences,
		TaskReferences: taskReferences,
		Attachments:    attachments,
	}, nil
}

func conversationActor(identity auth.Identity) conversation.Actor {
	return conversation.Actor{
		UserID:  identity.User.ID,
		IsAdmin: identity.User.Role == auth.RoleAdmin,
	}
}

func parseConversationID(c *gin.Context) (uuid.UUID, bool) {
	value, err := uuid.Parse(c.Param("conversationId"))
	if err != nil {
		AbortWithError(c, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
			Field: "conversationId", Reason: "必须是合法的 UUID",
		}}))
		return uuid.Nil, false
	}
	return value, true
}

func parseConversationCaseReferences(values []conversationCaseReferenceRequest) ([]conversation.CaseReference, error) {
	result := make([]conversation.CaseReference, 0, len(values))
	for _, value := range values {
		id, err := uuid.Parse(strings.TrimSpace(value.ExternalCaseID))
		if err != nil {
			return nil, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{Field: "caseReferences.externalCaseId", Reason: "必须是合法的 UUID"}})
		}
		kind := conversation.ReferenceKind(strings.TrimSpace(value.Kind))
		if kind == "" {
			kind = conversation.ReferenceKindSelected
		}
		result = append(result, conversation.CaseReference{ExternalCaseID: id, Kind: kind})
	}
	return result, nil
}

func parseConversationTaskReferences(values []conversationTaskReferenceRequest) ([]conversation.TaskReference, error) {
	result := make([]conversation.TaskReference, 0, len(values))
	for _, value := range values {
		id, err := uuid.Parse(strings.TrimSpace(value.TaskID))
		if err != nil {
			return nil, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{Field: "taskReferences.taskId", Reason: "必须是合法的 UUID"}})
		}
		kind := conversation.ReferenceKind(strings.TrimSpace(value.Kind))
		if kind == "" {
			kind = conversation.ReferenceKindReferenced
		}
		result = append(result, conversation.TaskReference{TaskID: id, Kind: kind})
	}
	return result, nil
}

func parseConversationAttachments(values []conversationAttachmentRequest) ([]conversation.MessageAttachmentInput, error) {
	if len(values) > conversation.MaxAttachmentsPerMessage {
		return nil, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
			Field: "attachments", Reason: "单条消息附件不能超过 8 个",
		}})
	}
	result := make([]conversation.MessageAttachmentInput, 0, len(values))
	for _, value := range values {
		id, err := uuid.Parse(strings.TrimSpace(value.AttachmentID))
		if err != nil {
			return nil, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{Field: "attachments.attachmentId", Reason: "必须是合法的 UUID"}})
		}
		result = append(result, conversation.MessageAttachmentInput{
			AttachmentID: id, Purpose: strings.TrimSpace(value.Purpose),
		})
	}
	return result, nil
}

func conversationResponseFrom(item conversation.Conversation) conversationResponse {
	return conversationResponse{
		ID: item.ID.String(), Title: item.Title, FirstUserMessage: item.FirstUserMessage, Status: item.Status,
		CreatedAt: item.CreatedAt.UTC(), UpdatedAt: item.UpdatedAt.UTC(), LastMessageAt: item.LastMessageAt,
	}
}

func conversationMessageResponseFrom(item conversation.Message) conversationMessageResponse {
	cases := make([]conversationCaseReferenceResp, 0, len(item.CaseReferences))
	for _, ref := range item.CaseReferences {
		cases = append(cases, conversationCaseReferenceResp{ExternalCaseID: ref.ExternalCaseID.String(), Kind: ref.Kind})
	}
	tasks := make([]conversationTaskReferenceResp, 0, len(item.TaskReferences))
	for _, ref := range item.TaskReferences {
		tasks = append(tasks, conversationTaskReferenceResp{TaskID: ref.TaskID.String(), Kind: ref.Kind})
	}
	attachments := make([]conversationAttachmentResp, 0, len(item.Attachments))
	for _, ref := range item.Attachments {
		attachments = append(attachments, conversationAttachmentResp{
			AttachmentID: ref.AttachmentID.String(), Position: ref.Position, Purpose: ref.Purpose,
			OriginalName: ref.OriginalName, MediaType: ref.MediaType, SizeBytes: ref.SizeBytes,
			ContentSHA256: ref.ContentSHA256, Status: ref.Status,
		})
	}
	citations := make([]conversationCitationResp, 0, len(item.Citations))
	for _, citation := range item.Citations {
		citations = append(citations, conversationCitationResp{
			Position: citation.Position, SourceType: citation.SourceType,
			SourceRef: citation.SourceRef, ContentSHA256: citation.ContentSHA256,
		})
	}
	response := conversationMessageResponse{
		ID: item.ID.String(), ConversationID: item.ConversationID.String(), Seq: item.Seq,
		Role: item.Role, Content: item.Content, ContentSchemaVersion: item.ContentSchemaVersion,
		CaseReferences: cases, TaskReferences: tasks, Attachments: attachments,
		Citations: citations, CreatedAt: item.CreatedAt.UTC(),
	}
	if item.TurnID != nil {
		turnID := item.TurnID.String()
		response.TurnID = &turnID
	}
	if item.Provenance != nil {
		sources := make([]conversationAnswerSourceCountResp, 0, len(item.Provenance.Sources))
		for _, source := range item.Provenance.Sources {
			sources = append(sources, conversationAnswerSourceCountResp{
				SourceType: source.SourceType, Count: source.Count,
			})
		}
		response.Provenance = &conversationAnswerProvenanceResp{
			ExecutionPath: item.Provenance.ExecutionPath, CacheLayer: item.Provenance.CacheLayer,
			Outcome: item.Provenance.Outcome, ToolCalls: item.Provenance.ToolCalls,
			DurationMillis: item.Provenance.DurationMillis, Sources: sources,
		}
	}
	return response
}

func conversationTurnDetailResponseFrom(item conversation.TurnDetail) conversationTurnDetailResponse {
	response := conversationTurnDetailResponse{
		TurnID: item.ID.String(), ConversationID: item.ConversationID.String(), Status: item.Status,
		UserMessageID: item.UserMessageID.String(), AttemptCount: item.AttemptCount,
		FailureSummary: item.FailureSummary, RetryAt: item.RetryAt,
		CreatedAt: item.CreatedAt.UTC(), UpdatedAt: item.UpdatedAt.UTC(), CompletedAt: item.CompletedAt,
	}
	if item.AssistantMessageID != nil {
		value := item.AssistantMessageID.String()
		response.AssistantMessageID = &value
	}
	return response
}

func conversationTurnEventResponseFrom(item conversation.TurnEvent) conversationTurnEventResponse {
	payload := item.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	return conversationTurnEventResponse{
		Seq: item.Seq, EventType: string(item.EventType), Payload: payload,
		PayloadSchemaVersion: item.PayloadSchemaVersion,
		CreatedAt:            item.CreatedAt.UTC().Format(timeRFC3339Nano),
	}
}

func conversationTurnEventsResponseFrom(page conversation.TurnEventPage) conversationTurnEventsResponse {
	items := make([]conversationTurnEventResponse, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, conversationTurnEventResponseFrom(item))
	}
	return conversationTurnEventsResponse{
		Items: items, AfterSeq: page.AfterSeq, NextAfterSeq: page.NextAfterSeq, HasMore: page.HasMore,
	}
}

func translateConversationError(operation string, err error) error {
	var appErr *apperror.Error
	if errors.As(err, &appErr) {
		return appErr
	}
	switch {
	case errors.Is(err, conversation.ErrInvalidConversation), errors.Is(err, conversation.ErrInvalidMessage):
		return apperror.Wrap(apperror.CodeValidationFailed, err)
	case errors.Is(err, conversation.ErrConversationArchived):
		return apperror.NewWithMessage(apperror.CodeConflict, "会话已归档，不能继续追加消息")
	case errors.Is(err, conversation.ErrCommandNotLatest):
		return apperror.NewWithMessage(apperror.CodeConflict, "该消息已不是会话中的最新用户消息")
	case errors.Is(err, conversation.ErrAgentUnavailable):
		return apperror.Wrap(apperror.CodeDependencyUnavailable, err)
	case errors.Is(err, conversation.ErrAsyncTurnUnavailable):
		return apperror.Wrap(apperror.CodeDependencyUnavailable, err)
	case errors.Is(err, conversation.ErrAgentResponseInvalid):
		return apperror.Wrap(apperror.CodeInternal, err)
	case errors.Is(err, conversation.ErrTurnIdempotencyConflict):
		return apperror.Wrap(apperror.CodeConflict, err)
	case errors.Is(err, conversation.ErrTurnInProgress):
		return apperror.Wrap(apperror.CodeConflict, err)
	case errors.Is(err, conversation.ErrTurnLeaseLost):
		return apperror.Wrap(apperror.CodeConflict, err)
	case errors.Is(err, repository.ErrNotFound), errors.Is(err, conversation.ErrReferenceNotFound):
		return apperror.Wrap(apperror.CodeNotFound, err)
	default:
		return apperror.Wrap(apperror.CodeInternal, fmt.Errorf("%s: %w", operation, err))
	}
}

var _ conversationUseCase = (*conversation.Service)(nil)
