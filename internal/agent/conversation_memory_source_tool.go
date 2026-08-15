package agent

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/conversationmemory"
	"github.com/chitandabb/GoAgent/internal/resilience"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/google/uuid"
)

const ToolReadConversationMemorySources = "read_conversation_memory_sources"

type ConversationMemorySourceReader interface {
	Read(context.Context, conversationmemory.SourceReadRequest) (conversationmemory.SourceReadResult, error)
}

type conversationMemorySourceInput struct {
	EntryID            string  `json:"entryId,omitempty" jsonschema_description:"当前 Active Snapshot 中尚未 superseded 的 Entry ID"`
	SourceMessageSeqs  []int64 `json:"sourceMessageSeqs,omitempty" jsonschema_description:"当前 Active Snapshot Entry 已声明的有序、去重源消息序号"`
	ContinuationCursor string  `json:"continuationCursor,omitempty" jsonschema_description:"本次 Run 内上一次调用返回的 continuation cursor"`
	Query              string  `json:"query,omitempty" jsonschema_description:"在已授权原始消息内定位相关窗口的查询，最多 256 个 Unicode 字符"`
	ContentOffsetRunes *int    `json:"contentOffsetRunes,omitempty" jsonschema_description:"单条已授权源消息的 Rune 起始偏移；仅可与一个 sourceMessageSeqs 一起使用"`
}

func NewConversationMemorySourceTool(reader ConversationMemorySourceReader) (tool.InvokableTool, error) {
	if reader == nil {
		return nil, errors.New("conversation memory source reader is nil")
	}
	return toolutils.InferTool(
		ToolReadConversationMemorySources,
		"按需恢复当前会话 Active Snapshot 授权的有界原始消息；优先用 query 定位相关窗口，必要时可读取单条消息的明确 Rune 偏移",
		func(ctx context.Context, input conversationMemorySourceInput) (conversationmemory.SourceReadResult, error) {
			commandContext, ok := conversation.CommandContextFromContext(ctx)
			if !ok || commandContext.Actor.UserID == uuid.Nil || commandContext.ConversationID == uuid.Nil ||
				commandContext.UserMessageID == uuid.Nil {
				return conversationmemory.SourceReadResult{}, conversation.ErrCommandContextRequired
			}
			// 执行期授权只来自 RunAccess：memory.read Permission 必须存在，
			// Actor 必须与命令上下文一致。
			access, ok := agentruntime.RunAccessFromContext(ctx)
			if !ok || !access.Allows(agentruntime.PermissionMemoryRead) {
				return conversationmemory.SourceReadResult{}, ErrRunAccessRequired
			}
			if access.Actor().UserID != commandContext.Actor.UserID {
				return conversationmemory.SourceReadResult{}, ErrRunAccessRequired
			}
			input.EntryID = strings.TrimSpace(input.EntryID)
			input.ContinuationCursor = strings.TrimSpace(input.ContinuationCursor)
			input.Query = strings.TrimSpace(input.Query)
			selectors := 0
			if input.EntryID != "" {
				selectors++
			}
			if len(input.SourceMessageSeqs) > 0 {
				selectors++
			}
			if input.ContinuationCursor != "" {
				selectors++
			}
			if selectors != 1 || utf8.RuneCountInString(input.Query) > 256 ||
				(input.ContentOffsetRunes != nil && *input.ContentOffsetRunes < 0) ||
				(input.ContinuationCursor != "" && (input.Query != "" || input.ContentOffsetRunes != nil)) ||
				(input.Query != "" && input.ContentOffsetRunes != nil) ||
				(input.ContentOffsetRunes != nil &&
					(input.EntryID != "" || len(input.SourceMessageSeqs) != 1)) {
				return conversationmemory.SourceReadResult{}, conversationmemory.ErrInvalidSourceRead
			}
			result, err := reader.Read(ctx, conversationmemory.SourceReadRequest{
				Actor: commandContext.Actor, ConversationID: commandContext.ConversationID,
				EntryID: input.EntryID, SourceMessageSeqs: input.SourceMessageSeqs,
				ContinuationCursor: input.ContinuationCursor, Query: input.Query,
				ContentOffsetRunes: input.ContentOffsetRunes,
			})
			if err != nil {
				return conversationmemory.SourceReadResult{}, err
			}
			if remaining, limited := agentToolRunPolicyFromContext(ctx).remaining(
				ToolReadConversationMemorySources,
			); result.HasMore && limited && remaining == 0 {
				result.ContinuationCursor = ""
				result.ContinuationAvailable = false
				result.TruncatedByTurnBudget = true
			}
			return result, nil
		},
	)
}

func NewConversationMemorySourceToolRegistration(
	reader ConversationMemorySourceReader,
) (ToolRegistration, error) {
	current, err := NewConversationMemorySourceTool(reader)
	if err != nil {
		return ToolRegistration{}, err
	}
	return ToolRegistration{
		Tool: current, FailurePolicy: resilience.PolicyBestEffort,
		RequiredPermissions: []agentruntime.Permission{agentruntime.PermissionMemoryRead},
	}, nil
}
