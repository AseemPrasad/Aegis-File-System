package ingress

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Sliding window counter Lua script
var slidingWindowScript = redis.NewScript(`
	local key = KEYS[1]
	local now = tonumber(ARGV[1])
	local window = tonumber(ARGV[2])
	local limit = tonumber(ARGV[3])
	local req_id = ARGV[4]

	local clear_before = now - window
	redis.call('ZREMRANGEBYSCORE', key, 0, clear_before)
	local current_count = redis.call('ZCARD', key)

	if current_count < limit then
		redis.call('ZADD', key, now, req_id)
		redis.call('PEXPIRE', key, window)
		return {1, limit - current_count - 1, limit}
	else
		return {0, 0, limit}
	end
`)

// RedisSlidingRateLimiter implements atomic sliding-window rate limiting across API pods.
type RedisSlidingRateLimiter struct {
	client     *redis.Client
	windowSize time.Duration
	defaultLimit int
}

func NewRedisSlidingRateLimiter(client *redis.Client, windowSize time.Duration, defaultLimit int) *RedisSlidingRateLimiter {
	if windowSize <= 0 {
		windowSize = 1 * time.Minute
	}
	if defaultLimit <= 0 {
		defaultLimit = 1000
	}
	return &RedisSlidingRateLimiter{
		client:     client,
		windowSize: windowSize,
		defaultLimit: defaultLimit,
	}
}

// Middleware wraps http.HandlerFunc with sliding-window rate limits and RFC 6585 headers.
func (rl *RedisSlidingRateLimiter) Middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenantID := r.Header.Get("X-Tenant-ID")
		if tenantID == "" {
			tenantID = "global"
		}

		key := fmt.Sprintf("aegis:ratelimit:%s", tenantID)
		nowMs := time.Now().UnixMilli()
		windowMs := rl.windowSize.Milliseconds()
		reqID := uuid.New().String()

		res, err := slidingWindowScript.Run(r.Context(), rl.client, []string{key}, nowMs, windowMs, rl.defaultLimit, reqID).Slice()
		if err != nil {
			// Fallback on Redis failure to unblock request
			next(w, r)
			return
		}

		allowed := res[0].(int64) == 1
		remaining := res[1].(int64)
		limit := res[2].(int64)

		resetTime := time.Now().Add(rl.windowSize).Unix()

		w.Header().Set("X-RateLimit-Limit", strconv.FormatInt(limit, 10))
		w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(remaining, 10))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetTime, 10))

		if !allowed {
			w.Header().Set("Retry-After", strconv.FormatInt(int64(rl.windowSize.Seconds()), 10))
			http.Error(w, `{"error":"too_many_requests","detail":"Rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}

		next(w, r)
	}
}
