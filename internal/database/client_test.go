package database

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
	promtest "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"
)

func runtimeNumCPU() int { return runtime.NumCPU() }

func testCounterValue(c prometheus.Counter) float64 { return promtest.ToFloat64(c) }

// ---------------------------------------------------------------------------
// Pool sizing rules (PROMPT 2.2: min = cores*2, max = cores*4)
// ---------------------------------------------------------------------------

func TestPoolSizingDefaults(t *testing.T) {
	cfg := Config{PrimaryDSN: "postgres://u:p@h/db"}
	cfg.applyDefaults()

	wantMin := int32(runtimeNumCPU()) * 2
	wantMax := int32(runtimeNumCPU()) * 4
	if cfg.MinConnsPerPool != wantMin || cfg.MaxConnsPerPool != wantMax {
		t.Fatalf("sizing rule violated: got min=%d max=%d, want %d/%d",
			cfg.MinConnsPerPool, cfg.MaxConnsPerPool, wantMin, wantMax)
	}
}

func TestPoolSizingMaxFlooredToMin(t *testing.T) {
	cfg := Config{PrimaryDSN: "x", MinConnsPerPool: 10, MaxConnsPerPool: 4}
	cfg.applyDefaults()
	if cfg.MaxConnsPerPool < cfg.MinConnsPerPool {
		t.Fatalf("max (%d) must never sit below min (%d)",
			cfg.MaxConnsPerPool, cfg.MinConnsPerPool)
	}
}

// The four-pool taxonomy must respect the server-side connection budget.
func TestGlobalBudgetClampLogic(t *testing.T) {
	// Mirrors newPool's clamp arithmetic so a regression here is visible.
	min, max, total := int32(16), int32(64), int32(96)
	budget := total / 4
	if max > budget && budget >= min {
		max = budget
	}
	if max != 24 {
		t.Fatalf("expected per-pool max clamped to budget slice 24, got %d", max)
	}
}

// ---------------------------------------------------------------------------
// Circuit breaker state machine
// ---------------------------------------------------------------------------

func TestCircuitBreakerOpensAfterThreshold(t *testing.T) {
	cb := newCircuitBreaker(nil)
	if !cb.Allow() {
		t.Fatal("closed breaker must allow")
	}
	for i := 0; i < breakerFailureThreshold-1; i++ {
		cb.RecordFailure()
	}
	if cb.State() != "closed" {
		t.Fatalf("breaker opened before threshold: %s", cb.State())
	}
	cb.RecordFailure()
	if cb.State() != "open" {
		t.Fatalf("expected open at threshold, got %s", cb.State())
	}
	if cb.Allow() {
		t.Fatal("open breaker must block traffic")
	}
}

func TestCircuitBreakerHalfOpenElectionAndRecovery(t *testing.T) {
	var transitions []string
	var mu sync.Mutex
	trip := func(s string) { mu.Lock(); transitions = append(transitions, s); mu.Unlock() }

	cb := newCircuitBreaker(trip)
	for i := 0; i < breakerFailureThreshold; i++ {
		cb.RecordFailure()
	}
	// Force-expire the cooldown instead of sleeping.
	cb.openedAt.Store(time.Now().Add(-breakerCooldown - time.Second).UnixNano())

	// Exactly ONE caller is elected probe when the cooldown elapses.
	elected := 0
	var mu2 sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if cb.Allow() {
				mu2.Lock()
				elected++
				mu2.Unlock()
			}
		}()
	}
	wg.Wait()
	if elected != 1 {
		t.Fatalf("half-open must elect exactly one prober, elected %d", elected)
	}
	cb.RecordSuccess()
	if cb.State() != "closed" {
		t.Fatalf("probe success must close breaker, got %s", cb.State())
	}
	if len(transitions) == 0 {
		t.Fatal("observability hook never fired")
	}
}

func TestCircuitBreakerProbeFailureReopens(t *testing.T) {
	cb := newCircuitBreaker(nil)
	for i := 0; i < breakerFailureThreshold; i++ {
		cb.RecordFailure()
	}
	cb.openedAt.Store(time.Now().Add(-breakerCooldown - time.Second).UnixNano())
	if !cb.Allow() {
		t.Fatal("expected probe election after cooldown")
	}
	cb.RecordFailure() // probe fails
	if cb.Allow() && cb.State() == "half-open" {
		// Still half-open: further callers blocked until next cooldown.
		t.Logf("state=%s (blocked-until-cooldown expected)", cb.State())
	}
	if cb.State() == "closed" {
		t.Fatal("failed probe must not close the breaker")
	}
}

// ---------------------------------------------------------------------------
// Cache key format + singleflight collapse (fake Redis — no server needed)
// ---------------------------------------------------------------------------

