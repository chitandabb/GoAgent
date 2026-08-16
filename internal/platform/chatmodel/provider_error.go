package chatmodel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"unicode"

	"github.com/cloudwego/eino-ext/components/model/openai"
)

// 稳定错误分类：Tool Selection evaluator 与任何调用方把 Provider 失败
// 归入这些固定类别并持久化到 observation.ErrorType。历史 "model_error"
// 值保持可读，不升级 Observation Schema，不改变 v3 配对身份。
const (
	ProviderErrorCategoryBadRequest  = "provider_bad_request"
	ProviderErrorCategoryAuth        = "provider_auth_error"
	ProviderErrorCategoryRateLimited = "provider_rate_limited"
	ProviderErrorCategoryServer      = "provider_server_error"
	ProviderErrorCategoryTimeout     = "provider_timeout"
	ProviderErrorCategoryTransport   = "provider_transport_error"
	ProviderErrorCategoryModel       = "model_error"
)

// 日志字段长度上限（按 rune 计）：所有来自 Provider 的字符串必须截断到
// 这些上限，任何情况下都不记录原始错误消息。
const (
	maxProviderErrorTypeLen   = 64
	maxProviderErrorCodeLen   = 64
	maxProviderErrorParamLen  = 128
	maxProviderErrorStatusLen = 32
)

// ProviderErrorCategory 是 Provider 失败的稳定、受限分类。它只携带
// category、HTTP status 与受限的 provider type/code/param，绝不包含原始
// 错误消息、响应正文、Prompt、Tool 参数或凭据。
type ProviderErrorCategory struct {
	Category       string `json:"category"`
	HTTPStatus     string `json:"httpStatus,omitempty"`
	HTTPStatusCode int    `json:"httpStatusCode,omitempty"`
	Type           string `json:"type,omitempty"`
	Code           string `json:"code,omitempty"`
	Param          string `json:"param,omitempty"`
}

// ClassifyProviderError 把 Eino OpenAI 兼容 Adapter 的错误稳定分类，支持
// wrapped error（errors.As/errors.Is 穿透 %w 链）：
//
//   - context.DeadlineExceeded 或 net.Error.Timeout() → provider_timeout；
//   - 其他 net.Error（传输层，如 dial/read 失败）→ provider_transport_error；
//   - openai.APIError 按 HTTPStatusCode 分类：401 → provider_auth_error，
//     429 → provider_rate_limited，5xx → provider_server_error，
//     其他 4xx → provider_bad_request，无状态码 → provider_model_error
//     兜底（model_error）；
//   - 未知错误 → model_error。
//
// nil 返回空分类（Category 为空字符串），调用方应自行决定语义。
func ClassifyProviderError(err error) ProviderErrorCategory {
	if err == nil {
		return ProviderErrorCategory{}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ProviderErrorCategory{Category: ProviderErrorCategoryTimeout}
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return ProviderErrorCategory{Category: ProviderErrorCategoryTimeout}
		}
		return ProviderErrorCategory{Category: ProviderErrorCategoryTransport}
	}
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		return classifyOpenAIAPIError(apiErr)
	}
	return ProviderErrorCategory{Category: ProviderErrorCategoryModel}
}

func classifyOpenAIAPIError(apiErr *openai.APIError) ProviderErrorCategory {
	category := ProviderErrorCategory{
		HTTPStatus:     boundedHTTPStatus(apiErr.HTTPStatusCode),
		HTTPStatusCode: apiErr.HTTPStatusCode,
		Type:           boundedProviderIdentifier(apiErr.Type, maxProviderErrorTypeLen),
		Code:           boundedProviderCode(apiErr.Code),
	}
	if apiErr.Param != nil {
		category.Param = boundedProviderIdentifier(*apiErr.Param, maxProviderErrorParamLen)
	}
	switch {
	case apiErr.HTTPStatusCode == 401:
		category.Category = ProviderErrorCategoryAuth
	case apiErr.HTTPStatusCode == 429:
		category.Category = ProviderErrorCategoryRateLimited
	case apiErr.HTTPStatusCode >= 500 && apiErr.HTTPStatusCode <= 599:
		category.Category = ProviderErrorCategoryServer
	case apiErr.HTTPStatusCode >= 400 && apiErr.HTTPStatusCode <= 499:
		category.Category = ProviderErrorCategoryBadRequest
	default:
		category.Category = ProviderErrorCategoryModel
	}
	return category
}

func boundedHTTPStatus(statusCode int) string {
	if statusCode <= 0 {
		return ""
	}
	statusText := http.StatusText(statusCode)
	if statusText == "" {
		return fmt.Sprintf("%d", statusCode)
	}
	return boundedRunes(fmt.Sprintf("%d %s", statusCode, statusText), maxProviderErrorStatusLen)
}

func boundedProviderCode(value any) string {
	if value == nil {
		return ""
	}
	switch current := value.(type) {
	case string:
		return boundedProviderIdentifier(current, maxProviderErrorCodeLen)
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64, bool:
		return boundedProviderIdentifier(fmt.Sprint(current), maxProviderErrorCodeLen)
	default:
		return ""
	}
}

// boundedProviderIdentifier 只保留短标识符和字段路径。Provider 控制的自由
// 文本即使被放进 type/code/param，也不会进入日志或持久化结果。
func boundedProviderIdentifier(value string, max int) string {
	value = boundedRunes(value, max)
	for _, current := range value {
		if unicode.IsLetter(current) || unicode.IsDigit(current) || strings.ContainsRune("_-.:/[]", current) {
			continue
		}
		return ""
	}
	return value
}

// boundedRunes 按 rune 截断到 max 长度，绝不返回更长的字符串。
func boundedRunes(value string, max int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > max {
		return string(runes[:max])
	}
	return value
}
