package telemetry

import (
	"context"
	"log/slog"
)

// TraceLoggingHandler wraps an existing slog.Handler to inject trace_id and span_id into JSON logs.
type TraceLoggingHandler struct {
	slog.Handler
}

func NewTraceLoggingHandler(h slog.Handler) *TraceLoggingHandler {
	return &TraceLoggingHandler{Handler: h}
}

func (h *TraceLoggingHandler) Handle(ctx context.Context, r slog.Record) error {
	traceID, spanID := GetTraceAndSpanIDs(ctx)
	if traceID != "" {
		r.AddAttrs(slog.String("trace_id", traceID), slog.String("span_id", spanID))
	}
	return h.Handler.Handle(ctx, r)
}
