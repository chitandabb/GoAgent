package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/attachment"
	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/google/uuid"
)

const ToolReadAttachment = "read_attachment"

type readAttachmentInput struct {
	AttachmentID string `json:"attachmentId" jsonschema:"required" jsonschema_description:"当前用户消息或诊断任务上下文中明确列出的附件 UUID"`
}

type readAttachmentElement struct {
	Index       int      `json:"index"`
	PageNumber  *int     `json:"pageNumber,omitempty"`
	ElementType string   `json:"elementType"`
	SectionPath []string `json:"sectionPath,omitempty"`
	ContentText string   `json:"contentText"`
}

type readAttachmentResponse struct {
	SourceType       string                  `json:"sourceType"`
	SourceRef        string                  `json:"sourceRef"`
	AttachmentID     string                  `json:"attachmentId"`
	OriginalName     string                  `json:"originalName"`
	MediaType        string                  `json:"mediaType"`
	SizeBytes        int64                   `json:"sizeBytes"`
	ContentSHA256    string                  `json:"contentSha256"`
	ParserVersion    string                  `json:"parserVersion"`
	Elements         []readAttachmentElement `json:"elements"`
	VisualAssetCount int                     `json:"visualAssetCount"`
	Truncated        bool                    `json:"truncated"`
}

type diagnosisAttachmentContextKey struct{}

func WithDiagnosisAttachmentContext(ctx context.Context, taskID uuid.UUID) context.Context {
	return context.WithValue(ctx, diagnosisAttachmentContextKey{}, taskID)
}

func diagnosisAttachmentTaskID(ctx context.Context) (uuid.UUID, bool) {
	taskID, ok := ctx.Value(diagnosisAttachmentContextKey{}).(uuid.UUID)
	return taskID, ok && taskID != uuid.Nil
}

func NewReadAttachmentTool(reader attachment.Reader) (tool.InvokableTool, error) {
	if reader == nil {
		return nil, errors.New("attachment reader is nil")
	}
	return toolutils.InferTool(
		ToolReadAttachment,
		"按需读取当前用户消息或诊断任务已冻结的文件；后端按运行上下文校验归属，只返回有界文本元素与视觉内容标记，不返回对象存储地址或二进制内容",
		func(ctx context.Context, input readAttachmentInput) (readAttachmentResponse, error) {
			attachmentID, err := uuid.Parse(strings.TrimSpace(input.AttachmentID))
			if err != nil {
				return readAttachmentResponse{}, errors.New("attachmentId must be a valid UUID")
			}
			// 运行时通用资源边界：attachmentId 必须在本轮 RunAccess 的
			// AttachmentIDs Grant 中（Conversation 与 Diagnosis 一致）；
			// 现有 CommandContext/任务归属校验作为第二层。
			if err := requireRuntimeResourceGrant(ctx, func(grants agentruntime.ResourceGrants) bool {
				return grants.AllowsAttachment(attachmentID)
			}); err != nil {
				return readAttachmentResponse{}, err
			}
			var result attachment.ReadResult
			if commandContext, ok := conversation.CommandContextFromContext(ctx); ok &&
				commandContext.Actor.UserID != uuid.Nil && commandContext.ConversationID != uuid.Nil &&
				commandContext.UserMessageID != uuid.Nil {
				result, err = reader.ReadForMessage(
					ctx, commandContext.Actor.UserID, commandContext.ConversationID,
					commandContext.UserMessageID, attachmentID, attachment.DefaultReadRunes,
				)
			} else if taskID, ok := diagnosisAttachmentTaskID(ctx); ok {
				// Diagnosis 任务附件：Actor 来自权威 v2 RunAccess。
				access, accessOK := agentruntime.RunAccessFromContext(ctx)
				if !accessOK || access.Actor().UserID == uuid.Nil {
					return readAttachmentResponse{}, ErrRunAccessRequired
				}
				result, err = reader.ReadForTask(ctx, access.Actor().UserID, taskID, attachmentID, attachment.DefaultReadRunes)
			} else {
				return readAttachmentResponse{}, conversation.ErrCommandContextRequired
			}
			if err != nil {
				if errors.Is(err, attachment.ErrAttachmentForbidden) {
					return readAttachmentResponse{}, errors.New("attachment is not available in the current run context")
				}
				return readAttachmentResponse{}, fmt.Errorf("read attachment: %w", err)
			}
			elements := make([]readAttachmentElement, 0, len(result.Elements))
			for _, element := range result.Elements {
				elements = append(elements, readAttachmentElement{
					Index: element.Index, PageNumber: element.PageNumber, ElementType: element.ElementType,
					SectionPath: element.SectionPath, ContentText: element.ContentText,
				})
			}
			return readAttachmentResponse{
				SourceType: "attachment", SourceRef: "attachment:" + result.Attachment.ID.String(),
				AttachmentID: result.Attachment.ID.String(), OriginalName: result.Attachment.Ref.OriginalName,
				MediaType: result.Attachment.Ref.MediaType, SizeBytes: result.Attachment.Ref.SizeBytes,
				ContentSHA256: result.Attachment.Ref.SHA256, ParserVersion: result.ParserVersion,
				Elements: elements, VisualAssetCount: result.VisualAssetCount, Truncated: result.Truncated,
			}, nil
		},
	)
}
