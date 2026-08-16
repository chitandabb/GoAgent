package diagnosis

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

const (
	// DefaultTaskListPageSize 是任务列表默认每页条数。
	DefaultTaskListPageSize = 20
	// MaxTaskListPageSize 是任务列表单页上限。
	MaxTaskListPageSize = 100
)

// TaskListQuery 是任务列表查询。非管理员 Actor 的可见范围固定为
// 自己创建的任务，管理员可查看全部。
type TaskListQuery struct {
	Actor    TaskActor
	Status   *TaskStatus
	Page     int
	PageSize int
}

// TaskListItem 是任务列表行：任务安全摘要 + 任务快照中的工单身份。
// 工单身份来自创建时冻结的快照，避免列表接口依赖 ERP 实时数据。
type TaskListItem struct {
	Task              DiagnosisTask
	ExternalCaseKey   string
	ExternalCaseTitle string
}

// TaskListPage 是任务列表的分页结果。
type TaskListPage struct {
	Items    []TaskListItem
	Total    int64
	Page     int
	PageSize int
}

// List 返回当前 Actor 可见的任务分页列表（按创建时间倒序）。
func (s *DiagnosisTaskService) List(ctx context.Context, query TaskListQuery) (TaskListPage, error) {
	if s == nil || s.repository == nil {
		return TaskListPage{}, errors.New("diagnosis task service is unavailable")
	}
	if query.Actor.UserID == uuid.Nil {
		return TaskListPage{}, ErrTaskForbidden
	}
	if query.Status != nil && !query.Status.Valid() {
		return TaskListPage{}, ErrInvalidTask
	}
	page, pageSize := normalizeTaskListPagination(query.Page, query.PageSize)
	query.Page, query.PageSize = page, pageSize
	return s.repository.ListTasks(ctx, query)
}

func normalizeTaskListPagination(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = DefaultTaskListPageSize
	} else if pageSize > MaxTaskListPageSize {
		pageSize = MaxTaskListPageSize
	}
	return page, pageSize
}

// Valid 判断 TaskStatus 是否为持久化协议中的合法值。
func (s TaskStatus) Valid() bool {
	switch s {
	case TaskPending, TaskRunning, TaskCancelRequested, TaskSucceeded, TaskFailed, TaskCancelled:
		return true
	}
	return false
}
