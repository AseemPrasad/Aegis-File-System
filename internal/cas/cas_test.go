package cas

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
)

// ---------------------------------------------------------------------------
// FakeRegistry — in-memory test double implementing Registry
// ---------------------------------------------------------------------------

type fakeRow struct {
	hash     []byte
	tenantID string
	size     int32
	refCount int64
	tier     string
	verified bool
	created  time.Time
}

type FakeRegistry struct {
	mu     sync.Mutex
	blocks map[string]*fakeRow
	err    error
}

var _ Registry = (*FakeRegistry)(nil)

func NewFakeRegistry() *FakeRegistry {
	return &FakeRegistry{blocks: make(map[string]*fakeRow)}
}

func (f *FakeRegistry) Seed(hash string, tenantID string, size int32, refCount int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blocks[hash] = &fakeRow{
		hash:     []byte(hash),
		tenantID: tenantID,
		size:     size,
		refCount: refCount,
		tier:     "HOT",
		created:  time.Now().Add(-8 * 24 * time.Hour), // old enough for GC
	}
}

func (f *FakeRegistry) SeedRaw(hashBytes []byte, tenantID string, size int32, refCount int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := hexEncode(hashBytes)
	f.blocks[key] = &fakeRow{
		hash:     hashBytes,
		tenantID: tenantID,
		size:     size,
		refCount: refCount,
		tier:     "HOT",
		created:  time.Now().Add(-8 * 24 * time.Hour),
	}
}

func (f *FakeRegistry) SetErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *FakeRegistry) EnsureBlock(_ context.Context, blockHash []byte, tenantID string, sizeBytes int32) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return false, f.err
	}
	key := hexEncode(blockHash)
	if _, exists := f.blocks[key]; exists {
		return false, nil
	}
	f.blocks[key] = &fakeRow{
		hash:     blockHash,
		tenantID: tenantID,
		size:     sizeBytes,
		refCount: 1,
		tier:     "HOT",
		created:  time.Now(),
	}
	return true, nil
}

func (f *FakeRegistry) BatchQueryExisting(_ context.Context, hashes [][]byte) (map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	result := make(map[string]bool, len(hashes))
	for _, h := range hashes {
		result[hexEncode(h)] = f.blocks[hexEncode(h)] != nil
	}
	return result, nil
}

func (f *FakeRegistry) GetBlock(_ context.Context, blockHash []byte) (*BlockInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	key := hexEncode(blockHash)
	row, ok := f.blocks[key]
	if !ok {
		return nil, ErrBlockNotFound
	}
	return &BlockInfo{
		BlockHash:   key,
		TenantID:    row.tenantID,
		SizeBytes:   row.size,
		RefCount:    row.refCount,
		StorageTier: row.tier,
		Verified:    row.verified,
		CreatedAt:   row.created,
		UpdatedAt:   row.created,
	}, nil
}

func (f *FakeRegistry) FindOrphans(_ context.Context, batchSize int) ([]BlockInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	var orphans []BlockInfo
	for key, row := range f.blocks {
		if row.refCount == 0 && time.Since(row.created) > GCSafetyWindow {
			orphans = append(orphans, BlockInfo{
				BlockHash: key, TenantID: row.tenantID,
				SizeBytes: row.size, RefCount: 0,
				StorageTier: row.tier, CreatedAt: row.created,
			})
			if len(orphans) >= batchSize {
				break
			}
		}
	}
	return orphans, nil
}

func (f *FakeRegistry) DeleteBlocks(_ context.Context, hashes [][]byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, f.err
	}
	deleted := 0
	for _, h := range hashes {
		key := hexEncode(h)
		if row, ok := f.blocks[key]; ok && row.refCount == 0 {
			delete(f.blocks, key)
			deleted++
		}
	}
	return deleted, nil
}

func (f *FakeRegistry) GetStorageStats(_ context.Context) (*StorageStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	s := &StorageStats{}
	var totalRefCount int64
	for _, row := range f.blocks {
		s.TotalBlocks++
		s.TotalBytes += int64(row.size)
		if row.refCount == 0 {
			s.OrphanBlocks++
			s.OrphanBytes += int64(row.size)
		}
		totalRefCount += row.refCount
		switch row.tier {
		case "HOT":
			s.HotBlocks++
		case "WARM":
			s.WarmBlocks++
		case "COLD":
			s.ColdBlocks++
		}
		if !row.verified {
			s.UnverifiedBlocks++
		}
		if row.refCount > s.MaxRefCount {
			s.MaxRefCount = row.refCount
		}
	}
	if s.TotalBlocks > 0 {
		s.AvgRefCount = float64(totalRefCount) / float64(s.TotalBlocks)
	}
	return s, nil
}

