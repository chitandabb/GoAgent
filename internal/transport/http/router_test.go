package httptransport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/apperror"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func newTestRouter(health HealthCheck) *gin.Engine {
	return NewRouter(zap.NewNop(), health)
}

func TestHealthReturnsUnifiedSuccessResponse(t *testing.T) {
	router := newTestRouter(func(context.Context) error { return nil })
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if !strings.Contains(response.Body.String(), `"code":0`) {
		t.Fatalf("body = %s", response.Body.String())
	}
	if response.Header().Get(requestIDHeader) == "" {
		t.Fatal("request id header is empty")
	}
}

func TestHealthReturnsDependencyError(t *testing.T) {
	router := newTestRouter(func(context.Context) error { return errors.New("postgres down") })
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(response.Body.String(), `"code":50301`) {
		t.Fatalf("body = %s", response.Body.String())
	}
	if strings.Contains(response.Body.String(), "postgres down") {
		t.Fatalf("response leaked internal error: %s", response.Body.String())
	}
}

func TestGlobalErrorMiddlewareConvertsApplicationError(t *testing.T) {
	router := newTestRouter(func(context.Context) error { return nil })
	router.GET("/test-error", func(c *gin.Context) {
		AbortWithError(c, apperror.New(apperror.CodeInvalidArgument))
	})
	request := httptest.NewRequest(http.MethodGet, "/test-error", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	if !strings.Contains(response.Body.String(), `"code":40001`) {
		t.Fatalf("body = %s", response.Body.String())
	}
}

func TestInternalErrorDoesNotLeakCustomMessage(t *testing.T) {
	router := newTestRouter(func(context.Context) error { return nil })
	router.GET("/test-internal-error", func(c *gin.Context) {
		AbortWithError(c, apperror.NewWithMessage(apperror.CodeInternal, "database password leaked"))
	})
	request := httptest.NewRequest(http.MethodGet, "/test-internal-error", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if strings.Contains(response.Body.String(), "database password leaked") {
		t.Fatalf("response leaked internal error: %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"message":"服务器内部错误"`) {
		t.Fatalf("body = %s", response.Body.String())
	}
}

func TestRecoveryReturnsInternalError(t *testing.T) {
	router := newTestRouter(func(context.Context) error { return nil })
	router.GET("/test-panic", func(*gin.Context) {
		panic("unexpected")
	})
	request := httptest.NewRequest(http.MethodGet, "/test-panic", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(response.Body.String(), `"code":50000`) {
		t.Fatalf("body = %s", response.Body.String())
	}
}

func TestUnknownRouteUsesUnifiedResponse(t *testing.T) {
	router := newTestRouter(func(context.Context) error { return nil })
	request := httptest.NewRequest(http.MethodGet, "/unknown", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
	if !strings.Contains(response.Body.String(), `"code":40401`) {
		t.Fatalf("body = %s", response.Body.String())
	}
}

func TestUnsupportedMethodUsesUnifiedResponse(t *testing.T) {
	router := newTestRouter(func(context.Context) error { return nil })
	request := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
	if !strings.Contains(response.Body.String(), `"code":40501`) {
		t.Fatalf("body = %s", response.Body.String())
	}
}

func TestRequestLoggerIncludesRequestContextFields(t *testing.T) {
	core, observed := observer.New(zap.InfoLevel)
	router := NewRouter(zap.New(core), func(context.Context) error { return nil })
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set(requestIDHeader, "request-for-test")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	entries := observed.FilterMessage("HTTP request completed").All()
	if len(entries) != 1 {
		t.Fatalf("request log count = %d, want 1", len(entries))
	}
	fields := entries[0].ContextMap()
	if fields["request_id"] != "request-for-test" {
		t.Fatalf("request_id = %v", fields["request_id"])
	}
	if fields["method"] != http.MethodGet || fields["path"] != "/healthz" {
		t.Fatalf("request fields = %#v", fields)
	}
	if fields["status"] != int64(http.StatusOK) {
		t.Fatalf("status = %v", fields["status"])
	}
}
