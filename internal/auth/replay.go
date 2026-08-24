// ============================================================================
// Project Aegis — anti-replay nonce store.
// PROMPT 3.2 (Cryptographic Ingress & HMAC Token System).
//
// Every pre-signed token carries a random 16-byte nonce inside the signed
// message. Validation consumes the nonce exactly once via SETNX semantics:
// the FIRST validator to see it wins, every subsequent request carrying the
// same token is rejected as a replay. Redis keys expire with the token
// validity window so the replay set self-cleans and cannot grow unbounded.
//
// Consume MUST only be reached after the signature check passes — otherwise
// an attacker could burn victim nonces (a denial-of-service on valid tokens)
// with garbage signatures. The ordering is enforced in ValidateToken.
// ============================================================================
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// NonceStore marks a nonce as consumed. It returns true when this call was
// the first (token accepted), false when the nonce was already seen
// (replay). Implementations must be safe for concurrent use; races between
// two validators MUST resolve to exactly one true.
type NonceStore interface {
	Consume(ctx context.Context, nonce string, ttl time.Duration) (bool, error)
}

const nonceKeyPrefix = "aegis:nonce:"

// nonceKey domain-separates the Redis keyspace and hashes away any raw
// entropy from key-space visibility (defence in depth; nonces are random
// anyway).
func nonceKey(nonce string) string {
	sum := sha256.Sum256([]byte(nonceKeyPrefix + nonce))
	return nonceKeyPrefix + hex.EncodeToString(sum[:])
}

// RedisNonceStore implements NonceStore on top of go-redis SETNX+TTL.
type RedisNonceStore struct {
	client *redis.Client
}

// NewRedisNonceStore wraps an existing go-redis client (shared with the
// cache subsystem; PROMPT 2.2 pool).
func NewRedisNonceStore(client *redis.Client) *RedisNonceStore {
	return &RedisNonceStore{client: client}
}

// Consume claims the nonce atomically: SET key 1 NX EX ttl.
func (s *RedisNonceStore) Consume(ctx context.Context, nonce string, ttl time.Duration) (bool, error) {
	ok, err := s.client.SetNX(ctx, nonceKey(nonce), 1, ttl).Result()
	if err != nil {
		return false, err
	}
	return ok, nil
}

// InMemoryNonceStore is the process-local implementation used by unit tests
// and by deployments that set IN_MEMORY_STORES=true (developer profile).
// Expired entries are swept lazily on access plus opportunistically every
// Consume call, bounding memory under test churn.
type InMemoryNonceStore struct {
	mu      sync.Mutex
	seen    map[string]time.Time // nonce -> expiry
	maxSize int
}

// NewInMemoryNonceStore builds a bounded store. maxEntries <= 0 selects a
// sane default; the sweeper evicts expired entries before enforcing it.
func NewInMemoryNonceStore(maxEntries int) *InMemoryNonceStore {
	if maxEntries <= 0 {
		maxEntries = 1 << 20
	}
	return &InMemoryNonceStore{seen: make(map[string]time.Time), maxSize: maxEntries}
}

func (s *InMemoryNonceStore) Consume(_ context.Context, nonce string, ttl time.Duration) (bool, error) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	// Lazy sweep keeps the map tight without a background goroutine.
	if len(s.seen) >= s.maxSize {
		for k, exp := range s.seen {
			if !exp.After(now) {
				delete(s.seen, k)
			}
		}
	}
	if exp, ok := s.seen[nonce]; ok && exp.After(now) {
		return false, nil // replay
	}
	s.seen[nonce] = now.Add(ttl)
	return true, nil
}
