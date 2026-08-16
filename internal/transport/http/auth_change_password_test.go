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

func TestAuthChangePasswordRequiresCSRFAndSucceeds(t *testing.T) {
	change := &stubChangePasswordUseCase{}
	router := newChangePasswordTestRouter(t, change)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/change-password", strings.NewReader(`{
		"currentPassword":"old-pass","newPassword":"new-pass-123"
	}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(csrfHeaderName, "csrf-token")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-token"})
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "csrf-token"})
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", response.Code, response.Body.String())
	}
	if change.gotInput.UserID == uuid.Nil ||
		change.gotInput.CurrentPassword != "old-pass" ||
		change.gotInput.NewPassword != "new-pass-123" {
		t.Fatalf("use case input = %+v", change.gotInput)
	}
}

func TestAuthChangePasswordRejectsMissingCSRF(t *testing.T) {
	change := &stubChangePasswordUseCase{}
	router := newChangePasswordTestRouter(t, change)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/change-password", strings.NewReader(`{
		"currentPassword":"old-pass","newPassword":"new-pass-123"
	}`))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-token"})
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d body = %s, want 403", response.Code, response.Body.String())
	}
	if change.calls != 0 {
		t.Fatalf("use case calls = %d, want 0", change.calls)
	}
}

func TestAuthChangePasswordMapsUnauthorizedUseCaseError(t *testing.T) {
	change := &stubChangePasswordUseCase{err: apperror.NewWithMessage(apperror.CodeUnauthorized, "用户名或密码错误")}
	router := newChangePasswordTestRouter(t, change)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/change-password", strings.NewReader(`{
		"currentPassword":"wrong","newPassword":"new-pass-123"
	}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(csrfHeaderName, "csrf-token")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-token"})
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "csrf-token"})
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body = %s, want 401", response.Code, response.Body.String())
	}
}

func newChangePasswordTestRouter(t *testing.T, change changePasswordUseCase) *gin.Engine {
	t.Helper()
	userID := uuid.New()
	sessions := &stubSessionUseCase{identity: auth.Identity{
		User: auth.User{ID: userID, Status: auth.UserStatusActive},
		Session: auth.Session{
			ID: uuid.New(), CSRFTokenHash: auth.HashToken("csrf-token"),
			IdleExpiresAt: time.Now().Add(time.Hour), AbsoluteExpiresAt: time.Now().Add(12 * time.Hour),
		},
	}}
	routes, err := NewAuthRoutes(&stubLoginUseCase{}, sessions, change, CookieSettings{}, []string{testOrigin})
	if err != nil {
		t.Fatalf("NewAuthRoutes(): %v", err)
	}
	router := NewRouter(zap.NewNop(), func(ctx context.Context) error { return nil }, routes)
	return router
}

type stubChangePasswordUseCase struct {
	gotInput auth.ChangePasswordInput
	calls    int
	err      error
}

func (s *stubChangePasswordUseCase) ChangePassword(_ context.Context, input auth.ChangePasswordInput) error {
	s.calls++
	s.gotInput = input
	return s.err
}
