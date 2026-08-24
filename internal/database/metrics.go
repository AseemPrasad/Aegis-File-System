package database

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics carries all observability primitives for the client
// (PROMPT 2.2 observability contract):
//   - query duration histogram per (pool, operation) → p50/p95/p99
//   - acquire-wait histogram → pool saturation signal
//   - acquired/released counters → leak detection (delta must return to 0)
//   - error counter per (pool, operation) → per-op error rate
//   - slow-query counter (threshold-gated, default 50ms)
type Metrics struct {
	reg prometheus.Registerer

	queryDuration *prometheus.HistogramVec // labels: pool, op
	acquireWait   prometheus.Histogram
	acquired      prometheus.Counter
	released      prometheus.Counter
	errors        *prometheus.CounterVec // labels: pool, op
	slowQueries   prometheus.Counter

	// waitReservoir feeds DatabaseClient.poolStats.waitTimeP95 without
	// requiring Prometheus quantile queries.
	waitReservoir waitReservoir
}

func newMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{reg: reg}
	m.queryDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "aegis",
		Subsystem: "db",
		Name:      "query_duration_seconds",
		Help:      "Query execution latency by pool tier and operation.",
		// Buckets resolve the sub-5ms p95 SLA: dense below 5ms.
		Buckets: []float64{0.0005, 0.001, 0.002, 0.003, 0.004, 0.005,
			0.0075, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
	}, []string{"pool", "op"})
	m.acquireWait = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "aegis",
		Subsystem: "db",
		Name:      "acquire_wait_seconds",
		Help:      "Time spent waiting for a free connection.",
		Buckets:   prometheus.ExponentialBuckets(0.0001, 2, 16), // 100µs..3.3s
	})
	m.acquired = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "aegis", Subsystem: "db", Name: "connections_acquired_total"})
	m.released = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "aegis", Subsystem: "db", Name: "connections_released_total"})
	m.errors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "aegis", Subsystem: "db", Name: "query_errors_total",
		Help: "Query errors by pool tier and operation."}, []string{"pool", "op"})
	m.slowQueries = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "aegis", Subsystem: "db", Name: "slow_queries_total",
		Help: "Queries exceeding the slow-query threshold (50ms)."})
	if reg != nil {
		reg.MustRegister(m.queryDuration, m.acquireWait, m.acquired,
			m.released, m.errors, m.slowQueries)
	}
	return m
}

// sharedMetrics guards the process-wide collector registration: Prometheus
// collectors must register exactly once per registry, while DatabaseClient
// instances may come and go freely in tests and hot-reload scenarios.
var (
	sharedOnce   sync.Once
	sharedMetric *Metrics
)

func defaultMetrics() *Metrics {
	sharedOnce.Do(func() { sharedMetric = newMetrics(prometheus.DefaultRegisterer) })
	return sharedMetric
}

// observeQuery records one executed statement.
func (m *Metrics) observeQuery(pool OpClass, op string, d time.Duration, err error) {
	if m == nil {
		return
	}
	m.queryDuration.WithLabelValues(string(pool), op).Observe(d.Seconds())
	if err != nil {
		m.errors.WithLabelValues(string(pool), op).Inc()
	}
}

// observeAcquire records connection-wait time.
func (m *Metrics) observeAcquire(d time.Duration) {
	if m == nil {
		return
	}
	m.acquireWait.Observe(d.Seconds())
	m.waitReservoir.add(d)
}

// waitReservoir is a fixed-size ring of recent acquire waits; p95 reads copy
// and sort under a tiny mutex — writes take an atomic slot index so the hot
// path stays contention-free.
type waitReservoir struct {
	buf  [1024]time.Duration
	idx  atomic.Uint64
	mu   sync.Mutex // guards sort/copy only, never writers
	full atomic.Bool
}

func (r *waitReservoir) add(d time.Duration) {
	i := r.idx.Add(1) - 1
	r.buf[i%uint64(len(r.buf))] = d
	if i+1 >= uint64(len(r.buf)) {
		r.full.Store(true)
	}
}

// p95 returns the 95th-percentile recent acquire wait; ok=false until at
// least one sample exists.
func (r *waitReservoir) p95() (time.Duration, bool) {
	n := uint64(len(r.buf))
	if !r.full.Load() {
		n = r.idx.Load()
	}
	if n == 0 {
		return 0, false
	}
	s := make([]time.Duration, n)
	r.mu.Lock()
	copy(s, r.buf[:n])
	r.mu.Unlock()
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	// Nearest-rank percentile: ceil(0.95*n) as a 1-based index.
	return s[(n*95+99)/100-1], true
}

// slowGate reports whether a query exceeded the slow threshold and, when it
// did, increments the counter and emits the IC-3 structured log line.
func (c *DatabaseClient) slowGate(op OpClass, operation, tenantID string, d time.Duration) {
	if d < c.cfg.SlowQueryThreshold {
		return
	}
	c.metrics.slowQueries.Inc()
	c.logger.Warn("slow query",
		"pool", string(op),
		"operation", operation,
		"tenant_id", tenantID,
		"duration_ms", d.Milliseconds(),
	)
}

// logOperation emits the structured operation record mandated by PROMPT 2.2:
//
//	{"operation":"move_directory","tenant_id":"...","duration_ms":12}
func (c *DatabaseClient) logOperation(ctx context.Context, op OpClass, operation, tenantID string, start time.Time, err error) {
	rec := struct {
		Operation  string `json:"operation"`
		TenantID   string `json:"tenant_id,omitempty"`
		DurationMS int64  `json:"duration_ms"`
		Pool       string `json:"pool"`
	}{operation, tenantID, time.Since(start).Milliseconds(), string(op)}
	level := slog.LevelInfo
	msg := "db.operation"
	if err != nil {
		level, msg = slog.LevelError, "db.operation.failed"
	}
	c.logger.Log(ctx, level, msg,
		"operation", rec.Operation,
		"tenant_id", rec.TenantID,
		"duration_ms", rec.DurationMS,
		"pool", rec.Pool,
		"err", err,
	)
}