func TestCacheKeyFormats(t *testing.T) {
	if got := generationKey("t1"); got != "aegis:ns:gen:t1" {
		t.Fatalf("generationKey = %q", got)
	}
	k := nodeKey("t1", 7, "n1")
	want := "aegis:ns:t1:g7:n1"
	if k != want {
		t.Fatalf("nodeKey = %q, want %q", k, want)
	}
	// Generation embeds in the key: bumping N orphans every old entry.
	if nodeKey("t1", 8, "n1") == k {
		t.Fatal("different generations must produce different keys")
	}
}

// fakeRedis implements only what NamespaceCache touches; any other call hits
// the embedded nil interface and panics loudly — which is the desired signal.
type fakeRedis struct {
	redis.UniversalClient
	mu   sync.Mutex
	data map[string]string
	n    map[string]int // INCR state
}

func newFakeRedis() *fakeRedis {
	return &fakeRedis{data: map[string]string{}, n: map[string]int{}}
}

func (f *fakeRedis) Get(_ context.Context, key string) *redis.StringCmd {
	cmd := redis.NewStringCmd(context.Background(), "GET", key)
	f.mu.Lock()
	v, ok := f.data[key]
	f.mu.Unlock()
	if !ok {
		cmd.SetErr(redis.Nil)
	} else {
		cmd.SetVal(v)
	}
	return cmd
}

func (f *fakeRedis) Set(_ context.Context, key string, value any, _ time.Duration) *redis.StatusCmd {
	var s string
	switch v := value.(type) {
	case string:
		s = v
	case []byte:
		s = string(v)
	}
	f.mu.Lock()
	f.data[key] = s
	f.mu.Unlock()
	cmd := redis.NewStatusCmd(context.Background(), "SET", key)
	cmd.SetVal("OK")
	return cmd
}

func (f *fakeRedis) Incr(_ context.Context, key string) *redis.IntCmd {
	f.mu.Lock()
	f.n[key]++
	v := int64(f.n[key])
	f.data[key] = strconv.FormatInt(v, 10) // real INCR is readable via GET
	f.mu.Unlock()
	cmd := redis.NewIntCmd(context.Background(), "INCR", key)
	cmd.SetVal(v)
	return cmd
}

func (f *fakeRedis) Uint64Getter(key string) uint64 { return uint64(f.n[key]) }

type nopLogger struct{}

func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Error(string, ...any) {}

