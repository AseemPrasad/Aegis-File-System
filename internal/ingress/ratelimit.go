// ============================================================================
// Project Aegis — Rate Limiting Middleware
//
// Per-tenant token bucket rate limiter. Each tenant gets a configurable
// request-per-second (RPS) budget with burst capacity. Uses a concurrent
// map of token buckets keyed by tenant ID (extracted from the request).
//
// Default: 1000 RPS per tenant with burst of 2000.
// ============================================================================

package ingress

import (
	"net/http"
	"sync"
	"time"
)

// RateLimiter implements per-tenant token bucket rate limiting.
type RateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*tokenBucket
	rps      int
	burst    int
	clock    func() time.Time
}

// tokenBucket is a single-tenant token bucket.
type tokenBucket struct {
	mu       sync.Mutex
	tokens   float64
	maxBurst float64
	rps      float64
	lastTime time.Time
}

// NewRateLimiter creates a rate limiter with the given RPS and burst limits.
func NewRateLimiter(rps, burst int) *RateLimiter {
	return &RateLimiter{
		buckets: make(map[string]*tokenBucket),
		rps:     rps,
		burst:   burst,
		clock:   time.Now,
	}
}

// NewRateLimiterWithClock creates a rate limiter with a custom clock (for testing).
func NewRateLimiterWithClock(rps, burst int, clock func() time.Time) *RateLimiter {
	return &RateLimiter{
		buckets: make(map[string]*tokenBucket),
		rps:     rps,
		burst:   burst,
		clock:   clock,
	}
}

// Allow checks whether a request from the given tenant is allowed.
func (rl *RateLimiter) Allow(tenantID string) bool {
	b := rl.getBucket(tenantID)
	return b.allow(rl.clock())
}

// AllowHTTP is an http.HandlerFunc wrapper that rejects with 429 if over limit.
func (rl *RateLimiter) AllowHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantID := r.Header.Get("X-Tenant-ID")
		if tenantID == "" {
			tenantID = "default"
		}
		if !rl.Allow(tenantID) {
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":"rate limit exceeded"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// getBucket returns (or creates) a token bucket for the given tenant.
func (rl *RateLimiter) getBucket(tenantID string) *tokenBucket {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	b, ok := rl.buckets[tenantID]
	if !ok {
		b = &tokenBucket{
			tokens:   float64(rl.burst),
			maxBurst: float64(rl.burst),
			rps:      float64(rl.rps),
			lastTime: rl.clock(),
		}
		rl.buckets[tenantID] = b
	}
	return b
}

// allow tries to consume one token from the bucket.
func (tb *tokenBucket) allow(now time.Time) bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	elapsed := now.Sub(tb.lastTime).Seconds()
	if elapsed > 0 {
		tb.tokens += elapsed * tb.rps
		if tb.tokens > tb.maxBurst {
			tb.tokens = tb.maxBurst
		}
		tb.lastTime = now
	}

	if tb.tokens < 1 {
		return false
	}
	tb.tokens--
	return true
}

// Reset clears all tenant buckets (for testing).
func (rl *RateLimiter) Reset() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.buckets = make(map[string]*tokenBucket)
}

// BucketCount returns the number of tracked tenants (for testing).
func (rl *RateLimiter) BucketCount() int {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return len(rl.buckets)
}
