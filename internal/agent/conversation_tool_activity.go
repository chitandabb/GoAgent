package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversation"
)

const conversationActivityWriteTimeout = 2 * time.Second

var (
	conversationSQLStringLiteral = regexp.MustCompile(`'(?:''|[^'])*'`)
	conversationSQLNumberLiteral = regexp.MustCompile(`\b\d+(?:\.\d+)?\b`)
)

func conversationToolDisplayName(toolName string) string {
	if label, ok := map[string]string{
		ToolSearchKnowledge:               "检索企业知识库",
		ToolWebSearch:                     "搜索公开网络",
		ToolFetchPublicPage:               "读取公开网页",
		ToolReadAttachment:                "读取会话附件",
		ToolReadExternalCase:              "读取工单信息",
		ToolCreateDiagnosisTask:           "创建排查任务",
		ToolGetDiagnosisTaskStatus:        "查询排查进度",
		ToolSearchSchemaCatalog:           "检索数据目录",
		ToolExecuteReadonlyQuery:          "查询业务数据",
		ToolDatabaseObjectDefinition:      "读取数据对象定义",
		ToolSkill:                         "加载排查指南",
		ToolReadSkillReference:            "读取排查参考资料",
		ToolReadConversationMemorySources: "补读会话记录",
		ToolReadConversationToolResult:    "继续读取工具结果",
		"search_repositories":             "搜索代码仓库",
		"search_code":                     "搜索代码",
		"get_repository_tree":             "读取仓库目录",
		"get_file_contents":               "读取代码文件",
		"list_commits":                    "查询提交记录",
		"get_commit":                      "读取提交详情",
	}[toolName]; ok {
		return label
	}
	return "使用系统工具"
}

func conversationToolInputSummary(toolName, raw string) string {
	payload := decodeActivityObject(raw)
	switch toolName {
	case ToolSearchKnowledge:
		return quotedActivity("检索", stringField(payload, "query"))
	case ToolWebSearch:
		return quotedActivity("搜索", stringField(payload, "query"))
	case ToolFetchPublicPage:
		return "读取搜索结果中选定的公开网页"
	case ToolExecuteReadonlyQuery:
		if query := stringField(payload, "query"); query != "" {
			return boundedActivityText("执行只读 SQL："+redactConversationSQL(query), 700)
		}
	case ToolSearchSchemaCatalog:
		return quotedActivity("检索字段或数据对象", stringField(payload, "keyword"))
	case ToolDatabaseObjectDefinition:
		objectName := strings.Trim(strings.Join([]string{
			stringField(payload, "schema"), stringField(payload, "objectName"),
		}, "."), ".")
		return quotedActivity("读取数据对象", objectName)
	case ToolReadSkillReference:
		return boundedActivityText("读取排查参考资料："+strings.Trim(strings.Join([]string{
			stringField(payload, "skill"), stringField(payload, "path"),
		}, " · "), " ·"), 700)
	case ToolCreateDiagnosisTask:
		return quotedActivity("排查目标", stringField(payload, "diagnosisGoal"))
	case ToolReadConversationMemorySources:
		if query := stringField(payload, "query"); query != "" {
			return quotedActivity("定位历史信息", query)
		}
		return "读取与当前问题相关的历史消息"
	case ToolReadAttachment:
		return "读取当前消息中已授权的附件"
	case ToolReadExternalCase:
		return "读取当前会话关联的工单"
	case ToolGetDiagnosisTaskStatus:
		return "查询当前会话关联任务的状态"
	case ToolReadConversationToolResult:
		return "继续读取上一工具被截断的结果"
	}
	parts := make([]string, 0, 4)
	for _, key := range []string{"query", "repository", "repo", "path"} {
		if value := stringField(payload, key); value != "" {
			parts = append(parts, value)
		}
	}
	if len(parts) > 0 {
		return boundedActivityText(strings.Join(parts, " · "), 700)
	}
	return "按当前会话授权范围执行"
}

