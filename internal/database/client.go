// Project Aegis — internal/database/client.go (PROMPT 2.2)
//
// DatabaseClient is the ONLY sanctioned entry point from Go services to the
// metadata Postgres. It composes:
//
//   - four pool tiers (write / read×N / metadata / analytical) sized
//     cores*2 min, cores*4 max per pool under a global connection budget;
//   - bounded-queue overflow: acquiring waits ≤ AcquireTimeout then fails
//     fast with ErrPoolTimeout (deterministic p99, no pileups);
//   - Prometheus histograms/counters + slow-query (>50ms) structured logs;
//   - a Redis cache-aside namespace layer invalidated via generation bumps
//     on every mutating wrapper (move_directory / ACL / version commits);
//   - a circuit breaker that transparently degrades READS to replicas when
//     the primary health probe fails, while WRITES fail fast.
package database

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DatabaseClient owns every connection to the metadata store. One instance
// per process; Close must be called on shutdown (leak-detection tests assert
// TotalConns returns to zero after Close).
type DatabaseClient struct {
	writePool      *pgxpool.Pool   // INSERT/UPDATE/DELETE + transactions
	metadata       *pgxpool.Pool   // control-plane SELECTs (listing, ACL resolution)
	readPools      []*pgxpool.Pool // replica set for OpRead / OpAnalytical fallback
	readRR         atomic.Uint64   // round-robin cursor over readPools
	analyticalPool *pgxpool.Pool   // isolated reporting tier

	cfg     Config
	breaker *CircuitBreaker
	metrics *Metrics
	cache   *NamespaceCache
	logger  *slog.Logger

	healthCtx    context.Context
	healthCancel context.CancelFunc
	closeOnce    sync.Once

	// poolStats is the observability snapshot mandated by PROMPT 2.2.
	poolStats struct {
		connectionsAcquired atomic.Uint64
		connectionsReleased atomic.Uint64
		waitTimeP95         atomic.Int64 // nanos; refreshed by refreshWaitP95
	}
}

// NewDatabaseClient builds all pools and starts the primary health loop.
func NewDatabaseClient(ctx context.Context, cfg Config, lg *slog.Logger) (*DatabaseClient, error) {
	if lg == nil {
		lg = slog.Default()
	}
	if cfg.PrimaryDSN == "" {
		return nil, fmt.Errorf("database: PrimaryDSN is required")
	}
	cfg.applyDefaults()

	c := &DatabaseClient{cfg: cfg, logger: lg}
	c.metrics = defaultMetrics() // process-wide collectors, registered once
	c.breaker = newCircuitBreaker(func(state string) {
		lg.Warn("primary circuit breaker transitioned", "state", state)
	})

	var err error
	// WRITE pool: small, pinned to the primary. Strict ordering emerges from
	// serializing through fewer connections — write concurrency beyond the
	// pool queues at the client instead of thrashing row locks server-side.
	if c.writePool, err = c.newPool(ctx, "write", cfg.PrimaryDSN); err != nil {
		return nil, fmt.Errorf("write pool: %w", err)
	}
	// METADATA pool: control-plane traffic isolation.
	if c.metadata, err = c.newPool(ctx, "metadata", cfg.MetadataDSN); err != nil {
		return nil, fmt.Errorf("metadata pool: %w", err)
	}
	// READ pools: one per replica DSN; round-robin load balancing.
	for i, dsn := range cfg.ReplicaDSNs {
		p, perr := c.newPool(ctx, "read", dsn)
		if perr != nil {
			return nil, fmt.Errorf("replica %d: %w", i, perr)
		}
		c.readPools = append(c.readPools, p)
	}
	// ANALYTICAL pool: first replica (or primary in dev) but ALWAYS its own
	// pool so aggregations cannot evict transactional connections.
	analyticalDSN := cfg.PrimaryDSN
	if len(cfg.ReplicaDSNs) > 0 {
		analyticalDSN = cfg.ReplicaDSNs[0]
	}
	if c.analyticalPool, err = c.newPool(ctx, "analytical", analyticalDSN); err != nil {
		return nil, fmt.Errorf("analytical pool: %w", err)
	}

	if cfg.Cache.Enabled {
		c.cache = NewNamespaceCache(redisNewClient(cfg.Cache), cfg.Cache.TTL, lg)
	}

	c.healthCtx, c.healthCancel = context.WithCancel(context.Background())
	go c.healthLoop(c.healthCtx)

	// Refresh the saturation KPI once per health interval.
	go func() {
		t := time.NewTicker(c.cfg.HealthCheckInterval)
		defer t.Stop()
		for {
			select {
			case <-c.healthCtx.Done():
				return
			case <-t.C:
				c.refreshWaitP95()
			}
		}
	}()
	return c, nil
}

