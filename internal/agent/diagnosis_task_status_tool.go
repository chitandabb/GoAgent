package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/diagnosis"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/google/uuid"
)

const ToolGetDiagnosisTaskStatus = "get_diagnosis_task_status"

type DiagnosisTaskStatusReader interface {
	GetDiagnosisTaskStatus(ctx context.Context, taskID uuid.UUID) (conversation.DiagnosisTaskStatusResult, error)
}

type getDiagnosisTaskStatusInput struct {
	TaskID string `json:"taskId" jsonschema:"required" jsonschema_description:"当前用户消息 referenced 或 created task reference 对应的诊断任务 UUID"`
}

type getDiagnosisTaskStatusResponse struct {
	TaskID           string               `json:"taskId"`
	Status           diagnosis.TaskStatus `json:"status"`
	Terminal         bool                 `json:"terminal"`
	AttemptCount     int                  `json:"attemptCount"`
	ReportAvailable  bool                 `json:"reportAvailable"`
	ReportID         string               `json:"reportId,omitempty"`
	LastErrorCode    string               `json:"lastErrorCode,omitempty"`
	LastErrorMessage string               `json:"lastErrorMessage,omitempty"`
	StartedAt        string               `json:"startedAt,omitempty"`
	CompletedAt      string               `json:"completedAt,omitempty"`
	UpdatedAt        string               `json:"updatedAt"`
}

// NewGetDiagnosisTaskStatusTool exposes an authorization-preserving task
// summary. Reference validation and owner/admin checks stay in application
// services instead of trusting model arguments.
func NewGetDiagnosisTaskStatusTool(reader DiagnosisTaskStatusReader) (tool.InvokableTool, error) {
	if reader == nil {
		return nil, errors.New("diagnosis task status reader is nil")
	}
	return toolutils.InferTool(
		ToolGetDiagnosisTaskStatus,
		"查询当前用户消息明确引用的异步诊断任务状态、尝试次数、失败摘要和报告可用性；不返回模型推理过程，也不能估算百分比或完成时间",
		func(ctx context.Context, input getDiagnosisTaskStatusInput) (getDiagnosisTaskStatusResponse, error) {
			taskID, err := uuid.Parse(strings.TrimSpace(input.TaskID))
			if err != nil {
				return getDiagnosisTaskStatusResponse{}, errors.New("taskId must be a valid UUID")
			}
			// Conversation 运行时：taskId 必须在本轮 RunAccess 的 TaskIDs
			// Grant 中；服务端 owner/admin 校验保留为第二层。
			if err := requireRuntimeResourceGrant(ctx, func(grants agentruntime.ResourceGrants) bool {
				return grants.AllowsTask(taskID)
			}); err != nil {
				return getDiagnosisTaskStatusResponse{}, err
			}
			result, err := reader.GetDiagnosisTaskStatus(ctx, taskID)
			if err != nil {
				return getDiagnosisTaskStatusResponse{}, fmt.Errorf("get diagnosis task status: %w", err)
			}
			task := result.Task
			response := getDiagnosisTaskStatusResponse{
				TaskID: task.ID.String(), Status: task.Status, Terminal: task.Status.IsTerminal(),
				AttemptCount: task.AttemptCount, ReportAvailable: task.ReportID != nil,
				LastErrorCode: task.LastErrorCode, LastErrorMessage: task.LastErrorMessage,
				UpdatedAt: formatTaskStatusTime(task.UpdatedAt),
			}
			if task.ReportID != nil {
				response.ReportID = task.ReportID.String()
				conversationReportReferenceTraceFromContext(ctx).append("report:" + task.ReportID.String())
			}
			if task.StartedAt != nil {
				response.StartedAt = formatTaskStatusTime(*task.StartedAt)
			}
			if task.CompletedAt != nil {
				response.CompletedAt = formatTaskStatusTime(*task.CompletedAt)
			}
			return response, nil
		},
	)
}

func formatTaskStatusTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
