package httptransport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/externalcase"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

func TestExternalCaseListBindsQueryAndDoesNotExposeObjectKey(t *testing.T) {
	useCase := &externalCaseUseCaseStub{listResult: externalcase.ListResult{
		Items: []externalcase.ExternalCase{externalCaseHTTPFixture()}, Total: 1,
	}}
	routes, err := NewExternalCaseRoutes(useCase, func(c *gin.Context) { c.Next() })
	if err != nil {
		t.Fatalf("routes: %v", err)
	}
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	dataSourceID := uuid.New()
	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/external-cases?dataSourceId="+dataSourceID.String()+"&page=0&pageSize=999&status=open&caseType=%20performance%20&sortBy=externalCaseKey&sortOrder=asc", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	if useCase.gotQuery.Page != 1 || useCase.gotQuery.PageSize != 100 {
		t.Fatalf("query pagination = %#v", useCase.gotQuery)
	}
	if useCase.gotQuery.DataSourceID != dataSourceID {
		t.Fatalf("dataSourceId = %s", useCase.gotQuery.DataSourceID)
	}
	if useCase.gotQuery.CaseType != "performance" {
		t.Fatalf("caseType = %q, want performance", useCase.gotQuery.CaseType)
	}
	body := response.Body.String()
	for _, want := range []string{`"externalCaseKey":"TKT-1001"`, `"workOrderNo":"WO-1"`, `"sourceFingerprint":"sha256:test"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %s: %s", want, body)
		}
	}
	if strings.Contains(body, "erp/private/object.png") || strings.Contains(body, "objectKey") {
		t.Fatalf("response leaked object key: %s", body)
	}
}

func TestDataSourceListReturnsSafeMetadataOnly(t *testing.T) {
	routes, _ := NewExternalCaseRoutes(&externalCaseUseCaseStub{}, func(c *gin.Context) { c.Next() })
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/data-sources", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"name":"ERP"`) {
		t.Fatalf("status=%d body=%s", response.Code, body)
	}
	for _, forbidden := range []string{"password", "dsn", "host", "database", "user"} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Fatalf("data source response leaked %q: %s", forbidden, body)
		}
	}
}

func TestExternalCaseListRejectsInvalidTimeRange(t *testing.T) {
	useCase := &externalCaseUseCaseStub{}
	routes, _ := NewExternalCaseRoutes(useCase, func(c *gin.Context) { c.Next() })
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/external-cases?dataSourceId="+uuid.NewString()+"&reportedFrom=2026-07-30T00:00:00Z&reportedTo=2026-07-29T00:00:00Z", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"field":"reportedFrom"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

type externalCaseUseCaseStub struct {
	listResult externalcase.ListResult
	listErr    error
	getResult  *externalcase.ExternalCase
	getErr     error
	gotQuery   externalcase.ListQuery
}

func (s *externalCaseUseCaseStub) DataSource() externalcase.DataSource {
	return externalcase.DataSource{ID: uuid.MustParse("8d5c67dc-4c09-4ee5-9e80-4d822303dc35"), Name: "ERP", Type: "sqlserver", Environment: "test", Status: "active"}
}

func (s *externalCaseUseCaseStub) List(_ context.Context, query externalcase.ListQuery) (externalcase.ListResult, error) {
	s.gotQuery = query
	return s.listResult, s.listErr
}

func (s *externalCaseUseCaseStub) Get(context.Context, uuid.UUID) (*externalcase.ExternalCase, error) {
	return s.getResult, s.getErr
}

func externalCaseHTTPFixture() externalcase.ExternalCase {
	now := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	return externalcase.ExternalCase{
		ID: uuid.New(), DataSourceID: uuid.New(), ExternalCaseKey: "TKT-1001",
		Title: "库存未更新", Description: "描述", Status: externalcase.StatusOpen,
		Priority: externalcase.PriorityHigh, ReportedAt: now, SourceUpdatedAt: now,
		Production: externalcase.ProductionContext{WorkOrderNo: "WO-1"},
		Attributes: map[string]any{}, SourceFingerprint: "sha256:test",
		Attachments: []externalcase.ExternalAttachment{{
			ExternalAttachmentKey: "ATT-1", FileName: "截图.png", MediaType: "image/png",
			ObjectKey: "erp/private/object.png", ContentHash: "sha256:a", SourceUpdatedAt: now,
		}},
	}
}
