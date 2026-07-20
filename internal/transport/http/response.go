package httptransport

import (
	"net/http"

	"github.com/chitandabb/GoAgent/internal/apperror"

	"github.com/gin-gonic/gin"
)

// Response 是所有 HTTP JSON 接口的统一响应结构。
type Response struct {
	Code      apperror.Code `json:"code"`
	Message   string        `json:"message"`
	Data      any           `json:"data,omitempty"`
	RequestID string        `json:"requestId,omitempty"`
}

// WriteSuccess 返回成功响应。
func WriteSuccess(c *gin.Context, data any) {
	WriteSuccessWithStatus(c, http.StatusOK, data)
}

// WriteSuccessWithStatus 允许创建类接口返回 201、202 等其他成功状态码。
func WriteSuccessWithStatus(c *gin.Context, status int, data any) {
	c.JSON(status, Response{
		Code:      apperror.CodeSuccess,
		Message:   apperror.CodeSuccess.Message(),
		Data:      data,
		RequestID: RequestIDFromContext(c),
	})
}

// WriteError 把任意 error 归一化后返回统一错误响应。
func WriteError(c *gin.Context, err error) {
	if err == nil {
		err = apperror.New(apperror.CodeInternal)
	}
	appErr := apperror.Normalize(err)
	status := httpStatus(appErr.Code)
	message := appErr.Message
	// 5xx 错误只向前端返回通用提示，原始原因由全局错误拦截器记录到日志。
	if status >= http.StatusInternalServerError {
		message = appErr.Code.Message()
	}
	c.JSON(status, Response{
		Code:      appErr.Code,
		Message:   message,
		RequestID: RequestIDFromContext(c),
	})
}

// httpStatus 只在 HTTP 层维护错误码到 HTTP 状态的映射。
func httpStatus(code apperror.Code) int {
	switch code {
	case apperror.CodeInvalidArgument:
		return http.StatusBadRequest
	case apperror.CodeUnauthorized:
		return http.StatusUnauthorized
	case apperror.CodeForbidden:
		return http.StatusForbidden
	case apperror.CodeNotFound:
		return http.StatusNotFound
	case apperror.CodeMethodNotAllowed:
		return http.StatusMethodNotAllowed
	case apperror.CodeConflict:
		return http.StatusConflict
	case apperror.CodeDependencyUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
