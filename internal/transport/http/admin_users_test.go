package httptransport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/apperror"
	"github.com/chitandabb/GoAgent/internal/auth"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

func TestAdminUsersRoutesRejectNonAdmin(t *testing.T) {
	useCase := &stubAdminUsersUseCase{}
	routes, err := NewAdminUsersRoutes(useCase, identityMiddleware(uuid.New(), false), func(c *gin.Context) { c.Next() })
	if err != nil {
		t.Fatalf("NewAdminUsersRoutes(): %v", err)
	}
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/admin/users"},
		{http.MethodPost, "/api/v1/admin/users"},
		{http.MethodPatch, "/api/v1/admin/users/" + uuid.NewString()},
	} {
		request := httptest.NewRequest(tc.method, tc.path, nil)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s %s status = %d body = %s, want 403", tc.method, tc.path, response.Code, response.Body.String())
		}
	}
	if useCase.calls != 0 {
		t.Fatalf("use case calls = %d, want 0", useCase.calls)
	}
}

func TestAdminUsersRoutesListPassesQuery(t *testing.T) {
	adminID := uuid.New()
	createdAt := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	useCase := &stubAdminUsersUseCase{page: auth.UserPage{
		Items: []auth.User{{
			ID: uuid.New(), Username: "analyst01", DisplayName: "分析员",
			Role: auth.RoleAnalyst, Status: auth.UserStatusActive, CreatedAt: createdAt, UpdatedAt: createdAt,
		}},
		Total: 1, Page: 2, PageSize: 10,
	}}
	routes, err := NewAdminUsersRoutes(useCase, identityMiddleware(adminID, true), func(c *gin.Context) { c.Next() })
	if err != nil {
		t.Fatalf("NewAdminUsersRoutes(): %v", err)
	}
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/users?page=2&pageSize=10&status=active&role=analyst", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", response.Code, response.Body.String())
	}
	if useCase.gotInput.Page != 2 || useCase.gotInput.PageSize != 10 {
		t.Fatalf("pagination = page %d size %d", useCase.gotInput.Page, useCase.gotInput.PageSize)
	}
	if useCase.gotInput.Status == nil || *useCase.gotInput.Status != auth.UserStatusActive {
		t.Fatalf("status filter = %v", useCase.gotInput.Status)
	}
	if useCase.gotInput.Role == nil || *useCase.gotInput.Role != auth.RoleAnalyst {
		t.Fatalf("role filter = %v", useCase.gotInput.Role)
	}
	body := response.Body.String()
	if !strings.Contains(body, `"total":1`) || !strings.Contains(body, `"page":2`) ||
		!strings.Contains(body, `"username":"analyst01"`) || strings.Contains(body, "PasswordHash") {
		t.Fatalf("body = %s", body)
	}
}

func TestAdminUsersRoutesCreateReturnsCreated(t *testing.T) {
	adminID := uuid.New()
	useCase := &stubAdminUsersUseCase{created: auth.User{
		ID: uuid.New(), Username: "analyst02", DisplayName: "新分析员",
		Role: auth.RoleAnalyst, Status: auth.UserStatusActive, MustChangePassword: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}}
	routes, err := NewAdminUsersRoutes(useCase, identityMiddleware(adminID, true), func(c *gin.Context) { c.Next() })
	if err != nil {
		t.Fatalf("NewAdminUsersRoutes(): %v", err)
	}
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", strings.NewReader(`{
		"username":"analyst02","displayName":"新分析员","role":"analyst","temporaryPassword":"temp-pass-123"
	}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s, want 201", response.Code, response.Body.String())
	}
	if useCase.gotCreate.Username != "analyst02" || useCase.gotCreate.Password != "temp-pass-123" {
		t.Fatalf("create input = %+v", useCase.gotCreate)
	}
	if !strings.Contains(response.Body.String(), `"mustChangePassword":true`) {
		t.Fatalf("body = %s", response.Body.String())
	}
}

