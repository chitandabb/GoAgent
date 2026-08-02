package diagnosis

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/chitandabb/GoAgent/internal/externalcase"

	"github.com/google/uuid"
)

var ErrInvalidTaskSnapshot = errors.New("diagnosis task snapshot is invalid")

// DecodeCaseSnapshot 将创建任务时冻结的 JSON 快照还原为只读 Tool 使用的领域对象。
// Worker 必须使用这份快照，不能在异步执行时重新读取已经变化的 ERP 工单正文。
func DecodeCaseSnapshot(raw json.RawMessage) (externalcase.ExternalCase, error) {
	var payload caseSnapshotPayload
	if len(raw) == 0 || json.Unmarshal(raw, &payload) != nil {
		return externalcase.ExternalCase{}, ErrInvalidTaskSnapshot
	}
	if payload.SchemaVersion != 1 {
		return externalcase.ExternalCase{}, fmt.Errorf("%w: unsupported schema version %d", ErrInvalidTaskSnapshot, payload.SchemaVersion)
	}
	externalCaseID, err := uuid.Parse(payload.ExternalCaseID)
	if err != nil {
		return externalcase.ExternalCase{}, fmt.Errorf("%w: external case id", ErrInvalidTaskSnapshot)
	}
	dataSourceID, err := uuid.Parse(payload.DataSourceID)
	if err != nil {
		return externalcase.ExternalCase{}, fmt.Errorf("%w: data source id", ErrInvalidTaskSnapshot)
	}
	if strings.TrimSpace(payload.ExternalCaseKey) == "" || strings.TrimSpace(payload.Title) == "" ||
		payload.ReportedAt.IsZero() || payload.SourceUpdatedAt.IsZero() ||
		(payload.Status != externalcase.StatusOpen && payload.Status != externalcase.StatusProcessing && payload.Status != externalcase.StatusClosed) ||
		(payload.Priority != externalcase.PriorityHigh && payload.Priority != externalcase.PriorityMedium && payload.Priority != externalcase.PriorityLow) {
		return externalcase.ExternalCase{}, ErrInvalidTaskSnapshot
	}

	attachments := make([]externalcase.ExternalAttachment, 0, len(payload.Attachments))
	for _, attachment := range payload.Attachments {
		attachments = append(attachments, externalcase.ExternalAttachment{
			ExternalAttachmentKey: attachment.ExternalAttachmentKey,
			FileName:              attachment.FileName,
			MediaType:             attachment.MediaType,
			SizeBytes:             attachment.SizeBytes,
			ContentHash:           attachment.ContentHash,
			SourceUpdatedAt:       attachment.SourceUpdatedAt,
		})
	}
	attributes := payload.Attributes
	if attributes == nil {
		attributes = map[string]any{}
	}
	return externalcase.ExternalCase{
		ID: externalCaseID, DataSourceID: dataSourceID,
		ExternalCaseKey: payload.ExternalCaseKey, CaseType: payload.CaseType,
		Title: payload.Title, Description: payload.Description,
		Category: payload.Category, Module: payload.Module,
		Status: payload.Status, Priority: payload.Priority,
		SourceStatus: payload.SourceStatus, SourcePriority: payload.SourcePriority,
		OccurredAt: payload.OccurredAt, ReportedAt: payload.ReportedAt,
		SourceUpdatedAt: payload.SourceUpdatedAt,
		Customer: externalcase.CustomerContext{
			Code: payload.Customer.Code, Name: payload.Customer.Name,
		},
		Product: externalcase.ProductContext{
			Code: payload.Product.Code, Name: payload.Product.Name, Version: payload.Product.Version,
		},
		Production: externalcase.ProductionContext{
			WorkOrderNo: payload.Production.WorkOrderNo, WorkpieceNo: payload.Production.WorkpieceNo,
			MaterialCode: payload.Production.MaterialCode, BatchNo: payload.Production.BatchNo,
			SerialNo: payload.Production.SerialNo, FactoryCode: payload.Production.FactoryCode,
			WorkshopCode: payload.Production.WorkshopCode, ProductionLineCode: payload.Production.ProductionLineCode,
			WorkstationCode: payload.Production.WorkstationCode, EquipmentCode: payload.Production.EquipmentCode,
		},
		Environment: externalcase.EnvironmentContext{
			SourceSystem:          payload.Environment.SourceSystem,
			DeploymentEnvironment: payload.Environment.DeploymentEnvironment,
			BusinessDatabaseAlias: payload.Environment.BusinessDatabaseAlias,
		},
		Attributes: attributes, Attachments: attachments,
		SourceFingerprint: payload.SourceFingerprint, Truncated: payload.Truncated,
	}, nil
}
