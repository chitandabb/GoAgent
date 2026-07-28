package postgres

import (
	"context"
	"errors"

	"github.com/chitandabb/GoAgent/internal/repository"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

type transactionContextKey struct{}

// TxManager 使用 GORM 实现 repository.TxManager。
// 重复调用 WithinTx 会加入当前事务，语义类似 Spring 的 PROPAGATION_REQUIRED。
type TxManager struct {
	db *gorm.DB
}

var _ repository.TxManager = (*TxManager)(nil)

// NewTxManager 创建 PostgreSQL 事务执行器。
func NewTxManager(db *gorm.DB) *TxManager {
	return &TxManager{db: db}
}

// WithinTx 在一个事务中执行 fn。fn 返回错误时回滚，返回 nil 时提交。
// 如果 ctx 已携带事务，内层调用直接复用该事务，不创建独立 Savepoint。
func (m *TxManager) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if fn == nil {
		return errors.New("transaction callback is nil")
	}
	if _, ok := transactionFromContext(ctx); ok {
		return fn(ctx)
	}
	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txCtx := context.WithValue(ctx, transactionContextKey{}, tx)
		return fn(txCtx)
	})
}

// ResolveDB 只供 PostgreSQL Repository 适配器使用。
// 它优先返回 ctx 中的事务连接，否则返回绑定了当前 ctx 的普通连接。
func ResolveDB(ctx context.Context, db *gorm.DB) *gorm.DB {
	if tx, ok := transactionFromContext(ctx); ok {
		return tx.WithContext(ctx)
	}
	return db.WithContext(ctx)
}

func transactionFromContext(ctx context.Context) (*gorm.DB, bool) {
	tx, ok := ctx.Value(transactionContextKey{}).(*gorm.DB)
	return tx, ok
}

// TranslateError 把与具体表无关的数据库错误转换为稳定 Repository 错误。
// 依赖约束名称的业务规则仍由各 Repository 自己转换。
func TranslateError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return repository.Wrap(repository.ErrNotFound, err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return repository.Wrap(repository.ErrConflict, err)
	}
	return err
}
