// ============================================================================
// Project Aegis — Audit Middleware Tests
// ============================================================================

package ingress

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuditMiddleware_LogsRequest(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})
	handler := AuditMiddleware(logger, inner)

	req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", nil)
	req.Header.Set("X-Tenant-ID", "tenant-abc")
	req.Header.Set("X-Request-ID", "req-123")
	req.Header.Set("User-Agent", "test-agent/1.0")
	req.RemoteAddr = "10.0.1.50:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	output := buf.String()
	if !strings.Contains(output, `"tenant_id":"tenant-abc"`) {
		t.Fatal("audit log missing tenant_id")
	}
	if !strings.Contains(output, `"method":"POST"`) {
		t.Fatal("audit log missing method")
	}
	if !strings.Contains(output, `"status":200`) {
		t.Fatal("audit log missing status")
	}
	if !strings.Contains(output, `"request_id":"req-123"`) {
		t.Fatal("audit log missing request_id")
	}
	if !strings.Contains(output, `"remote_addr":"10.0.1.50:12345"`) {
		t.Fatal("audit log missing remote_addr")
	}
	if !strings.Contains(output, `"audit"`) {
		t.Fatal("audit log missing 'audit' message")
	}
}

func TestAuditMiddleware_ErrorStatus(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"unauthorized"}`))
	})
	handler := AuditMiddleware(logger, inner)

	req := httptest.NewRequest("POST", "/api/v1/ingest/commit", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	output := buf.String()
	if !strings.Contains(output, `"status":401`) {
		t.Fatal("audit log should record 401 status")
	}
}

func TestAuditMiddleware_NoTenantID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := AuditMiddleware(logger, inner)

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	output := buf.String()
	// Should have empty tenant_id (or omitted)
	if strings.Contains(output, `"tenant_id":"tenant-`) {
		t.Fatal("audit log should not have fake tenant_id for health endpoint")
	}
}
