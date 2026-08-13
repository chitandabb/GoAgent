package agent

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/contextgovernance"
	"github.com/chitandabb/GoAgent/internal/resilience"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
)

const (
	ToolReadConversationToolResult           = "read_conversation_tool_result"
	maxConversationToolResultArtifactBytes   = 1024 * 1024
	maxConversationToolResultStoreTotalBytes = 8 * 1024 * 1024
	maxConversationToolResultReadBytes       = 4096
)

var ErrConversationToolResultStorageExceeded = errors.New("conversation Tool result storage limit exceeded")

type conversationToolResultStoreContextKey struct{}

type conversationToolResultStore struct {
	mu             sync.RWMutex
	entries        map[string]string
	maxEntries     int
	maxReadBytes   int
	totalBytes     int
	maxTotalBytes  int
	maxArtifactLen int
}

func newConversationToolResultStore(maxEntries, modelResultBytes int) *conversationToolResultStore {
	maxReadBytes := (modelResultBytes - 512) / 6
	if maxReadBytes < 64 {
		maxReadBytes = 64
	}
	if maxReadBytes > maxConversationToolResultReadBytes {
		maxReadBytes = maxConversationToolResultReadBytes
	}
	return &conversationToolResultStore{
		entries: make(map[string]string), maxEntries: maxEntries, maxReadBytes: maxReadBytes,
		maxTotalBytes:  maxConversationToolResultStoreTotalBytes,
		maxArtifactLen: maxConversationToolResultArtifactBytes,
	}
}

func withConversationToolResultStore(ctx context.Context, store *conversationToolResultStore) context.Context {
	if store == nil {
		return ctx
	}
	return context.WithValue(ctx, conversationToolResultStoreContextKey{}, store)
}

func conversationToolResultStoreFromContext(ctx context.Context) *conversationToolResultStore {
	store, _ := ctx.Value(conversationToolResultStoreContextKey{}).(*conversationToolResultStore)
	return store
}

func (s *conversationToolResultStore) put(result string) (string, error) {
	if s == nil || s.maxEntries < 1 || s.maxReadBytes < 1 || !utf8.ValidString(result) ||
		len(result) < 1 || len(result) > s.maxArtifactLen {
		return "", ErrConversationToolResultStorageExceeded
	}
	digest := sha256.Sum256([]byte(result))
	ref := fmt.Sprintf("sha256:%x", digest)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.entries[ref]; exists {
		return ref, nil
	}
	if len(s.entries) >= s.maxEntries || s.totalBytes+len(result) > s.maxTotalBytes {
		return "", ErrConversationToolResultStorageExceeded
	}
	s.entries[ref] = result
	s.totalBytes += len(result)
	return ref, nil
}

type conversationToolResultChunk struct {
	Ref             string `json:"ref"`
	OffsetBytes     int    `json:"offsetBytes"`
	NextOffsetBytes int    `json:"nextOffsetBytes"`
	TotalBytes      int    `json:"totalBytes"`
	Content         string `json:"content"`
	Truncated       bool   `json:"truncated"`
}

func (s *conversationToolResultStore) read(ref string, offsetBytes, maxBytes int) (conversationToolResultChunk, error) {
	ref = strings.ToLower(strings.TrimSpace(ref))
	if s == nil || !strings.HasPrefix(ref, "sha256:") ||
		!contextgovernance.IsSHA256Hex(strings.TrimPrefix(ref, "sha256:")) ||
		offsetBytes < 0 || maxBytes < 1 {
		return conversationToolResultChunk{}, errors.New("conversation Tool result read request is invalid")
	}
	s.mu.RLock()
	result, exists := s.entries[ref]
	readLimit := s.maxReadBytes
	s.mu.RUnlock()
	if !exists {
		return conversationToolResultChunk{}, errors.New("conversation Tool result is unavailable in the current run")
	}
	if offsetBytes > len(result) || !utf8.ValidString(result[offsetBytes:]) {
		return conversationToolResultChunk{}, errors.New("conversation Tool result offset is invalid")
	}
	if maxBytes > readLimit {
		maxBytes = readLimit
	}
	end := offsetBytes + maxBytes
	if end > len(result) {
		end = len(result)
	}
	for end > offsetBytes && !utf8.ValidString(result[offsetBytes:end]) {
		end--
	}
	return conversationToolResultChunk{
		Ref: ref, OffsetBytes: offsetBytes, NextOffsetBytes: end, TotalBytes: len(result),
		Content: result[offsetBytes:end], Truncated: end < len(result),
	}, nil
}

type readConversationToolResultInput struct {
	Ref         string `json:"ref" jsonschema:"required" jsonschema_description:"Tool 返回的 sha256 引用"`
	OffsetBytes int    `json:"offsetBytes,omitempty" jsonschema_description:"从上一响应 nextOffsetBytes 继续，默认 0"`
	MaxBytes    int    `json:"maxBytes,omitempty" jsonschema_description:"本次读取字节数，默认 1024，服务端会按运行预算收紧"`
}

func NewReadConversationToolResultTool() (tool.InvokableTool, error) {
	return toolutils.InferTool(
		ToolReadConversationToolResult,
		"仅按需分页读取当前 Conversation Agent Run 中被截断的 Tool 结果；只能使用本轮返回的 sha256 引用，不能跨 Run、跨会话或搜索其他结果",
		func(ctx context.Context, input readConversationToolResultInput) (conversationToolResultChunk, error) {
			if input.MaxBytes == 0 {
				input.MaxBytes = 1024
			}
			store := conversationToolResultStoreFromContext(ctx)
			if store == nil {
				return conversationToolResultChunk{}, errors.New("conversation Tool result store is unavailable")
			}
			return store.read(input.Ref, input.OffsetBytes, input.MaxBytes)
		},
	)
}

func NewConversationToolResultRegistration() (ToolRegistration, error) {
	reader, err := NewReadConversationToolResultTool()
	if err != nil {
		return ToolRegistration{}, err
	}
	return ToolRegistration{
		Tool:             reader,
		FailurePolicy:    resilience.PolicyBestEffort,
		AllowedRoles:     []auth.Role{auth.RoleAnalyst, auth.RoleAdmin},
		AllowedTaskTypes: []TaskType{TaskTypeConversation},
	}, nil
}
