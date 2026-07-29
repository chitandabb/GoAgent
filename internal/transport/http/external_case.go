package httptransport

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/apperror"
	"github.com/chitandabb/GoAgent/internal/externalcase"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type externalCaseUseCase interface {
	DataSource() externalcase.DataSource
	List(ctx context.Context, query externalcase.ListQuery) (externalcase.ListResult, error)
	Get(ctx context.Context, id uuid.UUID) (*externalcase.ExternalCase, error)
}

type ExternalCaseRoutes struct {
	useCase externalCaseUseCase
	auth    gin.HandlerFunc
}

func NewExternalCaseRoutes(useCase externalCaseUseCase, auth gin.HandlerFunc) (*ExternalCaseRoutes, error) {
	if useCase == nil || auth == nil {
		return nil, errors.New("external case route dependencies are nil")
	}
	return &ExternalCaseRoutes{useCase: useCase, auth: auth}, nil
}

func (r *ExternalCaseRoutes) Register(api *gin.RouterGroup) {
	protected := api.Group("")
	protected.Use(r.auth)
	protected.GET("/data-sources", r.listDataSources)
	routes := protected.Group("/external-cases")
	routes.GET("", r.list)
	routes.GET("/:externalCaseId", r.get)
}

func (r *ExternalCaseRoutes) listDataSources(c *gin.Context) {
	source := r.useCase.DataSource()
	WriteSuccess(c, gin.H{"items": []dataSourceResponse{{
		ID: source.ID.String(), Name: source.Name, Type: source.Type,
		Environment: source.Environment, Status: source.Status,
	}}})
}

type dataSourceResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Environment string `json:"environment"`
	Status      string `json:"status"`
}

type externalCaseListRequest struct {
	PageQuery
	DataSourceID string `form:"dataSourceId" binding:"required,uuid"`
	Keyword      string `form:"keyword" binding:"max=100"`
	Status       string `form:"status" binding:"omitempty,oneof=open processing closed"`
	Priority     string `form:"priority" binding:"omitempty,oneof=high medium low"`
	ReportedFrom string `form:"reportedFrom"`
	ReportedTo   string `form:"reportedTo"`
	SortBy       string `form:"sortBy" binding:"omitempty,oneof=reportedAt sourceUpdatedAt externalCaseKey"`
	SortOrder    string `form:"sortOrder" binding:"omitempty,oneof=asc desc"`
	CaseType     string `form:"caseType" binding:"omitempty,max=64"`
}

func (r *ExternalCaseRoutes) list(c *gin.Context) {
	request, ok := BindQuery[externalCaseListRequest](c)
	if !ok {
		return
	}
	request.PageQuery.Normalize()
	from, err := parseOptionalTime("reportedFrom", request.ReportedFrom)
	if err != nil {
		AbortWithError(c, err)
		return
	}
	to, err := parseOptionalTime("reportedTo", request.ReportedTo)
	if err != nil {
		AbortWithError(c, err)
		return
	}
	if from != nil && to != nil && from.After(*to) {
		AbortWithError(c, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
			Field: "reportedFrom", Reason: "不能晚于 reportedTo",
		}}))
		return
	}
	dataSourceID, err := uuid.Parse(request.DataSourceID)
	if err != nil {
		AbortWithError(c, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
			Field: "dataSourceId", Reason: "必须是合法的 UUID",
		}}))
		return
	}
	result, err := r.useCase.List(c.Request.Context(), externalcase.ListQuery{
		DataSourceID: dataSourceID, Keyword: strings.TrimSpace(request.Keyword), Status: externalcase.Status(request.Status),
		Priority: externalcase.Priority(request.Priority), ReportedFrom: from, ReportedTo: to,
		Page: request.Page, PageSize: request.PageSize, SortBy: request.SortBy, SortOrder: request.SortOrder,
		CaseType: strings.TrimSpace(request.CaseType),
	})
	if err != nil {
		AbortWithError(c, err)
		return
	}
	items := make([]externalCaseResponse, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, externalCaseResponseFrom(item))
	}
	WriteSuccess(c, PageData[externalCaseResponse]{
		Items: items, Page: request.Page, PageSize: request.PageSize, Total: result.Total,
	})
}

func (r *ExternalCaseRoutes) get(c *gin.Context) {
	id, err := uuid.Parse(c.Param("externalCaseId"))
	if err != nil {
		AbortWithError(c, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
			Field: "externalCaseId", Reason: "必须是合法的 UUID",
		}}))
		return
	}
	item, err := r.useCase.Get(c.Request.Context(), id)
	if err != nil {
		AbortWithError(c, err)
		return
	}
	WriteSuccess(c, externalCaseResponseFrom(*item))
}

func parseOptionalTime(field, value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
			Field: field, Reason: "必须是 RFC3339 时间",
		}})
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

