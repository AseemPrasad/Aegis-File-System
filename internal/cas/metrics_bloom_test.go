package cas

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// ---------------------------------------------------------------------------
// mockCmdRunner — test double for RedisCmdRunner
// ---------------------------------------------------------------------------

type mockCmdRunner struct {
	reserveErr  error
	addResult   bool
	addErr      error
	existResult bool
	existErr    error
	delResult   int64
	delErr      error

	addFn  func(ctx context.Context, key, element string) (bool, error)
	resFn  func(ctx context.Context, key string, errorRate float64, capacity int64) error
	exFn   func(ctx context.Context, key, element string) (bool, error)
	delFn  func(ctx context.Context, keys ...string) (int64, error)
}

func (m *mockCmdRunner) BFReserve(ctx context.Context, key string, errorRate float64, capacity int64) error {
	if m.resFn != nil {
		return m.resFn(ctx, key, errorRate, capacity)
	}
	return m.reserveErr
}

func (m *mockCmdRunner) BFAdd(ctx context.Context, key string, element string) (bool, error) {
	if m.addFn != nil {
		return m.addFn(ctx, key, element)
	}
	return m.addResult, m.addErr
}

func (m *mockCmdRunner) BFExists(ctx context.Context, key string, element string) (bool, error) {
	if m.exFn != nil {
		return m.exFn(ctx, key, element)
	}
	return m.existResult, m.existErr
}

func (m *mockCmdRunner) Del(ctx context.Context, keys ...string) (int64, error) {
	if m.delFn != nil {
		return m.delFn(ctx, keys...)
	}
	return m.delResult, m.delErr
}

// ---------------------------------------------------------------------------
// Tests — CASMetrics helpers
// ---------------------------------------------------------------------------

func TestCASMetrics_ObserveRefCounts(t *testing.T) {
	reg := prometheus.NewPedanticRegistry()
	m := NewCASMetrics(reg)

	m.ObserveRefCounts([]int64{1, 5, 100})

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather failed: %v", err)
	}
	for _, fam := range families {
		if fam.GetName() == "aegis_cas_ref_count_distribution" {
			h := fam.GetMetric()[0].GetHistogram()
			if h.GetSampleCount() != 3 {
				t.Errorf("expected 3 observations, got %d", h.GetSampleCount())
			}
			return
		}
	}
	t.Error("ref_count_distribution histogram not found")
}

func TestCASMetrics_ObserveRefCounts_Empty(t *testing.T) {
	reg := prometheus.NewPedanticRegistry()
	m := NewCASMetrics(reg)
	m.ObserveRefCounts([]int64{})
}

func TestCASMetrics_IncHelpers(t *testing.T) {
	reg := prometheus.NewPedanticRegistry()
	m := NewCASMetrics(reg)

	m.IncSessionsExpired()
	m.IncBlocksTombstoned()
	m.IncBlocksDeleted()
	m.IncDeletionFailed()
	m.IncRateLimitHit()
	m.IncDoubleCheckSaved()

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather failed: %v", err)
	}

	counters := make(map[string]float64)
	for _, fam := range families {
		if fam.GetMetric()[0].GetCounter() != nil {
			counters[fam.GetName()] = fam.GetMetric()[0].GetCounter().GetValue()
		}
	}

	expect := map[string]float64{
		"aegis_cas_gc_sessions_expired_total":  1,
		"aegis_cas_gc_blocks_tombstoned_total": 1,
		"aegis_cas_gc_blocks_deleted_total":    1,
		"aegis_cas_gc_deletion_failed_total":   1,
		"aegis_cas_gc_rate_limit_hit_total":    1,
		"aegis_cas_gc_double_check_saved_total": 1,
	}
	for name, want := range expect {
		if got := counters[name]; got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
}

func TestCASMetrics_RefreshFromStats_NilStats(t *testing.T) {
	reg := prometheus.NewPedanticRegistry()
	m := NewCASMetrics(reg)
	m.RefreshFromStats(nil, 0)
}

func TestCASMetrics_RefreshFromStats_NilReceiver(t *testing.T) {
	var m *CASMetrics
	m.RefreshFromStats(nil, 0)
}

func TestCASMetrics_RefreshFromStats_NilReceiverWithStats(t *testing.T) {
	var m *CASMetrics
	m.RefreshFromStats(&StorageStats{TotalBlocks: 10}, 0)
}

func TestCASMetrics_RefreshFromStats_ZeroContentBytes(t *testing.T) {
	reg := prometheus.NewPedanticRegistry()
	m := NewCASMetrics(reg)
	m.RefreshFromStats(&StorageStats{TotalBlocks: 5, TotalBytes: 100}, 0)
}

// ---------------------------------------------------------------------------
// Tests — RedisBloomFilter AddBatch
// ---------------------------------------------------------------------------

