package httptransport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/diagnosis"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

func TestReportReviewRoutesListReturnsCurrentAndHistory(t *testing.T) {
	reportID := uuid.New()
	ownerID := uuid.New()
	reviews := []diagnosis.ReportReview{
		{ID: uuid.New(), ReportID: reportID, ReviewedBy: ownerID, Verdict: diagnosis.ReviewRejected, Comment: "需要补充证据", CreatedAt: time.Date(2026, 8, 2, 2, 0, 0, 0, time.UTC)},
		{ID: uuid.New(), ReportID: reportID, ReviewedBy: ownerID, Verdict: diagnosis.ReviewPartiallyAdopted, CreatedAt: time.Date(2026, 8, 2, 1, 0, 0, 0, time.UTC)},
	}
	useCase := &reportReviewUseCaseStub{reviews: reviews}
	routes, err := NewReportReviewRoutes(useCase, identityMiddleware(ownerID, false), func(c *gin.Context) { c.Next() })
	if err != nil {
		t.Fatalf("NewReportReviewRoutes(): %v", err)
	}
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/diagnosis-reports/"+reportID.String()+"/reviews", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var envelope Response
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	body, err := json.Marshal(envelope.Data)
	if err != nil {
		t.Fatalf("encode data: %v", err)
	}
	if !strings.Contains(string(body), `"verdict":"rejected"`) || !strings.Contains(string(body), `"current"`) {
		t.Fatalf("response missing current review: %s", body)
	}
	if useCase.gotActor.UserID != ownerID || useCase.gotReportID != reportID {
		t.Fatalf("use case arguments actor=%+v report=%s", useCase.gotActor, useCase.gotReportID)
	}
}

func TestReportReviewRoutesCreateRequiresAuthenticatedIdentityAndPassesInput(t *testing.T) {
	reportID := uuid.New()
	ownerID := uuid.New()
	useCase := &reportReviewUseCaseStub{submitResult: diagnosis.ReportReview{
		ID: uuid.New(), ReportID: reportID, ReviewedBy: ownerID, Verdict: diagnosis.ReviewAdopted,
		Comment: "结论可采纳", CreatedAt: time.Date(2026, 8, 2, 3, 0, 0, 0, time.UTC),
	}}
	routes, err := NewReportReviewRoutes(useCase, identityMiddleware(ownerID, false), func(c *gin.Context) { c.Next() })
	if err != nil {
		t.Fatalf("NewReportReviewRoutes(): %v", err)
	}
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/diagnosis-reports/"+reportID.String()+"/reviews", strings.NewReader(`{"verdict":"adopted","comment":"  证据一致  "}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)

	if response.Code != http.StatusCreated || !strings.Contains(response.Header().Get("Location"), reportID.String()) {
		t.Fatalf("status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	if useCase.submitInput.Verdict != diagnosis.ReviewAdopted || useCase.submitInput.Comment != "  证据一致  " {
		t.Fatalf("submit input = %+v", useCase.submitInput)
	}

	unauthenticatedRoutes, _ := NewReportReviewRoutes(useCase, func(c *gin.Context) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 40101})
	}, func(c *gin.Context) { c.Next() })
	unauthenticatedRouter := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, unauthenticatedRoutes)
	unauthenticatedResponse := httptest.NewRecorder()
	unauthenticatedRouter.ServeHTTP(unauthenticatedResponse, httptest.NewRequest(http.MethodPost, "/api/v1/diagnosis-reports/"+reportID.String()+"/reviews", strings.NewReader(`{"verdict":"adopted"}`)))
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", unauthenticatedResponse.Code)
	}
}

func TestReportReviewRoutesRejectsInvalidReportID(t *testing.T) {
	routes, _ := NewReportReviewRoutes(&reportReviewUseCaseStub{}, identityMiddleware(uuid.New(), false), func(c *gin.Context) { c.Next() })
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/diagnosis-reports/not-a-uuid/reviews", nil))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"field":"reportId"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

type reportReviewUseCaseStub struct {
	reviews      []diagnosis.ReportReview
	submitResult diagnosis.ReportReview
	listErr      error
	submitErr    error
	gotActor     diagnosis.ReviewActor
	gotReportID  uuid.UUID
	submitInput  diagnosis.SubmitReviewInput
}

func (s *reportReviewUseCaseStub) List(_ context.Context, actor diagnosis.ReviewActor, reportID uuid.UUID) ([]diagnosis.ReportReview, error) {
	s.gotActor, s.gotReportID = actor, reportID
	return s.reviews, s.listErr
}

func (s *reportReviewUseCaseStub) Submit(_ context.Context, actor diagnosis.ReviewActor, reportID uuid.UUID, input diagnosis.SubmitReviewInput) (diagnosis.ReportReview, error) {
	s.gotActor, s.gotReportID, s.submitInput = actor, reportID, input
	return s.submitResult, s.submitErr
}

func identityMiddleware(userID uuid.UUID, admin bool) gin.HandlerFunc {
	role := auth.RoleAnalyst
	if admin {
		role = auth.RoleAdmin
	}
	return func(c *gin.Context) {
		c.Set(identityKey, auth.Identity{User: auth.User{ID: userID, Role: role, Status: auth.UserStatusActive}})
		c.Next()
	}
}
