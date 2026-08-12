package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/chitandabb/GoAgent/internal/conversationmemory"
	"github.com/google/uuid"
)

type pilotMemoryRepository struct {
	mu    sync.Mutex
	items map[uuid.UUID][]conversationmemory.Snapshot
	byID  map[uuid.UUID]conversationmemory.Snapshot
}

func newPilotMemoryRepository() *pilotMemoryRepository {
	return &pilotMemoryRepository{items: make(map[uuid.UUID][]conversationmemory.Snapshot), byID: make(map[uuid.UUID]conversationmemory.Snapshot)}
}

func (r *pilotMemoryRepository) Latest(_ context.Context, conversationID uuid.UUID) (*conversationmemory.Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	items := r.items[conversationID]
	if len(items) == 0 {
		return nil, conversationmemory.ErrSnapshotNotFound
	}
	latest := clonePilotSnapshot(items[len(items)-1])
	return &latest, nil
}

func (r *pilotMemoryRepository) Get(_ context.Context, snapshotID uuid.UUID) (conversationmemory.Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.byID[snapshotID]
	if !ok {
		return conversationmemory.Snapshot{}, conversationmemory.ErrSnapshotNotFound
	}
	return clonePilotSnapshot(item), nil
}

func (r *pilotMemoryRepository) Save(_ context.Context, candidate conversationmemory.CandidateSnapshot) (conversationmemory.Snapshot, error) {
	if err := candidate.Validate(); err != nil {
		return conversationmemory.Snapshot{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	items := r.items[candidate.ConversationID]
	if candidate.SupersedesSnapshotID == nil {
		for _, item := range items {
			if item.Status == conversationmemory.SnapshotStatusActive {
				return conversationmemory.Snapshot{}, conversationmemory.ErrInvalidSnapshot
			}
		}
	} else {
		previous, ok := r.byID[*candidate.SupersedesSnapshotID]
		if !ok || previous.ConversationID != candidate.ConversationID || previous.FromSeq != candidate.FromSeq ||
			previous.ThroughSeq >= candidate.ThroughSeq {
			return conversationmemory.Snapshot{}, conversationmemory.ErrInvalidSnapshot
		}
	}
	version := int64(len(items) + 1)
	result := clonePilotSnapshot(conversationmemory.Snapshot{CandidateSnapshot: candidate, Version: version})
	r.items[candidate.ConversationID] = append(items, result)
	r.byID[result.ID] = result
	return clonePilotSnapshot(result), nil
}

func (r *pilotMemoryRepository) Active(_ context.Context, conversationID uuid.UUID) (*conversationmemory.Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for index := len(r.items[conversationID]) - 1; index >= 0; index-- {
		if r.items[conversationID][index].Status == conversationmemory.SnapshotStatusActive {
			active := clonePilotSnapshot(r.items[conversationID][index])
			return &active, nil
		}
	}
	return nil, conversationmemory.ErrSnapshotNotFound
}

func (r *pilotMemoryRepository) ActiveIdentity(ctx context.Context, conversationID uuid.UUID) (conversationmemory.ActiveSnapshotIdentity, error) {
	active, err := r.Active(ctx, conversationID)
	if err != nil {
		return conversationmemory.ActiveSnapshotIdentity{}, err
	}
	return conversationmemory.ActiveSnapshotIdentity{
		ConversationID: active.ConversationID, SnapshotID: active.ID, Version: active.Version, PayloadSHA256: active.PayloadSHA256,
	}, nil
}

func (r *pilotMemoryRepository) Activate(_ context.Context, request conversationmemory.ActivationRequest) (conversationmemory.Snapshot, error) {
	if err := request.Validate(); err != nil {
		return conversationmemory.Snapshot{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	items := r.items[request.ConversationID]
	currentActiveID := uuid.Nil
	currentThrough := int64(0)
	for _, item := range items {
		if item.Status == conversationmemory.SnapshotStatusActive {
			currentActiveID, currentThrough = item.ID, item.ThroughSeq
			break
		}
	}
	candidate, ok := r.byID[request.CandidateSnapshotID]
	if !ok || candidate.ConversationID != request.ConversationID || candidate.Status != conversationmemory.SnapshotStatusCandidate {
		return conversationmemory.Snapshot{}, conversationmemory.ErrInvalidSnapshot
	}
	if request.ActivatedAt.Before(candidate.CreatedAt) {
		return conversationmemory.Snapshot{}, conversationmemory.ErrInvalidSnapshot
	}
	if request.ExpectedActiveSnapshotID == nil {
		if currentActiveID != uuid.Nil || candidate.SupersedesSnapshotID != nil {
			return conversationmemory.Snapshot{}, conversationmemory.ErrSnapshotActivationConflict
		}
	} else if currentActiveID != *request.ExpectedActiveSnapshotID || candidate.SupersedesSnapshotID == nil ||
		*candidate.SupersedesSnapshotID != currentActiveID || candidate.ThroughSeq <= currentThrough {
		return conversationmemory.Snapshot{}, conversationmemory.ErrSnapshotActivationConflict
	}
	for index := range items {
		if items[index].ID == candidate.ID {
			if currentActiveID != uuid.Nil {
				for previousIndex := range items {
					if items[previousIndex].ID == currentActiveID {
						items[previousIndex].Status = conversationmemory.SnapshotStatusSuperseded
						r.byID[currentActiveID] = items[previousIndex]
					}
				}
			}
			activatedAt := request.ActivatedAt.UTC()
			items[index].Status = conversationmemory.SnapshotStatusActive
			items[index].ActivatedAt = &activatedAt
			candidate = items[index]
			r.items[request.ConversationID] = items
			r.byID[candidate.ID] = candidate
			return clonePilotSnapshot(candidate), nil
		}
	}
	return conversationmemory.Snapshot{}, errors.New("Pilot Snapshot candidate is not indexed")
}

func clonePilotSnapshot(input conversationmemory.Snapshot) conversationmemory.Snapshot {
	output := input
	if input.SupersedesSnapshotID != nil {
		id := *input.SupersedesSnapshotID
		output.SupersedesSnapshotID = &id
	}
	encoded, err := json.Marshal(input.Payload)
	if err == nil {
		if payload, decodeErr := conversationmemory.DecodePayload(encoded); decodeErr == nil {
			output.Payload = payload
		}
	}
	if input.ActivatedAt != nil {
		activatedAt := *input.ActivatedAt
		output.ActivatedAt = &activatedAt
	}
	return output
}

func (r *pilotMemoryRepository) SnapshotCount(conversationID uuid.UUID) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.items[conversationID])
}

var _ conversationmemory.ActivationRepository = (*pilotMemoryRepository)(nil)
