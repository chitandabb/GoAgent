// Package conversation defines the durable conversation workspace boundary.
package conversation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/chitandabb/GoAgent/internal/externalcase"
	"github.com/chitandabb/GoAgent/internal/repository"

	"github.com/google/uuid"
)

type Status string

const (
	StatusActive   Status = "active"
	StatusArchived Status = "archived"
)

func (s Status) Valid() bool { return s == StatusActive || s == StatusArchived }

type TurnStatus string

const (
	TurnStatusRunning   TurnStatus = "running"
	TurnStatusFailed    TurnStatus = "failed"
	TurnStatusCompleted TurnStatus = "completed"
)

type MessageRole string

const (
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
	MessageRoleTool      MessageRole = "tool"
	MessageRoleSystem    MessageRole = "system"
)

func (r MessageRole) Valid() bool {
	return r == MessageRoleUser || r == MessageRoleAssistant || r == MessageRoleTool || r == MessageRoleSystem
}

type ReferenceKind string

const (
	ReferenceKindSelected   ReferenceKind = "selected"
	ReferenceKindMentioned  ReferenceKind = "mentioned"
	ReferenceKindCreated    ReferenceKind = "created"
	ReferenceKindReferenced ReferenceKind = "referenced"
)

func (k ReferenceKind) Valid() bool {
	return k == ReferenceKindSelected || k == ReferenceKindMentioned ||
		k == ReferenceKindCreated || k == ReferenceKindReferenced
}

var (
	ErrInvalidConversation     = errors.New("conversation is invalid")
	ErrConversationNotFound    = errors.New("conversation is not found")
	ErrConversationArchived    = errors.New("conversation is archived")
	ErrInvalidMessage          = errors.New("conversation message is invalid")
	ErrReferenceNotFound       = errors.New("conversation reference is not found")
	ErrCommandContextRequired  = errors.New("conversation command context is required")
	ErrCommandNotLatest        = errors.New("conversation command is not for the latest user message")
	ErrDiagnosisIntentRequired = errors.New("explicit diagnosis intent is required")
	ErrCaseReferenceRequired   = errors.New("exactly one selected case reference is required")
	ErrTaskReferenceRequired   = errors.New("referenced diagnosis task is required")
	ErrAgentUnavailable        = errors.New("conversation agent is unavailable")
	ErrAgentResponseInvalid    = errors.New("conversation agent response is invalid")
	ErrTurnIdempotencyConflict = errors.New("conversation turn idempotency key conflicts")
	ErrTurnInProgress          = errors.New("conversation turn is already in progress")
	ErrTurnLeaseLost           = errors.New("conversation turn execution lease is no longer owned")
)

const (
	DefaultPageSize = 20
	MaxPageSize     = 100
	MaxMessageLimit = 100
	MaxContentRunes = 20000
	MaxTitleRunes   = 200
)

type Conversation struct {
	ID            uuid.UUID
	UserID        uuid.UUID
	Title         string
	Status        Status
	CreatedAt     time.Time
	UpdatedAt     time.Time
	LastMessageAt *time.Time
}

type CaseReference struct {
	ExternalCaseID uuid.UUID
	Kind           ReferenceKind
}

type TaskReference struct {
	TaskID uuid.UUID
	Kind   ReferenceKind
}

type Message struct {
	ID                   uuid.UUID
	ConversationID       uuid.UUID
	Seq                  int64
	Role                 MessageRole
	Content              string
	ContentSchemaVersion int
	CaseReferences       []CaseReference
	TaskReferences       []TaskReference
	CreatedAt            time.Time
}

type ListQuery struct {
	Page     int
	PageSize int
}

func (q *ListQuery) Normalize() {
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PageSize < 1 {
		q.PageSize = DefaultPageSize
	} else if q.PageSize > MaxPageSize {
		q.PageSize = MaxPageSize
	}
}

type ListResult struct {
	Items []Conversation
	Total int
}

type MessageQuery struct {
	AfterSeq int64
	Limit    int
}

type MessagePage struct {
	Items        []Message
	AfterSeq     int64
	NextAfterSeq int64
	HasMore      bool
}

