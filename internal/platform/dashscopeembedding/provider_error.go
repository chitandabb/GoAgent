package dashscopeembedding

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// ProviderErrorCategory 是 Embedding Provider 失败的稳定、受限分类。
// 只有这些类别可以被调用方依赖；分类是结构化重试与观测的边界。
type ProviderErrorCategory string

const (
	ProviderErrorRateLimited     ProviderErrorCategory = "rate_limited"
	ProviderErrorAuth            ProviderErrorCategory = "auth"
	ProviderErrorBadRequest      ProviderErrorCategory = "bad_request"
	ProviderErrorServer          ProviderErrorCategory = "server"
	ProviderErrorTimeout         ProviderErrorCategory = "timeout"
	ProviderErrorTransport       ProviderErrorCategory = "transport"
	ProviderErrorInvalidResponse ProviderErrorCategory = "invalid_response"
)

// Provider 自由文本字段的长度上限（按 rune 计）。任何 Provider 控制的
// 字符串都必须先截断再进入 ProviderError；响应 message/body、输入文本、
// 向量和 API Key 永不进入 ProviderError。
const (
	maxProviderErrorCodeLen = 64
	maxProviderRequestIDLen = 128
)

// ProviderError 是 Embedding Provider 调用的结构化错误。它只携带受限的
// category、bounded status/code/requestId/retryAfter，绝不保存或输出响应
// message/body、输入文本、向量或 API Key。
type ProviderError struct {
	Category      ProviderErrorCategory `json:"category"`
	StatusCode    int                   `json:"statusCode,omitempty"`
	Code          string                `json:"code,omitempty"`
	RequestID     string                `json:"requestId,omitempty"`
	RetryAfter    time.Duration         `json:"-"`
	RetryAfterSet bool                  `json:"-"`
}

// Error 只输出受限字段；Provider 的 message/body 永不出现在错误文本中。
func (e *ProviderError) Error() string {
	if e == nil {
		return "embedding provider error"
	}
	var builder strings.Builder
	builder.WriteString("embedding provider error: category=")
	builder.WriteString(string(e.Category))
	statusCode := boundedStatusCode(e.StatusCode)
	if statusCode > 0 {
		builder.WriteString(" status=")
		builder.WriteString(strconv.Itoa(statusCode))
	}
	code := sanitizeProviderCode(e.Code)
	if code != "" {
		builder.WriteString(" code=")
		builder.WriteString(code)
	}
	requestID := sanitizeRequestID(e.RequestID)
	if requestID != "" {
		builder.WriteString(" requestId=")
		builder.WriteString(requestID)
	}
	if e.RetryAfterSet {
		builder.WriteString(" retryAfter=")
		builder.WriteString(e.RetryAfter.String())
	}
	return builder.String()
}

// MarshalJSON 固定输出四个受限字段。即使状态码或标识符为空也不省略键，
// 并在序列化边界再次净化，避免直接构造 ProviderError 绕过响应解码路径的
// 限界规则。Retry-After 只参与进程内重试，永不进入持久化合同。
func (e *ProviderError) MarshalJSON() ([]byte, error) {
	type boundedProviderError struct {
		Category   ProviderErrorCategory `json:"category"`
		StatusCode int                   `json:"statusCode"`
		Code       string                `json:"code"`
		RequestID  string                `json:"requestId"`
	}
	if e == nil {
		return []byte("null"), nil
	}
	return json.Marshal(boundedProviderError{
		Category:   e.Category,
		StatusCode: boundedStatusCode(e.StatusCode),
		Code:       sanitizeProviderCode(e.Code),
		RequestID:  sanitizeRequestID(e.RequestID),
	})
}

// Retryable 判定该错误是否值得有界重试：仅 429、明确可重试 5xx
// （500/502/503/504）与 timeout/transport；400/401、其他 4xx/5xx、
// invalid_response 不重试。context canceled/deadline 不进入此判定，
// 由调用方在检查 ctx 后直接返回。
func (e *ProviderError) Retryable() bool {
	if e == nil {
		return false
	}
	switch e.Category {
	case ProviderErrorRateLimited:
		return true
	case ProviderErrorServer:
		return retryableServerStatus(e.StatusCode)
	case ProviderErrorTimeout, ProviderErrorTransport:
		return true
	default:
		return false
	}
}

func retryableServerStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// parseRetryAfter 解析 Retry-After 头：优先 delta-seconds，其次 HTTP-date。
// 头缺失、非法、为负或指向过去时返回 (0, false)，调用方回退到有界指数退避；
// 不得假定供应商一定提供该头。
func parseRetryAfter(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds < 0 {
			return 0, false
		}
		const maxRetryAfter = time.Duration(1<<63 - 1)
		if seconds > int64(maxRetryAfter/time.Second) {
			return maxRetryAfter, true
		}
		return time.Duration(seconds) * time.Second, true
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	delay := time.Until(when)
	if delay < 0 {
		return 0, false
	}
	return delay, true
}

// boundedStatusCode 把 HTTP 状态码限制在合法范围内；非法值记为 0。
func boundedStatusCode(statusCode int) int {
	if statusCode < 100 || statusCode > 599 {
		return 0
	}
	return statusCode
}

// sanitizeProviderCode 只保留短标识符（字母、数字与 _.:/+-），截断到
// bounded 长度；任何自由文本都会导致整体丢弃。
func sanitizeProviderCode(value string) string {
	return boundedIdentifier(value, maxProviderErrorCodeLen)
}

// sanitizeRequestID 与 sanitizeProviderCode 相同，但允许更长。
func sanitizeRequestID(value string) string {
	return boundedIdentifier(value, maxProviderRequestIDLen)
}

func boundedIdentifier(value string, max int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > max {
		value = string(runes[:max])
	}
	for _, current := range value {
		if unicode.IsLetter(current) || unicode.IsDigit(current) ||
			strings.ContainsRune("_.:/+-", current) {
			continue
		}
		return ""
	}
	return value
}
