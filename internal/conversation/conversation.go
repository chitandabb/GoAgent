// Package conversation defines the durable conversation workspace boundary.
package conversation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/contextgovernance"
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
	TurnStatusQueued    TurnStatus = "queued"
	TurnStatusRunning   TurnStatus = "running"
	TurnStatusFailed    TurnStatus = "failed"
	TurnStatusCompleted TurnStatus = "completed"
)

func (s TurnStatus) Valid() bool {
	return s == TurnStatusQueued || s == TurnStatusRunning || s == TurnStatusFailed || s == TurnStatusCompleted
}

func (s TurnStatus) IsTerminal() bool {
	return s == TurnStatusFailed || s == TurnStatusCompleted
}

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
	ErrAsyncTurnUnavailable    = errors.New("asynchronous conversation turns are unavailable")
	ErrTurnAlreadyCompleted    = errors.New("conversation turn is already completed")
)

const (
	DefaultPageSize           = 20
	MaxPageSize               = 100
	MaxMessageLimit           = 100
	MaxContentRunes           = 20000
	MaxTitleRunes             = 200
	MaxAttachmentsPerMessage  = 8
	MaxAttachmentPurposeRunes = 64
	MaxCitationsPerMessage    = 20
	MaxCitationSourcesPerRun  = 200
	MaxCitationSourceRefBytes = 2048
	MaxRunDegradedChannels    = 32
)

type CitationSourceType string

const (
	CitationSourceKnowledgeChunk CitationSourceType = "knowledge_chunk"
	CitationSourceAttachment     CitationSourceType = "attachment"
	CitationSourceWeb            CitationSourceType = "web"
)

func (s CitationSourceType) Valid() bool {
	return s == CitationSourceKnowledgeChunk || s == CitationSourceAttachment || s == CitationSourceWeb
}

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
	Attachments          []MessageAttachment
	Citations            []MessageCitation
	CreatedAt            time.Time
}

// MessageCitation is a safe, immutable source identity attached to an assistant
// message. It never contains object-storage coordinates, credentials, raw
// binary data, or a permanent private URL.
type MessageCitation struct {
	Position      int                `json:"position"`
	SourceType    CitationSourceType `json:"sourceType"`
	SourceRef     string             `json:"sourceRef"`
	ContentSHA256 string             `json:"contentSha256"`
}

func (c MessageCitation) Validate() error {
	if c.Position < 0 || c.Position >= MaxCitationsPerMessage || validateCitationIdentity(c) != nil {
		return ErrInvalidMessage
	}
	return nil
}

type AgentRunOutcome string

const (
	AgentRunAnswered             AgentRunOutcome = "answered"
	AgentRunInsufficientEvidence AgentRunOutcome = "insufficient_evidence"
	AgentRunDegraded             AgentRunOutcome = "degraded"
	AgentRunFailed               AgentRunOutcome = "failed"
)

func (o AgentRunOutcome) Valid() bool {
	return o == AgentRunAnswered || o == AgentRunInsufficientEvidence ||
		o == AgentRunDegraded || o == AgentRunFailed
}

type AgentRunSource struct {
	SourceType    CitationSourceType
	SourceRef     string
	ContentSHA256 string
}

func (s AgentRunSource) Validate() error {
	return validateCitationIdentity(MessageCitation{
		SourceType: s.SourceType, SourceRef: s.SourceRef, ContentSHA256: s.ContentSHA256,
	})
}

type AgentRunUsage struct {
	ModelCalls       int
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CachedTokens     int
	ReasoningTokens  int
}

func (u AgentRunUsage) Validate() error {
	if u.ModelCalls < 0 || u.PromptTokens < 0 || u.CompletionTokens < 0 || u.TotalTokens < 0 ||
		u.CachedTokens < 0 || u.ReasoningTokens < 0 || u.TotalTokens < u.PromptTokens+u.CompletionTokens {
		return ErrAgentResponseInvalid
	}
	return nil
}