func (q *MessageQuery) Normalize() {
	if q.AfterSeq < 0 {
		q.AfterSeq = 0
	}
	if q.Limit < 1 {
		q.Limit = DefaultPageSize
	} else if q.Limit > MaxMessageLimit {
		q.Limit = MaxMessageLimit
	}
}

type Actor struct {
	UserID  uuid.UUID
	IsAdmin bool
}

type CreateInput struct {
	Title string
}

type AppendMessageInput struct {
	ConversationID uuid.UUID
	Role           MessageRole
	Content        string
	CaseReferences []CaseReference
	TaskReferences []TaskReference
}

// AgentRequest is the bounded input passed to the independent conversation Agent.
// Tool results are deliberately not persisted into this history contract; they are
// scoped to one invocation and can contain transient or untrusted data.
type AgentRequest struct {
	Conversation Conversation
	UserMessage  Message
	History      []Message
}

type AgentResponse struct {
	Content string
}

// AgentResponder is implemented by the Eino-backed runtime in internal/agent.
// Keeping this interface in the conversation domain avoids coupling persistence to
// a model framework and lets the message service own assistant-message writes.
type AgentResponder interface {
	Respond(ctx context.Context, request AgentRequest) (AgentResponse, error)
}

type ConversationTurn struct {
	UserMessage      Message
	AssistantMessage Message
}

type ConversationTurnResult struct {
	Turn     ConversationTurn
	Created  bool
	Replayed bool
}

type BeginTurnInput struct {
	Message            AppendMessageInput
	IdempotencyKey     string
	RequestFingerprint string
	StartedAt          time.Time
	LeaseExpiresAt     time.Time
}

type BeginTurnResult struct {
	TurnID           uuid.UUID
	UserMessage      Message
	AssistantMessage *Message
	Created          bool
}

type Repository interface {
	Create(ctx context.Context, userID uuid.UUID, input CreateInput, createdAt time.Time) (Conversation, error)
	Get(ctx context.Context, userID, conversationID uuid.UUID) (Conversation, error)
	List(ctx context.Context, userID uuid.UUID, query ListQuery) (ListResult, error)
	ListMessages(ctx context.Context, userID, conversationID uuid.UUID, query MessageQuery) (MessagePage, error)
	AppendMessage(ctx context.Context, userID uuid.UUID, input AppendMessageInput, createdAt time.Time) (Message, error)
	GetMessage(ctx context.Context, userID, conversationID, messageID uuid.UUID) (Message, error)
	GetLatestMessage(ctx context.Context, userID, conversationID uuid.UUID) (Message, error)
	AppendTaskReference(ctx context.Context, userID, messageID, taskID uuid.UUID, kind ReferenceKind, createdAt time.Time) error
	BeginTurn(ctx context.Context, userID uuid.UUID, input BeginTurnInput) (BeginTurnResult, error)
	CompleteTurn(ctx context.Context, userID, turnID uuid.UUID, assistantContent string, completedAt time.Time) (ConversationTurn, error)
	FailTurn(ctx context.Context, userID, turnID uuid.UUID, failedAt time.Time) error
}

// DiagnosisTaskCreator is the application command boundary. The conversation
// package never writes diagnosis tables directly; the existing diagnosis service
// remains responsible for snapshots, fingerprints, events and Outbox.
type DiagnosisTaskCreator interface {
	Create(ctx context.Context, actor diagnosis.TaskActor, input diagnosis.CreateTaskInput) (diagnosis.TaskCreateResult, error)
}

type DiagnosisTaskReader interface {
	Get(ctx context.Context, actor diagnosis.TaskActor, taskID uuid.UUID) (diagnosis.DiagnosisTask, error)
}

type ExternalCaseReader interface {
	Get(ctx context.Context, id uuid.UUID) (*externalcase.ExternalCase, error)
}

type CommandContext struct {
	ConversationID uuid.UUID
	UserMessageID  uuid.UUID
	Actor          Actor
}

type commandContextKey struct{}

func WithCommandContext(ctx context.Context, commandContext CommandContext) context.Context {
	return context.WithValue(ctx, commandContextKey{}, commandContext)
}

