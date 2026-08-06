package dashscopeembedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/platform/config"
)

const maxResponseBytes = 8 * 1024 * 1024

type Client struct {
	endpoint  string
	apiKey    string
	model     string
	profile   knowledge.EmbeddingProfile
	batchSize int
	http      *http.Client
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
	return &Client{
		endpoint: strings.TrimSpace(cfg.Endpoint), apiKey: apiKey, model: strings.TrimSpace(cfg.Model),
		profile: profile, batchSize: cfg.BatchSize, http: httpClient,
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

type responsePayload struct {
	RequestID string `json:"request_id"`
	Code      string `json:"code"`
	Message   string `json:"message"`
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

func (c *Client) Embed(ctx context.Context, input knowledge.EmbeddingRequest) (knowledge.EmbeddingResult, error) {
	if c == nil || c.http == nil {
		return knowledge.EmbeddingResult{}, errors.New("embedding client is unavailable")
	}
	if err := input.Validate(c.batchSize); err != nil {
		return knowledge.EmbeddingResult{}, err
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
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return knowledge.EmbeddingResult{}, fmt.Errorf("create embedding request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")

	response, err := c.http.Do(request)
	if err != nil {
		return knowledge.EmbeddingResult{}, fmt.Errorf("call embedding provider: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return knowledge.EmbeddingResult{}, fmt.Errorf("read embedding response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return knowledge.EmbeddingResult{}, errors.New("embedding response exceeds byte limit")
	}
	var decoded responsePayload
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&decoded); err != nil {
		return knowledge.EmbeddingResult{}, fmt.Errorf("decode embedding response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return knowledge.EmbeddingResult{}, errors.New("decode embedding response: trailing JSON content")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices || decoded.Code != "" {
		message := strings.TrimSpace(decoded.Message)
		if message == "" {
			message = http.StatusText(response.StatusCode)
		}
		return knowledge.EmbeddingResult{}, fmt.Errorf("embedding provider rejected request: status=%d code=%s message=%s",
			response.StatusCode, strings.TrimSpace(decoded.Code), message)
	}

	vectors := make([][]float32, len(input.Texts))
	seen := make([]bool, len(input.Texts))
	for _, item := range decoded.Output.Embeddings {
		if item.TextIndex < 0 || item.TextIndex >= len(vectors) || seen[item.TextIndex] {
			return knowledge.EmbeddingResult{}, errors.New("embedding response contains an invalid text index")
		}
		vector := append([]float32(nil), item.Embedding...)
		if c.profile.Normalize {
			if err := normalize(vector); err != nil {
				return knowledge.EmbeddingResult{}, err
			}
		}
		vectors[item.TextIndex] = vector
		seen[item.TextIndex] = true
	}
	result := knowledge.EmbeddingResult{
		Vectors: vectors, Usage: knowledge.EmbeddingUsage{TotalTokens: decoded.Usage.TotalTokens},
	}
	if err := result.Validate(len(input.Texts), c.profile.Dimensions, c.profile.Normalize); err != nil {
		return knowledge.EmbeddingResult{}, fmt.Errorf("validate embedding response: %w", err)
	}
	return result, nil
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