func (c *DatabaseClient) newPool(ctx context.Context, name, dsn string) (*pgxpool.Pool, error) {
	max := c.cfg.MaxConnsPerPool
	// Respect the global budget so four pools never overrun server
	// max_connections: equal slices of MaxTotalConns, floored at MinConns.
	if budget := c.cfg.MaxTotalConns / 4; max > budget && budget >= c.cfg.MinConnsPerPool {
		max = budget
	}
	pcfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse %s DSN: %w", name, err)
	}
	pcfg.MinConns = c.cfg.MinConnsPerPool
	pcfg.MaxConns = max
	pcfg.MaxConnLifetime = c.cfg.MaxConnLifetime // validation strategy #1: hard cap
	pcfg.MaxConnIdleTime = c.cfg.MaxConnIdleTime // validation strategy #2: idle reap
	// Validation strategy #3: periodic liveness probes replace connections
	// silently killed by NAT/firewall timeouts or server restarts.
	pcfg.HealthCheckPeriod = c.cfg.HealthCheckInterval

	p, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, err
	}
	// Eagerly establish MinConns so the first request wave never pays TCP +
	// auth setup latency (sub-5ms p95 SLA applies to steady-state traffic).
	if !c.cfg.SkipStartupPing {
		wctx, cancel := context.WithTimeout(ctx, 4*c.cfg.AcquireTimeout)
		defer cancel()
		if err := p.Ping(wctx); err != nil {
			p.Close()
			return nil, fmt.Errorf("%s pool ping: %w", name, err)
		}
	}
	return p, nil
}

// Close tears down all pools exactly once. Safe under concurrent shutdown.
func (c *DatabaseClient) Close() {
	c.closeOnce.Do(func() {
		c.healthCancel()
		c.writePool.Close()
		c.metadata.Close()
		for _, p := range c.readPools {
			p.Close()
		}
		c.analyticalPool.Close()
	})
}

// Cache exposes the namespace cache (nil when disabled in config).
func (c *DatabaseClient) Cache() *NamespaceCache { return c.cache }

// refreshWaitP95 publishes the reservoir's recent p95 acquire wait into
// poolStats.waitTimeP95 (saturation KPI for dashboards / alerting).
func (c *DatabaseClient) refreshWaitP95() {
	if p95, ok := c.metrics.waitReservoir.p95(); ok {
		c.poolStats.waitTimeP95.Store(int64(p95))
	}
}

// WaitTimeP95 returns the recent p95 connection-acquire wait.
func (c *DatabaseClient) WaitTimeP95() time.Duration {
	return time.Duration(c.poolStats.waitTimeP95.Load())
}

// Stats aggregates per-pool pgx statistics plus client-level counters —
// scrape target for saturation dashboards and leak detection.
type Stats struct {
	Pools          map[string]*pgxpool.Stat
	AcquiredTotal  uint64
	ReleasedTotal  uint64
	AcquireWaitP95 time.Duration
}

