package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// EvidenceSourceType 是运行时证据的稳定来源分类。
// 它不等同于数据库最终的 source_type；正式任务链路持久化时可以继续扩展映射。
type EvidenceSourceType string

const (
	EvidenceSourceCaseSnapshot  EvidenceSourceType = "case_snapshot"
	EvidenceSourceSchemaCatalog EvidenceSourceType = "schema_catalog"
	EvidenceSourceSQLDefinition EvidenceSourceType = "sql_object_definition"
	EvidenceSourceSQLQuery      EvidenceSourceType = "sql_query"
	EvidenceSourceCodeSearch    EvidenceSourceType = "code_search"
)

// EvidenceItem 是一次 Agent 运行中成功工具结果的不可变快照元数据。
// Snapshot 是已经经过 Tool 自身脱敏/限流，以及 Runner 结果字节限制后的内容；
// 它只存在于当前运行结果中，正式任务持久化会在 P7 的 DiagnosisTask 链路完成。
type EvidenceItem struct {
	ID          string             `json:"id"`
	SourceType  EvidenceSourceType `json:"sourceType"`
	SourceTool  string             `json:"sourceTool"`
	SourceRef   string             `json:"sourceRef"`
	CollectedAt time.Time          `json:"collectedAt"`
	Summary     string             `json:"summary"`
	Snapshot    string             `json:"snapshot,omitempty"`
	ContentHash string             `json:"contentHash"`
	Redacted    bool               `json:"redacted"`
	Truncated   bool               `json:"truncated"`
	Location    string             `json:"location,omitempty"`
}

// newToolEvidenceItem 只为已知的事实型只读工具创建证据，不把 Skill 指南或未知工具输出
// 混入诊断证据。sourceRef 同时作为模型报告中可引用的稳定运行时标识。
func newToolEvidenceItem(toolName, snapshot string, truncated bool) (EvidenceItem, bool) {
	sourceType, ok := evidenceSourceTypeForTool(toolName)
	if !ok || strings.TrimSpace(snapshot) == "" {
		return EvidenceItem{}, false
	}

	collectedAt := time.Now().UTC()
	sourceRef := "evidence:" + uuid.NewString()
	digest := sha256.Sum256([]byte(snapshot))
	return EvidenceItem{
		ID:          sourceRef,
		SourceType:  sourceType,
		SourceTool:  toolName,
		SourceRef:   sourceRef,
		CollectedAt: collectedAt,
		Summary:     summarizeToolEvidence(toolName, snapshot),
		Snapshot:    snapshot,
		ContentHash: "sha256:" + hex.EncodeToString(digest[:]),
		Redacted:    false,
		Truncated:   truncated || toolResultTruncated(snapshot),
		Location:    "tool-output",
	}, true
}

func evidenceSourceTypeForTool(toolName string) (EvidenceSourceType, bool) {
	switch toolName {
	case ToolReadExternalCase:
		return EvidenceSourceCaseSnapshot, true
	case ToolSearchSchemaCatalog:
		return EvidenceSourceSchemaCatalog, true
	case ToolDatabaseObjectDefinition:
		return EvidenceSourceSQLDefinition, true
	case ToolExecuteReadonlyQuery:
		return EvidenceSourceSQLQuery, true
	}
	for _, readOnlyTool := range GitHubReadOnlyTools {
		if toolName == readOnlyTool {
			return EvidenceSourceCodeSearch, true
		}
	}
	return "", false
}

func summarizeToolEvidence(toolName, snapshot string) string {
	summary := fmt.Sprintf("%s 返回了一份受限只读证据快照（%d 字节）", toolName, len(snapshot))
	var payload struct {
		ReturnedRows int  `json:"returnedRows"`
		Truncated    bool `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(snapshot), &payload); err == nil && payload.ReturnedRows >= 0 {
		summary = fmt.Sprintf("%s 返回 %d 行受限只读结果（%d 字节）", toolName, payload.ReturnedRows, len(snapshot))
		if payload.Truncated {
			summary += "，结果已截断"
		}
	}
	return summary
}

func toolResultTruncated(snapshot string) bool {
	var payload struct {
		Truncated bool `json:"truncated"`
	}
	return json.Unmarshal([]byte(snapshot), &payload) == nil && payload.Truncated
}
