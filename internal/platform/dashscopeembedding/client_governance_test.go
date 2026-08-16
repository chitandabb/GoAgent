package dashscopeembedding

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/platform/config"
)

func governanceTestConfig(endpoint string) config.EmbeddingModelConfig {
	cfg := testConfig(endpoint)
	cfg.RPM = 900
	cfg.TPM = 600_000
	cfg.MaxAttempts = 3
	cfg.BackoffMaxMillis = 10_000
	return cfg
}

func writeEmbeddingSuccess(w http.ResponseWriter) {
	vector := make([]float32, 1024)
	vector[0] = 1
	_ = json.NewEncoder(w).Encode(map[string]any{
		"output": map[string]any{"embeddings": []any{
			map[string]any{"text_index": 0, "embedding": vector},
		}},
		"usage": map[string]any{"total_tokens": 7},
	})
}

func writeProviderError(w http.ResponseWriter, status int, code, message string, retryAfter string) {
	if retryAfter != "" {
		w.Header().Set("Retry-After", retryAfter)
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code": code, "message": message, "request_id": "req-abc-123",
	})
}

func singleTextRequest() knowledge.EmbeddingRequest {
	return knowledge.EmbeddingRequest{
		Texts: []string{"confidential-input-xyz"}, InputType: knowledge.EmbeddingInputDocument,
	}
}

func TestEmbedRetries429WithRetryAfterThenSucceeds(t *testing.T) {
	t.Setenv("TEST_DASHSCOPE_KEY", "secret")
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			writeProviderError(w, http.StatusTooManyRequests, "Throttling.AllocationQuota", "temporary quota", "1")
			return
		}
		writeEmbeddingSuccess(w)
	}))
	defer server.Close()
	client, err := NewClient(governanceTestConfig(server.URL+"/embed"), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now()
	result, err := client.Embed(context.Background(), singleTextRequest())
	if err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 2 || len(result.Vectors) != 1 {
		t.Fatalf("attempts=%d vectors=%d, want 2 attempts and one vector", attempts.Load(), len(result.Vectors))
	}
	if elapsed := time.Since(startedAt); elapsed < 900*time.Millisecond {
		t.Fatalf("retry completed after %s, want at least 900ms from Retry-After: 1", elapsed)
	}
}

func TestEmbed429WithoutRetryAfterUsesBoundedExponentialBackoff(t *testing.T) {
	t.Setenv("TEST_DASHSCOPE_KEY", "secret")
	var mu sync.Mutex
	var arrivals []time.Time
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		arrivals = append(arrivals, time.Now())
		mu.Unlock()
		writeProviderError(w, http.StatusTooManyRequests, "Throttling.AllocationQuota", "temporary quota", "")
	}))
	defer server.Close()
	cfg := governanceTestConfig(server.URL + "/embed")
	cfg.MaxAttempts = 3
	cfg.BackoffMaxMillis = 1_000
	client, err := NewClient(cfg, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Embed(context.Background(), singleTextRequest())
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %v, want ProviderError", err)
	}
	if providerErr.Category != ProviderErrorRateLimited || providerErr.StatusCode != http.StatusTooManyRequests ||
		providerErr.RetryAfterSet || providerErr.Code != "Throttling.AllocationQuota" {
		t.Fatalf("provider error = %+v", providerErr)
	}
	if !strings.Contains(err.Error(), "after 3 attempts") {
		t.Fatalf("error must report the bounded attempt count: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(arrivals) != 3 {
		t.Fatalf("attempts = %d, want 3", len(arrivals))
	}
	if gap := arrivals[1].Sub(arrivals[0]); gap < 200*time.Millisecond {
		t.Fatalf("first backoff gap = %s, want at least 200ms (250ms base)", gap)
	}
	if gap := arrivals[2].Sub(arrivals[1]); gap < 400*time.Millisecond {
		t.Fatalf("second backoff gap = %s, want at least 400ms (500ms base)", gap)
	}
}

func TestEmbedDoesNotRetryBadRequestOrAuth(t *testing.T) {
	for _, test := range []struct {
		status   int
		category ProviderErrorCategory
	}{
		{status: http.StatusBadRequest, category: ProviderErrorBadRequest},
		{status: http.StatusUnauthorized, category: ProviderErrorAuth},
		{status: http.StatusForbidden, category: ProviderErrorBadRequest},
	} {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			t.Setenv("TEST_DASHSCOPE_KEY", "secret")
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempts.Add(1)
				writeProviderError(w, test.status, "SomeError", "not retryable", "")
			}))
			defer server.Close()
			client, err := NewClient(governanceTestConfig(server.URL+"/embed"), server.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Embed(context.Background(), singleTextRequest())
			var providerErr *ProviderError
			if !errors.As(err, &providerErr) || providerErr.Category != test.category {
				t.Fatalf("error = %v, want category %s", err, test.category)
			}
			if attempts.Load() != 1 {
				t.Fatalf("attempts = %d, want 1 (no retry for status %d)", attempts.Load(), test.status)
			}
		})
	}
}