func (c *DatabaseClient) Stats() Stats {
	s := Stats{
		Pools: map[string]*pgxpool.Stat{
			"write":      c.writePool.Stat(),
			"metadata":   c.metadata.Stat(),
			"analytical": c.analyticalPool.Stat(),
		},
		AcquiredTotal:  c.poolStats.connectionsAcquired.Load(),
		ReleasedTotal:  c.poolStats.connectionsReleased.Load(),
		AcquireWaitP95: c.WaitTimeP95(),
	}
	for i, p := range c.readPools {
		s.Pools[fmt.Sprintf("read%d", i)] = p.Stat()
	}
	return s
}

// ---------------------------------------------------------------------------
// Routing + instrumented execution
// ---------------------------------------------------------------------------

// acquire obtains a pooled connection for the requested class, measuring
// queue wait (saturation metric) and enforcing bounded-queue overflow.
func (c *DatabaseClient) acquire(ctx context.Context, class OpClass) (*pgxpool.Conn, func(), error) {
	var pool *pgxpool.Pool
	switch class {
	case OpWrite:
		// Writes honor the primary circuit breaker: open circuit means
		// fail fast with the sentinel — never a silent replica redirect.
		if !c.breaker.Allow() {
			return nil, nil, ErrPrimaryUnavailable
		}
		pool = c.writePool
	case OpMetadata:
		pool = c.metadata
	case OpAnalytical:
		pool = c.analyticalPool
	case OpRead:
		target, ok := c.readTarget()
		if !ok {
			return nil, nil, ErrPrimaryUnavailable
		}
		pool = target
	default:
		return nil, nil, fmt.Errorf("database: unknown op class %q", class)
	}

	start := time.Now()
	actx, cancel := context.WithTimeout(ctx, c.cfg.AcquireTimeout)
	conn, err := pool.Acquire(actx)
	cancel()
	waited := time.Since(start)
	c.metrics.observeAcquire(waited)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, nil, ErrPoolTimeout
		}
		return nil, nil, err
	}
	c.poolStats.connectionsAcquired.Add(1)
	release := func() {
		conn.Release()
		c.poolStats.connectionsReleased.Add(1)
	}
	return conn, release, nil
}

// queryOnce runs one statement on one freshly acquired connection.
func (c *DatabaseClient) queryOnce(ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) (pgx.Rows, func(), error) {
	start := time.Now()
	atx, cancel := context.WithTimeout(ctx, c.cfg.AcquireTimeout)
	conn, err := pool.Acquire(atx)
	cancel()
	c.metrics.observeAcquire(time.Since(start))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, nil, ErrPoolTimeout
		}
		return nil, nil, err
	}
	release := func() { conn.Release() }
	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		release()
		return nil, nil, err
	}
	return rows, release, nil
}

// QueryWithMetrics runs a SELECT against the requested tier with full
// observability: duration histogram, error counter, slow-query gate, and the
// IC-3 structured operation log line.
//
// READ failover: acquisition failures on the chosen target count toward the
// breaker and trigger exactly ONE transparent reroute onto the next replica
// (round-robin). SQL statement errors are deterministic results — they are
// never retried nor counted as infra failures.
func (c *DatabaseClient) QueryWithMetrics(ctx context.Context, class OpClass, operation, tenantID string, sql string, args ...any) (pgx.Rows, error) {
	start := time.Now()
	rows, err := c.queryRouted(ctx, class, sql, args...)
	dur := time.Since(start)
	c.metrics.observeQuery(class, operation, dur, err)
	c.slowGate(class, operation, tenantID, dur)
	c.logOperation(ctx, class, operation, tenantID, start, err)
	return rows, err
}

