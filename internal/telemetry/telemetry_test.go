package telemetry_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aegis-dev/aegis/internal/telemetry"
	"go.opentelemetry.io/otel"
)

func TestTracingMiddleware(t *testing.T) {
	_, err := telemetry.SetupTracing("aegis-test-service")
	if err != nil {
		t.Fatalf("failed to setup tracing: %v", err)
	}

	handler := telemetry.HTTPTracingMiddleware(func(w http.ResponseWriter, r *http.Request) {
		traceID, _ := telemetry.GetTraceAndSpanIDs(r.Context())
		if traceID == "" {
			t.Error("expected valid traceID in request context")
		}
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 OK", rec.Code)
	}
	if rec.Header().Get("X-Trace-ID") == "" {
		t.Error("expected X-Trace-ID header in response")
	}
}

func TestW3CContextInject(t *testing.T) {
	ctx, span := telemetry.Tracer.Start(context.Background(), "test-span")
	defer span.End()

	reqHeader := http.Header{}
	telemetry.InjectTraceContext(ctx, reqHeader)

	if reqHeader.Get("traceparent") == "" {
		t.Error("expected traceparent header to be injected")
	}
}

func TestTelemetryMetricCreation(t *testing.T) {
	metrics := telemetry.NewDeepMetrics(otel.GetMeterProvider())
	if metrics == nil {
		t.Error("expected non-nil DeepMetrics")
	}
}