func TestRedisBloomFilter_AddBatch(t *testing.T) {
	runner := &mockCmdRunner{addResult: true}
	bf := NewRedisBloomFilter(runner, "test-endpoint", nil)
	ctx := context.Background()

	added, err := bf.AddBatch(ctx, []string{"hash_a", "hash_b", "hash_c"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if added != 3 {
		t.Errorf("expected 3 added, got %d", added)
	}
}

func TestRedisBloomFilter_AddBatch_Deduplicates(t *testing.T) {
	seen := make(map[string]bool)
	runner := &mockCmdRunner{
		addFn: func(_ context.Context, _, element string) (bool, error) {
			if seen[element] {
				return false, nil
			}
			seen[element] = true
			return true, nil
		},
	}
	bf := NewRedisBloomFilter(runner, "test-endpoint", nil)
	ctx := context.Background()

	added, err := bf.AddBatch(ctx, []string{"hash_a", "hash_a", "hash_b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if added != 2 {
		t.Errorf("expected 2 added (dedup), got %d", added)
	}
}

func TestRedisBloomFilter_AddBatch_Error(t *testing.T) {
	call := 0
	runner := &mockCmdRunner{
		addFn: func(_ context.Context, _, _ string) (bool, error) {
			call++
			if call == 2 {
				return false, fmt.Errorf("redis connection lost")
			}
			return true, nil
		},
		reserveErr: fmt.Errorf("reserve also failed"),
	}
	bf := NewRedisBloomFilter(runner, "test-endpoint", nil)
	ctx := context.Background()

	added, err := bf.AddBatch(ctx, []string{"h1", "h2", "h3"})
	if err == nil {
		t.Fatal("expected error on second hash, got nil")
	}
	if added != 1 {
		t.Errorf("expected 1 added before error, got %d", added)
	}
}

func TestRedisBloomFilter_AddBatch_Empty(t *testing.T) {
	runner := &mockCmdRunner{}
	bf := NewRedisBloomFilter(runner, "test-endpoint", nil)
	ctx := context.Background()

	added, err := bf.AddBatch(ctx, []string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if added != 0 {
		t.Errorf("expected 0 added, got %d", added)
	}
}

func TestRedisBloomFilter_AddBatch_Nil(t *testing.T) {
	runner := &mockCmdRunner{}
	bf := NewRedisBloomFilter(runner, "test-endpoint", nil)
	ctx := context.Background()

	added, err := bf.AddBatch(ctx, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if added != 0 {
		t.Errorf("expected 0 added, got %d", added)
	}
}

// ---------------------------------------------------------------------------
// Tests — RedisBloomFilter reserve-retry path in Add
// ---------------------------------------------------------------------------

func TestRedisBloomFilter_Add_ReserveRetrySuccess(t *testing.T) {
	call := 0
	runner := &mockCmdRunner{
		addFn: func(_ context.Context, _, _ string) (bool, error) {
			call++
			if call == 1 {
				return false, fmt.Errorf("WRONGTYPE")
			}
			return true, nil
		},
		reserveErr: nil,
		delResult:  1,
	}
	bf := NewRedisBloomFilter(runner, "test-endpoint", nil)
	ctx := context.Background()

	isNew, err := bf.Add(ctx, "hash1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isNew {
		t.Error("expected isNew=true after reserve retry")
	}
}

func TestRedisBloomFilter_Add_ReserveFails(t *testing.T) {
	runner := &mockCmdRunner{
		addErr:     fmt.Errorf("WRONGTYPE"),
		reserveErr: fmt.Errorf("OOM"),
	}
	bf := NewRedisBloomFilter(runner, "test-endpoint", nil)
	ctx := context.Background()

	_, err := bf.Add(ctx, "hash1")
	if err == nil {
		t.Fatal("expected error when reserve fails")
	}
}

// ---------------------------------------------------------------------------
// Tests — RedisBloomFilter Reset
// ---------------------------------------------------------------------------

func TestRedisBloomFilter_Reset(t *testing.T) {
	runner := &mockCmdRunner{delResult: 1}
	bf := NewRedisBloomFilter(runner, "test-endpoint", nil)
	ctx := context.Background()

	err := bf.Reset(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRedisBloomFilter_Reset_DelError(t *testing.T) {
	runner := &mockCmdRunner{delErr: fmt.Errorf("del failed")}
	bf := NewRedisBloomFilter(runner, "test-endpoint", nil)
	ctx := context.Background()

	err := bf.Reset(ctx)
	if err == nil {
		t.Fatal("expected error when Del fails")
	}
}

// ---------------------------------------------------------------------------
// Tests — NewRedisBloomFilter constructor
// ---------------------------------------------------------------------------

func TestNewRedisBloomFilter_NilLogger(t *testing.T) {
	runner := &mockCmdRunner{}
	bf := NewRedisBloomFilter(runner, "ep1", nil)
	if bf == nil {
		t.Fatal("expected non-nil bloom filter")
	}
	if bf.key != "aegis:cas:bloom:ep1" {
		t.Errorf("unexpected key: %s", bf.key)
	}
}

func TestNewRedisBloomFilter_WithLogger(t *testing.T) {
	runner := &mockCmdRunner{}
	bf := NewRedisBloomFilter(runner, "ep2", nil)
	if bf.key != "aegis:cas:bloom:ep2" {
		t.Errorf("unexpected key: %s", bf.key)
	}
}

// ---------------------------------------------------------------------------
// Tests — RedisBloomFilter Add return values
// ---------------------------------------------------------------------------

func TestRedisBloomFilter_Add_ReturnsNew(t *testing.T) {
	runner := &mockCmdRunner{addResult: true}
	bf := NewRedisBloomFilter(runner, "test-endpoint", nil)
	ctx := context.Background()

	isNew, err := bf.Add(ctx, "h1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isNew {
		t.Error("expected isNew=true")
	}
}

func TestRedisBloomFilter_Add_ReturnsExisting(t *testing.T) {
	runner := &mockCmdRunner{addResult: false}
	bf := NewRedisBloomFilter(runner, "test-endpoint", nil)
	ctx := context.Background()

	isNew, err := bf.Add(ctx, "h1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isNew {
		t.Error("expected isNew=false")
	}
}

// ---------------------------------------------------------------------------
// Tests — RedisBloomFilter AddBatch all new vs all existing
// ---------------------------------------------------------------------------

func TestRedisBloomFilter_AddBatch_AllExisting(t *testing.T) {
	runner := &mockCmdRunner{addResult: false}
	bf := NewRedisBloomFilter(runner, "test-endpoint", nil)
	ctx := context.Background()

	added, err := bf.AddBatch(ctx, []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if added != 0 {
		t.Errorf("expected 0 new, got %d", added)
	}
}

func TestRedisBloomFilter_AddBatch_Mixed(t *testing.T) {
	calls := 0
	runner := &mockCmdRunner{
		addFn: func(_ context.Context, _, _ string) (bool, error) {
			calls++
			return calls%2 == 1, nil
		},
	}
	bf := NewRedisBloomFilter(runner, "test-endpoint", nil)
	ctx := context.Background()

	added, err := bf.AddBatch(ctx, []string{"a", "b", "c", "d"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if added != 2 {
		t.Errorf("expected 2 new (alternating), got %d", added)
	}
}

// ---------------------------------------------------------------------------
// GC sweep error path tests
// ---------------------------------------------------------------------------

func TestGCSweep_FindOrphansError(t *testing.T) {
	reg := NewFakeRegistry()
	reg.SetErr(fmt.Errorf("db down"))
	worker := NewGCWorker(reg, nil, nil, nil, GCConfig{})
	worker.sweep(context.Background())
}

func TestGCSweep_DeleteBlocksError(t *testing.T) {
	reg := NewFakeRegistry()
	mock := &deleteErrRegistry{FakeRegistry: reg}
	worker := NewGCWorker(mock, nil, nil, nil, GCConfig{})
	mock.Seed("aabb000000000000000000000000000000000000000000000000000000000001", "tenant", 100, 0)
	worker.sweep(context.Background())
}

func TestGCSweep_InvalidHexHash(t *testing.T) {
	reg := NewFakeRegistry()
	worker := NewGCWorker(reg, nil, nil, nil, GCConfig{})
	// Seed an orphan with an invalid hex hash (odd length).
	reg.mu.Lock()
	reg.blocks["zzz"] = &fakeRow{
		hash:     []byte("zzz"),
		tenantID: "t",
		size:     100,
		refCount: 0,
		tier:     "HOT",
		created:  time.Now().Add(-8 * 24 * time.Hour),
	}
	reg.mu.Unlock()
	worker.sweep(context.Background())
}

func TestGCSweep_WithMetrics(t *testing.T) {
	reg := NewFakeRegistry()
	reg.Seed("aabb000000000000000000000000000000000000000000000000000000000001", "tenant", 100, 0)
	metrics := NewCASMetrics(prometheus.NewPedanticRegistry())
	worker := NewGCWorker(reg, nil, metrics, nil, GCConfig{})
	worker.sweep(context.Background())
}

func TestGCSweep_WithBlobDeleterError(t *testing.T) {
	reg := NewFakeRegistry()
	reg.Seed("aabb000000000000000000000000000000000000000000000000000000000001", "tenant", 100, 0)
	blobErr := fmt.Errorf("blob unavailable")
	worker := NewGCWorker(reg, func(ctx context.Context, hash string) error {
		return blobErr
	}, nil, nil, GCConfig{})
	worker.sweep(context.Background())
}

func TestGCSweep_NoOrphans(t *testing.T) {
	reg := NewFakeRegistry()
	worker := NewGCWorker(reg, nil, nil, nil, GCConfig{})
	worker.sweep(context.Background())
}

type deleteErrRegistry struct {
	*FakeRegistry
	deleteErr error
}

func (d *deleteErrRegistry) DeleteBlocks(_ context.Context, hashes [][]byte) (int, error) {
	if d.deleteErr != nil {
		return 0, d.deleteErr
	}
	// Delegate to FakeRegistry but inject error on next call.
	d.deleteErr = fmt.Errorf("delete failed")
	return 0, d.deleteErr
}