func (c *DatabaseClient) queryRouted(ctx context.Context, class OpClass, sql string, args ...any) (pgx.Rows, error) {
	target, ok := c.pickPool(class)
	if !ok {
		return nil, ErrPrimaryUnavailable
	}
	rows, release, err := c.queryOnce(ctx, target, sql, args...)
	if err == nil {
		wrapped := &releasedRows{Rows: rows, onDone: release}
		return wrapped, nil
	}
	if !isTransportErr(err) || class != OpRead || len(c.readPools) == 0 {
		return nil, err
	}
	// Transport fault on the primary: feed the breaker, reroute once.
	c.breaker.RecordFailure()
	next := c.nextReplica()
	if next == nil {
		return nil, err
	}
	rows2, release2, err2 := c.queryOnce(ctx, next, sql, args...)
	if err2 != nil {
		return nil, err2
	}
	return &releasedRows{Rows: rows2, onDone: release2}, nil
}

// pickPool maps an OpClass to its pool tier (readTarget handles failover).
func (c *DatabaseClient) pickPool(class OpClass) (*pgxpool.Pool, bool) {
	switch class {
	case OpWrite:
		return c.writePool, true
	case OpMetadata:
		return c.metadata, true
	case OpAnalytical:
		return c.analyticalPool, true
	case OpRead:
		return c.readTarget()
	default:
		return nil, false
	}
}

// releasedRows guarantees the underlying pooled connection returns to the
// pool exactly when the caller finishes the Rows — the leak-detection story
// for streamed results.
type releasedRows struct {
	pgx.Rows
	onDone   func()
	released sync.Once
}

func (r *releasedRows) done() { r.released.Do(func() { r.onDone() }) }
func (r *releasedRows) Close() {
	r.Rows.Close()
	r.done()
}
func (r *releasedRows) Next() bool {
	more := r.Rows.Next()
	if !more {
		r.done() // exhausted: auto-release even if Close is forgotten
	}
	return more
}
func (r *releasedRows) Err() error { return r.Rows.Err() }

// ExecWithMetrics is the mutating counterpart of QueryWithMetrics. Writes go
// exclusively to the primary; when the breaker is open they return
// ErrPrimaryUnavailable immediately — never silently rerouted to a replica,
// because a stale-tolerant write would fork history (I-1 violation).
func (c *DatabaseClient) ExecWithMetrics(ctx context.Context, class OpClass, operation, tenantID string, sql string, args ...any) (pgconn.CommandTag, error) {
	start := time.Now()
	tag, err := func() (pgconn.CommandTag, error) {
		conn, release, aerr := c.acquire(ctx, class)
		if aerr != nil {
			return pgconn.CommandTag{}, aerr
		}
		defer release()
		return conn.Exec(ctx, sql, args...)
	}()
	c.recordPrimaryOutcome(class, err)
	dur := time.Since(start)
	c.metrics.observeQuery(class, operation, dur, err)
	c.slowGate(class, operation, tenantID, dur)
	c.logOperation(ctx, class, operation, tenantID, start, err)
	return tag, err
}

// BeginWriteTx starts a transaction on the write pool — the sanctioned path
// for multi-statement mutations (commit-path manifest inserts etc.). The
// returned Tx releases its pooled connection on Commit/Rollback whichever way
// the call lands, so callers cannot strand connections.
func (c *DatabaseClient) BeginWriteTx(ctx context.Context) (pgx.Tx, error) {
	conn, release, err := c.acquire(ctx, OpWrite)
	if err != nil {
		return nil, err
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		release()
		c.recordPrimaryOutcome(OpWrite, err)
		return nil, err
	}
	return &pooledTx{Tx: tx, release: release}, nil
}

// recordPrimaryOutcome feeds the write-path results into the circuit breaker.
// The sentinel ErrPrimaryUnavailable is excluded: it is the breaker's own
// output (open circuit), not evidence about the primary's health — counting
// it would keep pushing the cooldown forward under continuous write pressure.
func (c *DatabaseClient) recordPrimaryOutcome(class OpClass, err error) {
	if class != OpWrite || errors.Is(err, ErrPrimaryUnavailable) {
		return
	}
	switch {
	case err == nil:
		c.breaker.RecordSuccess()
	case isTransportErr(err):
		c.breaker.RecordFailure()
	}
}

