package dashscopeembedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/platform/embeddingquota"
)

const (
	maxResponseBytes  = 8 * 1024 * 1024
	backoffBaseMillis = 250
)

// Client 是进程级治理的 Embedding Provider 客户端：同一进程的所有消费者
// 必须共享同一个 Client（从而共享 limiter 与并发门禁），不能各自获得完整
// 额度。Client 不做跨进程共享；水平扩容需重新分配 RPM/TPM 预算。
type Client struct {
	endpoint      string
	apiKey        string
	model         string
	profile       knowledge.EmbeddingProfile
	batchSize     int
	maxConcurrent int
	http          *http.Client
	limiter       *embeddingquota.Limiter
	sem           chan struct{}
	maxAttempts   int
	backoffCap    time.Duration
}

func NewClient(cfg config.EmbeddingModelConfig, httpClient *http.Client) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, errors.New("embedding model is disabled")
	}
	apiKey, err := cfg.APIKey()
	if err != nil {
		return nil, err
	}
	profile, err := cfg.Profile()
	if err != nil {
		return nil, err
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: time.Duration(cfg.TimeoutMillis) * time.Millisecond}
	}
	limiter, err := embeddingquota.NewLimiter(cfg.EffectiveRPM(), cfg.EffectiveTPM())
	if err != nil {
		return nil, err
	}
	return &Client{
		endpoint: strings.TrimSpace(cfg.Endpoint), apiKey: apiKey, model: strings.TrimSpace(cfg.Model),
		profile: profile, batchSize: cfg.BatchSize, maxConcurrent: cfg.MaxConcurrent,
		http: httpClient, limiter: limiter, sem: make(chan struct{}, cfg.MaxConcurrent),
		maxAttempts: cfg.EffectiveMaxAttempts(),
		backoffCap:  time.Duration(cfg.EffectiveBackoffMaxMillis()) * time.Millisecond,
	}, nil
}

type requestPayload struct {
	Model      string            `json:"model"`
	Input      requestInput      `json:"input"`
	Parameters requestParameters `json:"parameters"`
}

type requestInput struct {
	Texts []string `json:"texts"`
}

type requestParameters struct {
	Dimension  int    `json:"dimension"`
	OutputType string `json:"output_type"`
	TextType   string `json:"text_type"`
}

