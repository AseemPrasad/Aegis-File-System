package telemetry

import (
	"context"
	"fmt"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

var Tracer = otel.Tracer("aegis-telemetry-engine")

// SetupTracing initializes OpenTelemetry W3C trace context propagation.
func SetupTracing(serviceName string) (*sdktrace.TracerProvider, error) {
	tp := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	return tp, nil
}

// HTTPTracingMiddleware extracts W3C traceparent headers or starts a new root span.
func HTTPTracingMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := Tracer.Start(ctx, fmt.Sprintf("HTTP %s %s", r.Method, r.URL.Path))
		defer span.End()

		w.Header().Set("X-Trace-ID", span.SpanContext().TraceID().String())
		next(w, r.WithContext(ctx))
	}
}

// InjectTraceContext injects active trace context headers into outgoing requests.
func InjectTraceContext(ctx context.Context, header http.Header) {
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(header))
}

// GetTraceAndSpanIDs returns the current trace_id and span_id strings from context.
func GetTraceAndSpanIDs(ctx context.Context) (string, string) {
	span := trace.SpanFromContext(ctx)
	if !span.SpanContext().IsValid() {
		return "", ""
	}
	return span.SpanContext().TraceID().String(), span.SpanContext().SpanID().String()
}