// AgentRunObservation contains only bounded operational facts needed to build
// recorded quality observations. It deliberately excludes prompts, raw tool
// payloads, reasoning text, object-store coordinates, and user content.
type AgentRunObservation struct {
	ModelProvider    string
	ModelID          string
	PromptVersion    string
	Outcome          AgentRunOutcome
	RetrievedSources []AgentRunSource
	DegradedChannels []string
	Usage            AgentRunUsage
	DurationMillis   int64
	SourcesTruncated bool
	PromptManifest   *contextgovernance.PromptManifest
}

// AgentRunFailureRecord is the bounded, persistence-safe projection carried
// with an Agent execution error. ErrorType is a stable machine label; raw
// provider or Tool error text is deliberately excluded from the run ledger.
type AgentRunFailureRecord struct {
	Observation AgentRunObservation
	ErrorType   string
}

const AgentRunErrorTypeContextPreparationFailed = "context_preparation_failed"

// AgentRunFailureRetryable is the single policy source for terminal failure
// events that may be safely submitted again by the user.
func AgentRunFailureRetryable(errorType string) bool {
	return strings.TrimSpace(errorType) == AgentRunErrorTypeContextPreparationFailed
}

func (r AgentRunFailureRecord) Validate() error {
	if r.Observation.Outcome != AgentRunFailed || r.Observation.Validate() != nil ||
		!validAgentRunMachineLabel(r.ErrorType, 64) {
		return ErrAgentResponseInvalid
	}
	return nil
}

// AgentRunFailure preserves the original error for retry and errors.Is/errors.As
// decisions while carrying only a validated, bounded record toward persistence.
type AgentRunFailure struct {
	cause  error
	record AgentRunFailureRecord
}

func NewAgentRunFailure(cause error, record AgentRunFailureRecord) error {
	if cause == nil {
		cause = ErrAgentResponseInvalid
	}
	return &AgentRunFailure{cause: cause, record: record}
}

func (e *AgentRunFailure) Error() string {
	if e == nil || e.cause == nil {
		return ErrAgentResponseInvalid.Error()
	}
	return e.cause.Error()
}

