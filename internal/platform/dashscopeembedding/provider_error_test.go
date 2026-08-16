package dashscopeembedding

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestProviderErrorCategories(t *testing.T) {
	tests := []struct {
		name      string
		err       *ProviderError
		retryable bool
	}{
		{name: "rate limited", err: &ProviderError{Category: ProviderErrorRateLimited, StatusCode: 429}, retryable: true},
		{name: "auth", err: &ProviderError{Category: ProviderErrorAuth, StatusCode: 401}},
		{name: "bad request", err: &ProviderError{Category: ProviderErrorBadRequest, StatusCode: 400}},
		{name: "server 500", err: &ProviderError{Category: ProviderErrorServer, StatusCode: 500}, retryable: true},
		{name: "server 502", err: &ProviderError{Category: ProviderErrorServer, StatusCode: 502}, retryable: true},
		{name: "server 503", err: &ProviderError{Category: ProviderErrorServer, StatusCode: 503}, retryable: true},
		{name: "server 504", err: &ProviderError{Category: ProviderErrorServer, StatusCode: 504}, retryable: true},
		{name: "server 501 not implemented", err: &ProviderError{Category: ProviderErrorServer, StatusCode: 501}},
		{name: "server 505", err: &ProviderError{Category: ProviderErrorServer, StatusCode: 505}},
		{name: "timeout", err: &ProviderError{Category: ProviderErrorTimeout}, retryable: true},
		{name: "transport", err: &ProviderError{Category: ProviderErrorTransport}, retryable: true},
		{name: "invalid response", err: &ProviderError{Category: ProviderErrorInvalidResponse}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.err.Retryable(); got != test.retryable {
				t.Fatalf("Retryable() = %t, want %t", got, test.retryable)
			}
			if test.err.Error() == "" {
				t.Fatal("ProviderError must produce a non-empty error string")
			}
		})
	}
}

func TestProviderErrorStringIsBoundedAndFreeOfProviderText(t *testing.T) {
	perr := &ProviderError{
		Category: ProviderErrorRateLimited, StatusCode: 429,
		Code:          sanitizeProviderCode(strings.Repeat("Throttling.AllocationQuota.", 10)), // 270 runes → truncated
		RequestID:     sanitizeRequestID(strings.Repeat("req-", 60)),
		RetryAfter:    5 * time.Second,
		RetryAfterSet: true,
	}
	text := perr.Error()
	if strings.Contains(text, "secret") || strings.Contains(text, "message") {
		t.Fatalf("error string must not contain provider text: %q", text)
	}
	if len([]rune(perr.Code)) > maxProviderErrorCodeLen {
		t.Fatalf("code was not truncated: %d runes", len([]rune(perr.Code)))
	}
	if len([]rune(perr.RequestID)) > maxProviderRequestIDLen {
		t.Fatalf("request id was not truncated: %d runes", len([]rune(perr.RequestID)))
	}
	if !strings.Contains(text, "category=rate_limited") || !strings.Contains(text, "status=429") ||
		!strings.Contains(text, "retryAfter=5s") {
		t.Fatalf("error string lost bounded fields: %q", text)
	}
}

func TestProviderErrorJSONCarriesOnlyBoundedFields(t *testing.T) {
	perr := &ProviderError{
		Category: ProviderErrorServer, StatusCode: 503,
		Code: "Throttling.AllocationQuota", RequestID: "req-abc-123",
		RetryAfter: 5 * time.Second, RetryAfterSet: true,
	}
	encoded, err := json.Marshal(perr)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["category"] != "server" || decoded["statusCode"] != float64(503) ||
		decoded["code"] != "Throttling.AllocationQuota" || decoded["requestId"] != "req-abc-123" {
		t.Fatalf("decoded = %v", decoded)
	}
	if len(decoded) != 4 {
		t.Fatalf("ProviderError JSON must carry only category/statusCode/code/requestId: %v", decoded)
	}
}

