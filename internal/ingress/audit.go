// ============================================================================
// Project Aegis — Audit Logging Middleware
//
// Structured audit log for all API calls. Every request generates a JSON
// audit entry with: timestamp, tenant, method, path, status, duration,
// remote address, request ID, and error (if any).
//
// Audit entries are written to slog at INFO level with an "audit" attribute
// for filtering. In production, route these to a dedicated audit log sink.
// ============================================================================

package ingress

import (
	"log/slog"
	"net/http"
	"time"
)

// AuditEntry represents a single API call audit record.
type AuditEntry struct {
	Timestamp  string `json:"timestamp"`
	TenantID   string `json:"tenant_id,omitempty"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	Status     int    `json:"status"`
	DurationMs int64  `json:"duration_ms"`
	RemoteAddr string `json:"remote_addr"`
	RequestID  string `json:"request_id,omitempty"`
	Error      string `json:"error,omitempty"`
	UserAgent  string `json:"user_agent,omitempty"`
}

// AuditMiddleware returns an http.Handler that logs every API call.
func AuditMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(sw, r)

		tenantID := r.Header.Get("X-Tenant-ID")
		if tenantID == "" {
			tenantID = extractTenantFromPath(r.URL.Path)
		}

		entry := AuditEntry{
			Timestamp:  start.UTC().Format(time.RFC3339Nano),
			TenantID:   tenantID,
			Method:     r.Method,
			Path:       r.URL.Path,
			Status:     sw.status,
			DurationMs: time.Since(start).Milliseconds(),
			RemoteAddr: r.RemoteAddr,
			RequestID:  r.Header.Get("X-Request-ID"),
			UserAgent:  r.Header.Get("User-Agent"),
		}

		if sw.status >= 400 {
			entry.Error = r.URL.Path // placeholder; real error extracted in wrap()
		}

		attrs := []slog.Attr{
			slog.String("audit_timestamp", entry.Timestamp),
			slog.String("tenant_id", entry.TenantID),
			slog.String("method", entry.Method),
			slog.String("path", entry.Path),
			slog.Int("status", entry.Status),
			slog.Int64("duration_ms", entry.DurationMs),
			slog.String("remote_addr", entry.RemoteAddr),
		}
		if entry.RequestID != "" {
			attrs = append(attrs, slog.String("request_id", entry.RequestID))
		}
		if entry.UserAgent != "" {
			attrs = append(attrs, slog.String("user_agent", entry.UserAgent))
		}
		if entry.Error != "" {
			attrs = append(attrs, slog.String("error", entry.Error))
		}

		logger.LogAttrs(r.Context(), slog.LevelInfo, "audit", attrs...)
	})
}

// extractTenantFromPath attempts to extract a tenant ID from the URL path.
// Falls back to empty string if not found.
func extractTenantFromPath(path string) string {
	// For now, return empty; tenant is identified via X-Tenant-ID header.
	return ""
}
