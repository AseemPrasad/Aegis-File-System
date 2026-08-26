// ============================================================================
// Project Aegis — Chaos Engineering Tests
//
// These tests verify resilience under failure conditions:
//   - Database connection failure -> graceful degradation
//   - Panic recovery -> 500, not crash
//   - Max body size enforcement
//   - Invalid JSON handling
//   - Concurrent load stability
//   - Rapid fire resilience
//
// Run with: go test -run TestChaos -v -timeout 120s ./internal/ingress/
// ============================================================================

package ingress

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

// TestChaos_DatabaseDown verifies behavior when database is unreachable.
func TestChaos_DatabaseDown(t *testing.T) {
	store := NewFakeStore()
	store.SetPingErr(fmt.Errorf("connection refused"))

	metrics := NewIngestMetrics(nil)
	events := &NoopBus{}
	cfg := Config{APIToken: "chaos-token", EndpointID: "chaos-edge", MaxBodySize: 100 << 20}
	srv := NewIngressServer(store, &stubTokenSigner{}, nil, events, metrics, cfg, nil)
	mux := http.NewServeMux()
	srv.SetupRoutes(mux)

	// Readyz should fail
	req := httptest.NewRequest("GET", "/readyz", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when DB down, got %d", w.Code)
	}

	// Healthz should still work
	req = httptest.NewRequest("GET", "/healthz", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for healthz even when DB down, got %d", w.Code)
	}
}

// TestChaos_PanicRecovery verifies panics don't crash the server.
func TestChaos_PanicRecovery(t *testing.T) {
	store := NewFakeStore()
	tenantID := uuid.New()
	store.SeedTenant(tenantID, 10000, 0)
	metrics := NewIngestMetrics(nil)
	events := &NoopBus{}
	cfg := Config{APIToken: "chaos-token", EndpointID: "chaos-edge", MaxBodySize: 100 << 20}

	srv := NewIngressServer(store, &panicSigner{}, nil, events, metrics, cfg, nil)
	mux := http.NewServeMux()
	srv.SetupRoutes(mux)

	body := mustJSON(InitiateRequest{
		TenantID:  tenantID.String(),
		FileName:  "test.txt",
		TotalSize: 1024,
		Chunks: []ChunkInfo{
			{BlockHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", SizeBytes: 1024},
		},
	})
	req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer chaos-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 after panic, got %d", w.Code)
	}
}

// TestChaos_MaxBodySize verifies oversized requests are rejected.
func TestChaos_MaxBodySize(t *testing.T) {
	store := NewFakeStore()
	metrics := NewIngestMetrics(nil)
	events := &NoopBus{}
	cfg := Config{APIToken: "chaos-token", EndpointID: "chaos-edge", MaxBodySize: 1024}
	srv := NewIngressServer(store, &stubTokenSigner{}, nil, events, metrics, cfg, nil)
	mux := http.NewServeMux()
	srv.SetupRoutes(mux)

	bigBody := bytes.Repeat([]byte("x"), 2048)
	req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", bytes.NewReader(bigBody))
	req.Header.Set("Authorization", "Bearer chaos-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Fatal("expected rejection for oversized body, got 200")
	}
}

// TestChaos_InvalidJSON verifies malformed JSON is handled gracefully.
func TestChaos_InvalidJSON(t *testing.T) {
	store := NewFakeStore()
	metrics := NewIngestMetrics(nil)
	events := &NoopBus{}
	cfg := Config{APIToken: "chaos-token", EndpointID: "chaos-edge", MaxBodySize: 100 << 20}
	srv := NewIngressServer(store, &stubTokenSigner{}, nil, events, metrics, cfg, nil)
	mux := http.NewServeMux()
	srv.SetupRoutes(mux)

	req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", bytes.NewReader([]byte("{invalid json")))
	req.Header.Set("Authorization", "Bearer chaos-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d", w.Code)
	}
}

// TestChaos_ConcurrentRequests verifies no data races under load.
func TestChaos_ConcurrentRequests(t *testing.T) {
	store := NewFakeStore()
	metrics := NewIngestMetrics(nil)
	events := &NoopBus{}
	cfg := Config{APIToken: "chaos-token", EndpointID: "chaos-edge", MaxBodySize: 100 << 20}
	srv := NewIngressServer(store, &stubTokenSigner{}, nil, events, metrics, cfg, nil)
	mux := http.NewServeMux()
	srv.SetupRoutes(mux)

	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			blockHash := fmt.Sprintf("%064x", idx)
			body := mustJSON(InitiateRequest{
				TenantID:  fmt.Sprintf("tenant-%d", idx%10),
				FileName:  fmt.Sprintf("file-%d.bin", idx),
				TotalSize: 1024,
				Chunks: []ChunkInfo{
					{BlockHash: blockHash, SizeBytes: 1024},
				},
			})
			req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer chaos-token")
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
		}(i)
	}
	wg.Wait()
}

// TestChaos_RapidFire verifies server handles burst without crashing.
func TestChaos_RapidFire(t *testing.T) {
	store := NewFakeStore()
	tenantID := uuid.New()
	store.SeedTenant(tenantID, 100000, 0)
	metrics := NewIngestMetrics(nil)
	events := &NoopBus{}
	cfg := Config{APIToken: "chaos-token", EndpointID: "chaos-edge", MaxBodySize: 100 << 20}
	srv := NewIngressServer(store, &stubTokenSigner{}, nil, events, metrics, cfg, nil)
	mux := http.NewServeMux()
	srv.SetupRoutes(mux)

	var successCount, errorCount atomic.Int32
	for i := 0; i < 1000; i++ {
		blockHash := fmt.Sprintf("%064x", i)
		body := mustJSON(InitiateRequest{
			TenantID:  tenantID.String(),
			FileName:  "rapid.bin",
			TotalSize: 1024,
			Chunks: []ChunkInfo{
				{BlockHash: blockHash, SizeBytes: 1024},
			},
		})
		req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer chaos-token")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code == 200 {
			successCount.Add(1)
		} else {
			errorCount.Add(1)
		}
	}

	t.Logf("Rapid fire: %d success, %d errors out of 1000", successCount.Load(), errorCount.Load())
	if successCount.Load() == 0 {
		t.Fatal("all requests failed -- server may be broken")
	}
}

// panicSigner is a TokenSigner that panics on every call.
type panicSigner struct{}

func (ps *panicSigner) GeneratePreSignedURL(_ context.Context, _, _, _ string) (string, error) {
	panic("intentional panic for chaos testing")
}
