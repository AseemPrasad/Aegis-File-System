# Connection Pooling & Database Client — Design Notes (PROMPT 2.2)

Implementation: `internal/database` (package `database`). Client entry point:
`NewDatabaseClient(ctx, Config, *slog.Logger)` in `client.go`.

## 1. Pool taxonomy and sizing

Four isolated pgx pools per `DatabaseClient`, one per `OpClass`
(`config.go:25`):

| Tier         | Target            | Purpose                                        |
|--------------|-------------------|------------------------------------------------|
| `write`      | primary           | Only tier permitted to mutate state            |
| `read`       | replicas (N)      | Round-robin fan-out; falls back to primary     |
| `metadata`   | metadata DSN      | Control-plane reads; isolated from dashboards  |
| `analytical` | replicas          | Reporting queries; cannot starve hot paths     |

Sizing rule (`Config.applyDefaults`, `config.go:85`):

- `MinConnsPerPool = cores × 2`
- `MaxConnsPerPool = cores × 4`
- Global budget `MaxTotalConns = 96`; each pool is clamped to
  `budget / pool_count` so five pools never overrun a typical
  `max_connections=100` server.
- `MaxConnLifetime = 30m` (hard cap, pgx applies jitter),
  `MaxConnIdleTime = 5m` (reaper).

Rationale: min keeps a warm complement so bursts don't pay TCP+SCRAM
handshake latency; the ×4 ceiling bounds memory (~5 MB/conn server-side)
and respects Postgres per-connection overhead.

## 2. Overflow policy — bounded queue, then fail fast

Acquisition waits at most `AcquireTimeout` (default **250 ms**) for a free
connection, then returns `ErrPoolTimeout` (`config.go:127`). No unbounded
queuing:

- p99 tail latency stays deterministic; callers apply §8 retry/backoff
  from `docs/isolation-policy.md`.
- A saturated pool sheds load instead of compounding it (the queue itself
  becomes the bottleneck past ~2× service capacity).

Verified by `TestIntegrationExhaustionFailsFast`: with `max=2`,
64 concurrent acquires yield exactly 2 successes + 62 fail-fast
rejections within one timeout window.

## 3. Validation strategy

- Startup ping against every configured DSN fails construction fast
  (bad credentials/topology die at boot, not on first request).
  `SkipStartupPing` exists solely for the failover harness.
- Per-acquire context deadline = `AcquireTimeout`; context
  deadline/cancel maps to `ErrPoolTimeout` so callers see one overflow
  sentinel regardless of which layer tripped.
- Server-side `statement_timeout` is not set globally; transactional
  mutators wrap statements in `BeginWriteTx` with caller-scoped contexts.

## 4. Failover semantics (`failover.go`)

Circuit breaker around the **primary**, shared by read reroute and write
gating:

- Opens after 3 consecutive transport failures; cooldown 2 s;
  half-open elects exactly ONE prober (`Allow()`), success closes,
  failure reopens. Concurrent half-open losers are rejected without
  dialing.
- Health loop pings `writePool` every 500 ms and records the outcome,
  so an idle system still detects primary death within
  `interval × threshold` ≈ 1.5 s.

**Reads** (`queryRouted`, `client.go:311`): transport-classified errors
(`isTransportErr`) feed the breaker and trigger exactly ONE transparent
reroute onto the next replica via round-robin. Without replicas, dev
fallback targets the primary gated by the breaker. SQL statement errors
are deterministic results — never retried, never counted as infra
faults.

**Writes** (`acquire` OpWrite arm + `recordPrimaryOutcome`): when the
breaker is open, writes return `ErrPrimaryUnavailable` immediately.
Writes are NEVER redirected to a replica: async replication means a
"successful" stale-tolerant write would fork history (I-1 violation).
Callers retry with backoff per `docs/isolation-policy.md` §8.
`ErrPrimaryUnavailable` itself is excluded from breaker accounting — it
is the breaker's output, not evidence; counting it would push the
cooldown forward forever under write pressure.

RPO/RTO: RPO ≈ 0 for acknowledged commits (single-writer primary);
async replicas may serve reads lagging by replication delay (staleness
tolerated by design — ACL epochs are monotonic). RTO ≈ detection
(≤1.5 s) + cooldown (2 s) before the first probe write/read succeeds.

## 5. Cache-aside namespace metadata (`cache.go`)

- Key layout: generation counter `aegis:ns:gen:{tenant}` plus entries
  `aegis:ns:{tenant}:g{N}:{node}` where `N` = current generation.
- Read path: GET gen → GET payload (2 round trips, lock-free); miss ⇒
  singleflight collapse then loader; negative results cached briefly.
- Write path: any committed mutation through the client bumps the
  tenant generation (INCR). All old-generation keys become unreachable
  instantly — O(1) subtree-wide invalidation with no scan, no locks,
  no reader-side fencing logic.
- TTL (default **5 m**) is garbage collection only, not coherence.
- Redis outage degrades to loader bypass (cache disabled at runtime),
  logged once; Postgres remains authoritative.

Verified by `TestIntegrationCacheCoherencyUnderWrites`: concurrent
reader observes monotonically non-decreasing `acl_epoch` while writers
move directories / commit versions; hit ratio ≫ misses.

## 6. Observability contract (IC-3)

Prometheus collectors (`metrics.go`), registered once process-wide
(`defaultMetrics`) so multiple clients don't double-register:

- `aegis_db_query_duration_seconds` histogram by class/operation
- `aegis_db_acquire_wait_seconds` histogram (saturation signal;
  p95 surfaced via `Stats().AcquireWaitP95`)
- `aegis_db_connections_acquired/released_total` (leak detection delta)
- `aegis_db_errors_total` by class
- `aegis_db_slow_queries_total` (>50 ms, mirrors server
  `log_min_duration_statement`)

Structured operation log line per query: ts, class, operation, tenant,
duration_ms, error — JSON via slog.

Leak detection: `releasedRows` auto-releases pooled connections when
result rows are exhausted even if the caller forgets `Close()`;
`pooledTx` releases on Commit/Rollback either way.
`TestIntegrationConnectionLeakDetection` asserts acquired == released
across 200 rounds with stable pool size.

## 7. Load characteristics (measured, this repo's harness)

1000 clients × 3 paced queries vs live compose Postgres:
~3000 qps sustained, p95 acquire wait < 25 ms CI gate, zero failures
(`TestIntegrationLoadThousandClients`). Arrival pacing models steady
state; synchronized stampedes queue by construction (queueing theory)
and are the exhaustion scenario's domain, not the SLA's.

## 8. Testing

| Suite        | Command                                          |
|--------------|--------------------------------------------------|
| Unit         | `go test -count=1 ./internal/database/`          |
| Integration  | `go test -tags=integration -count=1 ./internal/database/` |

Integration prerequisites: compose stack up
(`docker compose -f deploy/compose/docker-compose.yml up -d postgres redis`)
with schema applied (PROMPT 2.1 artifacts). Host-side tools should use
port **15432** — developer machines often run a native PostgreSQL on
5432; the compose file publishes both. Override hooks:
`AEGIS_PG_DSN`, `AEGIS_PG_REPLICA_DSN`, `AEGIS_REDIS_ADDR`,
`AEGIS_REDIS_PASSWORD`.
