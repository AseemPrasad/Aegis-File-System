package gc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Fake implementations
// ---------------------------------------------------------------------------

type fakeStore struct {
	mu          sync.Mutex
	sessions    map[string]SessionInfo
	blocks      map[string]BlockInfo
	expiredErr  error
	sweepErr    error
	doubleCheck map[string]bool // hash → still ref_count=0?
	deleteCount int
	deleteErr   error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		sessions:    make(map[string]SessionInfo),
		blocks:      make(map[string]BlockInfo),
		doubleCheck: make(map[string]bool),
	}
}

func (f *fakeStore) SeedSession(id, tenantID string, expiresAt time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[id] = SessionInfo{ID: id, TenantID: tenantID, ExpiresAt: expiresAt}
}

func (f *fakeStore) SeedBlock(hash, tenantID string, size int32, refCount int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blocks[hash] = BlockInfo{BlockHash: hash, TenantID: tenantID, SizeBytes: size, RefCount: refCount}
	f.doubleCheck[hash] = refCount == 0
}

func (f *fakeStore) SetDoubleCheck(hash string, stillZero bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.doubleCheck[hash] = stillZero
}

func (f *fakeStore) GetExpiredSessions(_ context.Context, limit int) ([]SessionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.expiredErr != nil {
		return nil, f.expiredErr
	}
	var result []SessionInfo
	now := time.Now()
	for _, s := range f.sessions {
		if now.After(s.ExpiresAt) {
			result = append(result, s)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

func (f *fakeStore) MarkSessionExpired(_ context.Context, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, sessionID)
	return nil
}

func (f *fakeStore) FindOrphanedBlocks(_ context.Context, _ time.Duration, batchSize int) ([]BlockInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sweepErr != nil {
		return nil, f.sweepErr
	}
	var result []BlockInfo
	now := time.Now()
	for _, b := range f.blocks {
		if b.RefCount == 0 && now.Sub(time.Now().Add(-8*24*time.Hour)) > 0 {
			result = append(result, b)
			if len(result) >= batchSize {
				break
			}
		}
	}
	return result, nil
}

func (f *fakeStore) DoubleCheckBlock(_ context.Context, blockHash string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.doubleCheck[blockHash]
	if !ok {
		return false, nil
	}
	return v, nil
}

func (f *fakeStore) HardDeleteBlocks(_ context.Context, hashes []string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return 0, f.deleteErr
	}
	deleted := 0
	for _, h := range hashes {
		if _, ok := f.blocks[h]; ok {
			delete(f.blocks, h)
			deleted++
		}
	}
	f.deleteCount += deleted
	return deleted, nil
}

func (f *fakeStore) DeleteCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deleteCount
}

// --- Fake publisher ---

type fakePublisher struct {
	mu      sync.Mutex
	events  []TombstoneEvent
	pubErr  error
}

func newFakePublisher() *fakePublisher {
	return &fakePublisher{}
}

func (f *fakePublisher) PublishBlockTombstone(_ context.Context, _ string, event TombstoneEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pubErr != nil {
		return f.pubErr
	}
	f.events = append(f.events, event)
	return nil
}

func (f *fakePublisher) Events() []TombstoneEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]TombstoneEvent, len(f.events))
	copy(out, f.events)
	return out
}

// --- Fake blob deleter ---

type fakeBlob struct {
	mu         sync.Mutex
	deleted    []string
	deleteErr  error
}

func newFakeBlob() *fakeBlob {
	return &fakeBlob{}
}

func (f *fakeBlob) DeleteBlock(_ context.Context, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, hash)
	return nil
}

func (f *fakeBlob) Deleted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.deleted))
	copy(out, f.deleted)
	return out
}

// --- Fake message source ---

type fakeMessageSource struct {
	messages []TombstoneMessage
	idx      int
	mu       sync.Mutex
	committed []string
}

func newFakeMessageSource(events ...TombstoneEvent) *fakeMessageSource {
	var msgs []TombstoneMessage
	for _, e := range events {
		data, _ := json.Marshal(e)
		msgs = append(msgs, TombstoneMessage{Key: e.BlockHash, Value: data})
	}
	return &fakeMessageSource{messages: msgs}
}

func (f *fakeMessageSource) ReadMessage(ctx context.Context) (TombstoneMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.idx >= len(f.messages) {
		// Block until context cancelled.
		<-ctx.Done()
		return TombstoneMessage{}, ctx.Err()
	}
	msg := f.messages[f.idx]
	f.idx++
	return msg, nil
}

