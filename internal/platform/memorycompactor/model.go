// Package memorycompactor adapts an Eino-compatible ChatModel to the
// provider-independent conversation memory Compactor contract.
package memorycompactor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/conversationmemory"

	modelopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

var (
	ErrInvalidConfig   = errors.New("conversation memory model compactor config is invalid")
	ErrInvalidInput    = errors.New("conversation memory model compactor input is invalid")
	ErrProviderRequest = errors.New("conversation memory model provider request failed")
	ErrOutputTooLarge  = errors.New("conversation memory model output is too large")
	ErrOutputTruncated = errors.New("conversation memory model output was truncated")
)

type Generator interface {
	Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error)
}

type Config struct {
	Generator      Generator
	Prompt         string
	PromptVersion  string
	Timeout        time.Duration
	MaxOutputBytes int
}

type ModelCompactor struct {
	generator      Generator
	prompt         string
	promptVersion  string
	timeout        time.Duration
	maxOutputBytes int
}

type providerRequestError struct {
	cause        error
	nonRetryable bool
	code         string
}

func (e *providerRequestError) Error() string { return ErrProviderRequest.Error() }

func (e *providerRequestError) Unwrap() []error { return []error{ErrProviderRequest, e.cause} }

func (e *providerRequestError) NonRetryableCompaction() bool { return e != nil && e.nonRetryable }

func (e *providerRequestError) CompactionFailureCode() string {
	if e == nil || e.code == "" {
		return "provider_request_failed"
	}
	return e.code
}

type outputTruncatedError struct{}

func (*outputTruncatedError) Error() string                 { return ErrOutputTruncated.Error() }
func (*outputTruncatedError) Unwrap() error                 { return ErrOutputTruncated }
func (*outputTruncatedError) CompactionRepairCode() string  { return "output_truncated" }
func (*outputTruncatedError) CompactionFailureCode() string { return "output_truncated" }

func New(config Config) (*ModelCompactor, error) {
	if config.Generator == nil || strings.TrimSpace(config.Prompt) == "" || config.Prompt != strings.TrimSpace(config.Prompt) ||
		strings.TrimSpace(config.PromptVersion) == "" || config.PromptVersion != strings.TrimSpace(config.PromptVersion) ||
		config.Timeout < time.Second || config.Timeout > 5*time.Minute ||
		config.MaxOutputBytes < 1024 || config.MaxOutputBytes > 1024*1024 {
		return nil, ErrInvalidConfig
	}
	return &ModelCompactor{
		generator: config.Generator, prompt: config.Prompt, promptVersion: config.PromptVersion,
		timeout: config.Timeout, maxOutputBytes: config.MaxOutputBytes,
	}, nil
}

func (c *ModelCompactor) Compact(ctx context.Context, input conversationmemory.CompactionInput) (conversationmemory.CompactionOutput, error) {
	if c == nil || c.generator == nil || input.ConversationID == uuid.Nil ||
		input.FromSeq < 1 || input.ThroughSeq < input.FromSeq || input.Attempt < 1 || len(input.NewMessages) == 0 {
		return conversationmemory.CompactionOutput{}, ErrInvalidInput
	}
	payload, err := json.Marshal(compactionRequest(input, c.promptVersion))
	if err != nil {
		return conversationmemory.CompactionOutput{}, fmt.Errorf("encode conversation memory model input: %w", err)
	}
	runCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	response, err := c.generator.Generate(runCtx, []*schema.Message{
		{Role: schema.System, Content: c.prompt},
		schema.UserMessage(string(payload)),
	})
	if err != nil {
		var apiErr *modelopenai.APIError
		if errors.As(err, &apiErr) && apiErr != nil && apiErr.HTTPStatusCode > 0 {
			return conversationmemory.CompactionOutput{}, newProviderRequestError(err)
		}
	}
	if response != nil && response.ResponseMeta != nil &&
		strings.EqualFold(strings.TrimSpace(response.ResponseMeta.FinishReason), "length") {
		return conversationmemory.CompactionOutput{}, &outputTruncatedError{}
	}
	if err != nil {
		return conversationmemory.CompactionOutput{}, newProviderRequestError(err)
	}
	if response == nil {
		return conversationmemory.CompactionOutput{}, conversationmemory.ErrInvalidPayloadSchema
	}
	if strings.TrimSpace(response.Content) == "" {
		return conversationmemory.CompactionOutput{}, conversationmemory.ErrInvalidPayloadSchema
	}
	if len(response.Content) > c.maxOutputBytes {
		return conversationmemory.CompactionOutput{}, ErrOutputTooLarge
	}
	normalized, err := normalizeJSON(response.Content)
	if err != nil {
		return conversationmemory.CompactionOutput{}, err
	}
	if len(normalized) > c.maxOutputBytes {
		return conversationmemory.CompactionOutput{}, ErrOutputTooLarge
	}
	structured, err := conversationmemory.DecodePayload([]byte(normalized))
	if err != nil {
		return conversationmemory.CompactionOutput{}, err
	}
	result := conversationmemory.CompactionOutput{Payload: structured}
	if response.ResponseMeta != nil && response.ResponseMeta.Usage != nil {
		usage := response.ResponseMeta.Usage
		result.Usage = conversationmemory.SummaryUsage{
			PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
			TotalTokens: usage.TotalTokens, CachedTokens: usage.PromptTokenDetails.CachedTokens,
		}
	}
	return result, nil
}

