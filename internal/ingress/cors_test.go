// ============================================================================
// Project Aegis — CORS Middleware Tests
// ============================================================================

package ingress

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORS_AllowedOrigin(t *testing.T) {
	cfg := CORSConfig{
		AllowedOrigins: []string{"https://app.aegis.dev"},
		AllowedMethods: []string{"GET", "POST"},
		AllowedHeaders: []string{"Authorization", "Content-Type"},
		MaxAge:         3600,
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	})
	handler := CORSMiddleware(cfg, inner)

	req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", nil)
	req.Header.Set("Origin", "https://app.aegis.dev")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "https://app.aegis.dev" {
		t.Fatal("missing or wrong Allow-Origin header")
	}
	if w.Header().Get("Access-Control-Allow-Methods") != "GET, POST" {
		t.Fatal("missing or wrong Allow-Methods header")
	}
	if w.Header().Get("Access-Control-Max-Age") != "3600" {
		t.Fatal("missing or wrong Max-Age header")
	}
}

func TestCORS_DisallowedOrigin(t *testing.T) {
	cfg := CORSConfig{
		AllowedOrigins: []string{"https://app.aegis.dev"},
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := CORSMiddleware(cfg, inner)

	req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", nil)
	req.Header.Set("Origin", "https://evil.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Should pass through (no CORS headers) but not block the request
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("should not set Allow-Origin for disallowed origin")
	}
}

func TestCORS_Preflight(t *testing.T) {
	cfg := CORSConfig{
		AllowedOrigins: []string{"https://app.aegis.dev"},
		AllowedMethods: []string{"GET", "POST", "DELETE"},
		AllowedHeaders: []string{"Authorization"},
		MaxAge:         86400,
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("preflight should not call inner handler")
	})
	handler := CORSMiddleware(cfg, inner)

	req := httptest.NewRequest("OPTIONS", "/api/v1/ingest/initiate", nil)
	req.Header.Set("Origin", "https://app.aegis.dev")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "https://app.aegis.dev" {
		t.Fatal("missing Allow-Origin on preflight")
	}
}

func TestCORS_NoOrigin(t *testing.T) {
	cfg := CORSConfig{
		AllowedOrigins: []string{"https://app.aegis.dev"},
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := CORSMiddleware(cfg, inner)

	// No Origin header → not a CORS request → pass through
	req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("should not set Allow-Origin when no Origin header")
	}
}

func TestCORS_EmptyOrigins_NoOp(t *testing.T) {
	cfg := CORSConfig{
		AllowedOrigins: []string{}, // empty
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := CORSMiddleware(cfg, inner)

	req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", nil)
	req.Header.Set("Origin", "https://app.aegis.dev")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Should pass through without CORS headers
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("empty AllowedOrigins should be no-op")
	}
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestCORS_Credentials(t *testing.T) {
	cfg := CORSConfig{
		AllowedOrigins:   []string{"https://app.aegis.dev"},
		AllowCredentials: true,
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := CORSMiddleware(cfg, inner)

	req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", nil)
	req.Header.Set("Origin", "https://app.aegis.dev")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatal("missing Allow-Credentials header")
	}
}
