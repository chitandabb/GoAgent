// Package conversation defines the durable conversation workspace boundary.
package conversation

import (
	"context"
	"errors"
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

type Repository interface {
	Create(ctx context.Context, userID uuid.UUID, input CreateInput, createdAt time.Time) (Conversation, error)
	Get(ctx context.Context, userID, conversationID uuid.UUID) (Conversation, error)
	List(ctx context.Context, userID uuid.UUID, query ListQuery) (ListResult, error)
	ListMessages(ctx context.Context, userID, conversationID uuid.UUID, query MessageQuery) (MessagePage, error)
	AppendMessage(ctx context.Context, userID uuid.UUID, input AppendMessageInput, createdAt time.Time) (Message, error)
	GetMessage(ctx context.Context, userID, conversationID, messageID uuid.UUID) (Message, error)
	GetLatestMessage(ctx context.Context, userID, conversationID uuid.UUID) (Message, error)
	AppendTaskReference(ctx context.Context, userID, messageID, taskID uuid.UUID, kind ReferenceKind, createdAt time.Time) error
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

type Service struct {
	repository          Repository
	clock               func() time.Time
	diagnosisTasks      DiagnosisTaskCreator
	diagnosisTaskReader DiagnosisTaskReader
	externalCases       ExternalCaseReader
}

func NewService(repository Repository) (*Service, error) {
	if repository == nil {
		return nil, errors.New("conversation repository is nil")
	}
	return &Service{repository: repository, clock: func() time.Time { return time.Now().UTC() }}, nil
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

func (s *Service) appendMessage(ctx context.Context, actor Actor, input AppendMessageInput) (Message, error) {
	if s == nil || s.repository == nil {
		return Message{}, errors.New("conversation service is unavailable")
	}
	if actor.UserID == uuid.Nil || input.ConversationID == uuid.Nil || !input.Role.Valid() {
		return Message{}, ErrInvalidMessage
	}
	if strings.TrimSpace(input.Content) == "" || len([]rune(input.Content)) > MaxContentRunes {
		return Message{}, ErrInvalidMessage
	}
	input.Content = strings.TrimSpace(input.Content)
	if err := validateReferences(input); err != nil {
		return Message{}, err
	}
	return s.repository.AppendMessage(ctx, actor.UserID, input, s.clock().UTC())
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
