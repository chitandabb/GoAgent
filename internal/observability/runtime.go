package observability

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

type RuntimeConfig struct {
	ServiceName   string
	Environment   string
	Endpoint      string
	Headers       map[string]string
	SampleRatio   float64
	ExportTimeout time.Duration
	ErrorHandler  otel.ErrorHandler
}

type Runtime struct {
	provider             *sdktrace.TracerProvider
	previousProvider     trace.TracerProvider
	previousErrorHandler otel.ErrorHandler
}

func NewRuntime(ctx context.Context, cfg RuntimeConfig) (*Runtime, error) {
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(cfg.Endpoint),
		otlptracehttp.WithHeaders(cfg.Headers),
		otlptracehttp.WithTimeout(cfg.ExportTimeout),
	)
	if err != nil {
		return nil, err
	}
	return NewRuntimeWithExporter(cfg, exporter)
}

func NewRuntimeWithExporter(cfg RuntimeConfig, exporter sdktrace.SpanExporter) (*Runtime, error) {
	if exporter == nil {
		return nil, errors.New("trace exporter is required")
	}
	res, err := resource.New(context.Background(), resource.WithAttributes(
		attribute.String("service.name", cfg.ServiceName),
		attribute.String("deployment.environment.name", cfg.Environment),
	))
	if err != nil {
		return nil, err
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
		sdktrace.WithBatcher(exporter, sdktrace.WithExportTimeout(cfg.ExportTimeout)),
	)
	previousProvider := otel.GetTracerProvider()
	previousErrorHandler := otel.GetErrorHandler()
	if cfg.ErrorHandler != nil {
		otel.SetErrorHandler(cfg.ErrorHandler)
	}
	otel.SetTracerProvider(provider)
	return &Runtime{
		provider: provider, previousProvider: previousProvider,
		previousErrorHandler: previousErrorHandler,
	}, nil
}

func (r *Runtime) Shutdown(ctx context.Context) error {
	if r == nil || r.provider == nil {
		return nil
	}
	err := r.provider.Shutdown(ctx)
	otel.SetTracerProvider(r.previousProvider)
	otel.SetErrorHandler(r.previousErrorHandler)
	return err
}

type rateLimitedErrorHandler struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
	notify   func(error)
	now      func() time.Time
}

func NewRateLimitedErrorHandler(interval time.Duration, notify func(error)) otel.ErrorHandler {
	return &rateLimitedErrorHandler{interval: interval, notify: notify, now: time.Now}
}

func (h *rateLimitedErrorHandler) Handle(err error) {
	if h == nil || err == nil || h.notify == nil || h.interval <= 0 {
		return
	}
	now := h.now()
	h.mu.Lock()
	if now.Before(h.next) {
		h.mu.Unlock()
		return
	}
	h.next = now.Add(h.interval)
	h.mu.Unlock()
	h.notify(err)
}