type externalCaseResponse struct {
	ExternalCaseID          string                       `json:"externalCaseId"`
	DataSourceID            string                       `json:"dataSourceId"`
	ExternalCaseKey         string                       `json:"externalCaseKey"`
	CaseType                string                       `json:"caseType"`
	Title                   string                       `json:"title"`
	Description             string                       `json:"description"`
	Category                string                       `json:"category"`
	Module                  string                       `json:"module"`
	Status                  externalcase.Status          `json:"status"`
	Priority                externalcase.Priority        `json:"priority"`
	OccurredAt              *time.Time                   `json:"occurredAt"`
	ReportedAt              time.Time                    `json:"reportedAt"`
	SourceUpdatedAt         time.Time                    `json:"sourceUpdatedAt"`
	CustomerCode            string                       `json:"customerCode"`
	CustomerName            string                       `json:"customerName"`
	ProductCode             string                       `json:"productCode"`
	ProductName             string                       `json:"productName"`
	ProductVersion          string                       `json:"productVersion"`
	WorkOrderNo             string                       `json:"workOrderNo"`
	WorkpieceNo             string                       `json:"workpieceNo"`
	MaterialCode            string                       `json:"materialCode"`
	BatchNo                 string                       `json:"batchNo"`
	SerialNo                string                       `json:"serialNo"`
	FactoryCode             string                       `json:"factoryCode"`
	WorkshopCode            string                       `json:"workshopCode"`
	ProductionLineCode      string                       `json:"productionLineCode"`
	WorkstationCode         string                       `json:"workstationCode"`
	EquipmentCode           string                       `json:"equipmentCode"`
	SourceSystem            string                       `json:"sourceSystem"`
	DeploymentEnvironment   string                       `json:"deploymentEnvironment"`
	BusinessDatabaseAlias   string                       `json:"businessDatabaseAlias"`
	Attributes              map[string]any               `json:"attributes"`
	AttributesSchemaVersion int                          `json:"attributesSchemaVersion"`
	Attachments             []externalAttachmentResponse `json:"attachments"`
	SourceFingerprint       string                       `json:"sourceFingerprint"`
	Truncated               bool                         `json:"truncated"`
}

type externalAttachmentResponse struct {
	ExternalAttachmentKey string    `json:"externalAttachmentKey"`
	FileName              string    `json:"fileName"`
	MediaType             string    `json:"mediaType"`
	SizeBytes             int64     `json:"sizeBytes"`
	ContentHash           string    `json:"contentHash"`
	SourceUpdatedAt       time.Time `json:"sourceUpdatedAt"`
}

func externalCaseResponseFrom(item externalcase.ExternalCase) externalCaseResponse {
	attachments := make([]externalAttachmentResponse, 0, len(item.Attachments))
	for _, attachment := range item.Attachments {
		attachments = append(attachments, externalAttachmentResponse{
			ExternalAttachmentKey: attachment.ExternalAttachmentKey, FileName: attachment.FileName,
			MediaType: attachment.MediaType, SizeBytes: attachment.SizeBytes,
			ContentHash: attachment.ContentHash, SourceUpdatedAt: attachment.SourceUpdatedAt,
		})
	}
	return externalCaseResponse{
		ExternalCaseID: item.ID.String(), DataSourceID: item.DataSourceID.String(),
		ExternalCaseKey: item.ExternalCaseKey, CaseType: item.CaseType, Title: item.Title,
		Description: item.Description, Category: item.Category, Module: item.Module,
		Status: item.Status, Priority: item.Priority, OccurredAt: item.OccurredAt,
		ReportedAt: item.ReportedAt, SourceUpdatedAt: item.SourceUpdatedAt,
		CustomerCode: item.Customer.Code, CustomerName: item.Customer.Name,
		ProductCode: item.Product.Code, ProductName: item.Product.Name, ProductVersion: item.Product.Version,
		WorkOrderNo: item.Production.WorkOrderNo, WorkpieceNo: item.Production.WorkpieceNo,
		MaterialCode: item.Production.MaterialCode, BatchNo: item.Production.BatchNo,
		SerialNo: item.Production.SerialNo, FactoryCode: item.Production.FactoryCode,
		WorkshopCode: item.Production.WorkshopCode, ProductionLineCode: item.Production.ProductionLineCode,
		WorkstationCode: item.Production.WorkstationCode, EquipmentCode: item.Production.EquipmentCode,
		SourceSystem: item.Environment.SourceSystem, DeploymentEnvironment: item.Environment.DeploymentEnvironment,
		BusinessDatabaseAlias: item.Environment.BusinessDatabaseAlias,
		Attributes:            item.Attributes, AttributesSchemaVersion: 1,
		Attachments: attachments, SourceFingerprint: item.SourceFingerprint, Truncated: item.Truncated,
	}
}
