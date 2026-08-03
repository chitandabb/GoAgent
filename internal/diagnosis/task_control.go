package diagnosis

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	DefaultTaskEventLimit = 100
	MaxTaskEventLimit     = 200
)

// TaskEvent 是可以返回给任务所有者的结构化任务轨迹，不包含日志或模型内部思维过程。
type TaskEvent struct {
	TaskID               uuid.UUID
	Seq                  int64
	EventType            TaskEventType
	Payload              map[string]any
	PayloadSchemaVersion int
	CreatedAt            time.Time
}

type TaskEventPage struct {
	Items        []TaskEvent
	AfterSeq     int64
	NextAfterSeq int64
	HasMore      bool
}

// TaskEventStream 表示一次已完成任务授权的持续事件读取会话。
// 后续补读只访问 TaskEvent 事实表，不在每次轮询时重复查询任务权限。
type TaskEventStream interface {
	InitialStatus() TaskStatus
	Next(ctx context.Context, afterSeq int64, limit int) (TaskEventPage, error)
}

type taskEventStream struct {
	repository    TaskRepository
	taskID        uuid.UUID
	initialStatus TaskStatus
}

func (s *taskEventStream) InitialStatus() TaskStatus {
	if s == nil {
		return ""
	}
	return s.initialStatus
}

func (s *taskEventStream) Next(ctx context.Context, afterSeq int64, limit int) (TaskEventPage, error) {
	if s == nil || s.repository == nil {
		return TaskEventPage{}, errors.New("diagnosis task event stream is unavailable")
	}
	if afterSeq < 0 || limit < 1 || limit > MaxTaskEventLimit {
		return TaskEventPage{}, ErrInvalidTask
	}
	return s.repository.ListTaskEvents(ctx, s.taskID, afterSeq, limit)
}

type TaskCancelResult struct {
	Task    DiagnosisTask
	Changed bool
}

// ListEvents 先复用任务查询完成 owner/admin 授权，再读取可断点续传的事件页。
func (s *DiagnosisTaskService) ListEvents(
	ctx context.Context,
	actor TaskActor,
	taskID uuid.UUID,
	afterSeq int64,
	limit int,
) (TaskEventPage, error) {
	if s == nil || s.repository == nil {
		return TaskEventPage{}, errors.New("diagnosis task service is unavailable")
	}
	if afterSeq < 0 || limit < 0 || limit > MaxTaskEventLimit {
		return TaskEventPage{}, ErrInvalidTask
	}
	if limit == 0 {
		limit = DefaultTaskEventLimit
	}
	if _, err := s.Get(ctx, actor, taskID); err != nil {
		return TaskEventPage{}, err
	}
	return s.repository.ListTaskEvents(ctx, taskID, afterSeq, limit)
}

// OpenEventStream 只在建流前执行一次 owner/admin 授权，返回受限于该任务的事件读取会话。
func (s *DiagnosisTaskService) OpenEventStream(
	ctx context.Context,
	actor TaskActor,
	taskID uuid.UUID,
) (TaskEventStream, error) {
	if s == nil || s.repository == nil {
		return nil, errors.New("diagnosis task service is unavailable")
	}
	task, err := s.Get(ctx, actor, taskID)
	if err != nil {
		return nil, err
	}
	return &taskEventStream{
		repository: s.repository, taskID: task.ID, initialStatus: task.Status,
	}, nil
}

// Cancel 将取消请求持久化为状态和 TaskEvent；重复请求不会重复追加事件。
func (s *DiagnosisTaskService) Cancel(
	ctx context.Context,
	actor TaskActor,
	taskID uuid.UUID,
) (TaskCancelResult, error) {
	if s == nil || s.repository == nil {
		return TaskCancelResult{}, errors.New("diagnosis task service is unavailable")
	}
	if _, err := s.Get(ctx, actor, taskID); err != nil {
		return TaskCancelResult{}, err
	}
	return s.repository.CancelTask(ctx, taskID, actor.UserID, s.clock().UTC())
}