func (f *fakeMessageSource) CommitMessage(_ context.Context, msg TombstoneMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.committed = append(f.committed, msg.Key)
	return nil
}

func (f *fakeMessageSource) CommittedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.committed)
}

// ---------------------------------------------------------------------------
// Tests — Config
// ---------------------------------------------------------------------------

func TestConfigDefaults(t *testing.T) {
	var c Config
	c.applyDefaults()
	if c.SessionCleanupInterval != time.Hour {
		t.Errorf("SessionCleanupInterval = %v, want 1h", c.SessionCleanupInterval)
	}
	if c.BlockSweepInterval != 24*time.Hour {
		t.Errorf("BlockSweepInterval = %v, want 24h", c.BlockSweepInterval)
	}
	if c.SafetyWindow != 7*24*time.Hour {
		t.Errorf("SafetyWindow = %v, want 168h", c.SafetyWindow)
	}
	if c.BatchSize != 1000 {
		t.Errorf("BatchSize = %d, want 1000", c.BatchSize)
	}
	if c.MaxBlocksPerHour != 5000 {
		t.Errorf("MaxBlocksPerHour = %d, want 5000", c.MaxBlocksPerHour)
	}
}

func TestConfigCustom(t *testing.T) {
	c := Config{
		SessionCleanupInterval: 30 * time.Minute,
		BlockSweepInterval:     6 * time.Hour,
		SafetyWindow:           3 * 24 * time.Hour,
		BatchSize:              500,
		MaxBlocksPerHour:       1000,
		DryRun:                 true,
	}
	c.applyDefaults()
	if c.SessionCleanupInterval != 30*time.Minute {
		t.Errorf("SessionCleanupInterval should be preserved")
	}
	if c.DryRun != true {
		t.Errorf("DryRun should be preserved")
	}
}

// ---------------------------------------------------------------------------
// Tests — Phase 1: Session cleanup
// ---------------------------------------------------------------------------

func TestCleanupExpiredSessions(t *testing.T) {
	store := newFakeStore()
	store.SeedSession("s1", "tenant1", time.Now().Add(-1*time.Hour))
	store.SeedSession("s2", "tenant1", time.Now().Add(1*time.Hour)) // not expired
	store.SeedSession("s3", "tenant2", time.Now().Add(-2*time.Hour))

	gc := New(store, newFakePublisher(), nil, nil, nil, Config{})
	var expired atomic.Int32
	gc.metrics.SessionsExpired = func() { expired.Add(1) }

	gc.cleanupExpiredSessions(context.Background())

	if expired.Load() != 2 {
		t.Errorf("expected 2 sessions expired, got %d", expired.Load())
	}
	if _, ok := store.sessions["s2"]; !ok {
		t.Error("s2 should still exist (not expired)")
	}
}

func TestCleanupExpiredSessions_Empty(t *testing.T) {
	store := newFakeStore()
	gc := New(store, newFakePublisher(), nil, nil, nil, Config{})
	gc.cleanupExpiredSessions(context.Background())
	// No panic, no errors = success
}

func TestCleanupExpiredSessions_AuditTrail(t *testing.T) {
	store := newFakeStore()
	store.SeedSession("s1", "tenant1", time.Now().Add(-1*time.Hour))

	gc := New(store, newFakePublisher(), nil, nil, nil, Config{})
	gc.cleanupExpiredSessions(context.Background())

	audit := gc.AuditLog()
	if len(audit) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(audit))
	}
	if audit[0].Action != "session_expired" {
		t.Errorf("action = %q, want session_expired", audit[0].Action)
	}
	if audit[0].SessionID != "s1" {
		t.Errorf("session_id = %q, want s1", audit[0].SessionID)
	}
}

// ---------------------------------------------------------------------------
// Tests — Phase 2: Block sweep + tombstone emission
// ---------------------------------------------------------------------------

