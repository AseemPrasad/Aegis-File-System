// ============================================================================
// Project Aegis — Rate Limiter Tests
// ============================================================================

package ingress

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestRateLimiter_Allow(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	rl := NewRateLimiterWithClock(10, 20, clock)

	// Should allow up to burst (20)
	for i := 0; i < 20; i++ {
		if !rl.Allow("tenant-1") {
			t.Fatalf("request %d should be allowed (burst capacity)", i)
		}
	}

	// 21st request should be denied (bucket empty)
	if rl.Allow("tenant-1") {
		t.Fatal("request 21 should be denied (burst exhausted)")
	}

	// Advance time to refill 10 tokens
	now = now.Add(1 * time.Second)
	for i := 0; i < 10; i++ {
		if !rl.Allow("tenant-1") {
			t.Fatalf("refill request %d should be allowed", i)
		}
	}

	// Should be denied again
	if rl.Allow("tenant-1") {
		t.Fatal("should be denied after refill consumed")
	}
}

func TestRateLimiter_TenantIsolation(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	rl := NewRateLimiterWithClock(10, 20, clock)

	// Exhaust tenant-1
	for i := 0; i < 20; i++ {
		rl.Allow("tenant-1")
	}
	if rl.Allow("tenant-1") {
		t.Fatal("tenant-1 should be exhausted")
	}

	// tenant-2 should still have full burst
	if !rl.Allow("tenant-2") {
		t.Fatal("tenant-2 should have full burst")
	}
}

func TestRateLimiter_Refill(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	rl := NewRateLimiterWithClock(10, 20, clock)

	// Exhaust bucket
	for i := 0; i < 20; i++ {
		rl.Allow("tenant-1")
	}

	// Advance 5 seconds → should refill 50 tokens (capped at burst=20)
	now = now.Add(5 * time.Second)
	if !rl.Allow("tenant-1") {
		t.Fatal("should allow after refill")
	}
}

func TestRateLimiter_BurstCap(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	rl := NewRateLimiterWithClock(10, 15, clock)

	// Wait 100 seconds (would accumulate 1000 tokens without cap)
	now = now.Add(100 * time.Second)

	// Should only have burst (15) tokens
	allowed := 0
	for i := 0; i < 20; i++ {
		if rl.Allow("tenant-1") {
			allowed++
		}
	}
	if allowed != 15 {
		t.Fatalf("expected 15 allowed (burst cap), got %d", allowed)
	}
}

func TestRateLimiter_Reset(t *testing.T) {
	rl := NewRateLimiter(10, 20)

	// Create some buckets
	rl.Allow("tenant-1")
	rl.Allow("tenant-2")
	if rl.BucketCount() != 2 {
		t.Fatalf("expected 2 buckets, got %d", rl.BucketCount())
	}

	rl.Reset()
	if rl.BucketCount() != 0 {
		t.Fatalf("expected 0 buckets after reset, got %d", rl.BucketCount())
	}
}

func TestRateLimiter_HTTPMiddleware(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	rl := NewRateLimiterWithClock(10, 5, clock)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	})
	handler := rl.AllowHTTP(inner)

	// Exhaust burst
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", nil)
		req.Header.Set("X-Tenant-ID", "tenant-1")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("request %d: expected 200, got %d", i, w.Code)
		}
	}

	// 6th request should be 429
	req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", nil)
	req.Header.Set("X-Tenant-ID", "tenant-1")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w.Code)
	}
	if w.Header().Get("Retry-After") != "1" {
		t.Fatal("expected Retry-After header")
	}
}

func TestRateLimiter_ConcurrentAccess(t *testing.T) {
	rl := NewRateLimiter(1000, 2000)
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			tenant := "tenant-" + string(rune('A'+id%5))
			for j := 0; j < 100; j++ {
				rl.Allow(tenant)
			}
		}(i)
	}
	wg.Wait()

	// Should have 5 buckets (A-E)
	if rl.BucketCount() != 5 {
		t.Fatalf("expected 5 buckets, got %d", rl.BucketCount())
	}
}

func TestRateLimiter_DefaultTenant(t *testing.T) {
	rl := NewRateLimiter(10, 5)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := rl.AllowHTTP(inner)

	// Request without X-Tenant-ID → uses "default"
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("request %d: expected 200, got %d", i, w.Code)
		}
	}

	// 6th request → 429
	req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w.Code)
	}
}
