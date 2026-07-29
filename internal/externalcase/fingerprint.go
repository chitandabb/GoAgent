package externalcase

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

const fingerprintSchemaVersion = 1

type fingerprintPayload struct {
	SchemaVersion   int                     `json:"schemaVersion"`
	ExternalCaseKey string                  `json:"externalCaseKey"`
	CaseType        string                  `json:"caseType"`
	Title           string                  `json:"title"`
	Description     string                  `json:"description"`
	Category        string                  `json:"category"`
	Module          string                  `json:"module"`
	Status          Status                  `json:"status"`
	Priority        Priority                `json:"priority"`
	SourceStatus    string                  `json:"sourceStatus"`
	SourcePriority  string                  `json:"sourcePriority"`
	OccurredAt      string                  `json:"occurredAt"`
	ReportedAt      string                  `json:"reportedAt"`
	SourceUpdatedAt string                  `json:"sourceUpdatedAt"`
	Customer        CustomerContext         `json:"customer"`
	Product         ProductContext          `json:"product"`
	Production      ProductionContext       `json:"production"`
	Environment     EnvironmentContext      `json:"environment"`
	Attributes      map[string]any          `json:"attributes"`
	Attachments     []fingerprintAttachment `json:"attachments"`
	Truncated       bool                    `json:"truncated"`
}

type fingerprintAttachment struct {
	Key             string `json:"key"`
	FileName        string `json:"fileName"`
	MediaType       string `json:"mediaType"`
	SizeBytes       int64  `json:"sizeBytes"`
	ContentHash     string `json:"contentHash"`
	SourceUpdatedAt string `json:"sourceUpdatedAt"`
}

// Fingerprint 对真正进入诊断的规范化输入计算稳定摘要。
func Fingerprint(item ExternalCase) (string, error) {
	attachments := make([]fingerprintAttachment, 0, len(item.Attachments))
	for _, attachment := range item.Attachments {
		attachments = append(attachments, fingerprintAttachment{
			Key:             normalizeText(attachment.ExternalAttachmentKey),
			FileName:        normalizeText(attachment.FileName),
			MediaType:       strings.ToLower(normalizeText(attachment.MediaType)),
			SizeBytes:       attachment.SizeBytes,
			ContentHash:     strings.ToLower(normalizeText(attachment.ContentHash)),
			SourceUpdatedAt: canonicalTime(attachment.SourceUpdatedAt),
		})
	}
	sort.Slice(attachments, func(i, j int) bool { return attachments[i].Key < attachments[j].Key })

	payload := fingerprintPayload{
		SchemaVersion:   fingerprintSchemaVersion,
		ExternalCaseKey: normalizeText(item.ExternalCaseKey),
		CaseType:        normalizeText(item.CaseType),
		Title:           normalizeText(item.Title),
		Description:     normalizeText(item.Description),
		Category:        normalizeText(item.Category),
		Module:          normalizeText(item.Module),
		Status:          item.Status,
		Priority:        item.Priority,
		SourceStatus:    normalizeText(item.SourceStatus),
		SourcePriority:  normalizeText(item.SourcePriority),
		OccurredAt:      canonicalOptionalTime(item.OccurredAt),
		ReportedAt:      canonicalTime(item.ReportedAt),
		SourceUpdatedAt: canonicalTime(item.SourceUpdatedAt),
		Customer:        normalizeCustomer(item.Customer),
		Product:         normalizeProduct(item.Product),
		Production:      normalizeProduction(item.Production),
		Environment:     normalizeEnvironment(item.Environment),
		Attributes:      item.Attributes,
		Attachments:     attachments,
		Truncated:       item.Truncated,
	}
	if payload.Attributes == nil {
		payload.Attributes = map[string]any{}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func normalizeText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.TrimSpace(value)
}

func canonicalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func canonicalOptionalTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return canonicalTime(*value)
}

func normalizeCustomer(value CustomerContext) CustomerContext {
	return CustomerContext{Code: normalizeText(value.Code), Name: normalizeText(value.Name)}
}

func normalizeProduct(value ProductContext) ProductContext {
	return ProductContext{Code: normalizeText(value.Code), Name: normalizeText(value.Name), Version: normalizeText(value.Version)}
}

func normalizeProduction(value ProductionContext) ProductionContext {
	return ProductionContext{
		WorkOrderNo: normalizeText(value.WorkOrderNo), WorkpieceNo: normalizeText(value.WorkpieceNo),
		MaterialCode: normalizeText(value.MaterialCode), BatchNo: normalizeText(value.BatchNo),
		SerialNo: normalizeText(value.SerialNo), FactoryCode: normalizeText(value.FactoryCode),
		WorkshopCode: normalizeText(value.WorkshopCode), ProductionLineCode: normalizeText(value.ProductionLineCode),
		WorkstationCode: normalizeText(value.WorkstationCode), EquipmentCode: normalizeText(value.EquipmentCode),
	}
}

func normalizeEnvironment(value EnvironmentContext) EnvironmentContext {
	return EnvironmentContext{
		SourceSystem:          normalizeText(value.SourceSystem),
		DeploymentEnvironment: normalizeText(value.DeploymentEnvironment),
		BusinessDatabaseAlias: normalizeText(value.BusinessDatabaseAlias),
	}
}