func TestSweepOrphanedBlocks(t *testing.T) {
	store := newFakeStore()
	pub := newFakePublisher()
	blob := newFakeBlob()
	store.SeedBlock("aa", "t1", 100, 0) // orphan, old enough
	store.SeedBlock("bb", "t1", 200, 1) // still referenced

	gc := New(store, pub, blob, nil, nil, Config{BatchSize: 10, MaxBlocksPerHour: 100})
	gc.sweepOrphanedBlocks(context.Background())

	events := pub.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 tombstone, got %d", len(events))
	}
	if events[0].BlockHash != "aa" {
		t.Errorf("expected hash aa, got %s", events[0].BlockHash)
	}
	if events[0].Reason != "orphan_gc" {
		t.Errorf("reason = %q, want orphan_gc", events[0].Reason)
	}

	deleted := blob.Deleted()
	if len(deleted) != 1 {
		t.Fatalf("expected 1 blob deleted, got %d", len(deleted))
	}

	if store.DeleteCount() != 1 {
		t.Errorf("expected 1 DB delete, got %d", store.DeleteCount())
	}
}

func TestSweepOrphanedBlocks_DoubleCheckPreventsDelete(t *testing.T) {
	store := newFakeStore()
	pub := newFakePublisher()
	store.SeedBlock("aa", "t1", 100, 0)
	store.SetDoubleCheck("aa", false) // re-referenced since last query

	gc := New(store, pub, nil, nil, nil, Config{BatchSize: 10, MaxBlocksPerHour: 100})
	var saved atomic.Int32
	gc.metrics.DoubleCheckSaved = func() { saved.Add(1) }
	gc.sweepOrphanedBlocks(context.Background())

	if len(pub.Events()) != 0 {
		t.Error("no tombstone should be emitted for re-referenced block")
	}
	if saved.Load() != 1 {
		t.Errorf("expected DoubleCheckSaved=1, got %d", saved.Load())
	}
}

func TestSweepOrphanedBlocks_RateLimit(t *testing.T) {
	store := newFakeStore()
	pub := newFakePublisher()
	blob := newFakeBlob()
	for i := 0; i < 10; i++ {
		store.SeedBlock(fmt.Sprintf("block%02d", i), "t1", 100, 0)
	}

	gc := New(store, pub, blob, nil, nil, Config{
		BatchSize:        10,
		MaxBlocksPerHour: 3, // very low limit
	})

	var rateLimited atomic.Int32
	gc.metrics.RateLimitHit = func() { rateLimited.Add(1) }
	gc.sweepOrphanedBlocks(context.Background())

	// Should have processed only 3 blocks before hitting rate limit.
	events := pub.Events()
	if len(events) > 3 {
		t.Errorf("expected at most 3 events due to rate limit, got %d", len(events))
	}
	if rateLimited.Load() < 1 {
		t.Error("expected at least 1 rate limit hit")
	}
}

func TestSweepOrphanedBlocks_DryRun(t *testing.T) {
	store := newFakeStore()
	pub := newFakePublisher()
	blob := newFakeBlob()
	store.SeedBlock("aa", "t1", 100, 0)

	gc := New(store, pub, blob, nil, nil, Config{
		BatchSize:        10,
		MaxBlocksPerHour: 100,
		DryRun:           true,
	})
	gc.sweepOrphanedBlocks(context.Background())

	if len(pub.Events()) != 0 {
		t.Error("dry run should not emit tombstones")
	}
	if len(blob.Deleted()) != 0 {
		t.Error("dry run should not delete blobs")
	}

	audit := gc.AuditLog()
	found := false
	for _, a := range audit {
		if a.Action == "dry_run_tombstone" {
			found = true
		}
	}
	if !found {
		t.Error("expected dry_run_tombstone audit entry")
	}
}

func TestSweepOrphanedBlocks_Empty(t *testing.T) {
	store := newFakeStore()
	gc := New(store, newFakePublisher(), nil, nil, nil, Config{})
	gc.sweepOrphanedBlocks(context.Background())
	// No panic
}

func TestSweepOrphanedBlocks_AuditTrail(t *testing.T) {
	store := newFakeStore()
	pub := newFakePublisher()
	blob := newFakeBlob()
	store.SeedBlock("aa", "t1", 100, 0)

	gc := New(store, pub, blob, nil, nil, Config{BatchSize: 10, MaxBlocksPerHour: 100})
	gc.sweepOrphanedBlocks(context.Background())

	audit := gc.AuditLog()
	if len(audit) < 2 {
		t.Fatalf("expected at least 2 audit entries (tombstone + blob_deleted), got %d", len(audit))
	}
	actions := map[string]bool{}
	for _, a := range audit {
		actions[a.Action] = true
	}
	if !actions["tombstone_emitted"] {
		t.Error("expected tombstone_emitted audit entry")
	}
	if !actions["blob_deleted"] {
		t.Error("expected blob_deleted audit entry")
	}
}

