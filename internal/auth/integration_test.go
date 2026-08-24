//go:build integration

// Project Aegis — HMAC ingress tokens against the REAL compose stack.
// PROMPT 3.2 (Cryptographic Ingress & HMAC Token System).
//
// Run:
//
//	docker compose -f deploy/compose/docker-compose.yml up -d postgres redis
//	go test -tags=integration -count=1 ./internal/auth/
//
// Covers what in-memory stores cannot: atomic SETNX semantics under
// concurrency (a token replayed by N racing requests admits exactly ONE)
// and TTL eviction of nonce keys on the actual Redis server.
package auth

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

var _ = uuid.New // reserved for future multi-tenant scenarios

func newIntegrationGenerator(t *testing.T, clock func() time.Time, opts ...Option) *TokenGenerator {
	t.Helper()
	addr := envOrString("AEGIS_REDIS_ADDR", "localhost:6379")
	pass := envOrString("AEGIS_REDIS_PASSWORD", "dev_redis_only")
	client := redis.NewClient(&redis.Options{Addr: addr, Password: pass})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("redis unreachable at %s (docker compose up redis?): %v", addr, err)
	}
	t.Cleanup(func() { _ = client.Close() })

	kms := NewStaticKMS(clock)
	kms.Provision(tenantA, 1, []byte(strings.Repeat("A", 32)))
	return NewTokenGenerator(kms, NewRedisNonceStore(client), append([]Option{WithClock(clock)}, opts...)...)
}

func envOrString(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func TestIntegrationReplayRaceExactlyOneWinner(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fakeNow := now
	clock := func() time.Time { return fakeNow }
	gen := newIntegrationGenerator(t, clock)

	st, err := gen.SignClaims(context.Background(), tenantA, blockAX, endpoint1)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	const racers = 64
	var winners, losers atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // maximize contention on one SETNX
			ok, err := gen.Validate(context.Background(), st)
			switch {
			case ok:
				winners.Add(1)
			case err == nil:
				t.Error("rejected with nil error")
			default:
				losers.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if winners.Load() != 1 || losers.Load() != racers-1 {
		t.Fatalf("replay race: winners=%d losers=%d want 1/%d",
			winners.Load(), losers.Load(), racers-1)
	}

	// And the winner's slot is spent: a clean re-request is still rejected.
	if ok, err := gen.Validate(context.Background(), st); ok || err == nil {
		t.Fatalf("post-race replay accepted: ok=%v err=%v", ok, err)
	}
}

func TestIntegrationNonceTTLEviction(t *testing.T) {
	// Shrunk windows so the Redis-native EX countdown is observable in real
	// seconds: nonce TTL = ttl+skew = 3s. The injected clock moves freshness
	// math; the eviction itself is genuine server-side expiry.
	const ttl = 2 * time.Second
	const skew = time.Second

	fakeNow := time.Now().UTC().Truncate(time.Second)
	clock := func() time.Time { return fakeNow }
	gen := newIntegrationGenerator(t, clock, WithTTL(ttl), WithSkew(skew))

	st, err := gen.SignClaims(context.Background(), tenantA, blockAX, endpoint1)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if ok, err := gen.Validate(context.Background(), st); !ok || err != nil {
		t.Fatalf("first use: ok=%v err=%v", ok, err)
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:     envOrString("AEGIS_REDIS_ADDR", "localhost:6379"),
		Password: envOrString("AEGIS_REDIS_PASSWORD", "dev_redis_only"),
	})
	defer rdb.Close()
	key := nonceKey(st.Claims.NonceHex)
	gotTTL, err := rdb.TTL(context.Background(), key).Result()
	if err != nil {
		t.Fatalf("TTL: %v", err)
	}
	if gotTTL <= 0 || gotTTL > ttl+skew+time.Second {
		t.Fatalf("nonce TTL %v outside (%v, %v]", gotTTL, 0, ttl+skew)
	}

	// The entry self-evicts via native EX. Afterwards the same token is
	// rejected on FRESHNESS (both gates move together), so no replay window
	// ever opens.
	deadline := time.Now().Add(6 * time.Second)
	for {
		exists, err := rdb.Exists(context.Background(), key).Result()
		if err != nil {
			t.Fatalf("exists: %v", err)
		}
		if exists == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("nonce key not evicted after validity window")
		}
		time.Sleep(100 * time.Millisecond)
	}

	fakeNow = fakeNow.Add(2 * (ttl + skew))
	if _, err := gen.Validate(context.Background(), st); !errors.Is(err, ErrExpired) {
		t.Fatalf("want expiry-class error after eviction, got %v", err)
	}
}
