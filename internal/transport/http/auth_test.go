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

	"github.com/google/uuid"
	"go.uber.org/zap"
)

const testOrigin = "http://localhost:5173"

func TestAuthLoginSetsSecureCookieContract(t *testing.T) {
	expiresAt := time.Now().Add(12 * time.Hour).UTC().Truncate(time.Second)
	login := &stubLoginUseCase{result: auth.LoginResult{
		User: auth.User{
			ID: uuid.New(), Username: "analyst01", DisplayName: "售后分析员",
			Role: auth.RoleAnalyst, Status: auth.UserStatusActive,
		},
		SessionToken:      "raw-session-token",
		CSRFToken:         "raw-csrf-token",
		IdleExpiresAt:     expiresAt.Add(-10 * time.Hour),
		AbsoluteExpiresAt: expiresAt,
	}}
	router := newAuthTestRouter(t, login, &stubSessionUseCase{}, CookieSettings{}, []string{testOrigin})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{
		"username":"analyst01","password":"secret"
	}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", testOrigin)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("cookie count = %d, want 2", len(cookies))
	}
	sessionCookie := cookieByName(t, cookies, sessionCookieName)
	csrfCookie := cookieByName(t, cookies, csrfCookieName)
	if !sessionCookie.HttpOnly || csrfCookie.HttpOnly {
		t.Fatalf("HttpOnly contract session=%v csrf=%v", sessionCookie.HttpOnly, csrfCookie.HttpOnly)
	}
	if sessionCookie.SameSite != http.SameSiteLaxMode || csrfCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("SameSite contract session=%v csrf=%v", sessionCookie.SameSite, csrfCookie.SameSite)
	}
	if sessionCookie.Value != "raw-session-token" || csrfCookie.Value != "raw-csrf-token" {
		t.Fatalf("cookie values = %q/%q", sessionCookie.Value, csrfCookie.Value)
	}
	body := response.Body.String()
	if !strings.Contains(body, `"csrfToken":"raw-csrf-token"`) || strings.Contains(body, "raw-session-token") {
		t.Fatalf("response token contract violated: %s", body)
	}
	if strings.Contains(body, "PasswordHash") || strings.Contains(body, "passwordHash") {
		t.Fatalf("response leaked password hash: %s", body)
	}
}

func TestAuthLoginRejectsUntrustedOriginBeforeUseCase(t *testing.T) {
	login := &stubLoginUseCase{}
	router := newAuthTestRouter(t, login, &stubSessionUseCase{}, CookieSettings{}, []string{testOrigin})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"analyst01","password":"secret"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://evil.example")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d body = %s, want 403", response.Code, response.Body.String())
	}
	if login.calls != 0 {
		t.Fatalf("login calls = %d, want 0", login.calls)
	}
}

func TestAuthMeRestoresUserAndCSRFToken(t *testing.T) {
	userID := uuid.New()
	sessions := &stubSessionUseCase{identity: auth.Identity{
		User: auth.User{ID: userID, Username: "analyst01", Role: auth.RoleAnalyst, Status: auth.UserStatusActive},
		Session: auth.Session{
			ID: uuid.New(), CSRFTokenHash: auth.HashToken("csrf-token"),
			IdleExpiresAt: time.Now().Add(time.Hour), AbsoluteExpiresAt: time.Now().Add(12 * time.Hour),
		},
	}}
	router := newAuthTestRouter(t, &stubLoginUseCase{}, sessions, CookieSettings{}, []string{testOrigin})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-token"})
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "csrf-token"})
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", response.Code, response.Body.String())
	}
	if sessions.gotSessionToken != "session-token" || sessions.gotCSRFToken != "csrf-token" {
		t.Fatalf("restored tokens = %q/%q", sessions.gotSessionToken, sessions.gotCSRFToken)
	}
	if !strings.Contains(response.Body.String(), userID.String()) || !strings.Contains(response.Body.String(), `"csrfToken":"csrf-token"`) {
		t.Fatalf("body = %s", response.Body.String())
	}
}