func (e *AgentRunFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func AgentRunFailureRecordFrom(err error) (AgentRunFailureRecord, bool) {
	var failure *AgentRunFailure
	if !errors.As(err, &failure) || failure == nil || failure.record.Validate() != nil {
		return AgentRunFailureRecord{}, false
	}
	record := failure.record
	record.Observation.RetrievedSources = append([]AgentRunSource(nil), record.Observation.RetrievedSources...)
	record.Observation.DegradedChannels = append([]string(nil), record.Observation.DegradedChannels...)
	return record, true
}

func (o AgentRunObservation) Validate() error {
	if !validAgentRunMachineLabel(o.ModelProvider, 64) || !validAgentRunLabel(o.ModelID, 256) ||
		!validAgentRunMachineLabel(o.PromptVersion, 128) || !o.Outcome.Valid() ||
		len(o.RetrievedSources) > MaxCitationSourcesPerRun ||
		len(o.DegradedChannels) > MaxRunDegradedChannels || o.DurationMillis < 0 ||
		o.DurationMillis > int64((5*time.Minute)/time.Millisecond) || o.Usage.Validate() != nil {
		return ErrAgentResponseInvalid
	}
	seenSources := make(map[string]struct{}, len(o.RetrievedSources))
	for _, source := range o.RetrievedSources {
		if source.Validate() != nil {
			return ErrAgentResponseInvalid
		}
		if _, exists := seenSources[source.SourceRef]; exists {
			return ErrAgentResponseInvalid
		}
		seenSources[source.SourceRef] = struct{}{}
	}
	seenChannels := make(map[string]struct{}, len(o.DegradedChannels))
	for _, channel := range o.DegradedChannels {
		if !validAgentRunMachineLabel(channel, 64) {
			return ErrAgentResponseInvalid
		}
		if _, exists := seenChannels[channel]; exists {
			return ErrAgentResponseInvalid
		}
		seenChannels[channel] = struct{}{}
	}
	if o.PromptManifest != nil {
		manifest := o.PromptManifest
		if manifest.Validate() != nil || manifest.RunDurationMillis != o.DurationMillis ||
			(manifest.ActualUsageAvailable &&
				(manifest.ActualPromptTokens > o.Usage.PromptTokens ||
					manifest.CacheHitTokens > o.Usage.CachedTokens ||
					manifest.CompletionTokens > o.Usage.CompletionTokens)) {
			return ErrAgentResponseInvalid
		}
	}
	return nil
}

// MessageAttachment is a safe message projection. It contains display
// metadata only; MinIO bucket/object coordinates stay in the attachment
// service and are never part of the conversation model context.
type MessageAttachment struct {
	AttachmentID  uuid.UUID
	Position      int
	Purpose       string
	OriginalName  string
	MediaType     string
	SizeBytes     int64
	ContentSHA256 string
	Status        string
}

type MessageAttachmentInput struct {
	AttachmentID uuid.UUID
	Purpose      string
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
	Attachments    []MessageAttachmentInput
	Citations      []MessageCitation
}

// AgentRequest is the bounded input passed to the independent conversation Agent.
// Tool results are deliberately not persisted into this history contract; they are
// scoped to one invocation and can contain transient or untrusted data.
type AgentRequest struct {
	Conversation          Conversation
	UserMessage           Message
	History               []Message
	KnownReportReferences map[string][]int64
}

type AgentResponse struct {
	Content        string
	Citations      []MessageCitation
	RunObservation *AgentRunObservation
}

func (r AgentResponse) Validate() error {
	_, err := prepareAgentResponse(r)
	return err
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
	TurnID   uuid.UUID
	Turn     ConversationTurn
	Status   TurnStatus
	Created  bool
	Replayed bool
}

type TurnExecution struct {
	TurnID       uuid.UUID
	Turn         ConversationTurn
	Conversation Conversation
	Actor        Actor
	History      []Message
	AttemptCount int
}

// RecordedAgentRun is the safe export projection used by offline quality
// tooling. It contains persisted user/assistant text and bounded source facts,
// but no prompt, raw Tool payload, reasoning trace, lease owner, or credentials.
type RecordedAgentRun struct {
	TurnID             uuid.UUID
	ConversationID     uuid.UUID
	UserMessageID      uuid.UUID
	AssistantMessageID *uuid.UUID
	UserQuery          string
	Answer             string
	Citations          []MessageCitation
	Observation        AgentRunObservation
	ErrorType          string
	CompletedAt        *time.Time
	ObservedAt         time.Time
}

type BeginTurnInput struct {
	Message            AppendMessageInput
	IdempotencyKey     string
	RequestFingerprint string
	StartedAt          time.Time
	LeaseExpiresAt     time.Time
	ExecutionMode      TurnExecutionMode
	CorrelationID      uuid.UUID
}

type TurnExecutionMode string

const (
	TurnExecutionSynchronous  TurnExecutionMode = "synchronous"
	TurnExecutionAsynchronous TurnExecutionMode = "asynchronous"
)

type BeginTurnResult struct {
	TurnID           uuid.UUID
	UserMessage      Message
	AssistantMessage *Message
	Status           TurnStatus
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
	CompleteTurn(ctx context.Context, userID, turnID uuid.UUID, response AgentResponse, completedAt time.Time) (ConversationTurn, error)
	FailTurn(ctx context.Context, userID, turnID uuid.UUID, failedAt time.Time) error
	GetTurn(ctx context.Context, userID, conversationID, turnID uuid.UUID) (TurnDetail, error)
	ListTurnEvents(ctx context.Context, userID, conversationID, turnID uuid.UUID, afterSeq int64, limit int) (TurnEventPage, error)
}

type AsyncRepository interface {
	Repository
	AcceptTurn(ctx context.Context, userID uuid.UUID, input BeginTurnInput) (BeginTurnResult, error)
	ClaimTurn(ctx context.Context, turnID uuid.UUID, workerID string, claimedAt, leaseExpiresAt time.Time) (TurnExecution, error)
	RenewTurnExecution(ctx context.Context, turnID uuid.UUID, workerID string, renewedAt, leaseExpiresAt time.Time) (bool, error)
	QueueTurnRetry(ctx context.Context, userID, turnID uuid.UUID, workerID string, scheduledAt, retryAt time.Time) error
	CompleteTurnExecution(ctx context.Context, userID, turnID uuid.UUID, workerID string, response AgentResponse, completedAt time.Time) (ConversationTurn, error)
	FailTurnExecution(ctx context.Context, userID, turnID uuid.UUID, workerID string, failure *AgentRunFailureRecord, failedAt time.Time) error
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
			TurnID: started.TurnID,
			Turn:   ConversationTurn{UserMessage: started.UserMessage, AssistantMessage: *started.AssistantMessage},
			Status: TurnStatusCompleted, Created: started.Created, Replayed: true,
		}, nil
	}
	result := ConversationTurnResult{
		TurnID:  started.TurnID,
		Turn:    ConversationTurn{UserMessage: started.UserMessage},
		Status:  TurnStatusRunning,
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
	response, err = prepareAgentResponse(response)
	if err != nil {
		return result, s.failTurn(ctx, actor.UserID, started.TurnID, ErrAgentResponseInvalid)
	}
	completed, err := s.repository.CompleteTurn(
		ctx, actor.UserID, started.TurnID, response, s.clock().UTC(),
	)
	if err != nil {
		return result, s.failTurn(ctx, actor.UserID, started.TurnID, err)
	}
	result.Turn = completed
	result.Status = TurnStatusCompleted
	return result, nil
}

