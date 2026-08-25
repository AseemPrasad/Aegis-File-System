package database

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// ---------------------------------------------------------------------------
// TestNamespaceCache_Stats
// ---------------------------------------------------------------------------

func TestNamespaceCache_Stats(t *testing.T) {
	fr := newFakeRedis()
	cache := NewNamespaceCache(fr, time.Minute, nopLogger{})
	ctx := context.Background()
	const tenant = "stats-tenant"

	loader := func(context.Context) (NodeMeta, error) {
		return NodeMeta{NodeID: "n1", Name: "loaded"}, nil
	}

	// First call: miss (generation fetch + node GET both miss).
	if _, err := cache.GetNodeMetadata(ctx, tenant, "n1", loader); err != nil {
		t.Fatal(err)
	}

	// Second call: hit (entry was cached by the first call).
	if _, err := cache.GetNodeMetadata(ctx, tenant, "n1", loader); err != nil {
		t.Fatal(err)
	}

	// Third call with a different node: miss.
	loader2 := func(context.Context) (NodeMeta, error) {
		return NodeMeta{NodeID: "n2", Name: "other"}, nil
	}
	if _, err := cache.GetNodeMetadata(ctx, tenant, "n2", loader2); err != nil {
		t.Fatal(err)
	}

	hits, misses := cache.Stats()
	// n1: miss then hit → 1 hit, 1 miss
	// n2: miss        → 0 hits, 1 miss
	// total: 1 hit, 2 misses
	if hits != 1 || misses != 2 {
		t.Fatalf("Stats() = hits=%d misses=%d, want hits=1 misses=2", hits, misses)
	}
}

// ---------------------------------------------------------------------------
// TestCacheConfig_ApplyDefaults
// ---------------------------------------------------------------------------

func TestCacheConfig_ApplyDefaults(t *testing.T) {
	cfg := CacheConfig{}
	cfg.applyDefaults()

	if cfg.TTL != DefaultCacheTTL {
		t.Fatalf("applyDefaults: TTL = %v, want %v", cfg.TTL, DefaultCacheTTL)
	}
}

func TestCacheConfig_ApplyDefaultsPreservesNonZero(t *testing.T) {
	custom := 10 * time.Minute
	cfg := CacheConfig{TTL: custom}
	cfg.applyDefaults()

	if cfg.TTL != custom {
		t.Fatalf("applyDefaults overwrote non-zero TTL: got %v, want %v", cfg.TTL, custom)
	}
}

// ---------------------------------------------------------------------------
// TestNamespaceCache_CorruptEntry_FallsThrough
// ---------------------------------------------------------------------------

func TestNamespaceCache_CorruptEntry_FallsThrough(t *testing.T) {
	fr := newFakeRedis()
	cache := NewNamespaceCache(fr, time.Minute, nopLogger{})
	ctx := context.Background()
	const tenant = "corrupt-tenant"

	// Pre-populate the generation so we skip the bump path.
	fr.mu.Lock()
	fr.n[generationKey(tenant)] = 1
	fr.data[generationKey(tenant)] = "1"
	fr.mu.Unlock()

	// Write non-JSON bytes under the generation-1 node key.
	corruptKey := nodeKey(tenant, 1, "bad-node")
	fr.mu.Lock()
	fr.data[corruptKey] = "<<<NOT JSON>>>"
	fr.mu.Unlock()

	loaderCalls := 0
	loader := func(context.Context) (NodeMeta, error) {
		loaderCalls++
		return NodeMeta{NodeID: "bad-node", Name: "recovered"}, nil
	}

	meta, err := cache.GetNodeMetadata(ctx, tenant, "bad-node", loader)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Name != "recovered" {
		t.Fatalf("expected loader result, got meta.Name=%q", meta.Name)
	}
	if loaderCalls != 1 {
		t.Fatalf("loader called %d times, want 1", loaderCalls)
	}

	// The loader result should have overwritten the corrupt entry.
	fr.mu.Lock()
	raw := fr.data[corruptKey]
	fr.mu.Unlock()
	if raw == "<<<NOT JSON>>>" {
		t.Fatal("corrupt entry was not overwritten after fallthrough")
	}
	// Verify the overwrite is valid JSON.
	var verify NodeMeta
	if err := json.Unmarshal([]byte(raw), &verify); err != nil {
		t.Fatalf("overwritten entry is not valid JSON: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestNamespaceCache_SetFailure_Warns
// ---------------------------------------------------------------------------

type failingSetRedis struct {
	*fakeRedis
}

func (f *failingSetRedis) Set(_ context.Context, _ string, _ any, _ time.Duration) *redis.StatusCmd {
	cmd := redis.NewStatusCmd(context.Background(), "SET")
	cmd.SetErr(errors.New("redis SET denied"))
	return cmd
}

func TestNamespaceCache_SetFailure_Warns(t *testing.T) {
	fr := newFakeRedis()
	cache := NewNamespaceCache(&failingSetRedis{fr}, time.Minute, nopLogger{})
	ctx := context.Background()

	meta, err := cache.GetNodeMetadata(ctx, "t", "n", func(context.Context) (NodeMeta, error) {
		return NodeMeta{NodeID: "n", Name: "ok"}, nil
	})
	if err != nil {
		t.Fatalf("GetNodeMetadata failed despite SET failure: %v", err)
	}
	if meta.NodeID != "n" || meta.Name != "ok" {
		t.Fatalf("unexpected meta: %+v", meta)
	}
}

// ---------------------------------------------------------------------------
// Metrics nil-receiver guards
// ---------------------------------------------------------------------------

func TestMetrics_NilReceiver_ObserveQuery(t *testing.T) {
	var m *Metrics
	m.observeQuery(OpRead, "test", time.Millisecond, nil)
}

func TestMetrics_NilReceiver_ObserveAcquire(t *testing.T) {
	var m *Metrics
	m.observeAcquire(time.Millisecond)
}

func TestMetrics_NilReceiver_LogOperation(t *testing.T) {
	c := &DatabaseClient{logger: slog.New(slog.NewTextHandler(&discardWriter{}, nil))}
	c.logOperation(context.Background(), OpRead, "test_op", "tenant-1", time.Now(), nil)
}

func TestMetrics_NilReceiver_LogOperationWithError(t *testing.T) {
	c := &DatabaseClient{logger: slog.New(slog.NewTextHandler(&discardWriter{}, nil))}
	c.logOperation(context.Background(), OpRead, "test_op", "tenant-1", time.Now(), errors.New("test error"))
}

func TestMetrics_ObserveQuery_WithError(t *testing.T) {
	m := newMetrics(nil)
	m.observeQuery(OpRead, "test_query", 10*time.Millisecond, errors.New("fail"))
}

func TestMetrics_ObserveQuery_Success(t *testing.T) {
	m := newMetrics(nil)
	m.observeQuery(OpRead, "test_query", 5*time.Millisecond, nil)
}

func TestMetrics_ObserveAcquire_NonNil(t *testing.T) {
	m := newMetrics(nil)
	m.observeAcquire(time.Millisecond)
}
