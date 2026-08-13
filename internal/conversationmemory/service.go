package conversationmemory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversation"

	"github.com/google/uuid"
)

var (
	ErrSnapshotNotFound           = errors.New("conversation memory snapshot is not found")
	ErrSnapshotActivationConflict = errors.New("conversation memory snapshot activation conflict")
	ErrInvalidSnapshot            = errors.New("conversation memory snapshot is invalid")
	ErrInvalidShadowInput         = errors.New("conversation memory shadow input is invalid")
	ErrCompactionFailed           = errors.New("conversation memory compaction failed")
)

type SnapshotStatus string

const (
	SnapshotStatusCandidate  SnapshotStatus = "candidate"
	SnapshotStatusActive     SnapshotStatus = "active"
	SnapshotStatusSuperseded SnapshotStatus = "superseded"
)

func (s SnapshotStatus) Valid() bool {
	return s == SnapshotStatusCandidate || s == SnapshotStatusActive || s == SnapshotStatusSuperseded
}

type SummaryUsage struct {
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
	CachedTokens     int `json:"cachedTokens"`
}

type SummaryProvenance struct {
	ModelProfile  string
	ModelProvider string
	ModelID       string
	PromptVersion string
}

func (p SummaryProvenance) normalized() SummaryProvenance {
	return SummaryProvenance{
		ModelProfile:  strings.TrimSpace(p.ModelProfile),
		ModelProvider: strings.ToLower(strings.TrimSpace(p.ModelProvider)),
		ModelID:       strings.TrimSpace(p.ModelID), PromptVersion: strings.TrimSpace(p.PromptVersion),
	}
}

func (p SummaryProvenance) Validate() error {
	p = p.normalized()
	if !snapshotLabelPattern.MatchString(p.ModelProfile) || !snapshotLabelPattern.MatchString(p.ModelProvider) ||
		!validSnapshotText(p.ModelID, 256) || !snapshotLabelPattern.MatchString(p.PromptVersion) {
		return ErrInvalidSnapshot
	}
	return nil
}

func (u SummaryUsage) Validate() error {
	if u.PromptTokens < 1 || u.CompletionTokens < 0 || u.TotalTokens < 1 || u.CachedTokens < 0 ||
		u.CachedTokens > u.PromptTokens || u.TotalTokens < u.PromptTokens+u.CompletionTokens {
		return ErrInvalidSnapshot
	}
	return nil
}

// CandidateSnapshot contains all immutable content and provenance fields. The
// repository assigns Version while serializing writes for one Conversation.
type CandidateSnapshot struct {
	ID                   uuid.UUID
	ConversationID       uuid.UUID
	SupersedesSnapshotID *uuid.UUID
	FromSeq              int64
	ThroughSeq           int64
	SchemaVersion        int
	Provenance           SummaryProvenance
	Payload              Payload
	PayloadSHA256        string
	Usage                SummaryUsage
	Status               SnapshotStatus
	CreatedAt            time.Time
	ActivatedAt          *time.Time
}

type Snapshot struct {
	CandidateSnapshot
	Version int64
}

func (s Snapshot) Validate() error {
	if s.Version < 1 || s.CandidateSnapshot.validateImmutableContent() != nil || !s.Status.Valid() {
		return ErrInvalidSnapshot
	}
	if s.Status == SnapshotStatusCandidate {
		if s.ActivatedAt != nil {
			return ErrInvalidSnapshot
		}
		return nil
	}
	if s.ActivatedAt == nil || s.ActivatedAt.Before(s.CreatedAt) {
		return ErrInvalidSnapshot
	}
	return nil
}

type NewCandidateSnapshotInput struct {
	ID                   uuid.UUID
	ConversationID       uuid.UUID
	SupersedesSnapshotID *uuid.UUID
	FromSeq              int64
	ThroughSeq           int64
	SchemaVersion        int
	Provenance           SummaryProvenance
	Payload              Payload
	Usage                SummaryUsage
	CreatedAt            time.Time
}

var snapshotLabelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func NewCandidateSnapshot(input NewCandidateSnapshotInput) (CandidateSnapshot, error) {
	encoded, err := json.Marshal(input.Payload)
	if err != nil {
		return CandidateSnapshot{}, ErrInvalidSnapshot
	}
	clonedPayload, err := DecodePayload(encoded)
	if err != nil {
		return CandidateSnapshot{}, ErrInvalidSnapshot
	}
	digest := sha256.Sum256(encoded)
	result := CandidateSnapshot{
		ID: input.ID, ConversationID: input.ConversationID, SupersedesSnapshotID: cloneUUIDPointer(input.SupersedesSnapshotID),
		FromSeq: input.FromSeq, ThroughSeq: input.ThroughSeq, SchemaVersion: input.SchemaVersion,
		Provenance: input.Provenance.normalized(),
		Payload:    clonedPayload, PayloadSHA256: hex.EncodeToString(digest[:]), Usage: input.Usage,
		Status: SnapshotStatusCandidate, CreatedAt: input.CreatedAt.UTC(),
	}
	if err := result.Validate(); err != nil {
		return CandidateSnapshot{}, err
	}
	return result, nil
}

func (s CandidateSnapshot) Validate() error {
	if s.validateImmutableContent() != nil || s.Status != SnapshotStatusCandidate || s.ActivatedAt != nil {
		return ErrInvalidSnapshot
	}
	return nil
}

