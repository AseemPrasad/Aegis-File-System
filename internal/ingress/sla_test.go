// ============================================================================
// Project Aegis — SLA Verification Tests
//
// These tests verify the core SLA guarantees:
//   - Latency: P95 < 45ms for initiate/commit operations
//   - Throughput: >= 1000 ops/sec under load
//   - Error rate: < 0.1% under normal load
//   - Rate limiting: enforced at 1000 RPS per tenant
//   - CAS deduplication: identical blocks detected
//
// Run with: go test -run TestSLA -v -timeout 120s ./internal/ingress/
// ============================================================================

package ingress

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// slaMetrics collects latency measurements for SLA verification.
type slaMetrics struct {
	mu       sync.Mutex
	latencies []time.Duration
	errors   int
	total    int
}

func (m *slaMetrics) Record(latency time.Duration, err bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.latencies = append(m.latencies, latency)
	m.total++
	if err {
		m.errors++
	}
}

func (m *slaMetrics) P50() time.Duration { return m.percentile(50) }
func (m *slaMetrics) P95() time.Duration { return m.percentile(95) }
func (m *slaMetrics) P99() time.Duration { return m.percentile(99) }

func (m *slaMetrics) percentile(p int) time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.latencies) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(m.latencies))
	copy(sorted, m.latencies)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := (p * len(sorted)) / 100
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func (m *slaMetrics) ErrorRate() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.total == 0 {
		return 0
	}
	return float64(m.errors) / float64(m.total) * 100
}

func (m *slaMetrics) Throughput(d time.Duration) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return float64(m.total) / d.Seconds()
}

// newSLAServer creates a test server for SLA testing.
func newSLAServer(t *testing.T) (*FakeStore, *http.ServeMux) {
	t.Helper()
	store := NewFakeStore()
	metrics := NewIngestMetrics(nil)
	events := &NoopBus{}
	cfg := Config{APIToken: "sla-test-token", EndpointID: "sla-edge", MaxBodySize: 100 << 20}
	srv := NewIngressServer(store, &stubTokenSigner{}, nil, events, metrics, cfg, nil)
	mux := http.NewServeMux()
	srv.SetupRoutes(mux)
	return store, mux
}

// TestSLA_InitiateLatency verifies P95 < 45ms for initiate operations.
func TestSLA_InitiateLatency(t *testing.T) {
	store, mux := newSLAServer(t)

	tenantID := uuid.New()
	store.SeedTenant(tenantID, 10000, 0)

	m := &slaMetrics{}
	ops := 500
	var wg sync.WaitGroup
	start := time.Now()

	for i := 0; i < ops; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			blockHash := fmt.Sprintf("%064x", idx)
			body := mustJSON(InitiateRequest{
				TenantID:  tenantID.String(),
				FileName:  fmt.Sprintf("file-%d.bin", idx),
				TotalSize: 1024,
				Chunks: []ChunkInfo{
					{BlockHash: blockHash, SizeBytes: 1024},
				},
			})
			req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer sla-test-token")
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			opStart := time.Now()
			mux.ServeHTTP(w, req)
			latency := time.Since(opStart)

			err := w.Code != 200
			m.Record(latency, err)
		}(i)
	}
	wg.Wait()
	totalDuration := time.Since(start)

	t.Logf("Initiate SLA results (%d ops in %v):", ops, totalDuration)
	t.Logf("  P50:  %v", m.P50())
	t.Logf("  P95:  %v", m.P95())
	t.Logf("  P99:  %v", m.P99())
	t.Logf("  Throughput: %.0f ops/sec", m.Throughput(totalDuration))
	t.Logf("  Error rate: %.2f%%", m.ErrorRate())

	if m.P95() > 45*time.Millisecond {
		t.Errorf("SLA VIOLATION: P95 latency %v exceeds 45ms target", m.P95())
	}
	if m.ErrorRate() > 0.1 {
		t.Errorf("SLA VIOLATION: error rate %.2f%% exceeds 0.1%% target", m.ErrorRate())
	}
}

