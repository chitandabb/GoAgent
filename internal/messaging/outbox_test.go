package messaging

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestOutboxRelayMarksConfirmedPublish(t *testing.T) {
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	event := OutboxEvent{ID: uuid.New(), EventType: "diagnosis.execute"}
	repository := &outboxRepositoryStub{events: []OutboxEvent{event}, publishedUpdated: true}
	publisher := &outboxPublisherStub{}
	relay, err := NewOutboxRelay(repository, publisher, RelayConfig{
		Owner: "relay-1", BatchSize: 1, LeaseDuration: time.Minute, PublishTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewOutboxRelay(): %v", err)
	}
	relay.clock = func() time.Time { return now }

	stats, err := relay.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce(): %v", err)
	}
	if stats.Published != 1 || repository.publishedID != event.ID || repository.failedID != uuid.Nil || publisher.calls != 1 {
		t.Fatalf("stats=%+v repository=%+v publisher=%+v", stats, repository, publisher)
	}
}

func TestOutboxRelayRetainsFailedPublishWithBackoff(t *testing.T) {
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	event := OutboxEvent{ID: uuid.New(), EventType: "diagnosis.execute", AttemptCount: 2}
	repository := &outboxRepositoryStub{events: []OutboxEvent{event}, failedUpdated: true}
	publisher := &outboxPublisherStub{err: errors.New("broker unavailable")}
	relay, _ := NewOutboxRelay(repository, publisher, RelayConfig{
		Owner: "relay-1", BatchSize: 1, LeaseDuration: time.Minute, PublishTimeout: 5 * time.Second,
	})
	relay.clock = func() time.Time { return now }

	stats, err := relay.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce(): %v", err)
	}
	if stats.Failed != 1 || repository.failedID != event.ID ||
		!repository.nextAvailableAt.Equal(now.Add(4*time.Second)) || repository.safeError != "broker unavailable" {
		t.Fatalf("stats=%+v repository=%+v", stats, repository)
	}
}

type outboxRepositoryStub struct {
	events           []OutboxEvent
	claimErr         error
	publishedID      uuid.UUID
	publishedUpdated bool
	publishedErr     error
	failedID         uuid.UUID
	failedUpdated    bool
	failedErr        error
	nextAvailableAt  time.Time
	safeError        string
}

func (s *outboxRepositoryStub) ClaimOutboxEvents(context.Context, string, time.Time, time.Time, int) ([]OutboxEvent, error) {
	return s.events, s.claimErr
}

func (s *outboxRepositoryStub) MarkOutboxPublished(_ context.Context, eventID uuid.UUID, _ string, _ time.Time) (bool, error) {
	s.publishedID = eventID
	return s.publishedUpdated, s.publishedErr
}

func (s *outboxRepositoryStub) MarkOutboxFailed(_ context.Context, eventID uuid.UUID, _ string, _ time.Time, nextAvailableAt time.Time, safeError string) (bool, error) {
	s.failedID, s.nextAvailableAt, s.safeError = eventID, nextAvailableAt, safeError
	return s.failedUpdated, s.failedErr
}

type outboxPublisherStub struct {
	calls int
	err   error
}

func (s *outboxPublisherStub) Publish(context.Context, OutboxEvent) error {
	s.calls++
	return s.err
}