func TestEmbedRetriesClearlyRetryableServerErrors(t *testing.T) {
	for _, status := range []int{500, 502, 503, 504} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Setenv("TEST_DASHSCOPE_KEY", "secret")
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if attempts.Add(1) == 1 {
					writeProviderError(w, status, "ServerError", "temporary", "")
					return
				}
				writeEmbeddingSuccess(w)
			}))
			defer server.Close()
			client, err := NewClient(governanceTestConfig(server.URL+"/embed"), server.Client())
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Embed(context.Background(), singleTextRequest())
			if err != nil || len(result.Vectors) != 1 || attempts.Load() != 2 {
				t.Fatalf("err=%v attempts=%d, want retry then success", err, attempts.Load())
			}
		})
	}
}

func TestEmbedDoesNotRetryOtherServerErrors(t *testing.T) {
	for _, status := range []int{501, 505} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Setenv("TEST_DASHSCOPE_KEY", "secret")
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempts.Add(1)
				writeProviderError(w, status, "ServerError", "permanent", "")
			}))
			defer server.Close()
			client, err := NewClient(governanceTestConfig(server.URL+"/embed"), server.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Embed(context.Background(), singleTextRequest())
			var providerErr *ProviderError
			if !errors.As(err, &providerErr) || providerErr.Category != ProviderErrorServer {
				t.Fatalf("error = %v, want category server", err)
			}
			if attempts.Load() != 1 {
				t.Fatalf("attempts = %d, want 1 for status %d", attempts.Load(), status)
			}
		})
	}
}

func TestEmbedRetriesClientTimeout(t *testing.T) {
	t.Setenv("TEST_DASHSCOPE_KEY", "secret")
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			time.Sleep(400 * time.Millisecond)
			writeEmbeddingSuccess(w)
			return
		}
		writeEmbeddingSuccess(w)
	}))
	defer server.Close()
	httpClient := server.Client()
	httpClient.Timeout = 100 * time.Millisecond
	client, err := NewClient(governanceTestConfig(server.URL+"/embed"), httpClient)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Embed(context.Background(), singleTextRequest())
	if err != nil || len(result.Vectors) != 1 || attempts.Load() != 2 {
		t.Fatalf("err=%v attempts=%d, want timed-out attempt retried then success", err, attempts.Load())
	}
}

func TestEmbedRetriesTransportError(t *testing.T) {
	t.Setenv("TEST_DASHSCOPE_KEY", "secret")
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			if hijacker, ok := w.(http.Hijacker); ok {
				conn, _, err := hijacker.Hijack()
				if err == nil {
					_ = conn.Close()
				}
			}
			return
		}
		writeEmbeddingSuccess(w)
	}))
	defer server.Close()
	client, err := NewClient(governanceTestConfig(server.URL+"/embed"), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Embed(context.Background(), singleTextRequest())
	if err != nil || len(result.Vectors) != 1 || attempts.Load() != 2 {
		t.Fatalf("err=%v attempts=%d, want transport error retried then success", err, attempts.Load())
	}
}

func TestEmbedContextDeadlineStopsRetryWait(t *testing.T) {
	t.Setenv("TEST_DASHSCOPE_KEY", "secret")
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		writeProviderError(w, http.StatusTooManyRequests, "Throttling.AllocationQuota", "temporary", "5")
	}))
	defer server.Close()
	client, err := NewClient(governanceTestConfig(server.URL+"/embed"), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err = client.Embed(ctx, singleTextRequest())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1 (deadline must not trigger another attempt)", attempts.Load())
	}
}

func TestEmbedCanceledContextDoesNotTouchProvider(t *testing.T) {
	t.Setenv("TEST_DASHSCOPE_KEY", "secret")
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		writeEmbeddingSuccess(w)
	}))
	defer server.Close()
	client, err := NewClient(governanceTestConfig(server.URL+"/embed"), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Embed(ctx, singleTextRequest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if attempts.Load() != 0 {
		t.Fatalf("attempts = %d, want 0", attempts.Load())
	}
}

func TestEmbedExhaustsAttemptsOnRepeated429(t *testing.T) {
	t.Setenv("TEST_DASHSCOPE_KEY", "secret")
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		writeProviderError(w, http.StatusTooManyRequests, "Throttling.AllocationQuota", "temporary", "")
	}))
	defer server.Close()
	cfg := governanceTestConfig(server.URL + "/embed")
	cfg.MaxAttempts = 4
	cfg.BackoffMaxMillis = 1_000
	client, err := NewClient(cfg, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Embed(context.Background(), singleTextRequest())
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Category != ProviderErrorRateLimited {
		t.Fatalf("error = %v, want rate_limited ProviderError", err)
	}
	if attempts.Load() != 4 {
		t.Fatalf("attempts = %d, want 4 (max attempts includes the first call)", attempts.Load())
	}
	if !strings.Contains(err.Error(), "after 4 attempts") {
		t.Fatalf("error must report the bounded attempt count: %v", err)
	}
}

