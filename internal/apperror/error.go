package apperror

import (
	"errors"
	"fmt"
)

// FieldError 描述单个字段的校验失败原因。
// Reason 会原样返回给前端，因此只能填写安全、可展示的内容，
// 不能包含数据库错误、内部结构或其他敏感信息。
type FieldError struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// Error 是应用内部的标准错误。
// Message 可以覆盖错误码的默认提示，Cause 保存仅供日志和排查使用的原始错误。
// Fields 是可选的字段级错误明细，用于参数校验类错误的响应输出。
type Error struct {
	Code    Code
	Message string
	Fields  []FieldError
	Cause   error
}

// New 使用错误码和默认消息创建应用错误。
func New(code Code) *Error {
	return &Error{Code: code, Message: code.Message()}
}

// NewWithMessage 使用自定义消息创建应用错误。
func NewWithMessage(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// NewWithFields 创建带字段级错误明细的应用错误。
func NewWithFields(code Code, fields []FieldError) *Error {
	return &Error{Code: code, Message: code.Message(), Fields: fields}
}

// Wrap 在保留原始错误的同时，转换为统一应用错误。
func Wrap(code Code, cause error) *Error {
	return &Error{Code: code, Message: code.Message(), Cause: cause}
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}

// Unwrap 让 errors.Is 和 errors.As 能继续检查原始错误。
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Normalize 将任意 error 转换为标准应用错误。
// 未显式分类的错误统一视为服务器内部错误，避免把底层错误直接返回给前端。
func Normalize(err error) *Error {
	if err == nil {
		return nil
	}
	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr
	}
	return Wrap(CodeInternal, err)
}
