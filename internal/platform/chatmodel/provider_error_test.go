package chatmodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/cloudwego/eino-ext/components/model/openai"
)

// apiErrorForTest 构造 Eino OpenAI 兼容 Adapter 会返回的 APIError。Message
// 故意包含敏感内容，用于证明分类结果绝不泄漏原始错误消息。
func apiErrorForTest(status int, typ, code string, param *string) error {
	return &openai.APIError{
		Code:           code,
		Message:        "sensitive provider message with prompt and credential details",
		Param:          param,
		Type:           typ,
		HTTPStatus:     fmt.Sprintf("%d status text", status),
		HTTPStatusCode: status,
	}
}

func TestClassifyProviderErrorMapsHTTPStatuses(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		wantCategory string
		wantStatus   int
	}{
		{name: "bad request 400", err: apiErrorForTest(400, "invalid_request_error", "bad_param", nil),
			wantCategory: ProviderErrorCategoryBadRequest, wantStatus: 400},
		{name: "bad request 403", err: apiErrorForTest(403, "permission_error", "forbidden", nil),
			wantCategory: ProviderErrorCategoryBadRequest, wantStatus: 403},
		{name: "auth 401", err: apiErrorForTest(401, "authentication_error", "invalid_api_key", nil),
			wantCategory: ProviderErrorCategoryAuth, wantStatus: 401},
		{name: "rate limited 429", err: apiErrorForTest(429, "rate_limit_error", "insufficient_quota", nil),
			wantCategory: ProviderErrorCategoryRateLimited, wantStatus: 429},
		{name: "server 500", err: apiErrorForTest(500, "server_error", "", nil),
			wantCategory: ProviderErrorCategoryServer, wantStatus: 500},
		{name: "server 503", err: apiErrorForTest(503, "server_error", "overloaded", nil),
			wantCategory: ProviderErrorCategoryServer, wantStatus: 503},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyProviderError(tt.err)
			if got.Category != tt.wantCategory {
				t.Fatalf("category = %q, want %q", got.Category, tt.wantCategory)
			}
			if got.HTTPStatusCode != tt.wantStatus {
				t.Fatalf("httpStatusCode = %d, want %d", got.HTTPStatusCode, tt.wantStatus)
			}
			if got.Type != tt.err.(*openai.APIError).Type {
				t.Fatalf("type = %q, want the bounded provider type", got.Type)
			}
		})
	}
}

func TestClassifyProviderErrorSupportsWrappedAPIError(t *testing.T) {
	wrapped := fmt.Errorf("probe request failed: %w",
		apiErrorForTest(429, "rate_limit_error", "qps_exceeded", nil))
	got := ClassifyProviderError(wrapped)
	if got.Category != ProviderErrorCategoryRateLimited {
		t.Fatalf("wrapped 429 must classify as %q, got %q", ProviderErrorCategoryRateLimited, got.Category)
	}
	if got.Code != "qps_exceeded" {
		t.Fatalf("bounded code = %q, want %q", got.Code, "qps_exceeded")
	}
}

func TestClassifyProviderErrorTimeoutAndTransport(t *testing.T) {
	if got := ClassifyProviderError(context.DeadlineExceeded); got.Category != ProviderErrorCategoryTimeout {
		t.Fatalf("DeadlineExceeded = %q, want %q", got.Category, ProviderErrorCategoryTimeout)
	}
	timeoutNetErr := &net.DNSError{Err: "lookup deadline", IsTimeout: true}
	if got := ClassifyProviderError(timeoutNetErr); got.Category != ProviderErrorCategoryTimeout {
		t.Fatalf("net timeout = %q, want %q", got.Category, ProviderErrorCategoryTimeout)
	}
	transportErr := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	if got := ClassifyProviderError(transportErr); got.Category != ProviderErrorCategoryTransport {
		t.Fatalf("transport = %q, want %q", got.Category, ProviderErrorCategoryTransport)
	}
	// 传输错误包着 APIError 时以 HTTP 状态为准（API 已返回）。
	mixed := fmt.Errorf("dial failed: %w", apiErrorForTest(503, "server_error", "", nil))
	if got := ClassifyProviderError(mixed); got.Category != ProviderErrorCategoryServer {
		t.Fatalf("wrapped API 503 must win over transport: %q", got.Category)
	}
}