func (f *FakeRegistry) UpdateTier(_ context.Context, blockHash []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	key := hexEncode(blockHash)
	row, ok := f.blocks[key]
	if !ok {
		return ErrBlockNotFound
	}
	switch {
	case row.refCount > TierThresholdHot:
		row.tier = "HOT"
	case row.refCount >= TierThresholdWarm:
		row.tier = "WARM"
	default:
		row.tier = "COLD"
	}
	return nil
}

func hexEncode(b []byte) string {
	const hexTable = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexTable[v>>4]
		out[i*2+1] = hexTable[v&0x0f]
	}
	return string(out)
}

// ---------------------------------------------------------------------------
// FakeTx — in-memory transaction (not used by FakeRegistry but needed for interface)
// ---------------------------------------------------------------------------

type FakeTx struct {
	execFn func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func (t *FakeTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if t.execFn != nil {
		return t.execFn(ctx, sql, args...)
	}
	return pgconn.CommandTag{}, nil
}
func (t *FakeTx) QueryRow(_ context.Context, _ string, _ ...any) interface{ Scan(...any) error } {
	return &fakeRowScanner{}
}
func (t *FakeTx) Query(_ context.Context, _ string, _ ...any) (interface{ Next() bool; Scan(...any) error; Close() error; Err() error }, error) {
	return &fakeRows{}, nil
}
func (t *FakeTx) Commit(_ context.Context) error   { return nil }
func (t *FakeTx) Rollback(_ context.Context) error { return nil }

type fakeRowScanner struct{}

func (s *fakeRowScanner) Scan(_ ...any) error { return nil }

type fakeRows struct{}

func (r *fakeRows) Next() bool                    { return false }
func (r *fakeRows) Scan(_ ...any) error           { return nil }
func (r *fakeRows) Close() error                  { return nil }
func (r *fakeRows) Err() error                    { return nil }

// ---------------------------------------------------------------------------
// Tests — Registry
// ---------------------------------------------------------------------------

// hexPair returns a 1-byte hex string from a single byte value.
// e.g. hexPair(0xAB) → "ab" as a []byte.
func hexPair(b byte) []byte {
	const hexTable = "0123456789abcdef"
	return []byte{hexTable[b>>4], hexTable[b&0x0f]}
}

func TestEnsureBlock_New(t *testing.T) {
	r := NewFakeRegistry()
	isNew, err := r.EnsureBlock(context.Background(), hexPair(0xAA), "tenant1", 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isNew {
		t.Error("expected isNew=true for first insert")
	}
	block, err := r.GetBlock(context.Background(), hexPair(0xAA))
	if err != nil {
		t.Fatalf("GetBlock failed: %v", err)
	}
	if block.RefCount != 1 {
		t.Errorf("expected ref_count=1, got %d", block.RefCount)
	}
	if block.SizeBytes != 1024 {
		t.Errorf("expected size=1024, got %d", block.SizeBytes)
	}
}

func TestEnsureBlock_Existing(t *testing.T) {
	r := NewFakeRegistry()
	hash := hexPair(0xAA)
	r.SeedRaw(hash, "tenant1", 512, 3)

	isNew, err := r.EnsureBlock(context.Background(), hash, "tenant1", 512)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isNew {
		t.Error("expected isNew=false for existing block")
	}
}

func TestBatchQueryExisting(t *testing.T) {
	r := NewFakeRegistry()
	hashA := hexPair(0xAA)
	hashB := hexPair(0xBB)
	hashC := hexPair(0xCC)
	r.SeedRaw(hashA, "t", 100, 1)
	r.SeedRaw(hashB, "t", 200, 1)

	existing, err := r.BatchQueryExisting(context.Background(), [][]byte{
		hashA, hashB, hashC,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !existing[hexEncode(hashA)] {
		t.Error("AA should exist")
	}
	if !existing[hexEncode(hashB)] {
		t.Error("BB should exist")
	}
	if existing[hexEncode(hashC)] {
		t.Error("CC should not exist")
	}
}

func TestBatchQueryExisting_Empty(t *testing.T) {
	r := NewFakeRegistry()
	existing, err := r.BatchQueryExisting(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(existing) != 0 {
		t.Errorf("expected empty map, got %d entries", len(existing))
	}
}

func TestFindOrphans(t *testing.T) {
	r := NewFakeRegistry()
	hashOrphan := hexPair(0x01)
	hashNew := hexPair(0x02)
	hashAlive := hexPair(0x03)
	// Block with ref_count=0 and old enough
	r.SeedRaw(hashOrphan, "t", 100, 0)
	// Block with ref_count=0 but too new
	r.SeedRaw(hashNew, "t", 100, 0)
	r.mu.Lock()
	r.blocks[hexEncode(hashNew)].created = time.Now() // too new for GC
	r.mu.Unlock()
	// Block with ref_count=1
	r.SeedRaw(hashAlive, "t", 200, 1)

	orphans, err := r.FindOrphans(context.Background(), 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(orphans) != 1 {
		t.Fatalf("expected 1 orphan, got %d", len(orphans))
	}
	if orphans[0].BlockHash != hexEncode(hashOrphan) {
		t.Errorf("expected orphan hash, got %s", orphans[0].BlockHash)
	}
}

func TestDeleteBlocks(t *testing.T) {
	r := NewFakeRegistry()
	hashOrphan := hexPair(0x01)
	hashAlive := hexPair(0x02)
	r.SeedRaw(hashOrphan, "t", 100, 0)
	r.SeedRaw(hashAlive, "t", 200, 1)

	deleted, err := r.DeleteBlocks(context.Background(), [][]byte{
		hashOrphan, hashAlive,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", deleted)
	}
	// alive should still exist
	if _, err := r.GetBlock(context.Background(), hashAlive); err != nil {
		t.Errorf("alive block should still exist: %v", err)
	}
}

func TestUpdateTier(t *testing.T) {
	r := NewFakeRegistry()
	tests := []struct {
		name     string
		refCount int64
		wantTier string
	}{
		{"cold_low", 1, "COLD"},
		{"cold_boundary", 9, "COLD"},
		{"warm_low", 10, "WARM"},
		{"warm_mid", 50, "WARM"},
		{"warm_boundary", 100, "WARM"},
		{"hot", 101, "HOT"},
		{"hot_high", 500, "HOT"},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash := hexPair(byte(i + 0x10))
			r.SeedRaw(hash, "t", 100, tt.refCount)
			err := r.UpdateTier(context.Background(), hash)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			block, _ := r.GetBlock(context.Background(), hash)
			if block.StorageTier != tt.wantTier {
				t.Errorf("ref_count=%d: want tier=%s, got %s", tt.refCount, tt.wantTier, block.StorageTier)
			}
		})
	}
}

func TestGetStorageStats(t *testing.T) {
	r := NewFakeRegistry()
	hashHOT := hexPair(0x10)
	hashWARM := hexPair(0x20)
	hashCOLD := hexPair(0x30)
	hashORPHAN := hexPair(0x40)
	r.SeedRaw(hashHOT, "t", 100, 200)  // HOT
	r.SeedRaw(hashWARM, "t", 200, 50)  // WARM
	r.SeedRaw(hashCOLD, "t", 300, 5)   // COLD
	r.SeedRaw(hashORPHAN, "t", 400, 0) // orphan + COLD
	r.mu.Lock()
	r.blocks[hexEncode(hashHOT)].tier = "HOT"
	r.blocks[hexEncode(hashWARM)].tier = "WARM"
	r.blocks[hexEncode(hashCOLD)].tier = "COLD"
	r.blocks[hexEncode(hashORPHAN)].tier = "COLD"
	r.mu.Unlock()

	stats, err := r.GetStorageStats(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stats.TotalBlocks != 4 {
		t.Errorf("TotalBlocks = %d, want 4", stats.TotalBlocks)
	}
	if stats.TotalBytes != 1000 {
		t.Errorf("TotalBytes = %d, want 1000", stats.TotalBytes)
	}
	if stats.OrphanBlocks != 1 {
		t.Errorf("OrphanBlocks = %d, want 1", stats.OrphanBlocks)
	}
	if stats.HotBlocks != 1 {
		t.Errorf("HotBlocks = %d, want 1", stats.HotBlocks)
	}
	if stats.WarmBlocks != 1 {
		t.Errorf("WarmBlocks = %d, want 1", stats.WarmBlocks)
	}
	if stats.ColdBlocks != 2 {
		t.Errorf("ColdBlocks = %d, want 2", stats.ColdBlocks)
	}
	if stats.AvgRefCount != 63.75 {
		t.Errorf("AvgRefCount = %f, want 63.75", stats.AvgRefCount)
	}
	if stats.MaxRefCount != 200 {
		t.Errorf("MaxRefCount = %d, want 200", stats.MaxRefCount)
	}
}

func TestEnsureBlock_Concurrent(t *testing.T) {
	r := NewFakeRegistry()
	var wg sync.WaitGroup
	var errors atomic.Int32
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.EnsureBlock(context.Background(), hexPair(0xAA), "t", 100)
			if err != nil {
				errors.Add(1)
			}
		}()
	}
	wg.Wait()
	if errors.Load() != 0 {
		t.Errorf("got %d errors during concurrent EnsureBlock", errors.Load())
	}
}

// ---------------------------------------------------------------------------
// Tests — Bloom filter (fake)
// ---------------------------------------------------------------------------

func TestFakeBloomFilter_Add(t *testing.T) {
	bf := NewFakeBloomFilter()
	ctx := context.Background()

	isNew, err := bf.Add(ctx, "hash1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isNew {
		t.Error("expected isNew=true for first add")
	}

	isNew, err = bf.Add(ctx, "hash1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isNew {
		t.Error("expected isNew=false for duplicate add")
	}
}

func TestFakeBloomFilter_MightContain(t *testing.T) {
	bf := NewFakeBloomFilter()
	ctx := context.Background()

	bf.Add(ctx, "hash1")
	if ok, _ := bf.MightContain(ctx, "hash1"); !ok {
		t.Error("should contain hash1")
	}
	if ok, _ := bf.MightContain(ctx, "hash2"); ok {
		t.Error("should not contain hash2")
	}
}

func TestFakeBloomFilter_AddBatch(t *testing.T) {
	bf := NewFakeBloomFilter()
	ctx := context.Background()

	added, err := bf.AddBatch(ctx, []string{"a", "b", "c", "a"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if added != 3 {
		t.Errorf("expected 3 new entries, got %d", added)
	}
}

func TestFakeBloomFilter_Reset(t *testing.T) {
	bf := NewFakeBloomFilter()
	ctx := context.Background()

	bf.Add(ctx, "hash1")
	bf.Reset(ctx)
	if ok, _ := bf.MightContain(ctx, "hash1"); ok {
		t.Error("should not contain hash1 after reset")
	}
}

// ---------------------------------------------------------------------------
// Tests — GC Worker
// ---------------------------------------------------------------------------

func TestGCWorker_Sweep(t *testing.T) {
	r := NewFakeRegistry()
	hashOrphan := hexPair(0x01)
	hashAlive := hexPair(0x02)
	r.SeedRaw(hashOrphan, "t", 100, 0)
	r.SeedRaw(hashAlive, "t", 200, 1)

	worker := NewGCWorker(r, nil, nil, nil, GCConfig{
		Interval:  1 * time.Hour,
		BatchSize: 10,
	})

	worker.sweep(context.Background())

	if _, err := r.GetBlock(context.Background(), hashOrphan); err == nil {
		t.Error("orphan block should have been deleted")
	}
	if _, err := r.GetBlock(context.Background(), hashAlive); err != nil {
		t.Error("alive block should still exist")
	}
}

func TestGCWorker_SweepWithBlobDeleter(t *testing.T) {
	r := NewFakeRegistry()
	hashOrphan := hexPair(0x01)
	r.SeedRaw(hashOrphan, "t", 100, 0)

	var blobDeletes atomic.Int32
	blobDeleter := func(_ context.Context, hash string) error {
		blobDeletes.Add(1)
		return nil
	}

	worker := NewGCWorker(r, blobDeleter, nil, nil, GCConfig{
		Interval:  1 * time.Hour,
		BatchSize: 10,
	})

	worker.sweep(context.Background())

	if blobDeletes.Load() != 1 {
		t.Errorf("expected 1 blob delete, got %d", blobDeletes.Load())
	}
}

func TestGCWorker_StartStop(t *testing.T) {
	r := NewFakeRegistry()
	worker := NewGCWorker(r, nil, nil, nil, GCConfig{Interval: 50 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	worker.Start(ctx)

	time.Sleep(100 * time.Millisecond)
	cancel()
	time.Sleep(50 * time.Millisecond)
	// No panic = success
}

// ---------------------------------------------------------------------------
// Tests — Metrics
// ---------------------------------------------------------------------------

func TestRefreshFromStats(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewCASMetrics(reg)

	stats := &StorageStats{
		TotalBlocks: 100,
		TotalBytes:  50000,
		HotBlocks:   50,
		WarmBlocks:  30,
		ColdBlocks:  20,
		AvgRefCount: 25.5,
		MaxRefCount: 200,
	}

	m.RefreshFromStats(stats, 200000)

	// Gather metrics and verify values.
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather failed: %v", err)
	}
	values := make(map[string]float64)
	for _, fam := range families {
		for _, m := range fam.GetMetric() {
			values[fam.GetName()] = m.GetGauge().GetValue()
		}
	}
	if v := values["aegis_cas_total_blocks"]; v != 100 {
		t.Errorf("aegis_cas_total_blocks = %v, want 100", v)
	}
	if v := values["aegis_cas_total_bytes"]; v != 50000 {
		t.Errorf("aegis_cas_total_bytes = %v, want 50000", v)
	}
}

// ---------------------------------------------------------------------------
// Tests — Tier thresholds
// ---------------------------------------------------------------------------

func TestTierThresholds(t *testing.T) {
	if TierThresholdWarm != 10 {
		t.Errorf("TierThresholdWarm = %d, want 10", TierThresholdWarm)
	}
	if TierThresholdHot != 100 {
		t.Errorf("TierThresholdHot = %d, want 100", TierThresholdHot)
	}
	if GCSafetyWindow != 7*24*time.Hour {
		t.Errorf("GCSafetyWindow = %v, want 168h", GCSafetyWindow)
	}
}
