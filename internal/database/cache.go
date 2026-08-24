package database

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

// NamespaceCache is the Redis-backed, cache-aside layer for namespace
// metadata (node rows, resolved ACLs) per PROMPT 2.2.
//
// COHERENCY MODEL (acceptance criterion: "invalidation is immediate"):
//
// Every key embeds a per-tenant namespace *generation*:
//
//	aegis:ns:gen:{tenant}          -> uint64 counter
//	aegis:ns:{tenant}:g{N}:{node}  -> cached JSON payload
//
// Mutating operations (move_directory, ACL grants, version commits) INCR the
// generation AFTER the database transaction commits. Consequences:
//
//   - Any entry written under generation N becomes unreachable the instant N+1
//     exists — O(1) invalidation of an entire subtree without SCAN/DEL.
//   - A reader racing the move either (a) reads under gen N before commit and
//     caches pre-move state under a key that INCR immediately orphans, or
//   - reads under gen N+1 after commit and therefore loads post-move state.
//     There is NO ordering in which stale data survives under the live
//     generation. Staleness window == ordinary READ COMMITTED semantics.
//   - TTL 5m is pure garbage collection of orphaned generations, not the
//     coherency mechanism.
//
// LOCK-FREE READS: hits are a single Redis GET — no mutex is held on the Go
// side. Misses collapse through singleflight so a hot key under expiry storms
// the database exactly once; all other goroutines share that loader's result.
type NamespaceCache struct {
	rdb    redis.UniversalClient
	ttl    time.Duration
	sf     singleflight.Group
	logger logger

	hits   atomicCounter
	misses atomicCounter
}

// logger is the minimal logging surface the cache needs (satisfied by
// *slog.Logger); kept as an interface so tests can capture emissions.
type logger interface {
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// atomicCounter is a tiny lock-free monotonic counter.
type atomicCounter struct{ n atomic.Int64 }

func (a *atomicCounter) Add(delta int64) int64 { return a.n.Add(delta) }
func (a *atomicCounter) Load() int64           { return a.n.Load() }

func NewNamespaceCache(rdb redis.UniversalClient, ttl time.Duration, lg logger) *NamespaceCache {
	return &NamespaceCache{rdb: rdb, ttl: ttl, logger: lg}
}

// redisNewClient builds the go-redis universal client from config. The
// default dial/read timeouts keep cache bypasses fast: a slow Redis must
// never hold the metadata plane hostage (degrade-to-DB contract).
func redisNewClient(cfg CacheConfig) redis.UniversalClient {
	return redis.NewClient(&redis.Options{
		Addr:         cfg.Addr,
		Password:     cfg.Password,
		DialTimeout:  DefaultAcquireTimeout,
		ReadTimeout:  DefaultAcquireTimeout,
		WriteTimeout: DefaultAcquireTimeout,
		PoolSize:     64, // cache fan-out is high but payloads are tiny
	})
}

// generationKey / nodeKey builders are pure functions — unit-tested.
func generationKey(tenantID string) string {
	return "aegis:ns:gen:" + tenantID
}

func nodeKey(tenantID string, generation uint64, nodeID string) string {
	return fmt.Sprintf("aegis:ns:%s:g%d:%s", tenantID, generation, nodeID)
}

// generation returns the current namespace generation for a tenant. A missing
// counter means "no mutation has ever happened" ⇒ generation 0.
func (c *NamespaceCache) generation(ctx context.Context, tenantID string) (uint64, error) {
	n, err := c.rdb.Get(ctx, generationKey(tenantID)).Uint64()
	if err != nil {
		if err == redis.Nil {
			return 0, nil
		}
		return 0, err
	}
	return n, nil
}

// BumpGeneration advances the tenant's namespace generation. Called by every
// mutating wrapper AFTER its DB transaction commits (see client.go wrappers).
// Returns the new generation for logging/metrics.
func (c *NamespaceCache) BumpGeneration(ctx context.Context, tenantID string) (uint64, error) {
	n, err := c.rdb.Incr(ctx, generationKey(tenantID)).Result()
	if err != nil {
		return 0, err
	}
	return uint64(n), nil
}

// NodeMeta is the cached payload shape. Keep it JSON-stable: it crosses the
// wire and outlives process restarts.
type NodeMeta struct {
	NodeID      string  `json:"node_id"`
	TenantID    string  `json:"tenant_id"`
	ParentID    *string `json:"parent_id,omitempty"`
	Name        string  `json:"name"`
	Type        string  `json:"type"` // FILE | DIRECTORY
	LineagePath string  `json:"lineage_path"`
	ACLEpoch    int64   `json:"acl_epoch"`
}

// GetNodeMetadata implements cache-aside with singleflight collapse:
//
//  1. GET under the live generation — hit path is one Redis round trip, zero
//     locks (lock-free read requirement).
//  2. Miss → singleflight.Do(key): exactly one goroutine runs loader() while
//     the rest block on its result — thundering-herd protection.
//  3. Loader result is SET with PX TTL then returned.
//
// Redis errors degrade to "cache bypass": metadata flows straight from the
// loader so a Redis outage can never take the metadata plane down.
func (c *NamespaceCache) GetNodeMetadata(
	ctx context.Context,
	tenantID, nodeID string,
	loader func(context.Context) (NodeMeta, error),
) (NodeMeta, error) {
	gen, err := c.generation(ctx, tenantID)
	if err != nil {
		return loader(ctx) // bypass
	}
	key := nodeKey(tenantID, gen, nodeID)

	if payload, err := c.rdb.Get(ctx, key).Bytes(); err == nil {
		c.hits.Add(1)
		var meta NodeMeta
		if jsonErr := json.Unmarshal(payload, &meta); jsonErr == nil {
			return meta, nil
		} // corrupt entry falls through to loader + overwrite
	} else if err != redis.Nil {
		c.logger.Warn("redis get failed; bypassing cache", "err", err.Error())
		return loader(ctx)
	}
	c.misses.Add(1)

	v, err, _ := c.sf.Do(key, func() (any, error) {
		meta, lerr := loader(ctx)
		if lerr != nil {
			return nil, lerr
		}
		if buf, jerr := json.Marshal(meta); jerr == nil {
			if serr := c.rdb.Set(ctx, key, buf, c.ttl).Err(); serr != nil {
				c.logger.Warn("redis set failed", "err", serr.Error())
			}
		}
		return meta, nil
	})
	if err != nil {
		return NodeMeta{}, err
	}
	meta, ok := v.(NodeMeta)
	if !ok {
		return NodeMeta{}, fmt.Errorf("namespace cache: unexpected loader type %T", v)
	}
	return meta, nil
}

// Stats exposes hit/miss counters for observability endpoints.
func (c *NamespaceCache) Stats() (hits, misses int64) {
	return c.hits.Load(), c.misses.Load()
}
