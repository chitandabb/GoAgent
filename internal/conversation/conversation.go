// Package conversation defines the durable conversation workspace boundary.
package conversation

import (
	"context"
	"errors"
	"strings"
	"time"

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
	ErrInvalidConversation  = errors.New("conversation is invalid")
	ErrConversationNotFound = errors.New("conversation is not found")
	ErrConversationArchived = errors.New("conversation is archived")
	ErrInvalidMessage       = errors.New("conversation message is invalid")
	ErrReferenceNotFound    = errors.New("conversation reference is not found")
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
	UserID uuid.UUID
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
}

type Service struct {
	repository Repository
	clock      func() time.Time
}

func NewService(repository Repository) (*Service, error) {
	if repository == nil {
		return nil, errors.New("conversation repository is nil")
	}
	return &Service{repository: repository, clock: func() time.Time { return time.Now().UTC() }}, nil
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
