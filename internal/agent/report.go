package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

type ConclusionStatus string

const (
	ConclusionConclusive   ConclusionStatus = "conclusive"
	ConclusionProbable     ConclusionStatus = "probable"
	ConclusionInconclusive ConclusionStatus = "inconclusive"
)

func (s ConclusionStatus) Valid() bool {
	return s == ConclusionConclusive || s == ConclusionProbable || s == ConclusionInconclusive
}

type RiskLevel string

const (
	RiskLow    RiskLevel = "low"
	RiskMedium RiskLevel = "medium"
	RiskHigh   RiskLevel = "high"
)

func (l RiskLevel) Valid() bool {
	return l == RiskLow || l == RiskMedium || l == RiskHigh
}

type ConfidenceLevel string

const (
	ConfidenceLow    ConfidenceLevel = "low"
	ConfidenceMedium ConfidenceLevel = "medium"
	ConfidenceHigh   ConfidenceLevel = "high"
)

func (l ConfidenceLevel) Valid() bool {
	return l == ConfidenceLow || l == ConfidenceMedium || l == ConfidenceHigh
}

type EvidenceSupportType string

const (
	EvidenceSupports    EvidenceSupportType = "supports"
	EvidenceContradicts EvidenceSupportType = "contradicts"
	EvidenceContext     EvidenceSupportType = "context"
)

func (t EvidenceSupportType) Valid() bool {
	return t == EvidenceSupports || t == EvidenceContradicts || t == EvidenceContext
}

// ReportEvidence 是报告中的证据引用。sourceRef 只是稳定定位信息，正式任务链路会把它解析为 EvidenceItem。
type ReportEvidence struct {
	Claim       string              `json:"claim"`
	SourceTool  string              `json:"sourceTool"`
	SourceRef   string              `json:"sourceRef"`
	SupportType EvidenceSupportType `json:"supportType"`
}

// StructuredReport 是模型与 Evidence Gate 之间的第一版稳定契约。
type StructuredReport struct {
	ConclusionStatus ConclusionStatus `json:"conclusionStatus"`
	RiskLevel        RiskLevel        `json:"riskLevel"`
	Conclusion       string           `json:"conclusion"`
	BusinessSummary  string           `json:"businessSummary"`
	TechnicalSummary string           `json:"technicalSummary"`
	Evidence         []ReportEvidence `json:"evidence"`
	Limitations      []string         `json:"limitations"`
	Confidence       ConfidenceLevel  `json:"confidence"`
}

func decodeStructuredReport(answer string) (*StructuredReport, error) {
	raw := strings.TrimSpace(answer)
	if strings.HasPrefix(raw, "```") {
		firstLineEnd := strings.IndexByte(raw, '\n')
		if firstLineEnd < 0 || !strings.HasSuffix(raw, "```") {
			return nil, errors.New("report code fence is incomplete")
		}
		raw = strings.TrimSpace(strings.TrimSuffix(raw[firstLineEnd+1:], "```"))
	}
	if raw == "" {
		return nil, errors.New("report is empty")
	}

	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	var report StructuredReport
	if err := decoder.Decode(&report); err != nil {
		return nil, fmt.Errorf("decode report JSON: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	return &report, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode trailing report JSON: %w", err)
	}
	return errors.New("report contains multiple JSON values")
}

func validateStructuredReport(
	report *StructuredReport,
	successfulTools []string,
	maxEvidenceItems int,
) []string {
	if report == nil {
		return []string{"缺少可解析的结构化报告"}
	}
	gaps := make([]string, 0, 8)
	if !report.ConclusionStatus.Valid() {
		gaps = append(gaps, "conclusionStatus 必须是 conclusive、probable 或 inconclusive")
	}
	if !report.RiskLevel.Valid() {
		gaps = append(gaps, "riskLevel 必须是 low、medium 或 high")
	}
	if strings.TrimSpace(report.Conclusion) == "" {
		gaps = append(gaps, "缺少结论")
	}
	if strings.TrimSpace(report.BusinessSummary) == "" {
		gaps = append(gaps, "缺少业务摘要")
	}
	if strings.TrimSpace(report.TechnicalSummary) == "" {
		gaps = append(gaps, "缺少技术摘要")
	}
	if report.Limitations == nil {
		gaps = append(gaps, "缺少 limitations 字段")
	}
	if !report.Confidence.Valid() {
		gaps = append(gaps, "confidence 必须是 high、medium 或 low")
	}
	if len(report.Evidence) == 0 {
		gaps = append(gaps, "至少需要一条可追溯证据")
	}
	if len(report.Evidence) > maxEvidenceItems {
		gaps = append(gaps, fmt.Sprintf("证据数量超过上限 %d", maxEvidenceItems))
	}
	for index, evidence := range report.Evidence {
		prefix := fmt.Sprintf("evidence[%d]", index)
		if strings.TrimSpace(evidence.Claim) == "" {
			gaps = append(gaps, prefix+" 缺少 claim")
		}
		if !slices.Contains(successfulTools, evidence.SourceTool) {
			gaps = append(gaps, prefix+" 的 sourceTool 未在本次任务中成功执行")
		}
		if strings.TrimSpace(evidence.SourceRef) == "" {
			gaps = append(gaps, prefix+" 缺少 sourceRef")
		}
		if !evidence.SupportType.Valid() {
			gaps = append(gaps, prefix+" 的 supportType 无效")
		}
	}
	if report.ConclusionStatus == ConclusionInconclusive && len(nonBlankStrings(report.Limitations)) == 0 {
		gaps = append(gaps, "inconclusive 报告必须说明限制")
	}
	return uniqueStrings(gaps)
}

// validateEvidenceReferences 把模型报告中的 sourceRef 绑定到本次运行实际捕获的
// EvidenceItem。仅校验 sourceTool 成功执行不足以保证引用的是哪一次结果。
func validateEvidenceReferences(report *StructuredReport, items []EvidenceItem) []string {
	if report == nil {
		return nil
	}
	byRef := make(map[string]EvidenceItem, len(items))
	for _, item := range items {
		if item.SourceRef != "" {
			byRef[item.SourceRef] = item
		}
	}
	gaps := make([]string, 0)
	for index, evidence := range report.Evidence {
		prefix := fmt.Sprintf("evidence[%d]", index)
		item, ok := byRef[strings.TrimSpace(evidence.SourceRef)]
		if !ok {
			gaps = append(gaps, prefix+" 的 sourceRef 未对应本次运行的 EvidenceItem")
			continue
		}
		if item.SourceTool != evidence.SourceTool {
			gaps = append(gaps, prefix+" 的 sourceTool 与 EvidenceItem 来源不一致")
		}
	}
	return uniqueStrings(gaps)
}

func nonBlankStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func uniqueStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" && !slices.Contains(result, trimmed) {
			result = append(result, trimmed)
		}
	}
	return result
}
