package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const maxOutboxErrorRunes = 1000

type OutboxEvent struct {
	ID                   uuid.UUID
	EventType            string
	AggregateType        string
	AggregateID          uuid.UUID
	CorrelationID        uuid.UUID
	CausationID          *uuid.UUID
	Payload              json.RawMessage
	PayloadSchemaVersion int
	AttemptCount         int
	AvailableAt          time.Time
	CreatedAt            time.Time
}

type OutboxRepository interface {
	ClaimOutboxEvents(ctx context.Context, owner string, now, lockedUntil time.Time, limit int) ([]OutboxEvent, error)
	MarkOutboxPublished(ctx context.Context, eventID uuid.UUID, owner string, publishedAt time.Time) (bool, error)
	MarkOutboxFailed(ctx context.Context, eventID uuid.UUID, owner string, failedAt, nextAvailableAt time.Time, safeError string) (bool, error)
}

type OutboxPublisher interface {
	Publish(ctx context.Context, event OutboxEvent) error
}

type RelayConfig struct {
	Owner          string
	BatchSize      int
	LeaseDuration  time.Duration
	PublishTimeout time.Duration
}

type RelayStats struct {
	Claimed   int
	Published int
	Failed    int
	Stale     int
}

type OutboxRelay struct {
	repository OutboxRepository
	publisher  OutboxPublisher
	config     RelayConfig
	clock      func() time.Time
}

func NewOutboxRelay(repository OutboxRepository, publisher OutboxPublisher, config RelayConfig) (*OutboxRelay, error) {
	config.Owner = strings.TrimSpace(config.Owner)
	if repository == nil || publisher == nil {
		return nil, errors.New("outbox relay dependencies are nil")
	}
	if config.Owner == "" || len(config.Owner) > 128 || config.BatchSize < 1 ||
		config.LeaseDuration <= 0 || config.PublishTimeout <= 0 ||
		config.LeaseDuration <= time.Duration(config.BatchSize)*config.PublishTimeout {
		return nil, errors.New("outbox relay config is invalid")
	}
	return &OutboxRelay{
		repository: repository, publisher: publisher, config: config,
		clock: func() time.Time { return time.Now().UTC() },
	}, nil
}

// RunOnce 领取一个有界批次并逐条等待 Publisher Confirm。
// Broker 拒绝或超时属于可恢复发布失败，只有数据库领取/状态提交失败才返回错误。
func (r *OutboxRelay) RunOnce(ctx context.Context) (RelayStats, error) {
	if r == nil || r.repository == nil || r.publisher == nil {
		return RelayStats{}, errors.New("outbox relay is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return RelayStats{}, err
	}
	now := r.clock().UTC()
	events, err := r.repository.ClaimOutboxEvents(
		ctx, r.config.Owner, now, now.Add(r.config.LeaseDuration), r.config.BatchSize,
	)
	if err != nil {
		return RelayStats{}, fmt.Errorf("claim outbox events: %w", err)
	}
	stats := RelayStats{Claimed: len(events)}
	for _, event := range events {
		publishCtx, cancel := context.WithTimeout(ctx, r.config.PublishTimeout)
		publishErr := r.publisher.Publish(publishCtx, event)
		cancel()
		finishedAt := r.clock().UTC()
		if publishErr == nil {
			updated, markErr := r.repository.MarkOutboxPublished(ctx, event.ID, r.config.Owner, finishedAt)
			if markErr != nil {
				return stats, fmt.Errorf("mark outbox event %s published: %w", event.ID, markErr)
			}
			if updated {
				stats.Published++
			} else {
				stats.Stale++
			}
			continue
		}

		nextAvailableAt := finishedAt.Add(outboxRetryDelay(event.AttemptCount))
		updated, markErr := r.repository.MarkOutboxFailed(
			ctx, event.ID, r.config.Owner, finishedAt, nextAvailableAt, safeOutboxError(publishErr),
		)
		if markErr != nil {
			return stats, fmt.Errorf("mark outbox event %s failed: %w", event.ID, markErr)
		}
		if updated {
			stats.Failed++
		} else {
			stats.Stale++
		}
	}
	return stats, nil
}

func outboxRetryDelay(previousFailures int) time.Duration {
	if previousFailures < 0 {
		previousFailures = 0
	}
	if previousFailures > 6 {
		previousFailures = 6
	}
	return time.Second * time.Duration(1<<previousFailures)
}

func safeOutboxError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = "publish failed"
	}
	if utf8.RuneCountInString(message) <= maxOutboxErrorRunes {
		return message
	}
	runes := []rune(message)
	return string(runes[:maxOutboxErrorRunes])
}
