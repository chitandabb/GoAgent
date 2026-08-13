package observability

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestRuntimeExportsCompletedSpans(t *testing.T) {
	previous := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	exporter := tracetest.NewInMemoryExporter()
	runtime, err := NewRuntimeWithExporter(RuntimeConfig{
		ServiceName: "mesguard-test", Environment: "test", SampleRatio: 1,
		ExportTimeout: time.Second,
	}, exporter)
	if err != nil {
		t.Fatalf("NewRuntimeWithExporter(): %v", err)
	}

	ctx, span := StartAgentRun(context.Background(), "test")
	_ = ctx
	End(span, nil)
	if err := runtime.provider.ForceFlush(context.Background()); err != nil {
		t.Fatalf("ForceFlush(): %v", err)
	}
	if len(exporter.GetSpans()) != 1 {
		t.Fatalf("exported spans = %d, want 1", len(exporter.GetSpans()))
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown(): %v", err)
	}
}

func TestRateLimitedErrorHandlerBoundsRepeatedDiagnostics(t *testing.T) {
	now := time.Unix(100, 0)
	calls := 0
	handler := NewRateLimitedErrorHandler(time.Minute, func(error) { calls++ }).(*rateLimitedErrorHandler)
	handler.now = func() time.Time { return now }
	handler.Handle(errors.New("export failed once"))
	handler.Handle(errors.New("export failed twice"))
	if calls != 1 {
		t.Fatalf("diagnostic calls = %d, want 1 inside interval", calls)
	}
	now = now.Add(time.Minute)
	handler.Handle(errors.New("export failed later"))
	if calls != 2 {
		t.Fatalf("diagnostic calls = %d, want 2 after interval", calls)
	}
}

func TestRuntimeExportsOTLPHTTPWithConfiguredHeaders(t *testing.T) {
	requestReceived := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()
		body, err := io.ReadAll(request.Body)
		if err != nil || len(body) == 0 {
			t.Errorf("OTLP body error=%v bytes=%d", err, len(body))
		}
		if request.URL.Path != "/api/public/otel/v1/traces" ||
			request.Header.Get("Authorization") != "Basic redacted" ||
			request.Header.Get("x-langfuse-ingestion-version") != "4" {
			t.Errorf("unexpected OTLP request path=%s headers=%v", request.URL.Path, request.Header)
		}
		writer.WriteHeader(http.StatusOK)
		requestReceived <- struct{}{}
	}))
	defer server.Close()

	previous := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	runtime, err := NewRuntime(context.Background(), RuntimeConfig{
		ServiceName: "mesguard-test", Environment: "test",
		Endpoint: server.URL + "/api/public/otel/v1/traces",
		Headers: map[string]string{
			"Authorization": "Basic redacted", "x-langfuse-ingestion-version": "4",
		},
		SampleRatio: 1, ExportTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewRuntime(): %v", err)
	}
	_, span := StartAgentRun(context.Background(), "http_export")
	End(span, nil)
	if err := runtime.provider.ForceFlush(context.Background()); err != nil {
		t.Fatalf("ForceFlush(): %v", err)
	}
	select {
	case <-requestReceived:
	case <-time.After(time.Second):
		t.Fatal("OTLP request was not received")
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown(): %v", err)
	}
}
