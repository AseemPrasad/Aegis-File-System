// Package database implements the Aegis metadata-plane client: pooled,
// observed, cached, and failover-aware access to PostgreSQL (PROMPT 2.2).
//
// Pool taxonomy (contract IC-3):
//
//	Write      — small pool pinned to the primary; strict ordering, the
//	             ONLY pool permitted to mutate state.
//	Read       — larger pool set, round-robin across read replicas.
//	Metadata   — control-plane queries (listing, ACL resolution); kept
//	             separate so dashboards never evict hot-path connections.
//	Analytical — isolated reporting pool pointed at replicas; a heavy
//	             aggregation cannot starve transactional work.
package database

import (
	"fmt"
	"runtime"
	"time"
)

// OpClass selects the pool tier a query is routed to. Routing is explicit at
// call sites — implicit routing hides accidental writes on read connections.
type OpClass string

const (
	OpWrite      OpClass = "write"
	OpRead       OpClass = "read"
	OpMetadata   OpClass = "metadata"
	OpAnalytical OpClass = "analytical"
)

const (
	// DefaultSlowQueryThreshold mirrors the Postgres server setting
	// log_min_duration_statement=50ms in deploy/compose (IC-3 contract).
	DefaultSlowQueryThreshold = 50 * time.Millisecond
	// DefaultAcquireTimeout bounds queueing for a connection. Overflow
	// behavior is "bounded queue then fail fast": callers wait up to this
	// long for a free connection before receiving ErrPoolTimeout, keeping
	// p99 tail latency deterministic instead of unbounded.
	DefaultAcquireTimeout = 250 * time.Millisecond
	// DefaultHealthInterval is the primary health-check period; detection
	// budget = interval * failureThreshold (see failover.go).
	DefaultHealthInterval = 500 * time.Millisecond
	// DefaultCacheTTL per PROMPT 2.2: namespace metadata lives 5 minutes.
	DefaultCacheTTL = 5 * time.Minute
)

// Config configures every pool plus failover and cache behavior.
type Config struct {
	PrimaryDSN  string   // writable primary (single host)
	ReplicaDSNs []string // read replicas; empty ⇒ reads fall back to primary
	MetadataDSN string   // control plane DSN; defaults to PrimaryDSN

	// Per-pool sizing. Zero values default to cores*2 / cores*4
	// (PROMPT 2.2 sizing rule). MaxConnsPerPool is additionally clamped by
	// MaxTotalConns across all pools to respect max_connections on the
	// server (see Validate).
	MinConnsPerPool int32
	MaxConnsPerPool int32
	MaxTotalConns   int32

	MaxConnLifetime     time.Duration // hard cap; pgx applies jitter internally
	MaxConnIdleTime     time.Duration // reaper threshold
	AcquireTimeout      time.Duration // bounded-queue overflow policy
	HealthCheckInterval time.Duration // primary liveness probe cadence

	SlowQueryThreshold time.Duration // > ⇒ logged + counter (default 50ms)

	// SkipStartupPing defers the eager MinConns warm-up ping at construction.
	// Production code must leave this false (fail-fast on bad DSNs); the
	// failover harness needs a client whose PRIMARY is intentionally dead.
	SkipStartupPing bool

	Cache CacheConfig
}

// CacheConfig controls the Redis namespace-metadata layer.
type CacheConfig struct {
	Enabled  bool
	Addr     string // host:port
	Password string
	TTL      time.Duration // default 5m (PROMPT 2.2)
}

func (c *Config) applyDefaults() {
	if c.MetadataDSN == "" {
		c.MetadataDSN = c.PrimaryDSN
	}
	if c.MinConnsPerPool <= 0 {
		c.MinConnsPerPool = int32(runtime.NumCPU()) * 2
	}
	if c.MaxConnsPerPool <= 0 {
		c.MaxConnsPerPool = int32(runtime.NumCPU()) * 4
	}
	if c.MaxConnsPerPool < c.MinConnsPerPool {
		c.MaxConnsPerPool = c.MinConnsPerPool
	}
	if c.MaxTotalConns <= 0 {
		// Four pools × cores*4 would overrun typical max_connections=100
		// servers, so the global budget defaults below that ceiling.
		c.MaxTotalConns = 96
	}
	if c.MaxConnLifetime <= 0 {
		c.MaxConnLifetime = 30 * time.Minute
	}
	if c.MaxConnIdleTime <= 0 {
		c.MaxConnIdleTime = 5 * time.Minute
	}
	if c.AcquireTimeout <= 0 {
		c.AcquireTimeout = DefaultAcquireTimeout
	}
	if c.HealthCheckInterval <= 0 {
		c.HealthCheckInterval = DefaultHealthInterval
	}
	if c.SlowQueryThreshold <= 0 {
		c.SlowQueryThreshold = DefaultSlowQueryThreshold
	}
	c.Cache.applyDefaults()
}

func (c *CacheConfig) applyDefaults() {
	if c.TTL <= 0 {
		c.TTL = DefaultCacheTTL
	}
}

// ErrPoolTimeout is returned when no connection became available within
// AcquireTimeout — the deliberate fail-fast signal of the overflow policy.
var ErrPoolTimeout = fmt.Errorf("database: connection acquire timeout (pool saturated)")
