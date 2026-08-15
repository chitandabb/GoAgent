package agent

import (
	"context"
	"errors"
	"time"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/externalcase"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/google/uuid"
)

type ExternalCaseGetter interface {
	Get(ctx context.Context, id uuid.UUID) (*externalcase.ExternalCase, error)
}

type readExternalCaseInput struct {
	ExternalCaseID string `json:"externalCaseId" jsonschema:"required" jsonschema_description:"MESGuard 中的外部工单 UUID"`
}

type externalCaseEvidence struct {
	ExternalCaseID    string                          `json:"externalCaseId"`
	ExternalCaseKey   string                          `json:"externalCaseKey"`
	CaseType          string                          `json:"caseType"`
	Title             string                          `json:"title"`
	Description       string                          `json:"description"`
	Category          string                          `json:"category"`
	Module            string                          `json:"module"`
	Status            externalcase.Status             `json:"status"`
	Priority          externalcase.Priority           `json:"priority"`
	ReportedAt        time.Time                       `json:"reportedAt"`
	SourceUpdatedAt   time.Time                       `json:"sourceUpdatedAt"`
	Product           externalcase.ProductContext     `json:"product"`
	Production        externalcase.ProductionContext  `json:"production"`
	Environment       externalcase.EnvironmentContext `json:"environment"`
	Attributes        map[string]any                  `json:"attributes"`
	Attachments       []attachmentEvidence            `json:"attachments"`
	SourceFingerprint string                          `json:"sourceFingerprint"`
	Truncated         bool                            `json:"truncated"`
}

type attachmentEvidence struct {
	ExternalAttachmentKey string `json:"externalAttachmentKey"`
	FileName              string `json:"fileName"`
	MediaType             string `json:"mediaType"`
	SizeBytes             int64  `json:"sizeBytes"`
	ContentHash           string `json:"contentHash"`
}

func NewReadExternalCaseTool(getter ExternalCaseGetter) (tool.InvokableTool, error) {
	if getter == nil {
		return nil, errors.New("external case getter is nil")
	}
	return toolutils.InferTool(
		ToolReadExternalCase,
		"读取指定 ERP 工单的标准化只读证据；返回内容不包含对象存储路径或访问凭证",
		func(ctx context.Context, input readExternalCaseInput) (externalCaseEvidence, error) {
			id, err := uuid.Parse(input.ExternalCaseID)
			if err != nil {
				return externalCaseEvidence{}, errors.New("externalCaseId must be a valid UUID")
			}
			// Conversation 运行时：externalCaseId 必须在本轮 RunAccess 的
			// ExternalCaseIDs Grant 中，否则在 getter.Get 前拒绝（未授权零调用）。
			if err := requireConversationResourceGrant(ctx, func(grants agentruntime.ResourceGrants) bool {
				return grants.AllowsExternalCase(id)
			}); err != nil {
				return externalCaseEvidence{}, err
			}
			item, err := getter.Get(ctx, id)
			if err != nil {
				return externalCaseEvidence{}, err
			}
			attachments := make([]attachmentEvidence, 0, len(item.Attachments))
			for _, attachment := range item.Attachments {
				attachments = append(attachments, attachmentEvidence{
					ExternalAttachmentKey: attachment.ExternalAttachmentKey,
					FileName:              attachment.FileName,
					MediaType:             attachment.MediaType,
					SizeBytes:             attachment.SizeBytes,
					ContentHash:           attachment.ContentHash,
				})
			}
			return externalCaseEvidence{
				ExternalCaseID: item.ID.String(), ExternalCaseKey: item.ExternalCaseKey,
				CaseType: item.CaseType, Title: item.Title, Description: item.Description,
				Category: item.Category, Module: item.Module, Status: item.Status, Priority: item.Priority,
				ReportedAt: item.ReportedAt, SourceUpdatedAt: item.SourceUpdatedAt,
				Product: item.Product, Production: item.Production, Environment: item.Environment,
				Attributes: item.Attributes, Attachments: attachments,
				SourceFingerprint: item.SourceFingerprint, Truncated: item.Truncated,
			}, nil
		},
	)
}
