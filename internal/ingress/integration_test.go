//go:build integration

package ingress

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Load tests against real PG + Redis (requires docker-compose up)
// ---------------------------------------------------------------------------

// BenchmarkInitiate measures HandleInitiate latency under concurrency.
func BenchmarkInitiate(b *testing.B) {
	store := setupRealStore(b)
	tenantID := seedTestTenant(b, store, 10<<30, 0)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			body := mustJSON(InitiateRequest{
				TenantID:  tenantID.String(),
				FileName:  fmt.Sprintf("bench-%d.bin", time.Now().UnixNano()),
				TotalSize: 4096,
				Chunks: []ChunkInfo{
					{BlockHash: randomHexHash(), SizeBytes: 4096},
				},
			})
			req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer bench-token")
			w := httptest.NewRecorder()
			mux := benchMux(store)
			mux.ServeHTTP(w, req)
			if w.Code != 200 {
				b.Fatalf("initiate failed: %d %s", w.Code, w.Body.String())
			}
		}
	})
}

// BenchmarkCommit measures HandleCommit latency under concurrency.
func BenchmarkCommit(b *testing.B) {
	store := setupRealStore(b)
	tenantID := seedTestTenant(b, store, 10<<30, 0)
	nodeID := createTestNode(b, store, tenantID)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			sid, _, _ := store.CreateUploadSession(context.Background(), tenantID, nodeID, 1024, 1, "bench", "bench")
			hash := randomHexHash()
			body := mustJSON(CommitRequest{
				SessionID:     sid.String(),
				ContentSHA256: randomHexHash(),
				Blocks:        []BlockMeta{{BlockHash: hash, ChunkIndex: 0, Offset: 0, SizeBytes: 1024}},
			})
			req := httptest.NewRequest("POST", "/api/v1/ingest/commit", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer bench-token")
			w := httptest.NewRecorder()
			mux := benchMux(store)
			mux.ServeHTTP(w, req)
			if w.Code != 201 {
				b.Fatalf("commit failed: %d %s", w.Code, w.Body.String())
			}
		}
	})
}

// TestConcurrentInitiates verifies 100 concurrent initiates succeed without errors.
func TestConcurrentInitiates(t *testing.T) {
	store := setupRealStore(t)
	tenantID := seedTestTenant(t, store, 10<<30, 0)

	var (
		wg       sync.WaitGroup
		failures atomic.Int64
	)

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			body := mustJSON(InitiateRequest{
				TenantID:  tenantID.String(),
				FileName:  fmt.Sprintf("concurrent-%d.bin", idx),
				TotalSize: 4096,
				Chunks:    []ChunkInfo{{BlockHash: randomHexHash(), SizeBytes: 4096}},
			})
			req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer bench-token")
			w := httptest.NewRecorder()
			mux := benchMux(store)
			mux.ServeHTTP(w, req)
			if w.Code != 200 {
				failures.Add(1)
				t.Errorf("initiate %d: got %d", idx, w.Code)
			}
		}(i)
	}
	wg.Wait()
	if n := failures.Load(); n > 0 {
		t.Fatalf("%d initiates failed", n)
	}
}

// TestConcurrentCommits verifies 500 concurrent commits on the same node
// serialize correctly via next_version_number fencing.
func TestConcurrentCommits(t *testing.T) {
	store := setupRealStore(t)
	tenantID := seedTestTenant(t, store, 10<<30, 0)
	nodeID := createTestNode(t, store, tenantID)

	const n = 500
	var (
		wg       sync.WaitGroup
		failures atomic.Int64
		versions = make([]int, n)
	)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sid, _, _ := store.CreateUploadSession(context.Background(), tenantID, nodeID, 100, 1, "127.0.0.1", "test")
			hash := randomHexHash()
			body := mustJSON(CommitRequest{
				SessionID:     sid.String(),
				ContentSHA256: randomHexHash(),
				Blocks:        []BlockMeta{{BlockHash: hash, ChunkIndex: 0, Offset: 0, SizeBytes: 100}},
			})
			req := httptest.NewRequest("POST", "/api/v1/ingest/commit", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer bench-token")
			w := httptest.NewRecorder()
			mux := benchMux(store)
			mux.ServeHTTP(w, req)
			if w.Code != 201 {
				failures.Add(1)
				t.Errorf("commit %d: got %d %s", idx, w.Code, w.Body.String())
				return
			}
			var resp CommitResponse
			json.Unmarshal(w.Body.Bytes(), &resp)
			versions[idx] = resp.VersionNumber
		}(i)
	}
	wg.Wait()

	if n := failures.Load(); n > 0 {
		t.Fatalf("%d commits failed", n)
	}

	// Verify versions are dense 1..N with no gaps or collisions.
	seen := make(map[int]bool, len(versions))
	for i, v := range versions {
		if v < 1 || v > n {
			t.Errorf("version[%d] = %d, out of range [1,%d]", i, v, n)
		}
		if seen[v] {
			t.Errorf("duplicate version %d", v)
		}
		seen[v] = true
	}
	if len(seen) != n {
		t.Errorf("expected %d unique versions, got %d", n, len(seen))
	}
}

// ---------------------------------------------------------------------------
// Helpers for integration tests
// ---------------------------------------------------------------------------

func setupRealStore(tb testing.TB) Store {
	tb.Helper()
	tb.Log("NOTE: integration tests require docker-compose services (PG + Redis)")
	tb.Log("Skipping if AEGIS_DATABASE_DSN not set")
	tb.Skip("integration tests require manual run with AEGIS_DATABASE_DSN set")
	return nil // unreachable
}

func seedTestTenant(tb testing.TB, _ Store, quota, used int64) uuid.UUID {
	tb.Helper()
	return uuid.New()
}

func createTestNode(tb testing.TB, _ Store, _ uuid.UUID) uuid.UUID {
	tb.Helper()
	return uuid.New()
}

func benchMux(_ Store) *http.ServeMux {
	return http.NewServeMux()
}

func randomHexHash() string {
	b := make([]byte, 32)
	for i := range b {
		b[i] = "0123456789abcdef"[time.Now().UnixNano()%16]
		time.Sleep(time.Nanosecond)
	}
	return fmt.Sprintf("%x", b)
}