func TestSweepOrphanedBlocks_BlobDeleteFailure(t *testing.T) {
	store := newFakeStore()
	pub := newFakePublisher()
	blob := newFakeBlob()
	blob.deleteErr = fmt.Errorf("S3 access denied")
	store.SeedBlock("aa", "t1", 100, 0)

	gc := New(store, pub, blob, nil, nil, Config{BatchSize: 10, MaxBlocksPerHour: 100})
	var failed atomic.Int32
	gc.metrics.DeletionFailed = func() { failed.Add(1) }
	gc.sweepOrphanedBlocks(context.Background())

	// Tombstone emitted, but blob delete failed.
	if len(pub.Events()) != 1 {
		t.Error("tombstone should still be emitted")
	}
	if failed.Load() != 1 {
		t.Errorf("expected 1 deletion failure, got %d", failed.Load())
	}
	// DB should NOT be deleted since blob failed.
	if store.DeleteCount() != 0 {
		t.Errorf("DB delete should not happen when blob delete fails, got %d", store.DeleteCount())
	}
}

// ---------------------------------------------------------------------------
// Tests — Phase 3: TombstoneConsumer
// ---------------------------------------------------------------------------

func TestTombstoneConsumer_BasicDelete(t *testing.T) {
	store := newFakeStore()
	store.SeedBlock("aa", "t1", 100, 0)
	blob := newFakeBlob()

	event := TombstoneEvent{BlockHash: "aa", TenantID: "t1", SizeBytes: 100}
	source := newFakeMessageSource(event)

	metrics := &Metrics{
		BlocksDeleted: func() {},
	}
	consumer := NewTombstoneConsumer(source, blob, store, metrics, nil, false)

	ctx, cancel := context.WithCancel(context.Background())
	go consumer.Run(ctx)
	time.Sleep(100 * time.Millisecond)
	cancel()
	time.Sleep(50 * time.Millisecond)

	if source.CommittedCount() != 1 {
		t.Errorf("expected 1 committed message, got %d", source.CommittedCount())
	}
	deleted := blob.Deleted()
	if len(deleted) != 1 || deleted[0] != "aa" {
		t.Errorf("expected blob aa deleted, got %v", deleted)
	}
}

func TestTombstoneConsumer_DryRun(t *testing.T) {
	store := newFakeStore()
	store.SeedBlock("aa", "t1", 100, 0)
	blob := newFakeBlob()

	event := TombstoneEvent{BlockHash: "aa", TenantID: "t1", SizeBytes: 100}
	source := newFakeMessageSource(event)

	consumer := NewTombstoneConsumer(source, blob, store, nil, nil, true)
	ctx, cancel := context.WithCancel(context.Background())
	go consumer.Run(ctx)
	time.Sleep(100 * time.Millisecond)
	cancel()
	time.Sleep(50 * time.Millisecond)

	if len(blob.Deleted()) != 0 {
		t.Error("dry run should not delete blobs")
	}
	if source.CommittedCount() != 1 {
		t.Error("dry run should still commit messages")
	}
}

func TestTombstoneConsumer_BlobDeleteFailure_Retries(t *testing.T) {
	store := newFakeStore()
	store.SeedBlock("aa", "t1", 100, 0)
	blob := newFakeBlob()
	blob.deleteErr = fmt.Errorf("transient error")

	event := TombstoneEvent{BlockHash: "aa", TenantID: "t1", SizeBytes: 100}
	source := newFakeMessageSource(event)

	consumer := NewTombstoneConsumer(source, blob, store, nil, nil, false)
	ctx, cancel := context.WithCancel(context.Background())
	go consumer.Run(ctx)
	time.Sleep(200 * time.Millisecond)
	cancel()
	time.Sleep(50 * time.Millisecond)

	// Should NOT commit on failure.
	if source.CommittedCount() != 0 {
		t.Error("should not commit on blob delete failure")
	}
}

// ---------------------------------------------------------------------------
// Tests — TombstoneEvent marshal/unmarshal
// ---------------------------------------------------------------------------

