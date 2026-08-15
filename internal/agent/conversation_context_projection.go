package agent

import (
	"encoding/json"
	"sort"

	"github.com/chitandabb/GoAgent/internal/conversation"

	"github.com/google/uuid"
)

// conversationContextProjection 是 turn_context 与历史 message_references 共用
// 的确定性 JSON 投影。encoding/json 默认开启 SetEscapeHTML，把 <、>、& 转义为
// 转义序列；字符串内的换行、引号也按 JSON 规则转义，因此恶意值永远不能闭合外层
// <turn_context>/<message_references> 块或注入额外结构。反引号不会被默认转义，
// 但它是 JSON 字符串内的普通字符，不会破坏结构。输出只包含安全白名单字段；
// 凭证、连接地址、对象存储坐标和原始附件内容永不进入投影。
type conversationContextProjection struct {
	Cases        []conversationCaseProjection       `json:"cases,omitempty"`
	Tasks        []conversationTaskProjection       `json:"tasks,omitempty"`
	Reports      []conversationReportProjection     `json:"reports,omitempty"`
	Attachments  []conversationAttachmentProjection `json:"attachments,omitempty"`
	DataSourceID string                             `json:"dataSourceId,omitempty"`
}

type conversationCaseProjection struct {
	ExternalCaseID string `json:"externalCaseId"`
	Kind           string `json:"kind"`
}

type conversationTaskProjection struct {
	TaskID string `json:"taskId"`
	Kind   string `json:"kind"`
}

type conversationReportProjection struct {
	ReferenceID string `json:"referenceId"`
}

type conversationAttachmentProjection struct {
	AttachmentID string `json:"attachmentId"`
	Name         string `json:"name"`
	MediaType    string `json:"mediaType"`
	Purpose      string `json:"purpose"`
	SizeBytes    int64  `json:"sizeBytes"`
	Status       string `json:"status"`
}

// projectionFromMessage 从消息引用构造确定性排序的投影。includeDataSource 仅对
// 当前轮 turn_context 为 true；历史 message_references 永不携带本轮数据源授权。
func projectionFromMessage(
	message conversation.Message,
	dataSourceID uuid.UUID,
	includeDataSource bool,
) conversationContextProjection {
	projection := conversationContextProjection{}

	cases := append([]conversation.CaseReference(nil), message.CaseReferences...)
	sort.Slice(cases, func(i, j int) bool {
		return cases[i].ExternalCaseID.String() < cases[j].ExternalCaseID.String()
	})
	for _, reference := range cases {
		projection.Cases = append(projection.Cases, conversationCaseProjection{
			ExternalCaseID: reference.ExternalCaseID.String(), Kind: string(reference.Kind),
		})
	}

	tasks := append([]conversation.TaskReference(nil), message.TaskReferences...)
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].TaskID.String() < tasks[j].TaskID.String()
	})
	for _, reference := range tasks {
		projection.Tasks = append(projection.Tasks, conversationTaskProjection{
			TaskID: reference.TaskID.String(), Kind: string(reference.Kind),
		})
	}

	reports := append([]conversation.ReportReference(nil), message.ReportReferences...)
	sort.Slice(reports, func(i, j int) bool {
		return reports[i].ReferenceID < reports[j].ReferenceID
	})
	for _, reference := range reports {
		projection.Reports = append(projection.Reports, conversationReportProjection{
			ReferenceID: reference.ReferenceID,
		})
	}

	attachments := append([]conversation.MessageAttachment(nil), message.Attachments...)
	sort.Slice(attachments, func(i, j int) bool {
		return attachments[i].AttachmentID.String() < attachments[j].AttachmentID.String()
	})
	for _, reference := range attachments {
		projection.Attachments = append(projection.Attachments, conversationAttachmentProjection{
			AttachmentID: reference.AttachmentID.String(), Name: reference.OriginalName,
			MediaType: reference.MediaType, Purpose: reference.Purpose,
			SizeBytes: reference.SizeBytes, Status: reference.Status,
		})
	}

	if includeDataSource && dataSourceID != uuid.Nil {
		projection.DataSourceID = dataSourceID.String()
	}
	return projection
}

// render 把非空投影渲染成外层标签包裹的单块 JSON；空投影返回空字符串（调用方
// 不得追加空块）。
func (p conversationContextProjection) render(openTag, closeTag string) string {
	if p.isEmpty() {
		return ""
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	return openTag + "\n" + string(encoded) + "\n" + closeTag
}

func (p conversationContextProjection) isEmpty() bool {
	return len(p.Cases) == 0 && len(p.Tasks) == 0 && len(p.Reports) == 0 &&
		len(p.Attachments) == 0 && p.DataSourceID == ""
}

// renderConversationTurnContext 渲染追加到当前 user 原文尾部的本轮 turn_context
// （原文 + 换行 + 块）。空引用且无授权数据源时返回空字符串。
func renderConversationTurnContext(
	message conversation.Message,
	sqlDataSourceID uuid.UUID,
	sqlAuthorized bool,
) string {
	projection := projectionFromMessage(message, sqlDataSourceID, sqlAuthorized)
	return projection.render("<turn_context>", "</turn_context>")
}

// renderConversationMessageReferences 渲染历史消息各自已持久化引用的
// message_references 块；永不携带本轮数据源授权。
func renderConversationMessageReferences(message conversation.Message) string {
	projection := projectionFromMessage(message, uuid.Nil, false)
	return projection.render("<message_references>", "</message_references>")
}
