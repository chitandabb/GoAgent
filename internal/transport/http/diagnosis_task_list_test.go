package httptransport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/diagnosis"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

func TestDiagnosisTaskRoutesListPassesActorAndQuery(t *testing.T) {
	ownerID := uuid.New()
	createdAt := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)
	useCase := &diagnosisTaskUseCaseStub{listPage: diagnosis.TaskListPage{
		Items: []diagnosis.TaskListItem{{
			Task: diagnosis.DiagnosisTask{
				ID: uuid.New(), CreatedBy: ownerID, ExternalCaseID: uuid.New(),
				RequestText: "检查批次回冲", Status: diagnosis.TaskSucceeded,
				CreatedAt: createdAt, UpdatedAt: createdAt,
			},
			ExternalCaseKey:   "WO-2026-0810",
			ExternalCaseTitle: "报工数量与完工数量不一致",
		}},
		Total: 1, Page: 1, PageSize: 20,
	}}
	routes, err := NewDiagnosisTaskRoutes(
		context.Background(), useCase, identityMiddleware(ownerID, false), func(c *gin.Context) { c.Next() },
	)
	if err != nil {
		t.Fatalf("NewDiagnosisTaskRoutes(): %v", err)
	}
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/diagnosis-tasks?page=1&pageSize=20&status=succeeded", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", response.Code, response.Body.String())
	}
	if useCase.gotListQuery.Actor.UserID != ownerID || useCase.gotListQuery.Actor.IsAdmin {
		t.Fatalf("actor = %+v, want owner non-admin", useCase.gotListQuery.Actor)
	}
	if useCase.gotListQuery.Status == nil || *useCase.gotListQuery.Status != diagnosis.TaskSucceeded {
		t.Fatalf("status filter = %v", useCase.gotListQuery.Status)
	}
	body := response.Body.String()
	if !strings.Contains(body, `"externalCaseKey":"WO-2026-0810"`) ||
		!strings.Contains(body, `"externalCaseTitle":"报工数量与完工数量不一致"`) ||
		!strings.Contains(body, `"total":1`) {
		t.Fatalf("body = %s", body)
	}
}

func TestDiagnosisTaskRoutesListMapsForbidden(t *testing.T) {
	useCase := &diagnosisTaskUseCaseStub{listErr: diagnosis.ErrTaskForbidden}
	routes, err := NewDiagnosisTaskRoutes(
		context.Background(), useCase, identityMiddleware(uuid.New(), false), func(c *gin.Context) { c.Next() },
	)
	if err != nil {
		t.Fatalf("NewDiagnosisTaskRoutes(): %v", err)
	}
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/diagnosis-tasks", nil))

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d body = %s, want 403", response.Code, response.Body.String())
	}
}