// responsePayload 只解码受限字段；响应 message/body 永不进入内存之外，
// Message 字段刻意不存在，避免任何保存或输出路径接触到 Provider 自由文本。
type responsePayload struct {
	RequestID string `json:"request_id"`
	Code      string `json:"code"`
	Output    struct {
		Embeddings []struct {
			Embedding []float32 `json:"embedding"`
			TextIndex int       `json:"text_index"`
		} `json:"embeddings"`
	} `json:"output"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

// Embed 在进程级 RPM/TPM 平滑预算与并发上限内执行请求。每次真实 HTTP
// attempt 都重新经过并发、RPM 与估算 TPM 门禁；只有 429、明确可重试 5xx
// 与 timeout/transport 做有界重试（最大尝试次数包含首次调用），等待与
// HTTP 调用都响应 Context 取消。
func (c *Client) Embed(ctx context.Context, input knowledge.EmbeddingRequest) (knowledge.EmbeddingResult, error) {
	if c == nil || c.http == nil {
		return knowledge.EmbeddingResult{}, errors.New("embedding client is unavailable")
	}
	if err := input.Validate(c.batchSize); err != nil {
		return knowledge.EmbeddingResult{}, err
	}
	estimatedTokens := 0
	for _, text := range input.Texts {
		estimatedTokens += embeddingquota.EstimateTextTokens(text)
	}
	payload, err := json.Marshal(requestPayload{
		Model: c.model, Input: requestInput{Texts: input.Texts},
		Parameters: requestParameters{
			Dimension: c.profile.Dimensions, OutputType: "dense", TextType: string(input.InputType),
		},
	})
	if err != nil {
		return knowledge.EmbeddingResult{}, fmt.Errorf("encode embedding request: %w", err)
	}
	var lastErr error
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			// 调用方已取消或超时：不再发起或重试任何请求。
			return knowledge.EmbeddingResult{}, err
		}
		if err := c.limiter.Wait(ctx, estimatedTokens); err != nil {
			return knowledge.EmbeddingResult{}, err
		}
		select {
		case c.sem <- struct{}{}:
		case <-ctx.Done():
			return knowledge.EmbeddingResult{}, ctx.Err()
		}
		result, attemptErr := c.doAttempt(ctx, input, payload)
		<-c.sem
		if attemptErr == nil {
			return result, nil
		}
		lastErr = attemptErr
		var providerErr *ProviderError
		if !errors.As(attemptErr, &providerErr) {
			return knowledge.EmbeddingResult{}, attemptErr
		}
		if !providerErr.Retryable() {
			return knowledge.EmbeddingResult{}, attemptErr
		}
		if attempt == c.maxAttempts {
			break
		}
		delay := c.retryDelay(providerErr, attempt)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return knowledge.EmbeddingResult{}, ctx.Err()
		case <-timer.C:
		}
	}
	return knowledge.EmbeddingResult{}, fmt.Errorf(
		"embedding provider request failed after %d attempts: %w", c.maxAttempts, lastErr,
	)
}

func (c *Client) doAttempt(
	ctx context.Context,
	input knowledge.EmbeddingRequest,
	payload []byte,
) (knowledge.EmbeddingResult, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return knowledge.EmbeddingResult{}, fmt.Errorf("create embedding request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")

	response, err := c.http.Do(request)
	if err != nil {
		return knowledge.EmbeddingResult{}, c.classifyTransportError(ctx, err)
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		var decoded responsePayload
		if readErr == nil && len(body) <= maxResponseBytes {
			decoded, _ = decodeResponse(body)
		}
		return knowledge.EmbeddingResult{}, c.providerErrorFromResponse(response, &decoded)
	}
	if readErr != nil {
		if ctx.Err() != nil {
			return knowledge.EmbeddingResult{}, ctx.Err()
		}
		return knowledge.EmbeddingResult{}, &ProviderError{Category: ProviderErrorTransport}
	}
	if len(body) > maxResponseBytes {
		return knowledge.EmbeddingResult{}, &ProviderError{Category: ProviderErrorInvalidResponse}
	}
	decoded, err := decodeResponse(body)
	if err != nil {
		return knowledge.EmbeddingResult{}, &ProviderError{Category: ProviderErrorInvalidResponse}
	}
	if decoded.Code != "" {
		return knowledge.EmbeddingResult{}, c.providerErrorFromResponse(response, &decoded)
	}

	vectors := make([][]float32, len(input.Texts))
	seen := make([]bool, len(input.Texts))
	for _, item := range decoded.Output.Embeddings {
		if item.TextIndex < 0 || item.TextIndex >= len(vectors) || seen[item.TextIndex] {
			return knowledge.EmbeddingResult{}, &ProviderError{Category: ProviderErrorInvalidResponse}
		}
		vector := append([]float32(nil), item.Embedding...)
		if c.profile.Normalize {
			if err := normalize(vector); err != nil {
				return knowledge.EmbeddingResult{}, &ProviderError{Category: ProviderErrorInvalidResponse}
			}
		}
		vectors[item.TextIndex] = vector
		seen[item.TextIndex] = true
	}
	result := knowledge.EmbeddingResult{
		Vectors: vectors, Usage: knowledge.EmbeddingUsage{TotalTokens: decoded.Usage.TotalTokens},
	}
	if err := result.Validate(len(input.Texts), c.profile.Dimensions, c.profile.Normalize); err != nil {
		return knowledge.EmbeddingResult{}, &ProviderError{Category: ProviderErrorInvalidResponse}
	}
	return result, nil
}

func decodeResponse(body []byte) (responsePayload, error) {
	var decoded responsePayload
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&decoded); err != nil {
		return responsePayload{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return responsePayload{}, errors.New("embedding response contains trailing JSON content")
	}
	return decoded, nil
}

// classifyTransportError 区分 caller 取消（不重试）、超时（重试）与传输层
// 错误（重试）。ctx 已结束时一律原样返回 ctx.Err()，杜绝把取消当作
// 可重试超时。
func (c *Client) classifyTransportError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &ProviderError{Category: ProviderErrorTimeout}
	}
	return &ProviderError{Category: ProviderErrorTransport}
}

func (c *Client) providerErrorFromResponse(response *http.Response, decoded *responsePayload) *ProviderError {
	retryAfter, retryAfterSet := parseRetryAfter(response.Header.Get("Retry-After"))
	return &ProviderError{
		Category:      classifyHTTPStatus(response.StatusCode),
		StatusCode:    boundedStatusCode(response.StatusCode),
		Code:          sanitizeProviderCode(decoded.Code),
		RequestID:     sanitizeRequestID(decoded.RequestID),
		RetryAfter:    retryAfter,
		RetryAfterSet: retryAfterSet,
	}
}

func classifyHTTPStatus(statusCode int) ProviderErrorCategory {
	switch {
	case statusCode == http.StatusTooManyRequests:
		return ProviderErrorRateLimited
	case statusCode == http.StatusUnauthorized:
		return ProviderErrorAuth
	case statusCode >= 400 && statusCode <= 499:
		return ProviderErrorBadRequest
	case statusCode >= 500 && statusCode <= 599:
		return ProviderErrorServer
	default:
		return ProviderErrorInvalidResponse
	}
}

// retryDelay 优先使用有效的 Retry-After；头缺失或非法时回退到有界指数退避。
func (c *Client) retryDelay(providerErr *ProviderError, attempt int) time.Duration {
	if providerErr != nil && providerErr.RetryAfterSet {
		if providerErr.RetryAfter > c.backoffCap {
			return c.backoffCap
		}
		return providerErr.RetryAfter
	}
	return exponentialBackoffDelay(attempt, c.backoffCap)
}

// exponentialBackoffDelay 返回第 attempt 次失败后的退避时长：250ms 起、
// 每次翻倍，并封顶到 cap。
func exponentialBackoffDelay(attempt int, cap time.Duration) time.Duration {
	delay := time.Duration(backoffBaseMillis) * time.Millisecond * (1 << (attempt - 1))
	if delay <= 0 || delay > cap {
		return cap
	}
	return delay
}

func normalize(vector []float32) error {
	var squaredNorm float64
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return errors.New("embedding response contains a non-finite value")
		}
		squaredNorm += float64(value) * float64(value)
	}
	if squaredNorm == 0 {
		return errors.New("embedding response contains a zero-norm vector")
	}
	norm := float32(math.Sqrt(squaredNorm))
	for index := range vector {
		vector[index] /= norm
	}
	return nil
}
