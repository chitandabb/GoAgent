package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/diagnosis"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/google/uuid"
)

const ToolCreateDiagnosisTask = "create_diagnosis_task"

type DiagnosisTaskCreator interface {
	CreateDiagnosisTask(ctx context.Context, input conversation.CreateDiagnosisInput) (conversation.CreateDiagnosisResult, error)
}

type createDiagnosisTaskInput struct {
	ExternalCaseID string   `json:"externalCaseId" jsonschema:"required" jsonschema_description:"当前会话 selected case reference 对应的 MESGuard 工单 UUID"`
	DiagnosisGoal  string   `json:"diagnosisGoal" jsonschema:"required" jsonschema_description:"用户明确要求执行的诊断目标；不要填充系统权限、工具或预算"`
	AttachmentIDs  []string `json:"attachmentIds,omitempty" jsonschema_description:"可选；只能选择当前用户消息 attachments 中的 UUID；省略时后端冻结当前消息全部附件"`
	ParentTaskID   string   `json:"parentTaskId,omitempty" jsonschema_description:"可选的同一用户已有任务 UUID，用于补充证据后的 follow-up"`
}

type createDiagnosisTaskResponse struct {
	TaskID   string               `json:"taskId"`
	Status   diagnosis.TaskStatus `json:"status"`
	Replayed bool                 `json:"replayed"`
}

// NewCreateDiagnosisTaskTool creates the only model-visible side-effecting
// command in the conversation Agent. It deliberately depends on a service
// that receives actor/message context from the server, not model arguments.
func NewCreateDiagnosisTaskTool(creator DiagnosisTaskCreator) (tool.InvokableTool, error) {
	if creator == nil {
		return nil, errors.New("diagnosis task creator is nil")
	}
	return toolutils.InferTool(
		ToolCreateDiagnosisTask,
		"根据当前用户最新消息和唯一 selected 工单引用创建异步诊断任务；不会直接修改 ERP，不接受 fingerprint、权限、工具、预算、队列或幂等键参数",
		func(ctx context.Context, input createDiagnosisTaskInput) (createDiagnosisTaskResponse, error) {
			caseID, err := uuid.Parse(strings.TrimSpace(input.ExternalCaseID))
			if err != nil {
				return createDiagnosisTaskResponse{}, errors.New("externalCaseId must be a valid UUID")
			}
			goal := strings.TrimSpace(input.DiagnosisGoal)
			if goal == "" || goal != input.DiagnosisGoal {
				return createDiagnosisTaskResponse{}, errors.New("diagnosisGoal must be non-empty and trimmed")
			}
			attachments := make([]uuid.UUID, 0, len(input.AttachmentIDs))
			for _, rawID := range input.AttachmentIDs {
				attachmentID, parseErr := uuid.Parse(strings.TrimSpace(rawID))
				if parseErr != nil {
					return createDiagnosisTaskResponse{}, errors.New("attachmentIds must contain valid UUIDs")
				}
				attachments = append(attachments, attachmentID)
			}
			var parentTaskID *uuid.UUID
			if rawID := strings.TrimSpace(input.ParentTaskID); rawID != "" {
				parsed, parseErr := uuid.Parse(rawID)
				if parseErr != nil {
					return createDiagnosisTaskResponse{}, errors.New("parentTaskId must be a valid UUID")
				}
				parentTaskID = &parsed
			}
			result, err := creator.CreateDiagnosisTask(ctx, conversation.CreateDiagnosisInput{
				ExternalCaseID: caseID, DiagnosisGoal: goal, AttachmentIDs: attachments, ParentTaskID: parentTaskID,
			})
			if err != nil {
				return createDiagnosisTaskResponse{}, fmt.Errorf("create diagnosis task command: %w", err)
			}
			return createDiagnosisTaskResponse{TaskID: result.Task.ID.String(), Status: result.Task.Status, Replayed: result.Replayed}, nil
		},
	)
}