func conversationToolOutputSummary(toolName, raw string, callErr error) string {
	if callErr != nil {
		return "调用失败，未取得可用结果"
	}
	value := decodeActivityValue(raw)
	switch toolName {
	case ToolSearchKnowledge:
		object, _ := value.(map[string]any)
		results := arrayField(object, "results")
		return resultCollectionSummary("找到", "个知识片段", results, "title")
	case ToolWebSearch:
		object, _ := value.(map[string]any)
		results := arrayField(object, "results")
		return resultCollectionSummary("找到", "个公开网页", results, "title", "domain")
	case ToolFetchPublicPage:
		object, _ := value.(map[string]any)
		title, domain := stringField(object, "title"), stringField(object, "domain")
		if title != "" || domain != "" {
			return boundedActivityText("已读取 "+strings.Trim(strings.Join([]string{title, domain}, " · "), " ·"), 900)
		}
	case ToolExecuteReadonlyQuery:
		object, _ := value.(map[string]any)
		if ok, exists := object["ok"].(bool); exists && !ok {
			return "查询被只读策略拦截，未读取业务数据"
		}
		rows := intField(object, "returnedRows")
		columns := stringArrayField(object, "columns")
		summary := fmt.Sprintf("返回 %d 行", rows)
		if len(columns) > 0 {
			summary += "，字段：" + strings.Join(limitStrings(columns, 6), "、")
		}
		if truncated, _ := object["truncated"].(bool); truncated {
			summary += "（结果已截断）"
		}
		return boundedActivityText(summary, 900)
	case ToolSearchSchemaCatalog:
		results, _ := value.([]any)
		return resultCollectionSummary("找到", "条数据目录记录", results, "objectName", "columnName")
	case ToolReadAttachment:
		object, _ := value.(map[string]any)
		name := stringField(object, "originalName")
		count := len(arrayField(object, "elements"))
		if name != "" {
			return boundedActivityText(fmt.Sprintf("已读取 %s，共 %d 个文本片段", name, count), 900)
		}
	case ToolCreateDiagnosisTask:
		object, _ := value.(map[string]any)
		status := stringField(object, "status")
		if status != "" {
			return "排查任务已创建，当前状态：" + status
		}
	case ToolGetDiagnosisTaskStatus:
		object, _ := value.(map[string]any)
		status := stringField(object, "status")
		if status != "" {
			return "当前排查状态：" + status
		}
	case ToolReadExternalCase:
		object, _ := value.(map[string]any)
		title := firstStringField(object, "title", "externalCaseKey", "caseKey")
		if title != "" {
			return boundedActivityText("已读取工单："+title, 900)
		}
	case ToolReadConversationMemorySources:
		object, _ := value.(map[string]any)
		for _, key := range []string{"messages", "items", "sources"} {
			if items := arrayField(object, key); items != nil {
				return fmt.Sprintf("已恢复 %d 条相关历史记录", len(items))
			}
		}
		return "已恢复与当前问题相关的历史信息"
	case ToolReadConversationToolResult:
		object, _ := value.(map[string]any)
		if total := intField(object, "totalBytes"); total > 0 {
			return fmt.Sprintf("已继续读取工具结果，共 %d 字节", total)
		}
	}
	if items, ok := value.([]any); ok {
		return fmt.Sprintf("返回 %d 条结果", len(items))
	}
	if object, ok := value.(map[string]any); ok {
		if status := firstStringField(object, "status", "result", "message"); status != "" {
			return boundedActivityText(status, 900)
		}
	}
	return "工具已完成并返回结果"
}

func recordConversationToolActivity(
	ctx context.Context,
	eventType conversation.TurnEventType,
	activity conversation.TurnToolActivity,
) {
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), conversationActivityWriteTimeout)
	defer cancel()
	_ = conversation.RecordTurnToolActivity(recordCtx, eventType, activity)
}

func decodeActivityValue(raw string) any {
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return nil
	}
	return value
}

func decodeActivityObject(raw string) map[string]any {
	value, _ := decodeActivityValue(raw).(map[string]any)
	return value
}

func stringField(value map[string]any, key string) string {
	if value == nil {
		return ""
	}
	result, _ := value[key].(string)
	return strings.TrimSpace(result)
}

func firstStringField(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if result := stringField(value, key); result != "" {
			return result
		}
	}
	return ""
}

func intField(value map[string]any, key string) int {
	if number, ok := value[key].(float64); ok && number >= 0 {
		return int(number)
	}
	return 0
}

func arrayField(value map[string]any, key string) []any {
	if value == nil {
		return nil
	}
	result, _ := value[key].([]any)
	return result
}

func stringArrayField(value map[string]any, key string) []string {
	items := arrayField(value, key)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
			result = append(result, strings.TrimSpace(text))
		}
	}
	return result
}

func resultCollectionSummary(prefix, suffix string, results []any, labelKeys ...string) string {
	labels := make([]string, 0, 3)
	for _, item := range results {
		object, _ := item.(map[string]any)
		label := firstStringField(object, labelKeys...)
		if label != "" && !containsString(labels, label) {
			labels = append(labels, label)
			if len(labels) == 3 {
				break
			}
		}
	}
	summary := fmt.Sprintf("%s %d %s", prefix, len(results), suffix)
	if len(labels) > 0 {
		summary += "：" + strings.Join(labels, "、")
	}
	return boundedActivityText(summary, 900)
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func quotedActivity(prefix, value string) string {
	if value == "" {
		return prefix + "当前问题相关信息"
	}
	return boundedActivityText(prefix+"“"+value+"”", 700)
}

func limitStrings(values []string, limit int) []string {
	if len(values) <= limit {
		return values
	}
	return values[:limit]
}

func boundedActivityText(value string, maxRunes int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes-1]) + "…"
}

func redactConversationSQL(query string) string {
	query = conversationSQLStringLiteral.ReplaceAllString(query, "'?'")
	return conversationSQLNumberLiteral.ReplaceAllString(query, "?")
}