func TestProviderErrorJSONAlwaysCarriesFourSanitizedFields(t *testing.T) {
	perr := &ProviderError{
		Category:   ProviderErrorTransport,
		StatusCode: 999,
		Code:       "secret provider message",
		RequestID:  strings.Repeat("r", maxProviderRequestIDLen+20),
	}
	encoded, err := json.Marshal(perr)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 4 {
		t.Fatalf("ProviderError JSON must always carry exactly four fields: %v", decoded)
	}
	if decoded["category"] != "transport" || decoded["statusCode"] != float64(0) ||
		decoded["code"] != "" || len([]rune(decoded["requestId"].(string))) != maxProviderRequestIDLen {
		t.Fatalf("ProviderError JSON fields were not bounded: %v", decoded)
	}
	if strings.Contains(string(encoded), "secret provider message") {
		t.Fatalf("ProviderError JSON leaked free provider text: %s", encoded)
	}
	if strings.Contains(perr.Error(), "secret provider message") {
		t.Fatalf("ProviderError text leaked free provider text: %s", perr.Error())
	}

	sparseEncoded, err := json.Marshal(&ProviderError{Category: ProviderErrorTimeout})
	if err != nil {
		t.Fatal(err)
	}
	var sparse map[string]any
	if err := json.Unmarshal(sparseEncoded, &sparse); err != nil {
		t.Fatal(err)
	}
	if len(sparse) != 4 || sparse["statusCode"] != float64(0) || sparse["code"] != "" || sparse["requestId"] != "" {
		t.Fatalf("sparse ProviderError JSON must retain the four-field contract: %v", sparse)
	}
}

func TestParseRetryAfterSeconds(t *testing.T) {
	tests := []struct {
		value string
		want  time.Duration
		ok    bool
	}{
		{value: "5", want: 5 * time.Second, ok: true},
		{value: "0", want: 0, ok: true},
		{value: "120", want: 2 * time.Minute, ok: true},
		{value: " 30 ", want: 30 * time.Second, ok: true},
		{value: "-5", ok: false},
		{value: "abc", ok: false},
		{value: "", ok: false},
	}
	for _, test := range tests {
		got, ok := parseRetryAfter(test.value)
		if ok != test.ok || (ok && got != test.want) {
			t.Fatalf("parseRetryAfter(%q) = %s, %t; want %s, %t", test.value, got, ok, test.want, test.ok)
		}
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	future := time.Now().Add(2 * time.Hour).UTC()
	got, ok := parseRetryAfter(future.Format(http.TimeFormat))
	if !ok {
		t.Fatal("parseRetryAfter rejected a valid HTTP-date")
	}
	if got < 90*time.Minute || got > 3*time.Hour {
		t.Fatalf("parseRetryAfter(HTTP-date) = %s, want about 2h", got)
	}
	past := time.Now().Add(-time.Hour).UTC()
	if got, ok := parseRetryAfter(past.Format(http.TimeFormat)); ok {
		t.Fatalf("parseRetryAfter accepted a past HTTP-date: %s", got)
	}
}

func TestSanitizerDropsFreeTextAndKeepsIdentifiers(t *testing.T) {
	if got := sanitizeProviderCode("Throttling.AllocationQuota"); got != "Throttling.AllocationQuota" {
		t.Fatalf("sanitizeProviderCode = %q", got)
	}
	if got := sanitizeProviderCode("quota detail with spaces and 中文"); got != "" {
		t.Fatalf("sanitizeProviderCode kept free text: %q", got)
	}
	if got := sanitizeRequestID("req_123-abc.def:ghi/jkl"); got != "req_123-abc.def:ghi/jkl" {
		t.Fatalf("sanitizeRequestID = %q", got)
	}
	if got := sanitizeRequestID("request id with space"); got != "" {
		t.Fatalf("sanitizeRequestID kept free text: %q", got)
	}
}