func (s CandidateSnapshot) validateImmutableContent() error {
	if s.ID == uuid.Nil || s.ConversationID == uuid.Nil || s.FromSeq < 1 || s.ThroughSeq < s.FromSeq ||
		s.SchemaVersion != CurrentSchemaVersion || s.Provenance.Validate() != nil || !validSHA256(s.PayloadSHA256) ||
		s.CreatedAt.IsZero() || s.Usage.Validate() != nil {
		return ErrInvalidSnapshot
	}
	if s.SupersedesSnapshotID != nil && (*s.SupersedesSnapshotID == uuid.Nil || *s.SupersedesSnapshotID == s.ID) {
		return ErrInvalidSnapshot
	}
	encoded, err := json.Marshal(s.Payload)
	if err != nil {
		return ErrInvalidSnapshot
	}
	digest := sha256.Sum256(encoded)
	if hex.EncodeToString(digest[:]) != s.PayloadSHA256 {
		return ErrInvalidSnapshot
	}
	return nil
}

func validSnapshotText(value string, maxBytes int) bool {
	return strings.TrimSpace(value) != "" && value == strings.TrimSpace(value) && len(value) <= maxBytes &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func cloneUUIDPointer(value *uuid.UUID) *uuid.UUID {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

type CompactionInput struct {
	ConversationID        uuid.UUID
	FromSeq               int64
	ThroughSeq            int64
	PreviousSnapshot      *Snapshot
	NewMessages           []conversation.Message
	KnownReportReferences map[string][]int64
	Attempt               int
	RepairCode            string
}

type CompactionOutput struct {
	Payload Payload
	Usage   SummaryUsage
}

// NonRetryableCompactionError lets provider adapters mark deterministic
// request/auth failures without coupling this domain to an HTTP client type.
type NonRetryableCompactionError interface {
	error
	NonRetryableCompaction() bool
}

type CompactionRepairCodeError interface {
	error
	CompactionRepairCode() string
}

// CompactionFailureCodeError exposes a stable, content-free failure category
// for observability. Implementations must not return provider error text.
type CompactionFailureCodeError interface {
	error
	CompactionFailureCode() string
}

// CompactionAttemptsError preserves the bounded failure category for every
// attempted compaction while keeping errors.Is/As compatible with the final
// cause.
type CompactionAttemptsError struct {
	cause        error
	failureCodes []string
}

func NewCompactionAttemptsError(cause error, failureCodes []string) *CompactionAttemptsError {
	normalized := make([]string, 0, len(failureCodes))
	for _, code := range failureCodes {
		normalized = append(normalized, NormalizeCompactionFailureCode(code))
	}
	return &CompactionAttemptsError{cause: cause, failureCodes: normalized}
}

func (e *CompactionAttemptsError) Error() string {
	if e == nil {
		return ErrCompactionFailed.Error()
	}
	return fmt.Sprintf("%s after %d attempts", ErrCompactionFailed, len(e.failureCodes))
}

func (e *CompactionAttemptsError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *CompactionAttemptsError) Codes() []string {
	if e == nil {
		return nil
	}
	return append([]string(nil), e.failureCodes...)
}

func (e *CompactionAttemptsError) AttemptCount() int {
	if e == nil {
		return 0
	}
	return len(e.failureCodes)
}

func CompactionAttemptFailureCodes(err error) []string {
	var attempts *CompactionAttemptsError
	if !errors.As(err, &attempts) || attempts == nil {
		return nil
	}
	return attempts.Codes()
}

type Compactor interface {
	Compact(context.Context, CompactionInput) (CompactionOutput, error)
}

type Repository interface {
	Latest(context.Context, uuid.UUID) (*Snapshot, error)
	Get(context.Context, uuid.UUID) (Snapshot, error)
	Save(context.Context, CandidateSnapshot) (Snapshot, error)
}

type ActivationRequest struct {
	ConversationID           uuid.UUID
	CandidateSnapshotID      uuid.UUID
	ExpectedActiveSnapshotID *uuid.UUID
	ActivatedAt              time.Time
}

func (r ActivationRequest) Validate() error {
	if r.ConversationID == uuid.Nil || r.CandidateSnapshotID == uuid.Nil || r.ActivatedAt.IsZero() {
		return ErrInvalidSnapshot
	}
	if r.ExpectedActiveSnapshotID != nil && *r.ExpectedActiveSnapshotID == uuid.Nil {
		return ErrInvalidSnapshot
	}
	return nil
}

type ActivationRepository interface {
	Repository
	Active(context.Context, uuid.UUID) (*Snapshot, error)
	ActiveIdentity(context.Context, uuid.UUID) (ActiveSnapshotIdentity, error)
	Activate(context.Context, ActivationRequest) (Snapshot, error)
}

type ServiceConfig struct {
	Repository       ActivationRepository
	Compactor        Compactor
	Coordinator      Coordinator
	SchemaVersion    int
	MaxPayloadBytes  int
	Provenance       SummaryProvenance
	MaxAttempts      int
	RetryBaseDelay   time.Duration
	RetryJitterRatio float64
	RandomFloat      func() float64
	Clock            func() time.Time
	Cache            SnapshotCache
	CacheExpected    bool
	CacheObserver    CacheObserver
}

type Service struct {
	repository       ActivationRepository
	compactor        Compactor
	coordinator      Coordinator
	schemaVersion    int
	maxPayloadBytes  int
	provenance       SummaryProvenance
	maxAttempts      int
	retryBaseDelay   time.Duration
	retryJitterRatio float64
	randomFloat      func() float64
	clock            func() time.Time
	cache            SnapshotCache
	cacheExpected    bool
	cacheObserver    CacheObserver
}

func NewService(config ServiceConfig) (*Service, error) {
	if config.Repository == nil || config.Compactor == nil || config.SchemaVersion != CurrentSchemaVersion ||
		config.MaxPayloadBytes < 1024 || config.MaxPayloadBytes > 1024*1024 ||
		config.Provenance.Validate() != nil ||
		config.MaxAttempts < 1 || config.MaxAttempts > 5 || config.RetryBaseDelay < 0 || config.RetryBaseDelay > time.Minute ||
		math.IsNaN(config.RetryJitterRatio) || math.IsInf(config.RetryJitterRatio, 0) ||
		config.RetryJitterRatio < 0 || config.RetryJitterRatio > 0.50 {
		return nil, ErrInvalidSnapshot
	}
	if config.Cache != nil && !config.CacheExpected {
		return nil, ErrInvalidSnapshot
	}
	if config.Coordinator == nil {
		config.Coordinator = NewLocalCoordinator()
	}
	clock := config.Clock
	if clock == nil {
		clock = time.Now
	}
	randomFloat := config.RandomFloat
	if randomFloat == nil {
		randomFloat = rand.Float64
	}
	return &Service{
		repository: config.Repository, compactor: config.Compactor, coordinator: config.Coordinator, schemaVersion: config.SchemaVersion,
		maxPayloadBytes: config.MaxPayloadBytes,
		provenance:      config.Provenance.normalized(),
		maxAttempts:     config.MaxAttempts, retryBaseDelay: config.RetryBaseDelay,
		retryJitterRatio: config.RetryJitterRatio, randomFloat: randomFloat, clock: clock,
		cache: config.Cache, cacheExpected: config.CacheExpected, cacheObserver: config.CacheObserver,
	}, nil
}

type ShadowRequest struct {
	ConversationID    uuid.UUID
	CompletedMessages []conversation.Message
}

type PrepareActiveRequest struct {
	ConversationID    uuid.UUID
	CompletedMessages []conversation.Message
	ActivationGate    ActivationGate
}

// PreparedActivation is a compatibility shape used while Snapshot persistence
// still stores candidate and Active rows separately. Runtime callers publish
// through PrepareActive so generation and publication share one coordinator.
type PreparedActivation struct {
	CandidateSnapshot        *Snapshot
	CurrentSnapshot          *Snapshot
	ExpectedActiveSnapshotID *uuid.UUID
}

// ActivationGate validates caller-specific runtime constraints after the
// immutable candidate is saved but before it can replace the current Active
// Snapshot. Structural and provenance validation remains owned by this module.
type ActivationGate interface {
	ValidateForActivation(context.Context, Snapshot) error
}

// GenerateShadow compacts and persists a validated candidate Snapshot without
// activating it or changing the Conversation Runner's model-visible prompt.
// Deprecated: production callers must use PrepareActive. This compatibility
// path is still serialized so it cannot race soft or hard compaction.
func (s *Service) GenerateShadow(ctx context.Context, request ShadowRequest) (Snapshot, error) {
	if s == nil || s.repository == nil || s.compactor == nil || s.coordinator == nil || request.ConversationID == uuid.Nil {
		return Snapshot{}, ErrInvalidShadowInput
	}
	var result Snapshot
	err := s.coordinator.WithinConversation(ctx, request.ConversationID, func(lockCtx context.Context) error {
		generated, generateErr := s.generateShadowLocked(lockCtx, request)
		result = generated
		return generateErr
	})
	return result, err
}

func (s *Service) generateShadowLocked(ctx context.Context, request ShadowRequest) (Snapshot, error) {
	previous, err := s.repository.Latest(ctx, request.ConversationID)
	if err != nil && !errors.Is(err, ErrSnapshotNotFound) {
		return Snapshot{}, fmt.Errorf("load previous conversation memory snapshot: %w", err)
	}
	if errors.Is(err, ErrSnapshotNotFound) {
		previous = nil
	}
	return s.generateCandidate(ctx, request, previous, 1, s.maxAttempts)
}

func (s *Service) Active(ctx context.Context, conversationID uuid.UUID) (*Snapshot, error) {
	if s == nil || conversationID == uuid.Nil {
		return nil, ErrInvalidShadowInput
	}
	if !s.cacheExpected {
		return s.repository.Active(ctx, conversationID)
	}
	if s.cache == nil {
		active, err := s.repository.Active(ctx, conversationID)
		if err == nil && active != nil {
			s.observeCache(ctx, CacheObservation{
				Operation: CacheOperationActiveLoad, Status: CacheStatusDegraded, Reason: CacheReasonUnavailable,
				ConversationID: conversationID, SnapshotID: active.ID,
			})
		}
		return active, err
	}
	identity, err := s.repository.ActiveIdentity(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	if identity.Validate() != nil || identity.ConversationID != conversationID {
		return nil, ErrInvalidSnapshot
	}

	started := time.Now()
	cached, cacheErr := s.cache.Load(ctx, conversationID, identity.SnapshotID)
	cacheDuration := time.Since(started)
	if cacheErr == nil && activeSnapshotMatchesIdentity(cached, identity) {
		s.observeCache(ctx, CacheObservation{
			Operation: CacheOperationActiveLoad, Status: CacheStatusHit,
			ConversationID: conversationID, SnapshotID: identity.SnapshotID, Duration: cacheDuration,
		})
		return cloneSnapshot(&cached), nil
	}

	status := CacheStatusMiss
	reason := CacheReason("")
	if cacheErr == nil {
		status = CacheStatusDegraded
		if cached.Validate() != nil || cached.ConversationID == uuid.Nil || cached.ID == uuid.Nil {
			reason = CacheReasonInvalid
		} else {
			reason = CacheReasonStale
		}
	} else if !errors.Is(cacheErr, ErrSnapshotCacheMiss) {
		status = CacheStatusDegraded
		switch {
		case errors.Is(cacheErr, context.DeadlineExceeded), errors.Is(cacheErr, context.Canceled):
			reason = CacheReasonTimeout
		case errors.Is(cacheErr, ErrSnapshotCacheInvalid):
			reason = CacheReasonInvalid
		default:
			reason = CacheReasonReadFailed
		}
	}

	active, err := s.repository.Active(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	storeStarted := time.Now()
	storeErr := s.cache.Store(ctx, *active)
	cacheDuration += time.Since(storeStarted)
	if storeErr != nil && status != CacheStatusDegraded {
		status = CacheStatusDegraded
		reason = CacheReasonWriteFailed
	}
	s.observeCache(ctx, CacheObservation{
		Operation: CacheOperationActiveLoad, Status: status, Reason: reason,
		ConversationID: conversationID, SnapshotID: active.ID, Duration: cacheDuration,
	})
	return cloneSnapshot(active), nil
}

// DeleteConversationCache is deliberately best effort. PostgreSQL owns the
// Conversation lifecycle; cache deletion failures are observable and expire by
// TTL rather than failing the caller's durable delete transaction.
func (s *Service) DeleteConversationCache(ctx context.Context, conversationID uuid.UUID) {
	if s == nil || conversationID == uuid.Nil || !s.cacheExpected {
		return
	}
	if s.cache == nil {
		s.observeCache(ctx, CacheObservation{
			Operation: CacheOperationDelete, Status: CacheStatusDegraded, Reason: CacheReasonUnavailable,
			ConversationID: conversationID,
		})
		return
	}
	started := time.Now()
	err := s.cache.DeleteConversation(ctx, conversationID)
	observation := CacheObservation{
		Operation: CacheOperationDelete, Status: CacheStatusSucceeded,
		ConversationID: conversationID, Duration: time.Since(started),
	}
	if err != nil {
		observation.Status = CacheStatusDegraded
		observation.Reason = CacheReasonDeleteFailed
	}
	s.observeCache(ctx, observation)
}

func (s *Service) observeCache(ctx context.Context, observation CacheObservation) {
	if s == nil || s.cacheObserver == nil || observation.Validate() != nil {
		return
	}
	s.cacheObserver.Observe(ctx, observation)
}

// PrepareActivationCandidate generates and validates an immutable candidate.
// Deprecated: production callers must use PrepareActive. Kept temporarily for
// schema-v1 compatibility and serialized by the Conversation coordinator.
func (s *Service) PrepareActivationCandidate(
	ctx context.Context,
	request PrepareActiveRequest,
) (PreparedActivation, error) {
	return s.prepareActivationCandidateCoordinated(ctx, request, 1, s.maxAttempts)
}

// PrepareActivationCandidateOnce performs exactly one model-backed compaction
// attempt. Async Jobs call it once per durable attempt so their configured Job
// limit is also the total model-call limit rather than multiplying the
// Service's synchronous repair loop.
func (s *Service) PrepareActivationCandidateOnce(
	ctx context.Context,
	request PrepareActiveRequest,
	attempt int,
) (PreparedActivation, error) {
	if attempt < 1 || attempt > 10 {
		return PreparedActivation{}, ErrInvalidShadowInput
	}
	return s.prepareActivationCandidateCoordinated(ctx, request, attempt, attempt)
}

func (s *Service) prepareActivationCandidateCoordinated(
	ctx context.Context,
	request PrepareActiveRequest,
	firstAttempt, lastAttempt int,
) (PreparedActivation, error) {
	if s == nil || s.coordinator == nil || request.ConversationID == uuid.Nil {
		return PreparedActivation{}, ErrInvalidShadowInput
	}
	var result PreparedActivation
	err := s.coordinator.WithinConversation(ctx, request.ConversationID, func(lockCtx context.Context) error {
		prepared, prepareErr := s.prepareActivationCandidate(lockCtx, request, firstAttempt, lastAttempt)
		result = prepared
		return prepareErr
	})
	return result, err
}

func (s *Service) prepareActivationCandidate(
	ctx context.Context,
	request PrepareActiveRequest,
	firstAttempt, lastAttempt int,
) (PreparedActivation, error) {
	if s == nil || request.ConversationID == uuid.Nil || request.ActivationGate == nil {
		return PreparedActivation{}, ErrInvalidShadowInput
	}
	previous, err := s.repository.Active(ctx, request.ConversationID)
	if err != nil && !errors.Is(err, ErrSnapshotNotFound) {
		return PreparedActivation{}, fmt.Errorf("load active conversation memory snapshot: %w", err)
	}
	if errors.Is(err, ErrSnapshotNotFound) {
		previous = nil
	}
	if previous != nil && (previous.ConversationID != request.ConversationID || previous.Validate() != nil) {
		return PreparedActivation{}, ErrInvalidShadowInput
	}
	if _, err := validatedCompletedMessages(request.ConversationID, request.CompletedMessages); err != nil {
		return PreparedActivation{}, err
	}
	if previous != nil && noMessagesAfter(request.CompletedMessages, previous.ThroughSeq) {
		current := cloneSnapshot(previous)
		if err := request.ActivationGate.ValidateForActivation(ctx, *current); err != nil {
			return PreparedActivation{}, fmt.Errorf("validate active conversation memory snapshot: %w", err)
		}
		return PreparedActivation{CurrentSnapshot: current}, nil
	}
	candidate, err := s.generateCandidate(ctx, ShadowRequest{
		ConversationID: request.ConversationID, CompletedMessages: request.CompletedMessages,
	}, previous, firstAttempt, lastAttempt)
	if err != nil {
		return PreparedActivation{}, err
	}
	if err := request.ActivationGate.ValidateForActivation(ctx, candidate); err != nil {
		return PreparedActivation{}, fmt.Errorf("validate conversation memory candidate for activation: %w", err)
	}
	result := PreparedActivation{CandidateSnapshot: cloneSnapshot(&candidate)}
	if previous != nil {
		result.ExpectedActiveSnapshotID = cloneUUIDPointer(&previous.ID)
	}
	return result, nil
}

// PrepareActive synchronously refreshes the current summary. Soft and hard
// triggers share the same Conversation coordinator and reload the current
// summary after acquiring it, so only one of them calls the model.
func (s *Service) PrepareActive(ctx context.Context, request PrepareActiveRequest) (Snapshot, error) {
	return s.prepareActive(ctx, request, 1, s.maxAttempts)
}

// PrepareActiveOnce gives a durable async Job exactly one model call per Job
// attempt while preserving the same coordinated publication path.
func (s *Service) PrepareActiveOnce(
	ctx context.Context,
	request PrepareActiveRequest,
	attempt int,
) (Snapshot, error) {
	if attempt < 1 || attempt > 10 {
		return Snapshot{}, ErrInvalidShadowInput
	}
	return s.prepareActive(ctx, request, attempt, attempt)
}

func (s *Service) prepareActive(
	ctx context.Context,
	request PrepareActiveRequest,
	firstAttempt, lastAttempt int,
) (Snapshot, error) {
	if s == nil || s.coordinator == nil || request.ConversationID == uuid.Nil {
		return Snapshot{}, ErrInvalidShadowInput
	}
	var result Snapshot
	err := s.coordinator.WithinConversation(ctx, request.ConversationID, func(lockCtx context.Context) error {
		prepared, prepareErr := s.prepareActiveLocked(lockCtx, request, firstAttempt, lastAttempt)
		result = prepared
		return prepareErr
	})
	return result, err
}

func (s *Service) prepareActiveLocked(
	ctx context.Context,
	request PrepareActiveRequest,
	firstAttempt, lastAttempt int,
) (Snapshot, error) {
	prepared, err := s.prepareActivationCandidate(ctx, request, firstAttempt, lastAttempt)
	if err != nil {
		return Snapshot{}, err
	}
	if prepared.CurrentSnapshot != nil {
		return *prepared.CurrentSnapshot, nil
	}
	if prepared.CandidateSnapshot == nil {
		return Snapshot{}, ErrInvalidSnapshot
	}
	candidate := *prepared.CandidateSnapshot
	activation := ActivationRequest{
		ConversationID: request.ConversationID, CandidateSnapshotID: candidate.ID,
		ExpectedActiveSnapshotID: cloneUUIDPointer(prepared.ExpectedActiveSnapshotID),
		ActivatedAt:              s.clock().UTC(),
	}
	activated, err := s.repository.Activate(ctx, activation)
	if err == nil {
		return activated, nil
	}
	if !errors.Is(err, ErrSnapshotActivationConflict) {
		return Snapshot{}, fmt.Errorf("activate conversation memory snapshot: %w", err)
	}
	winner, winnerErr := s.repository.Active(ctx, request.ConversationID)
	if winnerErr == nil && winner.FromSeq == candidate.FromSeq && winner.ThroughSeq >= candidate.ThroughSeq {
		if err := request.ActivationGate.ValidateForActivation(ctx, *winner); err != nil {
			return Snapshot{}, fmt.Errorf("validate winning conversation memory snapshot: %w", err)
		}
		return *winner, nil
	}
	if winnerErr != nil && !errors.Is(winnerErr, ErrSnapshotNotFound) {
		return Snapshot{}, fmt.Errorf("load winning conversation memory snapshot: %w", winnerErr)
	}
	return Snapshot{}, ErrSnapshotActivationConflict
}

func (s *Service) generateCandidate(
	ctx context.Context,
	request ShadowRequest,
	previous *Snapshot,
	firstAttempt, lastAttempt int,
) (Snapshot, error) {
	if firstAttempt < 1 || lastAttempt < firstAttempt || lastAttempt > 10 {
		return Snapshot{}, ErrInvalidShadowInput
	}
	newMessages, fromSeq, throughSeq, err := prepareMessages(request.ConversationID, request.CompletedMessages, previous)
	if err != nil {
		return Snapshot{}, err
	}
	knownReports := knownReportReferences(request.CompletedMessages)
	validation := buildValidationContext(fromSeq, throughSeq, s.maxPayloadBytes, previous, newMessages, knownReports)
	repairCode := ""
	var lastErr error
	failureCodes := make([]string, 0, lastAttempt-firstAttempt+1)
	for attempt := firstAttempt; attempt <= lastAttempt; attempt++ {
		if attempt > firstAttempt && s.retryBaseDelay > 0 {
			if err := waitForRetry(ctx, s.retryDelay(attempt-firstAttempt)); err != nil {
				return Snapshot{}, NewCompactionAttemptsError(errors.Join(
					fmt.Errorf("%w: retry wait canceled", ErrCompactionFailed), lastErr, err,
				), failureCodes)
			}
		}
		output, compactErr := s.compactor.Compact(ctx, CompactionInput{
			ConversationID: request.ConversationID, FromSeq: fromSeq, ThroughSeq: throughSeq,
			PreviousSnapshot: cloneSnapshot(previous), NewMessages: cloneMessages(newMessages),
			KnownReportReferences: cloneReferenceSources(knownReports), Attempt: attempt, RepairCode: repairCode,
		})
		if compactErr != nil {
			lastErr = compactErr
			var coded CompactionFailureCodeError
			if errors.As(compactErr, &coded) {
				failureCodes = append(failureCodes, NormalizeCompactionFailureCode(coded.CompactionFailureCode()))
			} else if code := FailureCode(compactErr); code != "" {
				failureCodes = append(failureCodes, code)
			} else {
				failureCodes = append(failureCodes, "compaction_failed")
			}
			var nonRetryable NonRetryableCompactionError
			if errors.As(compactErr, &nonRetryable) && nonRetryable.NonRetryableCompaction() {
				break
			}
			repairCode = compactionRepairCode(compactErr)
			continue
		}
		if output.Usage.Validate() != nil {
			lastErr = ErrInvalidSnapshot
			repairCode = "usage_invalid"
			failureCodes = append(failureCodes, repairCode)
			continue
		}
		// Stable references are an application-owned projection. The model may
		// summarize a referenced source, but it cannot mint, remove, or rewrite
		// the identity and provenance of a task, evidence item, or report.
		output.Payload = normalizeCurrentPayload(output.Payload)
		output.Payload = mergeTrustedReferences(output.Payload, previous, validation)
		if validateErr := ValidatePayload(output.Payload, validation); validateErr != nil {
			lastErr = validateErr
			repairCode = validationRepairCode(validateErr)
			failureCodes = append(failureCodes, repairCode)
			continue
		}
		id, idErr := uuid.NewV7()
		if idErr != nil {
			return Snapshot{}, fmt.Errorf("generate conversation memory snapshot id: %w", idErr)
		}
		var predecessor *uuid.UUID
		if previous != nil {
			predecessor = &previous.ID
		}
		candidate, candidateErr := NewCandidateSnapshot(NewCandidateSnapshotInput{
			ID: id, ConversationID: request.ConversationID, SupersedesSnapshotID: predecessor,
			FromSeq: fromSeq, ThroughSeq: throughSeq, SchemaVersion: s.schemaVersion,
			Provenance: s.provenance,
			Payload:    output.Payload, Usage: output.Usage, CreatedAt: s.clock().UTC(),
		})
		if candidateErr != nil {
			lastErr = candidateErr
			repairCode = "snapshot_invalid"
			failureCodes = append(failureCodes, repairCode)
			continue
		}
		return s.repository.Save(ctx, candidate)
	}
	return Snapshot{}, NewCompactionAttemptsError(fmt.Errorf("%w after %d attempts: %w", ErrCompactionFailed,
		lastAttempt-firstAttempt+1, lastErr), failureCodes)
}

func normalizeCurrentPayload(payload Payload) Payload {
	normalize := func(entries []Entry) []Entry {
		result := make([]Entry, 0, len(entries))
		for _, entry := range entries {
			if entry.Status == EntryStatusSuperseded {
				continue
			}
			entry.Status = EntryStatusActive
			entry.SupersedesEntryID = ""
			result = append(result, entry)
		}
		return result
	}
	if payload.ConversationGoal != nil {
		payload.ConversationGoal.Status = EntryStatusActive
		payload.ConversationGoal.SupersedesEntryID = ""
	}
	payload.Facts = normalize(payload.Facts)
	payload.Decisions = normalize(payload.Decisions)
	payload.Corrections = normalize(payload.Corrections)
	payload.OpenQuestions = normalize(payload.OpenQuestions)
	for index := range payload.Todos {
		payload.Todos[index].SupersedesEntryID = ""
	}
	return payload
}

// mergeTrustedReferences rebuilds all reference sections from the structured
// message catalog and the previous validated snapshot. Matching model entries
// contribute only their bounded conclusion text; identity, source sequences,
// hashes, status, and entry IDs are deterministic application data.
func mergeTrustedReferences(payload Payload, previous *Snapshot, context ValidationContext) Payload {
	result := payload
	result.EvidenceReferences = mergeEvidenceReferences(payload.EvidenceReferences, previousPayload(previous), context.KnownEvidenceReferences)
	result.TaskReferences = mergeStableReferences(payload.TaskReferences, previousPayload(previous), context.KnownTaskReferences, ReferenceTypeDiagnosisTask)
	result.ReportReferences = mergeStableReferences(payload.ReportReferences, previousPayload(previous), context.KnownReportReferences, ReferenceTypeDiagnosisReport)
	return result
}

func previousPayload(snapshot *Snapshot) *Payload {
	if snapshot == nil {
		return nil
	}
	payload := snapshot.Payload
	return &payload
}

func mergeEvidenceReferences(modelEntries []ReferenceEntry, previous *Payload, trusted map[string]EvidenceReferenceIdentity) []ReferenceEntry {
	previousEntries := make(map[string]ReferenceEntry)
	if previous != nil {
		for _, entry := range previous.EvidenceReferences {
			previousEntries[entry.ReferenceID] = entry
		}
	}
	modelText := referenceConclusionMap(modelEntries)
	ids := make([]string, 0, len(modelText))
	for id := range modelText {
		if _, exists := trusted[id]; exists {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	result := make([]ReferenceEntry, 0, len(ids)+len(previousEntries))
	for _, id := range ids {
		if entry, ok := previousEntries[id]; ok {
			entry.Content = modelText[id]
			result = append(result, entry)
			continue
		}
		identity := trusted[id]
		result = append(result, newTrustedReferenceEntry(identity.ReferenceType, id, identity.ContentSHA256, identity.SourceMessageSeqs, modelText[id]))
	}
	return result
}

func mergeStableReferences(modelEntries []ReferenceEntry, previous *Payload, trusted map[string]StableReferenceIdentity, referenceType ReferenceType) []ReferenceEntry {
	previousEntries := make(map[string]ReferenceEntry)
	if previous != nil {
		entries := previous.TaskReferences
		if referenceType == ReferenceTypeDiagnosisReport {
			entries = previous.ReportReferences
		}
		for _, entry := range entries {
			previousEntries[entry.ReferenceID] = entry
		}
	}
	modelText := referenceConclusionMap(modelEntries)
	ids := make([]string, 0, len(modelText))
	for id := range modelText {
		if _, exists := trusted[id]; exists {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	result := make([]ReferenceEntry, 0, len(ids)+len(previousEntries))
	for _, id := range ids {
		if entry, ok := previousEntries[id]; ok {
			entry.Content = modelText[id]
			result = append(result, entry)
			continue
		}
		result = append(result, newTrustedReferenceEntry(referenceType, id, "", trusted[id].SourceMessageSeqs, modelText[id]))
	}
	return result
}

func referenceConclusionMap(entries []ReferenceEntry) map[string]string {
	result := make(map[string]string, len(entries))
	for _, entry := range entries {
		content := strings.TrimSpace(entry.Content)
		if content != "" && len([]rune(content)) <= MaxEntryContentRunes {
			result[entry.ReferenceID] = content
		}
	}
	return result
}

func newTrustedReferenceEntry(referenceType ReferenceType, referenceID, contentSHA256 string, sourceSeqs []int64, conclusion string) ReferenceEntry {
	seed := sha256.Sum256([]byte(string(referenceType) + "\x00" + referenceID))
	entryID := "ref_" + hex.EncodeToString(seed[:])[:24]
	if conclusion == "" {
		conclusion = "Referenced source: " + referenceID
	}
	return ReferenceEntry{
		Entry:         Entry{EntryID: entryID, Content: conclusion, SourceMessageSeqs: append([]int64(nil), sourceSeqs...), Status: EntryStatusActive},
		ReferenceType: referenceType, ReferenceID: referenceID, ContentSHA256: contentSHA256,
	}
}

var compactionFailureCodes = map[string]struct{}{
	"provider_http_400": {}, "provider_http_401": {}, "provider_http_403": {},
	"provider_http_429": {}, "provider_http_5xx": {}, "provider_timeout": {},
	"provider_canceled": {}, "provider_connection_failed": {}, "provider_request_failed": {},
	"compaction_failed": {}, "usage_invalid": {}, "snapshot_invalid": {}, "output_truncated": {},
	"output_too_large": {}, "local_budget_exceeded": {},
}

func NormalizeCompactionFailureCode(code string) string {
	code = strings.TrimSpace(code)
	if ValidCompactionFailureCode(code) {
		return code
	}
	return "compaction_failed"
}

func ValidCompactionFailureCode(code string) bool {
	code = strings.TrimSpace(code)
	if _, ok := compactionFailureCodes[code]; ok {
		return true
	}
	return validDomainCompactionFailureCode(code)
}

func validDomainCompactionFailureCode(code string) bool {
	switch code {
	case "user_source_required", "source_out_of_range", "entry_reference_unknown", "stable_reference_unknown",
		"payload_too_large",
		"payload_schema_empty", "payload_schema_top_level_json", "payload_schema_top_level_extra_fields",
		"payload_schema_null_array", "payload_schema_invalid", "entry_entry_id", "entry_content",
		"entry_source_count", "entry_source_order", "entry_source_duplicate", "entry_invalid", "entry_status",
		"evidence_reference_id_unknown", "evidence_reference_identity_mismatch", "evidence_reference_source_mismatch",
		"task_reference_id_unknown", "task_reference_identity_mismatch", "task_reference_source_mismatch",
		"report_reference_id_unknown", "report_reference_identity_mismatch", "report_reference_source_mismatch":
		return true
	}
	const missingPrefix = "payload_schema_top_level_missing_"
	if strings.HasPrefix(code, missingPrefix) {
		return validPayloadSchemaField(strings.TrimPrefix(code, missingPrefix))
	}
	const fieldPrefix = "payload_schema_field_"
	if !strings.HasPrefix(code, fieldPrefix) {
		return false
	}
	remainder := strings.TrimPrefix(code, fieldPrefix)
	for _, kind := range []string{"invalid", "object", "array", "string", "null", "boolean", "number"} {
		if field := strings.TrimSuffix(remainder, "_"+kind); field != remainder && validPayloadSchemaField(field) {
			return true
		}
	}
	return false
}

func validPayloadSchemaField(field string) bool {
	switch field {
	case "conversation_goal", "facts", "decisions", "corrections", "evidence_references",
		"open_questions", "todos", "task_references", "report_references":
		return true
	default:
		return false
	}
}

func (s *Service) retryDelay(retryNumber int) time.Duration {
	base := s.retryBaseDelay * time.Duration(1<<(retryNumber-1))
	if s.retryJitterRatio == 0 {
		return base
	}
	random := s.randomFloat()
	if random < 0 {
		random = 0
	} else if random > 1 {
		random = 1
	}
	return time.Duration(float64(base) * (1 + (random*2-1)*s.retryJitterRatio))
}

func noMessagesAfter(messages []conversation.Message, throughSeq int64) bool {
	for _, message := range messages {
		if message.Seq > throughSeq {
			return false
		}
	}
	return true
}

func prepareMessages(conversationID uuid.UUID, messages []conversation.Message, previous *Snapshot) ([]conversation.Message, int64, int64, error) {
	ordered, err := validatedCompletedMessages(conversationID, messages)
	if err != nil {
		return nil, 0, 0, err
	}
	start := int64(1)
	fromSeq := int64(1)
	if previous != nil {
		if previous.ConversationID != conversationID || previous.Validate() != nil {
			return nil, 0, 0, ErrInvalidShadowInput
		}
		start = previous.ThroughSeq + 1
		fromSeq = previous.FromSeq
	}
	newMessages := make([]conversation.Message, 0, len(ordered))
	for _, message := range ordered {
		if message.Seq >= start {
			newMessages = append(newMessages, message)
		}
	}
	if len(newMessages) == 0 || newMessages[0].Seq != start {
		return nil, 0, 0, ErrInvalidShadowInput
	}
	for index := 1; index < len(newMessages); index++ {
		if newMessages[index].Seq != newMessages[index-1].Seq+1 {
			return nil, 0, 0, ErrInvalidShadowInput
		}
	}
	return newMessages, fromSeq, newMessages[len(newMessages)-1].Seq, nil
}

func validatedCompletedMessages(conversationID uuid.UUID, messages []conversation.Message) ([]conversation.Message, error) {
	if conversationID == uuid.Nil || len(messages) == 0 {
		return nil, ErrInvalidShadowInput
	}
	ordered := cloneMessages(messages)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Seq < ordered[j].Seq })
	for index, message := range ordered {
		if message.ID == uuid.Nil || message.ConversationID != conversationID || message.Seq < 1 || !message.Role.Valid() ||
			strings.TrimSpace(message.Content) == "" || (index > 0 && message.Seq == ordered[index-1].Seq) {
			return nil, ErrInvalidShadowInput
		}
	}
	return ordered, nil
}

func buildValidationContext(
	fromSeq, throughSeq int64,
	maxPayloadBytes int,
	previous *Snapshot,
	messages []conversation.Message,
	reportReferences map[string][]int64,
) ValidationContext {
	result := ValidationContext{
		FromSeq: fromSeq, ThroughSeq: throughSeq, MaxPayloadBytes: maxPayloadBytes,
		MessageRoles:            make(map[int64]conversation.MessageRole, len(messages)),
		KnownEvidenceReferences: make(map[string]EvidenceReferenceIdentity),
		KnownTaskReferences:     make(map[string]StableReferenceIdentity),
		KnownReportReferences:   make(map[string]StableReferenceIdentity, len(reportReferences)),
	}
	for reference, sourceSeqs := range reportReferences {
		result.KnownReportReferences[reference] = StableReferenceIdentity{SourceMessageSeqs: append([]int64(nil), sourceSeqs...)}
	}
	if previous != nil {
		previousPayload := previous.Payload
		result.PreviousPayload = &previousPayload
		for _, reference := range previous.Payload.EvidenceReferences {
			result.KnownEvidenceReferences[reference.ReferenceID] = EvidenceReferenceIdentity{
				ReferenceType: reference.ReferenceType, ContentSHA256: reference.ContentSHA256,
				SourceMessageSeqs: append([]int64(nil), reference.SourceMessageSeqs...),
			}
		}
		for _, reference := range previous.Payload.TaskReferences {
			result.KnownTaskReferences[reference.ReferenceID] = StableReferenceIdentity{
				SourceMessageSeqs: append([]int64(nil), reference.SourceMessageSeqs...),
			}
		}
		for _, reference := range previous.Payload.ReportReferences {
			result.KnownReportReferences[reference.ReferenceID] = StableReferenceIdentity{
				SourceMessageSeqs: append([]int64(nil), reference.SourceMessageSeqs...),
			}
		}
	}
	for _, message := range messages {
		result.MessageRoles[message.Seq] = message.Role
		for _, citation := range message.Citations {
			identity, exists := result.KnownEvidenceReferences[citation.SourceRef]
			if !exists {
				identity.ReferenceType = ReferenceType(citation.SourceType)
				identity.ContentSHA256 = citation.ContentSHA256
			}
			if identity.ReferenceType == ReferenceType(citation.SourceType) && identity.ContentSHA256 == citation.ContentSHA256 {
				identity.SourceMessageSeqs = appendUniqueSequence(identity.SourceMessageSeqs, message.Seq)
				result.KnownEvidenceReferences[citation.SourceRef] = identity
			}
		}
		for _, reference := range message.TaskReferences {
			key := reference.TaskID.String()
			identity := result.KnownTaskReferences[key]
			identity.SourceMessageSeqs = append(identity.SourceMessageSeqs, message.Seq)
			result.KnownTaskReferences[key] = identity
		}
	}
	return result
}

func appendUniqueSequence(sequences []int64, value int64) []int64 {
	for _, current := range sequences {
		if current == value {
			return sequences
		}
	}
	return append(sequences, value)
}

func validationRepairCode(err error) string {
	if code := FailureCode(err); code != "" {
		return code
	}
	return "payload_invalid"
}

func compactionRepairCode(err error) string {
	var coded CompactionRepairCodeError
	if errors.As(err, &coded) && coded.CompactionRepairCode() != "" {
		return coded.CompactionRepairCode()
	}
	if code := FailureCode(err); strings.HasPrefix(code, "payload_schema_") {
		return code
	}
	return "generation_failed"
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func cloneSnapshot(snapshot *Snapshot) *Snapshot {
	if snapshot == nil {
		return nil
	}
	copy := *snapshot
	copy.SupersedesSnapshotID = cloneUUIDPointer(snapshot.SupersedesSnapshotID)
	copy.Payload = clonePayload(snapshot.Payload)
	return &copy
}

func clonePayload(payload Payload) Payload {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Payload{}
	}
	copy, err := DecodePayload(encoded)
	if err != nil {
		return Payload{}
	}
	return copy
}

func cloneMessages(messages []conversation.Message) []conversation.Message {
	result := make([]conversation.Message, len(messages))
	copy(result, messages)
	for index := range result {
		result[index].CaseReferences = append([]conversation.CaseReference(nil), messages[index].CaseReferences...)
		result[index].TaskReferences = append([]conversation.TaskReference(nil), messages[index].TaskReferences...)
		result[index].ReportReferences = append([]conversation.ReportReference(nil), messages[index].ReportReferences...)
		result[index].Attachments = append([]conversation.MessageAttachment(nil), messages[index].Attachments...)
		result[index].Citations = append([]conversation.MessageCitation(nil), messages[index].Citations...)
	}
	return result
}

func cloneReferenceSources(source map[string][]int64) map[string][]int64 {
	result := make(map[string][]int64, len(source))
	for key, sequences := range source {
		result[key] = append([]int64(nil), sequences...)
	}
	return result
}

func knownReportReferences(messages []conversation.Message) map[string][]int64 {
	result := make(map[string][]int64)
	for _, message := range messages {
		for _, reference := range message.ReportReferences {
			result[reference.ReferenceID] = append(result[reference.ReferenceID], message.Seq)
		}
	}
	return result
}