type pooledTx struct {
	pgx.Tx
	release func()
	once    sync.Once
}

func (t *pooledTx) finish() { t.once.Do(t.release) }
func (t *pooledTx) Commit(ctx context.Context) error {
	err := t.Tx.Commit(ctx)
	t.finish()
	return err
}
func (t *pooledTx) Rollback(ctx context.Context) error {
	err := t.Tx.Rollback(ctx)
	t.finish()
	return err
}

// ---------------------------------------------------------------------------
// Mutating wrappers — DB mutation + immediate generation bump so cached
// namespace metadata becomes unreachable the instant the mutation lands
// (acceptance criterion: invalidation immediate, no stale reads).
// ---------------------------------------------------------------------------

// MoveDirectory executes move_directory_status (cycle-safe subtree move with
// acl_epoch increments — PROMPT 2.1 stored procedures) and invalidates the
// namespace cache for the tenant. Structured log emitted with duration_ms.
func (c *DatabaseClient) MoveDirectory(ctx context.Context, tenantID string, nodeID, newParentID string) error {
	start := time.Now()
	err := func() error {
		tx, terr := c.BeginWriteTx(ctx)
		if terr != nil {
			return terr
		}
		if _, xerr := tx.Exec(ctx,
			"SELECT move_directory_status($1,$2,$3)", tenantID, nodeID, newParentID); xerr != nil {
			_ = tx.Rollback(ctx)
			return xerr
		}
		return tx.Commit(ctx)
	}()
	dur := time.Since(start)
	c.metrics.observeQuery(OpWrite, "move_directory", dur, err)
	c.slowGate(OpWrite, "move_directory", tenantID, dur)
	c.logOperation(ctx, OpWrite, "move_directory", tenantID, start, err)
	c.bumpGenerationOnSuccess(ctx, tenantID, err, "move")
	return err
}

// GrantACLEntry records a role grant and bumps the generation. (Directory
// moves increment acl_epoch server-side; explicit grants invalidate here.)
func (c *DatabaseClient) GrantACLEntry(ctx context.Context, tenantID string, nodeID, principalType, principalID, role, grantedBy string) error {
	start := time.Now()
	err := func() error {
		tx, terr := c.BeginWriteTx(ctx)
		if terr != nil {
			return terr
		}
		if _, xerr := tx.Exec(ctx,
			`INSERT INTO acl_entries (tenant_id,node_id,principal_type,principal_id,role,granted_by)
			 VALUES ($1,$2,$3,$4,$5,$6)`,
			tenantID, nodeID, principalType, principalID, role, grantedBy); xerr != nil {
			_ = tx.Rollback(ctx)
			return xerr
		}
		return tx.Commit(ctx)
	}()
	dur := time.Since(start)
	c.metrics.observeQuery(OpWrite, "grant_acl", dur, err)
	c.slowGate(OpWrite, "grant_acl", tenantID, dur)
	c.logOperation(ctx, OpWrite, "grant_acl", tenantID, start, err)
	c.bumpGenerationOnSuccess(ctx, tenantID, err, "acl")
	return err
}