func newProviderRequestError(err error) error {
	nonRetryable := false
	code := "provider_request_failed"
	var coded conversationmemory.CompactionFailureCodeError
	var deterministic conversationmemory.NonRetryableCompactionError
	if errors.As(err, &coded) {
		code = coded.CompactionFailureCode()
	}
	if errors.As(err, &deterministic) && deterministic.NonRetryableCompaction() {
		nonRetryable = true
	}
	if code == "local_budget_exceeded" && nonRetryable {
		return err
	}
	var apiErr *modelopenai.APIError
	if errors.As(err, &apiErr) && apiErr != nil {
		switch {
		case apiErr.HTTPStatusCode == 400:
			code, nonRetryable = "provider_http_400", true
		case apiErr.HTTPStatusCode == 401:
			code, nonRetryable = "provider_http_401", true
		case apiErr.HTTPStatusCode == 403:
			code, nonRetryable = "provider_http_403", true
		case apiErr.HTTPStatusCode == 429:
			code = "provider_http_429"
		case apiErr.HTTPStatusCode >= 500 && apiErr.HTTPStatusCode <= 599:
			code = "provider_http_5xx"
		}
	} else if errors.Is(err, context.Canceled) {
		code, nonRetryable = "provider_canceled", true
	} else if errors.Is(err, context.DeadlineExceeded) {
		code = "provider_timeout"
	} else {
		var timeout net.Error
		var dnsErr *net.DNSError
		var operationErr *net.OpError
		switch {
		case errors.As(err, &timeout) && timeout.Timeout():
			code = "provider_timeout"
		case errors.As(err, &dnsErr), errors.As(err, &operationErr):
			code = "provider_connection_failed"
		}
	}
	return &providerRequestError{cause: err, nonRetryable: nonRetryable, code: code}
}

type requestCoverage struct {
	FromSeq    int64 `json:"fromSeq"`
	ThroughSeq int64 `json:"throughSeq"`
}

type previousSnapshotProjection struct {
	SnapshotID string                     `json:"snapshotId"`
	Coverage   requestCoverage            `json:"coverage"`
	Payload    conversationmemory.Payload `json:"payload"`
}

type messageProjection struct {
	Seq            int64                    `json:"seq"`
	Role           conversation.MessageRole `json:"role"`
	Content        string                   `json:"content"`
	CaseReferences []string                 `json:"caseReferences"`
	TaskReferences []string                 `json:"taskReferences"`
	Attachments    []attachmentProjection   `json:"attachments"`
	Citations      []citationProjection     `json:"citations"`
}

type attachmentProjection struct {
	AttachmentID  string `json:"attachmentId"`
	Purpose       string `json:"purpose"`
	ContentSHA256 string `json:"contentSha256"`
}

type citationProjection struct {
	SourceType    conversation.CitationSourceType `json:"sourceType"`
	SourceRef     string                          `json:"sourceRef"`
	ContentSHA256 string                          `json:"contentSha256"`
}