func CommandContextFromContext(ctx context.Context) (CommandContext, bool) {
	value, ok := ctx.Value(commandContextKey{}).(CommandContext)
	return value, ok
}

type CreateDiagnosisInput struct {
	ExternalCaseID uuid.UUID
	DiagnosisGoal  string
	AttachmentIDs  []uuid.UUID
	ParentTaskID   *uuid.UUID
}

type CreateDiagnosisResult struct {
	Task     diagnosis.DiagnosisTask
	Replayed bool
}

// DiagnosisTaskStatusResult is the safe task summary exposed to a bounded
// conversation turn. The diagnosis service remains the authorization source.
type DiagnosisTaskStatusResult struct {
	Task diagnosis.DiagnosisTask
}

type Service struct {
	repository          Repository
	clock               func() time.Time
	turnLease           time.Duration
	agent               AgentResponder
	diagnosisTasks      DiagnosisTaskCreator
	diagnosisTaskReader DiagnosisTaskReader
	externalCases       ExternalCaseReader
}

func (s *Service) WithAgentResponder(agent AgentResponder) (*Service, error) {
	if s == nil || s.repository == nil {
		return nil, errors.New("conversation service is unavailable")
	}
	if agent == nil {
		return nil, errors.New("conversation agent responder is nil")
	}
	s.agent = agent
	return s, nil
}

func (s *Service) WithTurnLease(lease time.Duration) (*Service, error) {
	if s == nil || s.repository == nil {
		return nil, errors.New("conversation service is unavailable")
	}
	if lease < time.Second || lease > 10*time.Minute {
		return nil, errors.New("conversation turn lease must be between 1 second and 10 minutes")
	}
	s.turnLease = lease
	return s, nil
}

func NewService(repository Repository) (*Service, error) {
	if repository == nil {
		return nil, errors.New("conversation repository is nil")
	}
	return &Service{
		repository: repository,
		clock:      func() time.Time { return time.Now().UTC() },
		turnLease:  6 * time.Minute,
	}, nil
}

// WithDiagnosisCommandDependencies adds the side-effecting command boundary
// after the read/write conversation service has been constructed.
func (s *Service) WithDiagnosisCommandDependencies(
	diagnosisTasks DiagnosisTaskCreator,
	diagnosisTaskReader DiagnosisTaskReader,
	externalCases ExternalCaseReader,
) (*Service, error) {
	if s == nil || s.repository == nil {
		return nil, errors.New("conversation service is unavailable")
	}
	if diagnosisTasks == nil || diagnosisTaskReader == nil || externalCases == nil {
		return nil, errors.New("conversation diagnosis command dependencies are nil")
	}
	s.diagnosisTasks = diagnosisTasks
	s.diagnosisTaskReader = diagnosisTaskReader
	s.externalCases = externalCases
	return s, nil
}

func (s *Service) Create(ctx context.Context, actor Actor, input CreateInput) (Conversation, error) {
	if s == nil || s.repository == nil {
		return Conversation{}, errors.New("conversation service is unavailable")
	}
	if actor.UserID == uuid.Nil {
		return Conversation{}, ErrInvalidConversation
	}
	input.Title = strings.TrimSpace(input.Title)
	if len([]rune(input.Title)) > MaxTitleRunes {
		return Conversation{}, ErrInvalidConversation
	}
	return s.repository.Create(ctx, actor.UserID, input, s.clock().UTC())
}

func (s *Service) Get(ctx context.Context, actor Actor, conversationID uuid.UUID) (Conversation, error) {
	if s == nil || s.repository == nil {
		return Conversation{}, errors.New("conversation service is unavailable")
	}
	if actor.UserID == uuid.Nil || conversationID == uuid.Nil {
		return Conversation{}, ErrInvalidConversation
	}
	return s.repository.Get(ctx, actor.UserID, conversationID)
}