func TestTombstoneRoundTrip(t *testing.T) {
	event := TombstoneEvent{
		BlockHash: "aabb",
		TenantID:  "t1",
		SizeBytes: 1024,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Reason:    "orphan_gc",
	}

	data, err := MarshalTombstone(event)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got, err := UnmarshalTombstone(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.BlockHash != event.BlockHash {
		t.Errorf("BlockHash = %q, want %q", got.BlockHash, event.BlockHash)
	}
	if got.TenantID != event.TenantID {
		t.Errorf("TenantID = %q, want %q", got.TenantID, event.TenantID)
	}
	if got.SizeBytes != event.SizeBytes {
		t.Errorf("SizeBytes = %d, want %d", got.SizeBytes, event.SizeBytes)
	}
	if got.Reason != event.Reason {
		t.Errorf("Reason = %q, want %q", got.Reason, event.Reason)
	}
}

func TestTombstoneUnmarshalInvalid(t *testing.T) {
	_, err := UnmarshalTombstone([]byte("not json"))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// ---------------------------------------------------------------------------
// Tests — Start/Stop lifecycle
// ---------------------------------------------------------------------------

func TestStartStop(t *testing.T) {
	store := newFakeStore()
	gc := New(store, newFakePublisher(), nil, nil, nil, Config{
		SessionCleanupInterval: 50 * time.Millisecond,
		BlockSweepInterval:     50 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	gc.Start(ctx)

	time.Sleep(100 * time.Millisecond)
	cancel()
	gc.Stop()
	time.Sleep(50 * time.Millisecond)
	// No panic = success
}

// ---------------------------------------------------------------------------
// Tests — AuditEntry JSON
// ---------------------------------------------------------------------------

func TestAuditEntryJSON(t *testing.T) {
	entry := AuditEntry{
		Timestamp: time.Now(),
		Action:    "blob_deleted",
		BlockHash: "aabb",
		TenantID:  "t1",
		SizeBytes: 100,
		Reason:    "orphan_gc",
	}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), "blob_deleted") {
		t.Error("expected action in JSON")
	}
}

// ---------------------------------------------------------------------------
// Fuzz test — random sequence of operations
// ---------------------------------------------------------------------------

func TestFuzzUploadDeleteGC(t *testing.T) {
	store := newFakeStore()
	pub := newFakePublisher()
	blob := newFakeBlob()

	gc := New(store, pub, blob, nil, nil, Config{
		BatchSize:        100,
		MaxBlocksPerHour: 10000,
	})

	// Simulate: create blocks, some with ref_count=0, some with ref>0.
	for i := 0; i < 50; i++ {
		hash := fmt.Sprintf("block%02d", i)
		var refCount int64
		if i%3 == 0 {
			refCount = 0 // orphan
		} else {
			refCount = int64(i % 5) // still referenced
		}
		store.SeedBlock(hash, "tenant1", 100, refCount)
	}

	gc.sweepOrphanedBlocks(context.Background())

	// Only orphan blocks (refCount==0) should have been tombstoned.
	// Note: i%3==0 are orphans by design. But also i%3!=0 && i%5==0 gives
	// refCount=0 (unintended), so count all blocks with final refCount==0.
	orphanCount := 0
	for i := 0; i < 50; i++ {
		var refCount int64
		if i%3 == 0 {
			refCount = 0
		} else {
			refCount = int64(i % 5)
		}
		if refCount == 0 {
			orphanCount++
		}
	}

	events := pub.Events()
	if len(events) != orphanCount {
		t.Errorf("expected %d tombstones, got %d", orphanCount, len(events))
	}
}

// ---------------------------------------------------------------------------
// Tests — Metrics wiring
// ---------------------------------------------------------------------------

func TestMetricsWiring(t *testing.T) {
	store := newFakeStore()
	pub := newFakePublisher()
	blob := newFakeBlob()
	store.SeedBlock("aa", "t1", 100, 0)

	var tombstoned, deleted, rateLimited int
	m := &Metrics{
		SessionsExpired:  func() {},
		BlocksTombstoned: func() { tombstoned++ },
		BlocksDeleted:    func() { deleted++ },
		DeletionFailed:   func() {},
		RateLimitHit:     func() { rateLimited++ },
		DoubleCheckSaved: func() {},
	}

	gc := New(store, pub, blob, m, nil, Config{BatchSize: 10, MaxBlocksPerHour: 100})
	gc.sweepOrphanedBlocks(context.Background())

	if tombstoned != 1 {
		t.Errorf("BlocksTombstoned = %d, want 1", tombstoned)
	}
	if deleted != 1 {
		t.Errorf("BlocksDeleted = %d, want 1", deleted)
	}
	if rateLimited != 0 {
		t.Errorf("RateLimitHit = %d, want 0", rateLimited)
	}
}
