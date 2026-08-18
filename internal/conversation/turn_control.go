package conversation

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	DefaultTurnEventLimit = 100
	MaxTurnEventLimit     = 200

	// TurnDeltaChunkRunes 是 turn_message_delta 事件单块内容的最大 rune 数。
	// 分块推送让前端能以打字机效果流式渲染助手回答，事件仍然一次性落库，
	// 但 SSE 订阅者按序逐块收到内容。
	TurnDeltaChunkRunes = 40
)

type TurnEventType string

const (
	TurnEventQueued         TurnEventType = "turn_queued"
	TurnEventRunning        TurnEventType = "turn_running"
	TurnEventRetryScheduled TurnEventType = "turn_retry_scheduled"
	TurnEventMessageDelta   TurnEventType = "turn_message_delta"
	TurnEventCompleted      TurnEventType = "turn_completed"
	TurnEventFailed         TurnEventType = "turn_failed"
)

func (e TurnEventType) Valid() bool {
	switch e {
	case TurnEventQueued, TurnEventRunning, TurnEventRetryScheduled,
		TurnEventMessageDelta, TurnEventCompleted, TurnEventFailed:
		return true
	default:
		return false
	}
}

func (e TurnEventType) IsTerminal() bool {
	return e == TurnEventCompleted || e == TurnEventFailed
}

// ChunkTurnContent 按 rune 边界（而非字节边界）把回答内容切成等长块，
// 保证中文等多字节字符不会被截断。size <= 0 时返回 nil。
func ChunkTurnContent(content string, size int) []string {
	if size <= 0 {
		return nil
	}
	runes := []rune(content)
	if len(runes) == 0 {
		return []string{}
	}
	chunks := make([]string, 0, (len(runes)+size-1)/size)
	for start := 0; start < len(runes); start += size {
		end := start + size
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[start:end]))
	}
	return chunks
}

// joinTurnChunks 把分块按顺序拼回原始内容，用于测试与校验。
func joinTurnChunks(chunks []string) string {
	var builder strings.Builder
	for _, chunk := range chunks {
		builder.WriteString(chunk)
	}
	return builder.String()
}

type TurnDetail struct {
	ID                 uuid.UUID
	ConversationID     uuid.UUID
	UserMessageID      uuid.UUID
	AssistantMessageID *uuid.UUID
	Status             TurnStatus
	AttemptCount       int
	FailureSummary     string
	RetryAt            *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
	CompletedAt        *time.Time
}

type TurnEvent struct {
	TurnID               uuid.UUID
	ConversationID       uuid.UUID
	Seq                  int64
	EventType            TurnEventType
	Payload              map[string]any
	PayloadSchemaVersion int
	CreatedAt            time.Time
}

type TurnEventPage struct {
	Items        []TurnEvent
	AfterSeq     int64
	NextAfterSeq int64
	HasMore      bool
}

type TurnEventStream interface {
	InitialStatus() TurnStatus
	Next(ctx context.Context, afterSeq int64, limit int) (TurnEventPage, error)
}

type turnEventStream struct {
	repository     Repository
	userID         uuid.UUID
	conversationID uuid.UUID
	turnID         uuid.UUID
	initialStatus  TurnStatus
}

func (s *turnEventStream) InitialStatus() TurnStatus {
	if s == nil {
		return ""
	}
	return s.initialStatus
}

func (s *turnEventStream) Next(ctx context.Context, afterSeq int64, limit int) (TurnEventPage, error) {
	if s == nil || s.repository == nil {
		return TurnEventPage{}, errors.New("conversation turn event stream is unavailable")
	}
	if afterSeq < 0 || limit < 1 || limit > MaxTurnEventLimit {
		return TurnEventPage{}, ErrInvalidMessage
	}
	return s.repository.ListTurnEvents(ctx, s.userID, s.conversationID, s.turnID, afterSeq, limit)
}

func (s *Service) GetTurn(
	ctx context.Context,
	actor Actor,
	conversationID, turnID uuid.UUID,
) (TurnDetail, error) {
	if s == nil || s.repository == nil {
		return TurnDetail{}, errors.New("conversation service is unavailable")
	}
	if actor.UserID == uuid.Nil || conversationID == uuid.Nil || turnID == uuid.Nil {
		return TurnDetail{}, ErrInvalidMessage
	}
	return s.repository.GetTurn(ctx, actor.UserID, conversationID, turnID)
}

func (s *Service) ListTurnEvents(
	ctx context.Context,
	actor Actor,
	conversationID, turnID uuid.UUID,
	afterSeq int64,
	limit int,
) (TurnEventPage, error) {
	if s == nil || s.repository == nil {
		return TurnEventPage{}, errors.New("conversation service is unavailable")
	}
	if actor.UserID == uuid.Nil || conversationID == uuid.Nil || turnID == uuid.Nil ||
		afterSeq < 0 || limit < 0 || limit > MaxTurnEventLimit {
		return TurnEventPage{}, ErrInvalidMessage
	}
	if limit == 0 {
		limit = DefaultTurnEventLimit
	}
	return s.repository.ListTurnEvents(ctx, actor.UserID, conversationID, turnID, afterSeq, limit)
}

func (s *Service) OpenTurnEventStream(
	ctx context.Context,
	actor Actor,
	conversationID, turnID uuid.UUID,
) (TurnEventStream, error) {
	turn, err := s.GetTurn(ctx, actor, conversationID, turnID)
	if err != nil {
		return nil, err
	}
	return &turnEventStream{
		repository: s.repository, userID: actor.UserID,
		conversationID: conversationID, turnID: turnID, initialStatus: turn.Status,
	}, nil
}