func TestAdminUsersRoutesPatchStatusAndRole(t *testing.T) {
	adminID := uuid.New()
	targetID := uuid.New()
	useCase := &stubAdminUsersUseCase{}
	routes, err := NewAdminUsersRoutes(useCase, identityMiddleware(adminID, true), func(c *gin.Context) { c.Next() })
	if err != nil {
		t.Fatalf("NewAdminUsersRoutes(): %v", err)
	}
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	request := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/users/"+targetID.String(), strings.NewReader(`{
		"status":"disabled","role":"admin"
	}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", response.Code, response.Body.String())
	}
	if useCase.gotStatusUserID != targetID || useCase.gotStatus == nil || *useCase.gotStatus != auth.UserStatusDisabled {
		t.Fatalf("status args = %v %v", useCase.gotStatusUserID, useCase.gotStatus)
	}
	if useCase.gotRoleUserID != targetID || useCase.gotRole == nil || *useCase.gotRole != auth.RoleAdmin {
		t.Fatalf("role args = %v %v", useCase.gotRoleUserID, useCase.gotRole)
	}
}

func TestAdminUsersRoutesPatchRequiresAtLeastOneField(t *testing.T) {
	adminID := uuid.New()
	useCase := &stubAdminUsersUseCase{}
	routes, err := NewAdminUsersRoutes(useCase, identityMiddleware(adminID, true), func(c *gin.Context) { c.Next() })
	if err != nil {
		t.Fatalf("NewAdminUsersRoutes(): %v", err)
	}
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	request := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/users/"+uuid.NewString(), strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body = %s, want 422", response.Code, response.Body.String())
	}
	if useCase.calls != 0 {
		t.Fatalf("use case calls = %d, want 0", useCase.calls)
	}
}

func TestAdminUsersRoutesResetPassword(t *testing.T) {
	adminID := uuid.New()
	targetID := uuid.New()
	useCase := &stubAdminUsersUseCase{}
	routes, err := NewAdminUsersRoutes(useCase, identityMiddleware(adminID, true), func(c *gin.Context) { c.Next() })
	if err != nil {
		t.Fatalf("NewAdminUsersRoutes(): %v", err)
	}
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users/"+targetID.String()+"/reset-password", strings.NewReader(`{
		"temporaryPassword":"reset-pass-123"
	}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", response.Code, response.Body.String())
	}
	if useCase.gotResetUserID != targetID || useCase.gotResetPassword != "reset-pass-123" {
		t.Fatalf("reset args = %v %q", useCase.gotResetUserID, useCase.gotResetPassword)
	}
}

func TestAdminUsersRoutesMapsNotFound(t *testing.T) {
	adminID := uuid.New()
	targetID := uuid.New()
	useCase := &stubAdminUsersUseCase{err: apperror.Wrap(apperror.CodeNotFound, context.Canceled)}
	routes, err := NewAdminUsersRoutes(useCase, identityMiddleware(adminID, true), func(c *gin.Context) { c.Next() })
	if err != nil {
		t.Fatalf("NewAdminUsersRoutes(): %v", err)
	}
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	request := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/users/"+targetID.String(), strings.NewReader(`{"status":"active"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s, want 404", response.Code, response.Body.String())
	}
}

type stubAdminUsersUseCase struct {
	page        auth.UserPage
	created     auth.User
	err         error
	calls       int
	gotInput    auth.ListUsersInput
	gotCreate   auth.CreateUserInput
	gotStatusUserID  uuid.UUID
	gotStatus        *auth.UserStatus
	gotRoleUserID    uuid.UUID
	gotRole          *auth.Role
	gotResetUserID   uuid.UUID
	gotResetPassword string
}

func (s *stubAdminUsersUseCase) List(_ context.Context, input auth.ListUsersInput) (auth.UserPage, error) {
	s.calls++
	s.gotInput = input
	return s.page, s.err
}

func (s *stubAdminUsersUseCase) Create(_ context.Context, input auth.CreateUserInput) (auth.User, error) {
	s.calls++
	s.gotCreate = input
	return s.created, s.err
}

func (s *stubAdminUsersUseCase) SetStatus(_ context.Context, _ uuid.UUID, userID uuid.UUID, status auth.UserStatus) error {
	s.calls++
	s.gotStatusUserID = userID
	s.gotStatus = &status
	return s.err
}

func (s *stubAdminUsersUseCase) SetRole(_ context.Context, _ uuid.UUID, userID uuid.UUID, role auth.Role) error {
	s.calls++
	s.gotRoleUserID = userID
	s.gotRole = &role
	return s.err
}

func (s *stubAdminUsersUseCase) ResetPassword(_ context.Context, _ uuid.UUID, userID uuid.UUID, temporaryPassword string) error {
	s.calls++
	s.gotResetUserID = userID
	s.gotResetPassword = temporaryPassword
	return s.err
}