func (s *Service) List(ctx context.Context, actor Actor, query ListQuery) (ListResult, error) {
	if s == nil || s.repository == nil {
		return ListResult{}, errors.New("conversation service is unavailable")
	}
	if actor.UserID == uuid.Nil {
		return ListResult{}, ErrInvalidConversation
	}
	query.Normalize()
	return s.repository.List(ctx, actor.UserID, query)
}

func (s *Service) ListMessages(ctx context.Context, actor Actor, conversationID uuid.UUID, query MessageQuery) (MessagePage, error) {
	if s == nil || s.repository == nil {
		return MessagePage{}, errors.New("conversation service is unavailable")
	}
	if actor.UserID == uuid.Nil || conversationID == uuid.Nil {
		return MessagePage{}, ErrInvalidConversation
	}
	query.Normalize()
	return s.repository.ListMessages(ctx, actor.UserID, conversationID, query)
}

func (s *Service) AppendUserMessage(ctx context.Context, actor Actor, input AppendMessageInput) (Message, error) {
	input.Role = MessageRoleUser
	return s.appendMessage(ctx, actor, input)
}

// AppendAssistantMessage is reserved for the conversation Agent boundary. It uses
// the same transaction and reference validation as user messages.
func (s *Service) AppendAssistantMessage(ctx context.Context, actor Actor, input AppendMessageInput) (Message, error) {
	input.Role = MessageRoleAssistant
	return s.appendMessage(ctx, actor, input)
}

// ExecuteTurn is the synchronous HTTP-facing turn boundary. The durable turn
// record prevents retries from appending duplicate user messages. Diagnosis
// execution remains asynchronous: the Agent can only create the task, while the
// Diagnosis Worker owns the long-running investigation.
func (s *Service) ExecuteTurn(
	ctx context.Context,
	actor Actor,
	idempotencyKey string,
	input AppendMessageInput,
) (ConversationTurnResult, error) {
	if s == nil || s.repository == nil {
		return ConversationTurnResult{}, errors.New("conversation service is unavailable")
	}
	if s.agent == nil {
		return ConversationTurnResult{}, ErrAgentUnavailable
	}
	parsedKey, err := uuid.Parse(strings.TrimSpace(idempotencyKey))
	if err != nil {
		return ConversationTurnResult{}, ErrInvalidMessage
	}
	input.Role = MessageRoleUser
	input, err = prepareMessageInput(actor, input)
	if err != nil {
		return ConversationTurnResult{}, err
	}
	fingerprint, err := turnRequestFingerprint(input)
	if err != nil {
		return ConversationTurnResult{}, err
	}
	startedAt := s.clock().UTC()
	started, err := s.repository.BeginTurn(ctx, actor.UserID, BeginTurnInput{
		Message: input, IdempotencyKey: parsedKey.String(), RequestFingerprint: fingerprint,
		StartedAt: startedAt, LeaseExpiresAt: startedAt.Add(s.turnLease),
	})
	if err != nil {
		return ConversationTurnResult{}, err
	}
	if started.AssistantMessage != nil {
		return ConversationTurnResult{
			Turn:    ConversationTurn{UserMessage: started.UserMessage, AssistantMessage: *started.AssistantMessage},
			Created: started.Created, Replayed: true,
		}, nil
	}
	result := ConversationTurnResult{
		Turn:    ConversationTurn{UserMessage: started.UserMessage},
		Created: started.Created,
	}
	current, err := s.repository.Get(ctx, actor.UserID, input.ConversationID)
	if err != nil {
		return result, s.failTurn(ctx, actor.UserID, started.TurnID, err)
	}
	afterSeq := started.UserMessage.Seq - MaxMessageLimit
	if afterSeq < 0 {
		afterSeq = 0
	}
	history, err := s.repository.ListMessages(ctx, actor.UserID, input.ConversationID, MessageQuery{
		AfterSeq: afterSeq,
		Limit:    MaxMessageLimit,
	})
	if err != nil {
		return result, s.failTurn(ctx, actor.UserID, started.TurnID, err)
	}
	commandCtx := WithCommandContext(ctx, CommandContext{
		ConversationID: input.ConversationID,
		UserMessageID:  started.UserMessage.ID,
		Actor:          actor,
	})
	response, err := s.agent.Respond(commandCtx, AgentRequest{
		Conversation: current,
		UserMessage:  started.UserMessage,
		History:      history.Items,
	})
	if err != nil {
		return result, s.failTurn(ctx, actor.UserID, started.TurnID, err)
	}
	response.Content = strings.TrimSpace(response.Content)
	if response.Content == "" || len([]rune(response.Content)) > MaxContentRunes {
		return result, s.failTurn(ctx, actor.UserID, started.TurnID, ErrAgentResponseInvalid)
	}
	completed, err := s.repository.CompleteTurn(
		ctx, actor.UserID, started.TurnID, response.Content, s.clock().UTC(),
	)
	if err != nil {
		return result, s.failTurn(ctx, actor.UserID, started.TurnID, err)
	}
	result.Turn = completed
	return result, nil
}

