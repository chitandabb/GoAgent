package conversationmemory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversation"

	"github.com/google/uuid"
)

var (
	ErrSnapshotNotFound   = errors.New("conversation memory snapshot is not found")
	ErrInvalidSnapshot    = errors.New("conversation memory snapshot is invalid")
	ErrInvalidShadowInput = errors.New("conversation memory shadow input is invalid")
	ErrCompactionFailed   = errors.New("conversation memory compaction failed")
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

type Compactor interface {
	Compact(context.Context, CompactionInput) (CompactionOutput, error)
}

type Repository interface {
	Latest(context.Context, uuid.UUID) (*Snapshot, error)
	Get(context.Context, uuid.UUID) (Snapshot, error)
	Save(context.Context, CandidateSnapshot) (Snapshot, error)
}

type ServiceConfig struct {
	Repository      Repository
	Compactor       Compactor
	SchemaVersion   int
	MaxPayloadBytes int
	Provenance      SummaryProvenance
	MaxAttempts     int
	RetryBaseDelay  time.Duration
	Clock           func() time.Time
}

type Service struct {
	repository      Repository
	compactor       Compactor
	schemaVersion   int
	maxPayloadBytes int
	provenance      SummaryProvenance
	maxAttempts     int
	retryBaseDelay  time.Duration
	clock           func() time.Time
}

func NewService(config ServiceConfig) (*Service, error) {
	if config.Repository == nil || config.Compactor == nil || config.SchemaVersion != CurrentSchemaVersion ||
		config.MaxPayloadBytes < 1024 || config.MaxPayloadBytes > 1024*1024 ||
		config.Provenance.Validate() != nil ||
		config.MaxAttempts < 1 || config.MaxAttempts > 5 || config.RetryBaseDelay < 0 || config.RetryBaseDelay > time.Minute {
		return nil, ErrInvalidSnapshot
	}
	clock := config.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Service{
		repository: config.Repository, compactor: config.Compactor, schemaVersion: config.SchemaVersion,
		maxPayloadBytes: config.MaxPayloadBytes,
		provenance:      config.Provenance.normalized(),
		maxAttempts:     config.MaxAttempts, retryBaseDelay: config.RetryBaseDelay, clock: clock,
	}, nil
}

type ShadowRequest struct {
	ConversationID        uuid.UUID
	CompletedMessages     []conversation.Message
	KnownReportReferences map[string][]int64
}

// GenerateShadow compacts and persists a validated candidate Snapshot without
// activating it or changing the Conversation Runner's model-visible prompt.
func (s *Service) GenerateShadow(ctx context.Context, request ShadowRequest) (Snapshot, error) {
	if s == nil || s.repository == nil || s.compactor == nil || request.ConversationID == uuid.Nil {
		return Snapshot{}, ErrInvalidShadowInput
	}
	previous, err := s.repository.Latest(ctx, request.ConversationID)
	if err != nil && !errors.Is(err, ErrSnapshotNotFound) {
		return Snapshot{}, fmt.Errorf("load previous conversation memory snapshot: %w", err)
	}
	if errors.Is(err, ErrSnapshotNotFound) {
		previous = nil
	}
	newMessages, fromSeq, throughSeq, err := prepareMessages(request.ConversationID, request.CompletedMessages, previous)
	if err != nil {
		return Snapshot{}, err
	}
	validation := buildValidationContext(fromSeq, throughSeq, s.maxPayloadBytes, previous, newMessages, request.KnownReportReferences)
	repairCode := ""
	var lastErr error
	for attempt := 1; attempt <= s.maxAttempts; attempt++ {
		if attempt > 1 && s.retryBaseDelay > 0 {
			if err := waitForRetry(ctx, s.retryBaseDelay*time.Duration(1<<(attempt-2))); err != nil {
				return Snapshot{}, fmt.Errorf("%w: %v", ErrCompactionFailed, err)
			}
		}
		output, compactErr := s.compactor.Compact(ctx, CompactionInput{
			ConversationID: request.ConversationID, FromSeq: fromSeq, ThroughSeq: throughSeq,
			PreviousSnapshot: cloneSnapshot(previous), NewMessages: cloneMessages(newMessages),
			KnownReportReferences: cloneReferenceSources(request.KnownReportReferences), Attempt: attempt, RepairCode: repairCode,
		})
		if compactErr != nil {
			lastErr = compactErr
			repairCode = "generation_failed"
			continue
		}
		if output.Usage.Validate() != nil {
			lastErr = ErrInvalidSnapshot
			repairCode = "usage_invalid"
			continue
		}
		if validateErr := ValidatePayload(output.Payload, validation); validateErr != nil {
			lastErr = validateErr
			repairCode = validationRepairCode(validateErr)
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
			continue
		}
		return s.repository.Save(ctx, candidate)
	}
	return Snapshot{}, fmt.Errorf("%w after %d attempts: %v", ErrCompactionFailed, s.maxAttempts, lastErr)
}

func prepareMessages(conversationID uuid.UUID, messages []conversation.Message, previous *Snapshot) ([]conversation.Message, int64, int64, error) {
	if len(messages) == 0 {
		return nil, 0, 0, ErrInvalidShadowInput
	}
	ordered := cloneMessages(messages)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Seq < ordered[j].Seq })
	for index, message := range ordered {
		if message.ID == uuid.Nil || message.ConversationID != conversationID || message.Seq < 1 || !message.Role.Valid() ||
			strings.TrimSpace(message.Content) == "" || (index > 0 && message.Seq == ordered[index-1].Seq) {
			return nil, 0, 0, ErrInvalidShadowInput
		}
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
	switch {
	case errors.Is(err, ErrUserSourceRequired):
		return "user_source_required"
	case errors.Is(err, ErrSourceOutOfRange):
		return "source_out_of_range"
	case errors.Is(err, ErrUnknownEntryReference):
		return "entry_reference_unknown"
	case errors.Is(err, ErrUnknownStableReference):
		return "stable_reference_unknown"
	case errors.Is(err, ErrSupersedeCycle):
		return "supersede_cycle"
	case errors.Is(err, ErrMultipleActiveEntries):
		return "multiple_active_entries"
	case errors.Is(err, ErrPayloadTooLarge):
		return "payload_too_large"
	default:
		return "payload_invalid"
	}
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