// AcceptTurn persists a user turn and its Outbox event without invoking the
// model in the HTTP process. Completed idempotent requests replay immediately.
func (s *Service) AcceptTurn(
	ctx context.Context,
	actor Actor,
	idempotencyKey string,
	input AppendMessageInput,
) (ConversationTurnResult, error) {
	if s == nil || s.repository == nil {
		return ConversationTurnResult{}, errors.New("conversation service is unavailable")
	}
	asyncRepository, ok := s.repository.(AsyncRepository)
	if !ok {
		return ConversationTurnResult{}, ErrAsyncTurnUnavailable
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
	acceptedAt := s.clock().UTC()
	accepted, err := asyncRepository.AcceptTurn(ctx, actor.UserID, BeginTurnInput{
		Message: input, IdempotencyKey: parsedKey.String(), RequestFingerprint: fingerprint,
		StartedAt: acceptedAt, ExecutionMode: TurnExecutionAsynchronous, CorrelationID: uuid.New(),
	})
	if err != nil {
		return ConversationTurnResult{}, err
	}
	if accepted.TurnID == uuid.Nil || accepted.UserMessage.ID == uuid.Nil || !accepted.Status.Valid() {
		return ConversationTurnResult{}, ErrInvalidMessage
	}
	switch accepted.Status {
	case TurnStatusCompleted:
		if accepted.AssistantMessage == nil || accepted.AssistantMessage.ID == uuid.Nil {
			return ConversationTurnResult{}, ErrInvalidMessage
		}
	case TurnStatusQueued, TurnStatusRunning:
		if accepted.AssistantMessage != nil {
			return ConversationTurnResult{}, ErrInvalidMessage
		}
	default:
		return ConversationTurnResult{}, ErrInvalidMessage
	}
	result := ConversationTurnResult{
		TurnID: accepted.TurnID, Turn: ConversationTurn{UserMessage: accepted.UserMessage}, Status: accepted.Status,
		Created: accepted.Created,
	}
	if accepted.AssistantMessage != nil {
		result.Turn.AssistantMessage = *accepted.AssistantMessage
		result.Status = TurnStatusCompleted
		result.Replayed = true
	}
	return result, nil
}

// ExecuteAcceptedTurn is called by the background worker after it acquires the
// turn lease. It rebuilds the same bounded Agent request from durable facts.
func (s *Service) ExecuteAcceptedTurn(ctx context.Context, execution TurnExecution, workerID string) (ConversationTurn, error) {
	if s == nil || s.repository == nil {
		return ConversationTurn{}, errors.New("conversation service is unavailable")
	}
	asyncRepository, ok := s.repository.(AsyncRepository)
	if !ok || s.agent == nil {
		return ConversationTurn{}, ErrAsyncTurnUnavailable
	}
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || execution.TurnID == uuid.Nil || execution.Conversation.ID == uuid.Nil ||
		execution.Turn.UserMessage.ID == uuid.Nil || execution.Turn.UserMessage.ConversationID != execution.Conversation.ID ||
		execution.Actor.UserID == uuid.Nil || execution.Actor.UserID != execution.Conversation.UserID || execution.AttemptCount < 1 {
		return ConversationTurn{}, ErrInvalidMessage
	}
	commandCtx := WithCommandContext(ctx, CommandContext{
		ConversationID: execution.Conversation.ID,
		UserMessageID:  execution.Turn.UserMessage.ID,
		Actor:          execution.Actor,
	})
	response, err := s.agent.Respond(commandCtx, AgentRequest{
		Conversation: execution.Conversation,
		UserMessage:  execution.Turn.UserMessage,
		History:      execution.History,
	})
	if err != nil {
		return ConversationTurn{}, err
	}
	response, err = prepareAgentResponse(response)
	if err != nil {
		return ConversationTurn{}, ErrAgentResponseInvalid
	}
	return asyncRepository.CompleteTurnExecution(
		ctx, execution.Actor.UserID, execution.TurnID, workerID,
		response, s.clock().UTC(),
	)
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
	if input.Role == MessageRoleAssistant {
		prepared, err := prepareAgentResponse(AgentResponse{Content: input.Content, Citations: input.Citations})
		if err != nil {
			return AppendMessageInput{}, ErrInvalidMessage
		}
		input.Content, input.Citations = prepared.Content, prepared.Citations
	} else if len(input.Citations) > 0 {
		return AppendMessageInput{}, ErrInvalidMessage
	}
	for index := range input.Attachments {
		input.Attachments[index].Purpose = strings.TrimSpace(input.Attachments[index].Purpose)
		if input.Attachments[index].Purpose == "" {
			input.Attachments[index].Purpose = "context"
		}
	}
	if err := validateReferences(input); err != nil {
		return AppendMessageInput{}, err
	}
	return input, nil
}

func prepareAgentResponse(response AgentResponse) (AgentResponse, error) {
	response.Content = strings.TrimSpace(response.Content)
	if response.Content == "" || len([]rune(response.Content)) > MaxContentRunes ||
		len(response.Citations) > MaxCitationsPerMessage {
		return AgentResponse{}, ErrAgentResponseInvalid
	}
	resolved, err := ResolveAnswerCitations(response.Content, response.Citations)
	if err != nil || !slices.Equal(resolved, response.Citations) {
		return AgentResponse{}, ErrAgentResponseInvalid
	}
	response.Citations = append([]MessageCitation(nil), resolved...)
	if response.RunObservation != nil {
		if response.RunObservation.Validate() != nil {
			return AgentResponse{}, ErrAgentResponseInvalid
		}
		retrievedByRef := make(map[string]AgentRunSource, len(response.RunObservation.RetrievedSources))
		for _, source := range response.RunObservation.RetrievedSources {
			retrievedByRef[source.SourceRef] = source
		}
		for _, citation := range response.Citations {
			source, exists := retrievedByRef[citation.SourceRef]
			if !exists || source.SourceType != citation.SourceType || source.ContentSHA256 != citation.ContentSHA256 {
				return AgentResponse{}, ErrAgentResponseInvalid
			}
		}
		if response.RunObservation.Outcome == AgentRunInsufficientEvidence && len(response.Citations) > 0 {
			return AgentResponse{}, ErrAgentResponseInvalid
		}
		observation := *response.RunObservation
		observation.RetrievedSources = append([]AgentRunSource(nil), observation.RetrievedSources...)
		observation.DegradedChannels = append([]string(nil), observation.DegradedChannels...)
		if observation.PromptManifest != nil {
			manifest := *observation.PromptManifest
			manifest.DegradedReasons = append([]string(nil), manifest.DegradedReasons...)
			observation.PromptManifest = &manifest
		}
		response.RunObservation = &observation
	}
	return response, nil
}

// ResolveAnswerCitations accepts only source markers backed by sources exposed
// during the same Agent run. Repeated markers are allowed in answer text but
// become one ordered citation record.
func FormatAnswerCitationMarker(citation MessageCitation) (string, error) {
	if validateCitationIdentity(citation) != nil {
		return "", ErrInvalidMessage
	}
	return "[source:" + citation.SourceRef + "]", nil
}

func ResolveAnswerCitations(content string, available []MessageCitation) ([]MessageCitation, error) {
	if len(available) > MaxCitationSourcesPerRun {
		return nil, ErrInvalidMessage
	}
	availableByRef := make(map[string]MessageCitation, len(available))
	for _, citation := range available {
		if validateCitationIdentity(citation) != nil {
			return nil, ErrInvalidMessage
		}
		if _, exists := availableByRef[citation.SourceRef]; exists {
			return nil, ErrInvalidMessage
		}
		availableByRef[citation.SourceRef] = citation
	}
	markerRefs, err := citationMarkerRefs(content)
	if err != nil {
		return nil, err
	}
	if len(markerRefs) > MaxCitationSourcesPerRun {
		return nil, ErrInvalidMessage
	}
	resolved := make([]MessageCitation, 0, len(markerRefs))
	seen := make(map[string]struct{}, len(markerRefs))
	for _, sourceRef := range markerRefs {
		if _, duplicate := seen[sourceRef]; duplicate {
			continue
		}
		if len(resolved) >= MaxCitationsPerMessage {
			return nil, ErrInvalidMessage
		}
		citation, exists := availableByRef[sourceRef]
		if !exists {
			return nil, ErrInvalidMessage
		}
		citation.Position = len(resolved)
		resolved = append(resolved, citation)
		seen[sourceRef] = struct{}{}
	}
	return resolved, nil
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
		ConversationID uuid.UUID                `json:"conversationId"`
		Content        string                   `json:"content"`
		CaseReferences []CaseReference          `json:"caseReferences"`
		TaskReferences []TaskReference          `json:"taskReferences"`
		Attachments    []MessageAttachmentInput `json:"attachments"`
	}{
		ConversationID: input.ConversationID,
		Content:        input.Content,
		CaseReferences: caseReferences,
		TaskReferences: taskReferences,
		Attachments:    input.Attachments,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode conversation turn fingerprint: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validateReferences(input AppendMessageInput) error {
	if len(input.CaseReferences) > 20 || len(input.TaskReferences) > 20 ||
		len(input.Attachments) > MaxAttachmentsPerMessage || len(input.Citations) > MaxCitationsPerMessage {
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
	attachmentSeen := make(map[uuid.UUID]struct{}, len(input.Attachments))
	for _, ref := range input.Attachments {
		if ref.AttachmentID == uuid.Nil || strings.TrimSpace(ref.Purpose) == "" ||
			len([]rune(ref.Purpose)) > MaxAttachmentPurposeRunes {
			return ErrInvalidMessage
		}
		if _, exists := attachmentSeen[ref.AttachmentID]; exists {
			return ErrInvalidMessage
		}
		attachmentSeen[ref.AttachmentID] = struct{}{}
	}
	citationSeen := make(map[string]struct{}, len(input.Citations))
	for index, citation := range input.Citations {
		if citation.Position != index || citation.Validate() != nil {
			return ErrInvalidMessage
		}
		if _, exists := citationSeen[citation.SourceRef]; exists {
			return ErrInvalidMessage
		}
		citationSeen[citation.SourceRef] = struct{}{}
	}
	return nil
}

func citationMarkerRefs(content string) ([]string, error) {
	const markerPrefix = "[source:"
	refs := make([]string, 0)
	remaining := content
	for {
		start := strings.Index(remaining, markerPrefix)
		if start < 0 {
			return refs, nil
		}
		valueStart := start + len(markerPrefix)
		end := strings.IndexByte(remaining[valueStart:], ']')
		if end < 0 {
			return nil, ErrInvalidMessage
		}
		sourceRef := remaining[valueStart : valueStart+end]
		if strings.TrimSpace(sourceRef) == "" || sourceRef != strings.TrimSpace(sourceRef) ||
			len(sourceRef) > MaxCitationSourceRefBytes {
			return nil, ErrInvalidMessage
		}
		refs = append(refs, sourceRef)
		remaining = remaining[valueStart+end+1:]
	}
}

func validCitationSourceRef(sourceType CitationSourceType, sourceRef string) bool {
	if strings.TrimSpace(sourceRef) == "" || sourceRef != strings.TrimSpace(sourceRef) ||
		len(sourceRef) > MaxCitationSourceRefBytes || strings.ContainsAny(sourceRef, "[]\r\n") {
		return false
	}
	switch sourceType {
	case CitationSourceKnowledgeChunk:
		if !strings.HasPrefix(sourceRef, "knowledge:") {
			return false
		}
		parts := strings.Split(strings.TrimPrefix(sourceRef, "knowledge:"), "/")
		if len(parts) != 2 {
			return false
		}
		versionID, versionErr := uuid.Parse(parts[0])
		chunkID, chunkErr := uuid.Parse(parts[1])
		return versionErr == nil && chunkErr == nil && versionID.String() == parts[0] && chunkID.String() == parts[1]
	case CitationSourceAttachment:
		if !strings.HasPrefix(sourceRef, "attachment:") {
			return false
		}
		value := strings.TrimPrefix(sourceRef, "attachment:")
		attachmentID, err := uuid.Parse(value)
		return err == nil && attachmentID.String() == value
	case CitationSourceWeb:
		parsed, err := url.ParseRequestURI(sourceRef)
		return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
	default:
		return false
	}
}

func validCitationSHA256(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validAgentRunLabel(value string, maxBytes int) bool {
	return strings.TrimSpace(value) != "" && value == strings.TrimSpace(value) &&
		len(value) <= maxBytes && !strings.ContainsAny(value, "\x00\r\n")
}

func validAgentRunMachineLabel(value string, maxBytes int) bool {
	if !validAgentRunLabel(value, maxBytes) {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

func validateCitationIdentity(citation MessageCitation) error {
	if !citation.SourceType.Valid() || !validCitationSourceRef(citation.SourceType, citation.SourceRef) ||
		!validCitationSHA256(citation.ContentSHA256) {
		return ErrInvalidMessage
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
	if len(input.AttachmentIDs) > MaxAttachmentsPerMessage {
		return CreateDiagnosisResult{}, ErrInvalidMessage
	}
	for _, attachmentID := range input.AttachmentIDs {
		if attachmentID == uuid.Nil {
			return CreateDiagnosisResult{}, ErrInvalidMessage
		}
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
	attachments, err := taskAttachmentsFromMessage(message.Attachments, input.AttachmentIDs)
	if err != nil {
		return CreateDiagnosisResult{}, err
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
	var attachmentSource *diagnosis.TaskAttachmentSource
	if len(attachments) > 0 {
		attachmentSource = &diagnosis.TaskAttachmentSource{
			ConversationID: commandContext.ConversationID,
			MessageID:      commandContext.UserMessageID,
		}
	}
	result, err := s.diagnosisTasks.Create(ctx, diagnosis.TaskActor{UserID: actor.UserID, IsAdmin: actor.IsAdmin}, diagnosis.CreateTaskInput{
		ExternalCaseID: input.ExternalCaseID, ExpectedSourceFingerprint: item.SourceFingerprint,
		RequestText: strings.TrimSpace(input.DiagnosisGoal), RetryOfTaskID: input.ParentTaskID,
		Attachments: attachments, AttachmentSource: attachmentSource,
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

func taskAttachmentsFromMessage(
	messageAttachments []MessageAttachment,
	requestedIDs []uuid.UUID,
) ([]diagnosis.TaskAttachment, error) {
	if len(messageAttachments) == 0 {
		if len(requestedIDs) > 0 {
			return nil, ErrReferenceNotFound
		}
		return nil, nil
	}
	available := make(map[uuid.UUID]MessageAttachment, len(messageAttachments))
	for _, current := range messageAttachments {
		available[current.AttachmentID] = current
	}
	selected := requestedIDs
	if len(selected) == 0 {
		selected = make([]uuid.UUID, 0, len(messageAttachments))
		for _, current := range messageAttachments {
			selected = append(selected, current.AttachmentID)
		}
	}
	seen := make(map[uuid.UUID]struct{}, len(selected))
	result := make([]diagnosis.TaskAttachment, 0, len(selected))
	for _, attachmentID := range selected {
		current, ok := available[attachmentID]
		if !ok {
			return nil, ErrReferenceNotFound
		}
		if _, duplicate := seen[attachmentID]; duplicate {
			return nil, ErrInvalidMessage
		}
		seen[attachmentID] = struct{}{}
		result = append(result, diagnosis.TaskAttachment{
			AttachmentID: attachmentID,
			Purpose:      current.Purpose,
		})
	}
	return result, nil
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