func (s *Service) failTurn(ctx context.Context, userID, turnID uuid.UUID, cause error) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.repository.FailTurn(cleanupCtx, userID, turnID, s.clock().UTC()); err != nil {
		return errors.Join(cause, fmt.Errorf("mark conversation turn failed: %w", err))
	}
	return cause
}

func (s *Service) appendMessage(ctx context.Context, actor Actor, input AppendMessageInput) (Message, error) {
	if s == nil || s.repository == nil {
		return Message{}, errors.New("conversation service is unavailable")
	}
	var err error
	input, err = prepareMessageInput(actor, input)
	if err != nil {
		return Message{}, err
	}
	return s.repository.AppendMessage(ctx, actor.UserID, input, s.clock().UTC())
}

func prepareMessageInput(actor Actor, input AppendMessageInput) (AppendMessageInput, error) {
	if actor.UserID == uuid.Nil || input.ConversationID == uuid.Nil || !input.Role.Valid() {
		return AppendMessageInput{}, ErrInvalidMessage
	}
	input.Content = strings.TrimSpace(input.Content)
	if input.Content == "" || len([]rune(input.Content)) > MaxContentRunes {
		return AppendMessageInput{}, ErrInvalidMessage
	}
	if err := validateReferences(input); err != nil {
		return AppendMessageInput{}, err
	}
	return input, nil
}

