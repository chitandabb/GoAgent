// Package repository 定义业务模块与持久化实现之间共享的最小契约。
// 该包不依赖 GORM、Gin 或具体数据库驱动。
package repository

import (
	"context"
	"errors"
	"fmt"
)

var (
	// ErrNotFound 表示目标资源在当前查询范围内不存在。
	ErrNotFound = errors.New("repository: resource not found")
	// ErrConflict 表示唯一键、幂等键或并发状态条件发生冲突。
	ErrConflict = errors.New("repository: resource conflict")
)

// TxManager 为需要跨多个 Repository 保持原子性的用例提供事务边界。
// fn 只能执行数据库操作，不能在其中调用模型、消息队列或其他网络服务。
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// Error 同时保留稳定错误分类和底层原因。
// 上层通过 errors.Is 判断分类，日志仍可通过 errors.As/Unwrap 检查根因。
type Error struct {
	Kind  error
	Cause error
}

// Wrap 将底层错误包装为稳定的 Repository 错误。
func Wrap(kind, cause error) error {
	if cause == nil {
		return nil
	}
	return &Error{Kind: kind, Cause: cause}
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s: %v", e.Kind, e.Cause)
}

// Unwrap 返回错误分类和原始原因，使 errors.Is/errors.As 都能继续工作。
func (e *Error) Unwrap() []error {
	if e == nil {
		return nil
	}
	return []error{e.Kind, e.Cause}
}