func TestEmbedErrorDoesNotLeakProviderMessageOrSecrets(t *testing.T) {
	t.Setenv("TEST_DASHSCOPE_KEY", "test-secret-key-abc")
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		writeProviderError(w, http.StatusTooManyRequests, "Throttling.AllocationQuota", "secret-provider-message-xyz", "0")
	}))
	defer server.Close()
	cfg := governanceTestConfig(server.URL + "/embed")
	cfg.MaxAttempts = 1
	client, err := NewClient(cfg, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Embed(context.Background(), singleTextRequest())
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %v, want ProviderError", err)
	}
	if providerErr.Code != "Throttling.AllocationQuota" || providerErr.RequestID != "req-abc-123" ||
		!providerErr.RetryAfterSet || providerErr.RetryAfter != 0 || providerErr.StatusCode != 429 {
		t.Fatalf("provider error = %+v", providerErr)
	}
	for _, forbidden := range []string{"secret-provider-message-xyz", "test-secret-key-abc", "confidential-input-xyz"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error text leaked %q: %v", forbidden, err)
		}
	}
	encoded, marshalErr := json.Marshal(providerErr)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	for _, forbidden := range []string{"secret-provider-message-xyz", "test-secret-key-abc", "confidential-input-xyz"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("provider error JSON leaked %q: %s", forbidden, encoded)
		}
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", attempts.Load())
	}
}

func TestEmbedInvalidResponseIsNotRetried(t *testing.T) {
	t.Setenv("TEST_DASHSCOPE_KEY", "secret")
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("this is not a JSON embedding response"))
	}))
	defer server.Close()
	cfg := governanceTestConfig(server.URL + "/embed")
	cfg.MaxAttempts = 3
	client, err := NewClient(cfg, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Embed(context.Background(), singleTextRequest())
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Category != ProviderErrorInvalidResponse {
		t.Fatalf("error = %v, want invalid_response ProviderError", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1 (invalid responses must not be retried)", attempts.Load())
	}
}

func TestEmbedClassifiesHTTPFailureBeforeDecodingBody(t *testing.T) {
	for _, test := range []struct {
		name       string
		statusCode int
		want       ProviderErrorCategory
	}{
		{name: "plain text 429", statusCode: http.StatusTooManyRequests, want: ProviderErrorRateLimited},
		{name: "empty 503", statusCode: http.StatusServiceUnavailable, want: ProviderErrorServer},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TEST_DASHSCOPE_KEY", "secret")
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if attempts.Add(1) == 1 {
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(test.statusCode)
					if test.statusCode == http.StatusTooManyRequests {
						_, _ = w.Write([]byte("provider maintenance page"))
					}
					return
				}
				writeEmbeddingSuccess(w)
			}))
			defer server.Close()

			cfg := governanceTestConfig(server.URL + "/embed")
			cfg.MaxAttempts = 2
			client, err := NewClient(cfg, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Embed(context.Background(), singleTextRequest())
			if err != nil || len(result.Vectors) != 1 {
				t.Fatalf("Embed() err=%v vectors=%d, want retry after %s", err, len(result.Vectors), test.want)
			}
			if attempts.Load() != 2 {
				t.Fatalf("attempts = %d, want 2", attempts.Load())
			}
		})
	}
}

func TestEmbedCapsRetryAfterAtConfiguredBackoffLimit(t *testing.T) {
	t.Setenv("TEST_DASHSCOPE_KEY", "secret")
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			writeProviderError(w, http.StatusTooManyRequests, "Throttling.AllocationQuota", "retry later", "120")
			return
		}
		writeEmbeddingSuccess(w)
	}))
	defer server.Close()

	cfg := governanceTestConfig(server.URL + "/embed")
	cfg.MaxAttempts = 2
	cfg.BackoffMaxMillis = 1000
	client, err := NewClient(cfg, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	result, err := client.Embed(ctx, singleTextRequest())
	if err != nil || len(result.Vectors) != 1 {
		t.Fatalf("Embed() err=%v vectors=%d, want Retry-After capped to configured backoff", err, len(result.Vectors))
	}
	if elapsed := time.Since(startedAt); elapsed < 900*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("elapsed = %s, want configured 1s cap", elapsed)
	}
}