func turnRequestFingerprint(input AppendMessageInput) (string, error) {
	caseReferences := append(make([]CaseReference, 0, len(input.CaseReferences)), input.CaseReferences...)
	slices.SortFunc(caseReferences, func(left, right CaseReference) int {
		if compared := strings.Compare(left.ExternalCaseID.String(), right.ExternalCaseID.String()); compared != 0 {
			return compared
		}
		return strings.Compare(string(left.Kind), string(right.Kind))
	})
	taskReferences := append(make([]TaskReference, 0, len(input.TaskReferences)), input.TaskReferences...)
	slices.SortFunc(taskReferences, func(left, right TaskReference) int {
		if compared := strings.Compare(left.TaskID.String(), right.TaskID.String()); compared != 0 {
			return compared
		}
		return strings.Compare(string(left.Kind), string(right.Kind))
	})
	payload := struct {
		ConversationID uuid.UUID       `json:"conversationId"`
		Content        string          `json:"content"`
		CaseReferences []CaseReference `json:"caseReferences"`
		TaskReferences []TaskReference `json:"taskReferences"`
	}{
		ConversationID: input.ConversationID,
		Content:        input.Content,
		CaseReferences: caseReferences,
		TaskReferences: taskReferences,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode conversation turn fingerprint: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validateReferences(input AppendMessageInput) error {
	if len(input.CaseReferences) > 20 || len(input.TaskReferences) > 20 {
		return ErrInvalidMessage
	}
	caseSeen := make(map[uuid.UUID]struct{}, len(input.CaseReferences))
	for _, ref := range input.CaseReferences {
		if ref.ExternalCaseID == uuid.Nil || !ref.Kind.Valid() ||
			(ref.Kind != ReferenceKindSelected && ref.Kind != ReferenceKindMentioned) {
			return ErrInvalidMessage
		}
		if _, exists := caseSeen[ref.ExternalCaseID]; exists {
			return ErrInvalidMessage
		}
		caseSeen[ref.ExternalCaseID] = struct{}{}
	}
	taskSeen := make(map[uuid.UUID]struct{}, len(input.TaskReferences))
	for _, ref := range input.TaskReferences {
		if ref.TaskID == uuid.Nil || !ref.Kind.Valid() ||
			(ref.Kind != ReferenceKindCreated && ref.Kind != ReferenceKindReferenced) {
			return ErrInvalidMessage
		}
		if _, exists := taskSeen[ref.TaskID]; exists {
			return ErrInvalidMessage
		}
		taskSeen[ref.TaskID] = struct{}{}
	}
	return nil
}

// CreateDiagnosisTask converts a direct conversation command into the existing
// durable diagnosis-task application command. The latest user turn and its
// selected case reference participate in authorization; content from a case,
// attachment, Tool result or web page never authorizes this command.
func (s *Service) CreateDiagnosisTask(ctx context.Context, input CreateDiagnosisInput) (CreateDiagnosisResult, error) {
	if s == nil || s.repository == nil || s.diagnosisTasks == nil || s.diagnosisTaskReader == nil || s.externalCases == nil {
		return CreateDiagnosisResult{}, errors.New("conversation diagnosis command is unavailable")
	}
	commandContext, ok := CommandContextFromContext(ctx)
	if !ok || commandContext.ConversationID == uuid.Nil || commandContext.UserMessageID == uuid.Nil || commandContext.Actor.UserID == uuid.Nil {
		return CreateDiagnosisResult{}, ErrCommandContextRequired
	}
	actor := commandContext.Actor
	if input.ExternalCaseID == uuid.Nil || strings.TrimSpace(input.DiagnosisGoal) == "" ||
		len([]rune(strings.TrimSpace(input.DiagnosisGoal))) > MaxContentRunes {
		return CreateDiagnosisResult{}, ErrInvalidMessage
	}
	if len(input.AttachmentIDs) > 0 {
		return CreateDiagnosisResult{}, diagnosis.ErrAttachmentsUnsupported
	}
	message, err := s.repository.GetMessage(ctx, actor.UserID, commandContext.ConversationID, commandContext.UserMessageID)
	if err != nil {
		return CreateDiagnosisResult{}, err
	}
	latest, err := s.repository.GetLatestMessage(ctx, actor.UserID, commandContext.ConversationID)
	if err != nil {
		return CreateDiagnosisResult{}, err
	}
	if message.ID != latest.ID || message.Role != MessageRoleUser {
		return CreateDiagnosisResult{}, ErrCommandNotLatest
	}
	if !hasExplicitDiagnosisIntent(message.Content) {
		return CreateDiagnosisResult{}, ErrDiagnosisIntentRequired
	}
	if len(message.CaseReferences) != 1 || message.CaseReferences[0].Kind != ReferenceKindSelected ||
		message.CaseReferences[0].ExternalCaseID != input.ExternalCaseID {
		return CreateDiagnosisResult{}, ErrCaseReferenceRequired
	}
	if input.ParentTaskID != nil {
		if *input.ParentTaskID == uuid.Nil {
			return CreateDiagnosisResult{}, ErrInvalidMessage
		}
		// Parent ownership remains creator-scoped even for an admin conversation;
		// task references use the same visibility boundary.
		if _, err := s.diagnosisTaskReader.Get(ctx, diagnosis.TaskActor{UserID: actor.UserID}, *input.ParentTaskID); err != nil {
			return CreateDiagnosisResult{}, err
		}
	}
	item, err := s.externalCases.Get(ctx, input.ExternalCaseID)
	if err != nil {
		return CreateDiagnosisResult{}, err
	}
	if item == nil || item.ID != input.ExternalCaseID || strings.TrimSpace(item.SourceFingerprint) == "" {
		return CreateDiagnosisResult{}, repository.ErrNotFound
	}
	result, err := s.diagnosisTasks.Create(ctx, diagnosis.TaskActor{UserID: actor.UserID, IsAdmin: actor.IsAdmin}, diagnosis.CreateTaskInput{
		ExternalCaseID: input.ExternalCaseID, ExpectedSourceFingerprint: item.SourceFingerprint,
		RequestText: strings.TrimSpace(input.DiagnosisGoal), RetryOfTaskID: input.ParentTaskID,
		IdempotencyKey: commandIdempotencyKey(actor.UserID, commandContext.UserMessageID, input.ExternalCaseID, input.ParentTaskID),
		CorrelationID:  uuid.New(),
	})
	if err != nil {
		return CreateDiagnosisResult{}, err
	}
	if err := s.repository.AppendTaskReference(ctx, actor.UserID, commandContext.UserMessageID, result.Task.ID, ReferenceKindCreated, s.clock().UTC()); err != nil {
		return CreateDiagnosisResult{}, err
	}
	return CreateDiagnosisResult{Task: result.Task, Replayed: result.Replayed}, nil
}

// GetDiagnosisTaskStatus reads only a task explicitly referenced by the latest
// user message. A model-supplied UUID alone is never sufficient authorization.
func (s *Service) GetDiagnosisTaskStatus(ctx context.Context, taskID uuid.UUID) (DiagnosisTaskStatusResult, error) {
	if s == nil || s.repository == nil || s.diagnosisTaskReader == nil {
		return DiagnosisTaskStatusResult{}, errors.New("conversation diagnosis task status is unavailable")
	}
	commandContext, ok := CommandContextFromContext(ctx)
	if !ok || commandContext.ConversationID == uuid.Nil || commandContext.UserMessageID == uuid.Nil ||
		commandContext.Actor.UserID == uuid.Nil {
		return DiagnosisTaskStatusResult{}, ErrCommandContextRequired
	}
	if taskID == uuid.Nil {
		return DiagnosisTaskStatusResult{}, ErrInvalidMessage
	}
	actor := commandContext.Actor
	message, err := s.repository.GetMessage(
		ctx, actor.UserID, commandContext.ConversationID, commandContext.UserMessageID,
	)
	if err != nil {
		return DiagnosisTaskStatusResult{}, err
	}
	latest, err := s.repository.GetLatestMessage(ctx, actor.UserID, commandContext.ConversationID)
	if err != nil {
		return DiagnosisTaskStatusResult{}, err
	}
	if message.ID != latest.ID || message.Role != MessageRoleUser {
		return DiagnosisTaskStatusResult{}, ErrCommandNotLatest
	}
	referenced := false
	for _, reference := range message.TaskReferences {
		if reference.TaskID == taskID &&
			(reference.Kind == ReferenceKindCreated || reference.Kind == ReferenceKindReferenced) {
			referenced = true
			break
		}
	}
	if !referenced {
		return DiagnosisTaskStatusResult{}, ErrTaskReferenceRequired
	}
	task, err := s.diagnosisTaskReader.Get(ctx, diagnosis.TaskActor{
		UserID: actor.UserID, IsAdmin: actor.IsAdmin,
	}, taskID)
	if err != nil {
		return DiagnosisTaskStatusResult{}, err
	}
	return DiagnosisTaskStatusResult{Task: task}, nil
}

func hasExplicitDiagnosisIntent(content string) bool {
	value := strings.ToLower(strings.TrimSpace(content))
	if value == "" || strings.Contains(value, "不要诊断") || strings.Contains(value, "不需要诊断") || strings.Contains(value, "don't diagnose") {
		return false
	}
	for _, phrase := range []string{
		"诊断", "排查", "故障分析", "根因分析", "定位原因", "查原因", "调查这个工单",
		"diagnose", "troubleshoot", "investigate", "root cause", "analyze this ticket",
	} {
		if strings.Contains(value, phrase) {
			return true
		}
	}
	return false
}

func commandIdempotencyKey(userID, messageID, caseID uuid.UUID, parentTaskID *uuid.UUID) string {
	parent := ""
	if parentTaskID != nil {
		parent = parentTaskID.String()
	}
	value := strings.Join([]string{"mesguard.create_diagnosis_task.v1", userID.String(), messageID.String(), caseID.String(), parent}, ":")
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(value)).String()
}