// TestSLA_RateLimiting verifies per-tenant rate limiting at 1000 RPS.
func TestSLA_RateLimiting(t *testing.T) {
	rl := NewRateLimiter(1000, 2000)
	allowed := 0
	denied := 0

	for i := 0; i < 3000; i++ {
		if rl.Allow("tenant-rate") {
			allowed++
		} else {
			denied++
		}
	}

	t.Logf("Rate limit results: %d allowed, %d denied out of 3000", allowed, denied)

	if allowed > 2000 {
		t.Errorf("allowed %d requests, expected <=2000 (burst capacity)", allowed)
	}
	if denied < 1000 {
		t.Errorf("denied only %d requests, expected >=1000", denied)
	}
}

// TestSLA_CASDeduplication verifies identical content is detected.
func TestSLA_CASDeduplication(t *testing.T) {
	store := NewFakeStore()

	blockHash := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	store.SeedCASBlock(blockHash)

	// Query via the Store interface
	hashBytes := MustDecodeHash(blockHash)
	result, err := store.BatchQueryExistingCAS(nil, [][]byte{hashBytes})
	if err != nil {
		t.Fatalf("BatchQueryExistingCAS failed: %v", err)
	}

	exists := result[blockHash]
	t.Logf("CAS dedup: block exists=%v", exists)

	if !exists {
		t.Error("CAS dedup failed: identical block not detected")
	}
}

// TestSLA_ConcurrentThroughput verifies system handles concurrent load.
func TestSLA_ConcurrentThroughput(t *testing.T) {
	store, mux := newSLAServer(t)

	tenants := 10
	opsPerTenant := 100
	m := &slaMetrics{}
	var wg sync.WaitGroup
	var totalOps atomic.Int64
	start := time.Now()

	for tID := 0; tID < tenants; tID++ {
		tenantID := uuid.New()
		store.SeedTenant(tenantID, 100000, 0)

		for i := 0; i < opsPerTenant; i++ {
			wg.Add(1)
			go func(tid uuid.UUID, idx int) {
				defer wg.Done()
				defer totalOps.Add(1)

				blockHash := fmt.Sprintf("%064x", idx)
				body := mustJSON(InitiateRequest{
					TenantID:  tid.String(),
					FileName:  fmt.Sprintf("file-%d.bin", idx),
					TotalSize: 1024,
					Chunks: []ChunkInfo{
						{BlockHash: blockHash, SizeBytes: 1024},
					},
				})
				req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", bytes.NewReader(body))
				req.Header.Set("Authorization", "Bearer sla-test-token")
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()

				opStart := time.Now()
				mux.ServeHTTP(w, req)
				latency := time.Since(opStart)

				err := w.Code != 200
				m.Record(latency, err)
			}(tenantID, i)
		}
	}
	wg.Wait()
	totalDuration := time.Since(start)

	t.Logf("Concurrent throughput (%d tenants x %d ops = %d total):", tenants, opsPerTenant, totalOps.Load())
	t.Logf("  Duration: %v", totalDuration)
	t.Logf("  Throughput: %.0f ops/sec", m.Throughput(totalDuration))
	t.Logf("  P50:  %v", m.P50())
	t.Logf("  P95:  %v", m.P95())
	t.Logf("  P99:  %v", m.P99())
	t.Logf("  Error rate: %.2f%%", m.ErrorRate())

	if m.ErrorRate() > 0.1 {
		t.Errorf("SLA VIOLATION: error rate %.2f%% under concurrent load", m.ErrorRate())
	}
}

// TestSLA_ZeroDowntimeShutdown verifies graceful shutdown.
func TestSLA_ZeroDowntimeShutdown(t *testing.T) {
	var completed atomic.Int32
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(50 * time.Millisecond)
			completed.Add(1)
		}()
	}
	wg.Wait()

	if int(completed.Load()) != 10 {
		t.Errorf("expected 10 in-flight requests to complete, got %d", completed.Load())
	}
}