func TestClassifyProviderErrorUnknownFallsBackToModelError(t *testing.T) {
	if got := ClassifyProviderError(errors.New("plain non-provider error")); got.Category != ProviderErrorCategoryModel {
		t.Fatalf("unknown = %q, want %q", got.Category, ProviderErrorCategoryModel)
	}
	// APIError 不带 HTTP 状态（如响应体解析失败）也归为 model_error。
	if got := ClassifyProviderError(&openai.APIError{Type: "parse_error", Message: "bad body"}); got.Category != ProviderErrorCategoryModel {
		t.Fatalf("stateless APIError = %q, want %q", got.Category, ProviderErrorCategoryModel)
	}
	if got := ClassifyProviderError(nil); got.Category != "" {
		t.Fatalf("nil error category = %q, want empty", got.Category)
	}
}

func TestClassifyProviderErrorLeavesNilCodeEmpty(t *testing.T) {
	got := ClassifyProviderError(&openai.APIError{
		Message:        "sensitive provider message",
		Type:           "invalid_request_error",
		HTTPStatus:     "400 Bad Request",
		HTTPStatusCode: 400,
	})
	if got.Code != "" {
		t.Fatalf("nil provider code = %q, want empty", got.Code)
	}
}

func TestClassifyProviderErrorDropsUnsafeProviderMetadata(t *testing.T) {
	unsafeParam := "field contains secret value"
	got := ClassifyProviderError(&openai.APIError{
		Message:        "sensitive provider message",
		Type:           "invalid request secret",
		Code:           map[string]any{"secret": "credential"},
		Param:          &unsafeParam,
		HTTPStatus:     "400 body contains secret",
		HTTPStatusCode: 400,
	})
	if got.Type != "" || got.Code != "" || got.Param != "" {
		t.Fatalf("unsafe provider metadata must be dropped: %+v", got)
	}
	if got.HTTPStatus != "400 Bad Request" {
		t.Fatalf("httpStatus = %q, want status derived from numeric code", got.HTTPStatus)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal category: %v", err)
	}
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "credential") {
		t.Fatalf("category leaked unsafe provider metadata: %s", encoded)
	}
}

func TestClassifyProviderErrorBoundsFieldsWithoutMessage(t *testing.T) {
	longType := strings.Repeat("t", 200)
	longCode := strings.Repeat("c", 200)
	longParam := strings.Repeat("p", 400)
	longStatus := strings.Repeat("s", 200)
	secretMessage := "top secret credential value"
	err := &openai.APIError{
		Type: longType, Code: longCode, Param: &longParam,
		HTTPStatus: longStatus, HTTPStatusCode: 401, Message: secretMessage,
	}
	got := ClassifyProviderError(err)
	if got.Category != ProviderErrorCategoryAuth {
		t.Fatalf("category = %q, want %q", got.Category, ProviderErrorCategoryAuth)
	}
	if len(got.Type) > maxProviderErrorTypeLen {
		t.Fatalf("type not bounded: %d > %d", len(got.Type), maxProviderErrorTypeLen)
	}
	if len(got.Code) > maxProviderErrorCodeLen {
		t.Fatalf("code not bounded: %d > %d", len(got.Code), maxProviderErrorCodeLen)
	}
	if len(got.Param) > maxProviderErrorParamLen {
		t.Fatalf("param not bounded: %d > %d", len(got.Param), maxProviderErrorParamLen)
	}
	if len(got.HTTPStatus) > maxProviderErrorStatusLen {
		t.Fatalf("httpStatus not bounded: %d > %d", len(got.HTTPStatus), maxProviderErrorStatusLen)
	}
	// 分类结果（含 JSON 序列化）绝不能包含原始错误消息。
	encoded, marshalErr := json.Marshal(got)
	if marshalErr != nil {
		t.Fatalf("marshal category: %v", marshalErr)
	}
	if strings.Contains(string(encoded), "secret") {
		t.Fatalf("category must not leak the error message, got %s", encoded)
	}
	if strings.Contains(got.Type, strings.Repeat("t", maxProviderErrorTypeLen+1)) {
		t.Fatal("type must be truncated, not passed through")
	}
}