func TestEmbedCapsOverflowingRetryAfterAtConfiguredBackoffLimit(t *testing.T) {
	t.Setenv("TEST_DASHSCOPE_KEY", "secret")
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			writeProviderError(w, http.StatusTooManyRequests, "Throttling.AllocationQuota", "retry later", "9223372036854775807")
			return
		}
		writeEmbeddingSuccess(w)
	}))
	defer server.Close()

	cfg := governanceTestConfig(server.URL + "/embed")
	cfg.MaxAttempts = 2
	cfg.BackoffMaxMillis = 1000
	client, err := NewClient(cfg, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	result, err := client.Embed(ctx, singleTextRequest())
	if err != nil || len(result.Vectors) != 1 {
		t.Fatalf("Embed() err=%v vectors=%d, want overflowing Retry-After capped to configured backoff", err, len(result.Vectors))
	}
	if elapsed := time.Since(startedAt); elapsed < 900*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("elapsed = %s, want configured 1s cap", elapsed)
	}
}

func TestEmbedConcurrencyCap(t *testing.T) {
	t.Setenv("TEST_DASHSCOPE_KEY", "secret")
	var inFlight atomic.Int32
	var maxInFlight atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		current := inFlight.Add(1)
		for {
			observed := maxInFlight.Load()
			if current <= observed || maxInFlight.CompareAndSwap(observed, current) {
				break
			}
		}
		time.Sleep(150 * time.Millisecond)
		inFlight.Add(-1)
		writeEmbeddingSuccess(w)
	}))
	defer server.Close()
	cfg := governanceTestConfig(server.URL + "/embed")
	cfg.MaxConcurrent = 2
	client, err := NewClient(cfg, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := client.Embed(context.Background(), singleTextRequest())
			errs <- err
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := maxInFlight.Load(); got != 2 {
		t.Fatalf("max in-flight requests = %d, want 2 (maxConcurrent)", got)
	}
}

func TestEmbedRePassesLimiterBetweenRetries(t *testing.T) {
	t.Setenv("TEST_DASHSCOPE_KEY", "secret")
	var mu sync.Mutex
	var arrivals []time.Time
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		arrivals = append(arrivals, time.Now())
		mu.Unlock()
		if len(arrivals) == 1 {
			writeProviderError(w, http.StatusTooManyRequests, "Throttling.AllocationQuota", "temporary", "0")
			return
		}
		writeEmbeddingSuccess(w)
	}))
	defer server.Close()
	cfg := governanceTestConfig(server.URL + "/embed")
	cfg.RPM = 600 // 100ms request spacing
	client, err := NewClient(cfg, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Embed(context.Background(), singleTextRequest())
	if err != nil || len(result.Vectors) != 1 {
		t.Fatalf("err=%v, want success after one retry", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(arrivals) != 2 {
		t.Fatalf("attempts = %d, want 2", len(arrivals))
	}
	if gap := arrivals[1].Sub(arrivals[0]); gap < 90*time.Millisecond {
		t.Fatalf("retry did not re-pass the RPM gate: gap = %s, want at least 90ms", gap)
	}
}

func TestExponentialBackoffDelayIsBoundedAndDoubling(t *testing.T) {
	cap := 10 * time.Second
	sequence := []time.Duration{
		250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second,
		4 * time.Second, 8 * time.Second, cap, cap,
	}
	for attempt := 1; attempt <= 8; attempt++ {
		if got := exponentialBackoffDelay(attempt, cap); got != sequence[attempt-1] {
			t.Fatalf("exponentialBackoffDelay(%d) = %s, want %s", attempt, got, sequence[attempt-1])
		}
	}
	if got := exponentialBackoffDelay(3, 400*time.Millisecond); got != 400*time.Millisecond {
		t.Fatalf("bounded backoff = %s, want the cap 400ms", got)
	}
}

func TestNewClientRejectsOutOfRangeQuota(t *testing.T) {
	t.Setenv("TEST_DASHSCOPE_KEY", "secret")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeEmbeddingSuccess(w)
	}))
	defer server.Close()
	base := governanceTestConfig(server.URL + "/embed")
	for name, mutate := range map[string]func(*config.EmbeddingModelConfig){
		"rpm above ceiling": func(c *config.EmbeddingModelConfig) { c.RPM = config.MaxEmbeddingRPM + 1 },
		"tpm above ceiling": func(c *config.EmbeddingModelConfig) { c.TPM = config.MaxEmbeddingTPM + 1 },
		"attempts too high": func(c *config.EmbeddingModelConfig) { c.MaxAttempts = 9 },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := base
			mutate(&cfg)
			if _, err := NewClient(cfg, server.Client()); err == nil {
				t.Fatal("NewClient accepted an out-of-range quota")
			}
		})
	}
}