type modelRequest struct {
	Mode                  string                      `json:"mode"`
	PromptVersion         string                      `json:"promptVersion"`
	ConversationID        string                      `json:"conversationId"`
	Coverage              requestCoverage             `json:"coverage"`
	PreviousSnapshot      *previousSnapshotProjection `json:"previousSnapshot"`
	NewMessages           []messageProjection         `json:"newMessages"`
	KnownReportReferences []reportReferenceProjection `json:"knownReportReferences"`
	Attempt               int                         `json:"attempt"`
	RepairCode            string                      `json:"repairCode"`
}

type reportReferenceProjection struct {
	ReferenceID       string  `json:"referenceId"`
	SourceMessageSeqs []int64 `json:"sourceMessageSeqs"`
}

func compactionRequest(input conversationmemory.CompactionInput, promptVersion string) modelRequest {
	request := modelRequest{
		Mode: "initial", PromptVersion: promptVersion, ConversationID: input.ConversationID.String(),
		Coverage:              requestCoverage{FromSeq: input.FromSeq, ThroughSeq: input.ThroughSeq},
		NewMessages:           make([]messageProjection, 0, len(input.NewMessages)),
		KnownReportReferences: make([]reportReferenceProjection, 0, len(input.KnownReportReferences)),
		Attempt:               input.Attempt, RepairCode: input.RepairCode,
	}
	if input.PreviousSnapshot != nil {
		request.Mode = "incremental"
		request.PreviousSnapshot = &previousSnapshotProjection{
			SnapshotID: input.PreviousSnapshot.ID.String(),
			Coverage:   requestCoverage{FromSeq: input.PreviousSnapshot.FromSeq, ThroughSeq: input.PreviousSnapshot.ThroughSeq},
			Payload:    input.PreviousSnapshot.Payload,
		}
	}
	for _, message := range input.NewMessages {
		projection := messageProjection{
			Seq: message.Seq, Role: message.Role, Content: message.Content,
			CaseReferences: make([]string, 0, len(message.CaseReferences)),
			TaskReferences: make([]string, 0, len(message.TaskReferences)),
			Attachments:    make([]attachmentProjection, 0, len(message.Attachments)),
			Citations:      make([]citationProjection, 0, len(message.Citations)),
		}
		for _, reference := range message.CaseReferences {
			projection.CaseReferences = append(projection.CaseReferences, reference.ExternalCaseID.String())
		}
		for _, reference := range message.TaskReferences {
			projection.TaskReferences = append(projection.TaskReferences, reference.TaskID.String())
		}
		for _, attachment := range message.Attachments {
			projection.Attachments = append(projection.Attachments, attachmentProjection{
				AttachmentID: attachment.AttachmentID.String(), Purpose: attachment.Purpose,
				ContentSHA256: attachment.ContentSHA256,
			})
		}
		for _, citation := range message.Citations {
			projection.Citations = append(projection.Citations, citationProjection{
				SourceType: citation.SourceType, SourceRef: citation.SourceRef, ContentSHA256: citation.ContentSHA256,
			})
		}
		request.NewMessages = append(request.NewMessages, projection)
	}
	for reference, sourceSequences := range input.KnownReportReferences {
		sequences := append([]int64(nil), sourceSequences...)
		sort.Slice(sequences, func(i, j int) bool { return sequences[i] < sequences[j] })
		request.KnownReportReferences = append(request.KnownReportReferences, reportReferenceProjection{
			ReferenceID: reference, SourceMessageSeqs: sequences,
		})
	}
	sort.Slice(request.KnownReportReferences, func(i, j int) bool {
		return request.KnownReportReferences[i].ReferenceID < request.KnownReportReferences[j].ReferenceID
	})
	return request
}

func normalizeJSON(content string) (string, error) {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed, nil
	}
	lineEnd := strings.IndexByte(trimmed, '\n')
	if lineEnd < 0 || !strings.EqualFold(strings.TrimSpace(trimmed[:lineEnd]), "```json") {
		return "", conversationmemory.ErrInvalidPayloadSchema
	}
	remainder := strings.TrimSpace(trimmed[lineEnd+1:])
	closingLine := strings.LastIndexByte(remainder, '\n')
	if closingLine < 0 || strings.TrimSpace(remainder[closingLine+1:]) != "```" {
		return "", conversationmemory.ErrInvalidPayloadSchema
	}
	payload := strings.TrimSpace(remainder[:closingLine])
	if payload == "" || strings.Contains(payload, "```") {
		return "", conversationmemory.ErrInvalidPayloadSchema
	}
	return payload, nil
}
