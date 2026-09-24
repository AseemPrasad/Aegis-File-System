package ingress

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
)

type CachedResponse struct {
	StatusCode int    `json:"status_code"`
	Body       string `json:"body"`
}

// IdempotencyEngine intercepts requests carrying Idempotency-Key headers and returns cached results.
type IdempotencyEngine struct {
	client *redis.Client
	ttl    time.Duration
}

func NewIdempotencyEngine(client *redis.Client, ttl time.Duration) *IdempotencyEngine {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &IdempotencyEngine{
		client: client,
		ttl:    ttl,
	}
}

// Middleware wraps state-changing handlers (/files/initiate, /files/commit) with idempotency safety.
func (ie *IdempotencyEngine) Middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idempotencyKey := r.Header.Get("Idempotency-Key")
		if idempotencyKey == "" {
			next(w, r)
			return
		}

		tenantID := r.Header.Get("X-Tenant-ID")
		if tenantID == "" {
			tenantID = "global"
		}

		key := fmt.Sprintf("aegis:idempotency:%s:%s", tenantID, idempotencyKey)

		// 1. Atomic Redis Fencing: SET NX EX
		acquired, err := ie.client.SetNX(r.Context(), key, "IN_PROGRESS", ie.ttl).Result()
		if err != nil {
			// Redis fail-open fallback
			next(w, r)
			return
		}

		if !acquired {
			// Request already exists
			val, err := ie.client.Get(r.Context(), key).Result()
			if err == nil && val != "IN_PROGRESS" {
				var cached CachedResponse
				if err := json.Unmarshal([]byte(val), &cached); err == nil {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("X-Cache-Lookup", "HIT-IDEMPOTENT")
					w.WriteHeader(cached.StatusCode)
					_, _ = w.Write([]byte(cached.Body))
					return
				}
			}

			// In-progress lock collision
			w.Header().Set("Retry-After", "2")
			http.Error(w, `{"error":"idempotency_conflict","detail":"Request in progress"}`, http.StatusConflict)
			return
		}

		// 2. Intercept Response Body & Status
		recorder := &responseRecorder{ResponseWriter: w, body: &bytes.Buffer{}, statusCode: http.StatusOK}
		next(recorder, r)

		// 3. Cache Result Payload
		cached := CachedResponse{
			StatusCode: recorder.statusCode,
			Body:       recorder.body.String(),
		}
		cachedBytes, _ := json.Marshal(cached)
		_ = ie.client.Set(r.Context(), key, cachedBytes, ie.ttl).Err()
	}
}

type responseRecorder struct {
	http.ResponseWriter
	body       *bytes.Buffer
	statusCode int
}

func (r *responseRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	r.body.Write(b)
	return r.ResponseWriter.Write(b)
}