func TestAuthLogoutRequiresCSRFAndIsIdempotentWithoutSession(t *testing.T) {
	identity := auth.Identity{
		User:    auth.User{ID: uuid.New(), Status: auth.UserStatusActive},
		Session: auth.Session{ID: uuid.New(), CSRFTokenHash: auth.HashToken("csrf-token")},
	}

	t.Run("valid csrf revokes session", func(t *testing.T) {
		sessions := &stubSessionUseCase{identity: identity}
		router := newAuthTestRouter(t, &stubLoginUseCase{}, sessions, CookieSettings{}, []string{testOrigin})
		request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
		request.Header.Set("Origin", testOrigin)
		request.Header.Set(csrfHeaderName, "csrf-token")
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-token"})
		request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "csrf-token"})
		response := httptest.NewRecorder()

		router.ServeHTTP(response, request)

		if response.Code != http.StatusOK || !sessions.loggedOut {
			t.Fatalf("status = %d loggedOut = %v body = %s", response.Code, sessions.loggedOut, response.Body.String())
		}
		for _, cookie := range response.Result().Cookies() {
			if cookie.MaxAge != -1 {
				t.Fatalf("cleared cookie %s MaxAge = %d, want -1", cookie.Name, cookie.MaxAge)
			}
		}
	})

	t.Run("invalid csrf is forbidden", func(t *testing.T) {
		sessions := &stubSessionUseCase{identity: identity}
		router := newAuthTestRouter(t, &stubLoginUseCase{}, sessions, CookieSettings{}, []string{testOrigin})
		request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
		request.Header.Set("Origin", testOrigin)
		request.Header.Set(csrfHeaderName, "wrong-token")
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-token"})
		request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "csrf-token"})
		response := httptest.NewRecorder()

		router.ServeHTTP(response, request)

		if response.Code != http.StatusForbidden || sessions.loggedOut {
			t.Fatalf("status = %d loggedOut = %v, want forbidden", response.Code, sessions.loggedOut)
		}
	})

	t.Run("missing session is successful", func(t *testing.T) {
		router := newAuthTestRouter(t, &stubLoginUseCase{}, &stubSessionUseCase{}, CookieSettings{}, []string{testOrigin})
		request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
		request.Header.Set("Origin", testOrigin)
		response := httptest.NewRecorder()

		router.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s, want idempotent 200", response.Code, response.Body.String())
		}
	})
}

func newAuthTestRouter(
	t *testing.T,
	login loginUseCase,
	sessions sessionUseCase,
	cookies CookieSettings,
	origins []string,
) http.Handler {
	t.Helper()
	routes, err := NewAuthRoutes(login, sessions, cookies, origins)
	if err != nil {
		t.Fatalf("NewAuthRoutes(): %v", err)
	}
	return NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
}

func cookieByName(t *testing.T, cookies []*http.Cookie, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("cookie %q not found", name)
	return nil
}

type stubLoginUseCase struct {
	result auth.LoginResult
	err    error
	calls  int
}

func (s *stubLoginUseCase) Login(context.Context, auth.LoginInput) (auth.LoginResult, error) {
	s.calls++
	return s.result, s.err
}

type stubSessionUseCase struct {
	identity        auth.Identity
	authenticateErr error
	validateErr     error
	logoutErr       error
	gotSessionToken string
	gotCSRFToken    string
	loggedOut       bool
}

func (s *stubSessionUseCase) Authenticate(_ context.Context, rawToken string) (auth.Identity, error) {
	s.gotSessionToken = rawToken
	return s.identity, s.authenticateErr
}

func (s *stubSessionUseCase) ValidateCSRF(_ auth.Identity, rawToken string) error {
	s.gotCSRFToken = rawToken
	if s.validateErr != nil {
		return s.validateErr
	}
	if rawToken == "" {
		return apperror.New(apperror.CodeForbidden)
	}
	return nil
}

func (s *stubSessionUseCase) Logout(context.Context, auth.Identity) error {
	if s.logoutErr != nil {
		return s.logoutErr
	}
	s.loggedOut = true
	return nil
}
