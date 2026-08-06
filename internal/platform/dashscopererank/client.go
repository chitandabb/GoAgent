package dashscopererank

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

const maxResponseBytes = 4 * 1024 * 1024

type Client struct {
	endpoint      string
	apiKey        string
	model         string
	maxCandidates int
	http          *http.Client
}

func NewClient(cfg config.RerankModelConfig, httpClient *http.Client) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, errors.New("rerank model is disabled")
	}
	apiKey, err := cfg.APIKey()
	if err != nil {
		return nil, err
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: time.Duration(cfg.TimeoutMillis) * time.Millisecond}
	}
	return &Client{
		endpoint: strings.TrimSpace(cfg.Endpoint), apiKey: apiKey, model: strings.TrimSpace(cfg.Model),
		maxCandidates: cfg.MaxCandidates, http: httpClient,
	}, nil
}

type requestPayload struct {
	Model      string            `json:"model"`
	Input      requestInput      `json:"input"`
	Parameters requestParameters `json:"parameters"`
}

type requestInput struct {
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
}

type requestParameters struct {
	TopN            int  `json:"top_n"`
	ReturnDocuments bool `json:"return_documents"`
}

type responsePayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Output  struct {
		Results []struct {
			Index          int     `json:"index"`
			RelevanceScore float64 `json:"relevance_score"`
		} `json:"results"`
	} `json:"output"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

func (c *Client) Rerank(ctx context.Context, request knowledge.RerankRequest) (knowledge.RerankResult, error) {
	if c == nil || c.http == nil {
		return knowledge.RerankResult{}, errors.New("rerank client is unavailable")
	}
	if err := request.Validate(c.maxCandidates); err != nil {
		return knowledge.RerankResult{}, err
	}
	payload, err := json.Marshal(requestPayload{
		Model: c.model, Input: requestInput{Query: request.Query, Documents: documentTexts(request.Documents)},
		Parameters: requestParameters{TopN: request.TopN, ReturnDocuments: false},
	})
	if err != nil {
		return knowledge.RerankResult{}, fmt.Errorf("encode rerank request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return knowledge.RerankResult{}, fmt.Errorf("create rerank request: %w", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	response, err := c.http.Do(httpRequest)
	if err != nil {
		return knowledge.RerankResult{}, fmt.Errorf("call rerank provider: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return knowledge.RerankResult{}, fmt.Errorf("read rerank response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return knowledge.RerankResult{}, errors.New("rerank response exceeds byte limit")
	}
	var decoded responsePayload
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&decoded); err != nil {
		return knowledge.RerankResult{}, fmt.Errorf("decode rerank response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return knowledge.RerankResult{}, errors.New("decode rerank response: trailing JSON content")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices || decoded.Code != "" {
		message := strings.TrimSpace(decoded.Message)
		if message == "" {
			message = http.StatusText(response.StatusCode)
		}
		return knowledge.RerankResult{}, fmt.Errorf("rerank provider rejected request: status=%d code=%s message=%s",
			response.StatusCode, strings.TrimSpace(decoded.Code), message)
	}
	result := knowledge.RerankResult{Usage: knowledge.RerankUsage{TotalTokens: decoded.Usage.TotalTokens}}
	for _, item := range decoded.Output.Results {
		if math.IsNaN(item.RelevanceScore) || math.IsInf(item.RelevanceScore, 0) {
			return knowledge.RerankResult{}, errors.New("rerank response contains a non-finite score")
		}
		result.Items = append(result.Items, knowledge.RerankItem{Index: item.Index, RelevanceScore: item.RelevanceScore})
	}
	if err := result.Validate(len(request.Documents), request.TopN); err != nil {
		return knowledge.RerankResult{}, fmt.Errorf("validate rerank response: %w", err)
	}
	return result, nil
}

func documentTexts(documents []knowledge.RerankDocument) []string {
	texts := make([]string, len(documents))
	for index, document := range documents {
		texts[index] = document.Content
	}
	return texts
}
