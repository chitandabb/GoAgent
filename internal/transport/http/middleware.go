package httptransport

import (
	"fmt"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/apperror"
	platformlogger "github.com/chitandabb/GoAgent/internal/platform/logger"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	requestIDHeader = "X-Request-ID"
	requestIDKey    = "requestID"
)

// RequestID 为每个请求生成或复用请求编号，便于串联日志和前端报错信息。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := strings.TrimSpace(c.GetHeader(requestIDHeader))
		if requestID == "" {
			requestID = uuid.NewString()
		}
		c.Set(requestIDKey, requestID)
		c.Header(requestIDHeader, requestID)
		c.Next()
	}
}

// RequestLogger 记录一条结构化访问日志，并把带 requestId 的 Logger 放入请求 context。
func RequestLogger(base *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		startedAt := time.Now()
		requestLog := base.With(zap.String("request_id", RequestIDFromContext(c)))
		c.Request = c.Request.WithContext(platformlogger.IntoContext(c.Request.Context(), requestLog))

		c.Next()

		path := c.FullPath()
		if path == "" {
			path = c.Request.URL.Path
		}
		fields := []zap.Field{
			zap.String("method", c.Request.Method),
			zap.String("path", path),
			zap.Int("status", c.Writer.Status()),
			zap.Duration("latency", time.Since(startedAt)),
			zap.Int("response_bytes", c.Writer.Size()),
			zap.String("client_ip", c.ClientIP()),
			zap.Int("error_count", len(c.Errors)),
		}

		switch status := c.Writer.Status(); {
		case status >= 500:
			requestLog.Error("HTTP request completed", fields...)
		case status >= 400:
			requestLog.Warn("HTTP request completed", fields...)
		default:
			requestLog.Info("HTTP request completed", fields...)
		}
	}
}

// RequestIDFromContext 从 Gin 上下文读取请求编号。
func RequestIDFromContext(c *gin.Context) string {
	requestID, _ := c.Get(requestIDKey)
	value, _ := requestID.(string)
	return value
}

// ErrorHandler 是全局错误拦截器。
// Handler 只需要调用 c.Error(err)，该中间件会在请求结束时统一生成响应。
func ErrorHandler(base *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		if len(c.Errors) == 0 || c.Writer.Written() {
			return
		}
		err := c.Errors.Last().Err
		appErr := apperror.Normalize(err)
		if httpStatus(appErr.Code) >= 500 {
			platformlogger.FromContext(c.Request.Context(), base).Error(
				"HTTP request failed",
				zap.Int("code", int(appErr.Code)),
				zap.Error(err),
			)
		}
		WriteError(c, appErr)
	}
}

// Recovery 捕获 panic，防止单个请求导致整个进程退出。
// panic 的详细信息只写入服务日志，前端始终收到通用内部错误。
func Recovery(base *zap.Logger) gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered any) {
		platformlogger.FromContext(c.Request.Context(), base).Error(
			"panic recovered",
			zap.Any("panic", recovered),
			zap.Stack("stack"),
		)
		if !c.Writer.Written() {
			WriteError(c, apperror.Wrap(apperror.CodeInternal, fmt.Errorf("panic: %v", recovered)))
		}
		c.Abort()
	})
}

// AbortWithError 记录错误并立即中止后续 Handler。
func AbortWithError(c *gin.Context, err error) {
	_ = c.Error(err)
	c.Abort()
}