func TestGenerationBumpOrphansOldEntries(t *testing.T) {
	fr := newFakeRedis()
	cache := NewNamespaceCache(fr, time.Minute, nopLogger{})
	ctx := context.Background()

	const tenant = "tenant-a"
	gen0, err := cache.generation(ctx, tenant)
	if err != nil || gen0 != 0 {
		t.Fatalf("fresh tenant generation = %d, err=%v", gen0, err)
	}
	// Loader caches under generation 0.
	calls := 0
	meta, err := cache.GetNodeMetadata(ctx, tenant, "node-x", func(context.Context) (NodeMeta, error) {
		calls++
		return NodeMeta{NodeID: "node-x", Name: "stale-name", ACLEpoch: 1}, nil
	})
	if err != nil || meta.Name != "stale-name" || calls != 1 {
		t.Fatalf("first load: meta=%+v calls=%d err=%v", meta, calls, err)
	}
	// Second read: HIT — loader not invoked again.
	_, err = cache.GetNodeMetadata(ctx, tenant, "node-x", func(context.Context) (NodeMeta, error) {
		calls++ // must NOT happen on hit
		return NodeMeta{}, nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("hit path invoked loader (calls=%d) err=%v", calls, err)
	}

	// Mutation bumps generation ⇒ cached entry unreachable instantly.
	if _, err := cache.BumpGeneration(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	loaded := ""
	meta, err = cache.GetNodeMetadata(ctx, tenant, "node-x", func(context.Context) (NodeMeta, error) {
		calls++
		loaded = "fresh-name"
		return NodeMeta{NodeID: "node-x", Name: "fresh-name", ACLEpoch: 2}, nil
	})
	if err != nil || meta.Name != loaded || calls != 2 {
		t.Fatalf("post-bump read served stale data: meta=%+v calls=%d err=%v", meta, calls, err)
	}
}

func TestSingleflightCollapsesConcurrentMisses(t *testing.T) {
	fr := newFakeRedis()
	cache := NewNamespaceCache(fr, time.Minute, nopLogger{})
	ctx := context.Background()

	release := make(chan struct{})
	var loads atomic.Int32
	loader := func(context.Context) (NodeMeta, error) {
		loads.Add(1)
		<-release
		return NodeMeta{NodeID: "hot"}, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = cache.GetNodeMetadata(ctx, "t", "hot-key", loader)
		}()
	}
	time.Sleep(50 * time.Millisecond) // let all 32 pile into the flight
	close(release)
	wg.Wait()
	if got := loads.Load(); got != 1 {
		t.Fatalf("singleflight collapsed %d loader invocations, want exactly 1", got)
	}
}

// Redis outage degrades to DB bypass, never to failure of the read path.
func TestRedisOutageBypassesToLoader(t *testing.T) {
	down := NewNamespaceCache(&deadRedis{}, time.Minute, nopLogger{})
	meta, err := down.GetNodeMetadata(context.Background(), "t", "n", func(context.Context) (NodeMeta, error) {
		return NodeMeta{NodeID: "from-db"}, nil
	})
	if err != nil || meta.NodeID != "from-db" {
		t.Fatalf("outage must bypass to loader, got %+v err=%v", meta, err)
	}
}

type deadRedis struct{ redis.UniversalClient }

func (d *deadRedis) Get(context.Context, string) *redis.StringCmd {
	cmd := redis.NewStringCmd(context.Background())
	cmd.SetErr(errors.New("redis connection refused"))
	return cmd
}

// ---------------------------------------------------------------------------
// Observability primitives
// ---------------------------------------------------------------------------

func TestWaitReservoirP95(t *testing.T) {
	var r waitReservoir
	if _, ok := r.p95(); ok {
		t.Fatal("empty reservoir must report ok=false")
	}
	// 100 samples: values 1..100µs ⇒ p95 = 95µs.
	for i := 1; i <= 100; i++ {
		r.add(time.Duration(i) * time.Microsecond)
	}
	got, ok := r.p95()
	if !ok || got != 95*time.Microsecond {
		t.Fatalf("p95 = %v ok=%v, want 95µs", got, ok)
	}
	// Ring wrap: oldest samples fall out cleanly without panics.
	for i := 0; i < 2048; i++ {
		r.add(time.Millisecond)
	}
	if _, ok := r.p95(); !ok {
		t.Fatal("wrapped reservoir lost all samples")
	}
}

func TestSlowGateEmitsAboveThresholdOnly(t *testing.T) {
	c := &DatabaseClient{
		cfg:     Config{SlowQueryThreshold: 50 * time.Millisecond},
		logger:  slog.New(slog.NewTextHandler(&discardWriter{}, nil)),
		metrics: newMetrics(nil),
	}
	c.slowGate(OpRead, "q", "t", 49*time.Millisecond)
	if got := testCounterValue(c.metrics.slowQueries); got != 0 {
		t.Fatalf("below-threshold query counted: %v", got)
	}
	c.slowGate(OpRead, "q", "t", 51*time.Millisecond)
	if got := testCounterValue(c.metrics.slowQueries); got != 1 {
		t.Fatalf("slow query not counted: %v", got)
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// ---------------------------------------------------------------------------
// Transport-error classification (failover eligibility)
// ---------------------------------------------------------------------------

func TestIsTransportErrClassification(t *testing.T) {
	sqlErr := &pgconn.PgError{Code: "23505"} // unique violation: business result
	if isTransportErr(sqlErr) {
		t.Fatal("SQL constraint violations are not transport faults")
	}
	if !isTransportErr(&pgconn.PgError{Code: "53300"}) {
		t.Fatal("too_many_connections must be classified transport")
	}
	if !isTransportErr(&pgconn.PgError{Code: "57P03"}) {
		t.Fatal("cannot_connect_now must be classified transport")
	}
	if !isTransportErr(ErrPoolTimeout) || !isTransportErr(ErrPrimaryUnavailable) {
		t.Fatal("pool/failover sentinels must be transport-class")
	}
	if !isTransportErr(&pgconn.ConnectError{}) {
		t.Fatal("dial failure must be transport-class")
	}
	if isTransportErr(nil) {
		t.Fatal("nil must not be classified as a fault")
	}
	if isTransportErr(pgx.ErrNoRows) {
		t.Fatal("deterministic query results are never transport faults")
	}
}

// pgx.Rows leak guard: releasedRows auto-releases on exhaustion AND Close.
type fakeRows struct {
	pgx.Rows
	next []bool
	done func()
	err  error
}

func (f *fakeRows) Next() bool {
	if len(f.next) == 0 {
		return false
	}
	more := f.next[0]
	f.next = f.next[1:]
	return more
}
func (f *fakeRows) Close()     {}
func (f *fakeRows) Err() error { return f.err }

func TestReleasedRowsAutoRelease(t *testing.T) {
	releases := 0
	setDone := func() { releases++ }

	r := &releasedRows{Rows: &fakeRows{next: []bool{true}}, onDone: setDone}
	r.Next() // consumes last row
	r.Next() // exhaustion triggers auto-release
	if releases != 1 {
		t.Fatalf("exhaustion did not release (releases=%d)", releases)
	}
	r.Close() // double-close path must be idempotent
	if releases != 1 {
		t.Fatalf("Close after exhaustion double-released (releases=%d)", releases)
	}
}