// CommitFileVersion allocates the next fenced version number (node-row lock,
// gapless per isolation-policy §4), inserts the immutable revision, bumps the
// generation, and returns the dense version number.
func (c *DatabaseClient) CommitFileVersion(ctx context.Context, tenantID, nodeID string, totalSize int64, contentSHA256 []byte, createdBy string) (int, error) {
	start := time.Now()
	version, err := func() (int, error) {
		tx, terr := c.BeginWriteTx(ctx)
		if terr != nil {
			return 0, terr
		}
		var v int
		if qerr := tx.QueryRow(ctx, "SELECT next_version_number($1)", nodeID).Scan(&v); qerr != nil {
			_ = tx.Rollback(ctx)
			return 0, qerr
		}
		if _, ierr := tx.Exec(ctx,
			`INSERT INTO file_versions (node_id,version_number,total_size_bytes,content_sha256,created_by)
			 VALUES ($1,$2,$3,$4,$5)`,
			nodeID, v, totalSize, contentSHA256, createdBy); ierr != nil {
			_ = tx.Rollback(ctx)
			return 0, ierr
		}
		if cerr := tx.Commit(ctx); cerr != nil {
			return 0, cerr
		}
		return v, nil
	}()
	dur := time.Since(start)
	c.metrics.observeQuery(OpWrite, "commit_file_version", dur, err)
	c.slowGate(OpWrite, "commit_file_version", tenantID, dur)
	c.logOperation(ctx, OpWrite, "commit_file_version", tenantID, start, err)
	c.bumpGenerationOnSuccess(ctx, tenantID, err, "version")
	return version, err
}

// bumpGenerationOnSuccess performs the post-commit cache invalidation; a
// Redis outage here MUST NOT fail an already-committed mutation — we log at
// error level and rely on TTL expiry as the backstop (documented tradeoff:
// worst case staleness ≤ CacheTTL during a Redis incident).
func (c *DatabaseClient) bumpGenerationOnSuccess(ctx context.Context, tenantID string, dbErr error, cause string) {
	if dbErr != nil || c.cache == nil {
		return
	}
	bctx, cancel := context.WithTimeout(context.Background(), c.cfg.AcquireTimeout)
	defer cancel()
	if _, gerr := c.cache.BumpGeneration(bctx, tenantID); gerr != nil {
		c.logger.Error("cache generation bump failed",
			"cause", cause, "tenant_id", tenantID, "err", gerr.Error())
	}
}

// GetNodeMetadataCached resolves node metadata through the Redis layer
// (cache-aside); falls back to a direct metadata-pool read when caching is
// disabled. Loader traffic lands on the METADATA tier — never on writes.
func (c *DatabaseClient) GetNodeMetadataCached(ctx context.Context, tenantID, nodeID string) (NodeMeta, error) {
	loader := func(lctx context.Context) (NodeMeta, error) {
		conn, release, aerr := c.acquire(lctx, OpMetadata)
		if aerr != nil {
			return NodeMeta{}, aerr
		}
		defer release()
		row := conn.QueryRow(lctx, `
			SELECT n.node_id::text, n.tenant_id::text, n.parent_id::text,
			       n.name, n.type::text, n.lineage_path::text, n.acl_epoch
			  FROM namespace_nodes n
			 WHERE n.tenant_id = $1 AND n.node_id = $2`, tenantID, nodeID)
		var m NodeMeta
		if serr := row.Scan(&m.NodeID, &m.TenantID, &m.ParentID,
			&m.Name, &m.Type, &m.LineagePath, &m.ACLEpoch); serr != nil {
			return NodeMeta{}, serr
		}
		return m, nil
	}
	if c.cache == nil {
		return loader(ctx)
	}
	return c.cache.GetNodeMetadata(ctx, tenantID, nodeID, loader)
}

// isTransportErr classifies infrastructure faults eligible for failover
// handling; SQL semantics errors are excluded on purpose — retrying them
// would double-apply business effects.
func isTransportErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrPoolTimeout) || errors.Is(err, ErrPrimaryUnavailable) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "53300": // too_many_connections
			return true
		case "57P01", "57P02", "57P03": // admin shutdown / crash / cannot connect now
			return true
		}
		return false
	}
	var timeoutErr interface{ Timeout() bool }
	if errors.As(err, &timeoutErr) {
		return timeoutErr.Timeout()
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, syscallECONNREFUSED) {
		return true
	}
	// pgx dial failures (refused/unreachable host) arrive as ConnectError.
	var connErr *pgconn.ConnectError
	return errors.As(err, &connErr)
}
