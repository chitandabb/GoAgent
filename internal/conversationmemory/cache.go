package conversationmemory

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrSnapshotCacheMiss    = errors.New("conversation memory snapshot cache miss")
	ErrSnapshotCacheInvalid = errors.New("conversation memory snapshot cache payload is invalid")
)

type ActiveSnapshotIdentity struct {
	ConversationID uuid.UUID
	SnapshotID     uuid.UUID
	Version        int64
	PayloadSHA256  string
}

func (i ActiveSnapshotIdentity) Validate() error {
	if i.ConversationID == uuid.Nil || i.SnapshotID == uuid.Nil || i.Version < 1 || !validSHA256(i.PayloadSHA256) {
		return ErrInvalidSnapshot
	}
	return nil
}

// SnapshotCache stores immutable Snapshot payloads. It never decides which
// Snapshot is Active and therefore cannot participate in publication or CAS.
type SnapshotCache interface {
	Load(context.Context, uuid.UUID, uuid.UUID) (Snapshot, error)
	Store(context.Context, Snapshot) error
	DeleteConversation(context.Context, uuid.UUID) error
}

type CacheOperation string

const (
	CacheOperationActiveLoad CacheOperation = "active_load"
	CacheOperationDelete     CacheOperation = "delete_conversation"
)

func (o CacheOperation) valid() bool {
	return o == CacheOperationActiveLoad || o == CacheOperationDelete
}

type CacheStatus string

const (
	CacheStatusHit       CacheStatus = "hit"
	CacheStatusMiss      CacheStatus = "miss"
	CacheStatusSucceeded CacheStatus = "succeeded"
	CacheStatusDegraded  CacheStatus = "degraded"
)

func (s CacheStatus) valid() bool {
	return s == CacheStatusHit || s == CacheStatusMiss || s == CacheStatusSucceeded || s == CacheStatusDegraded
}

type CacheReason string

const (
	CacheReasonUnavailable  CacheReason = "unavailable"
	CacheReasonTimeout      CacheReason = "timeout"
	CacheReasonReadFailed   CacheReason = "read_failed"
	CacheReasonInvalid      CacheReason = "invalid_payload"
	CacheReasonStale        CacheReason = "stale_snapshot"
	CacheReasonWriteFailed  CacheReason = "write_failed"
	CacheReasonDeleteFailed CacheReason = "delete_failed"
)

func (r CacheReason) validFor(operation CacheOperation) bool {
	switch operation {
	case CacheOperationActiveLoad:
		return r == CacheReasonUnavailable || r == CacheReasonTimeout || r == CacheReasonReadFailed ||
			r == CacheReasonInvalid || r == CacheReasonStale || r == CacheReasonWriteFailed
	case CacheOperationDelete:
		return r == CacheReasonUnavailable || r == CacheReasonDeleteFailed
	default:
		return false
	}
}

type CacheObservation struct {
	Operation      CacheOperation
	Status         CacheStatus
	Reason         CacheReason
	ConversationID uuid.UUID
	SnapshotID     uuid.UUID
	Duration       time.Duration
}

func (o CacheObservation) Validate() error {
	if !o.Operation.valid() || !o.Status.valid() || o.ConversationID == uuid.Nil || o.Duration < 0 {
		return errors.New("conversation memory cache observation is invalid")
	}
	switch o.Operation {
	case CacheOperationActiveLoad:
		if o.SnapshotID == uuid.Nil || o.Status == CacheStatusSucceeded {
			return errors.New("conversation memory cache observation is invalid")
		}
	case CacheOperationDelete:
		if o.SnapshotID != uuid.Nil || o.Status == CacheStatusHit || o.Status == CacheStatusMiss {
			return errors.New("conversation memory cache observation is invalid")
		}
	}
	if o.Status == CacheStatusDegraded && !o.Reason.validFor(o.Operation) {
		return errors.New("conversation memory cache observation is invalid")
	}
	if o.Status != CacheStatusDegraded && o.Reason != "" {
		return errors.New("conversation memory cache observation is invalid")
	}
	return nil
}

type CacheObserver interface {
	Observe(context.Context, CacheObservation)
}

func activeSnapshotMatchesIdentity(snapshot Snapshot, identity ActiveSnapshotIdentity) bool {
	return snapshot.Validate() == nil && snapshot.Status == SnapshotStatusActive &&
		snapshot.ConversationID == identity.ConversationID && snapshot.ID == identity.SnapshotID &&
		snapshot.Version == identity.Version && snapshot.PayloadSHA256 == identity.PayloadSHA256
}
