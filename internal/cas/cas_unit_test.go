package cas

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// FakeBloomFilter — NEW standalone tests not covered by cas_test.go
// ---------------------------------------------------------------------------

func TestFakeBloomFilter_AddNewIsNew(t *testing.T) {
	f := NewFakeBloomFilter()
	ok, err := f.Add(context.Background(), "brand-new-hash")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !ok {
		t.Error("first Add of a new hash should return true")
	}
}

func TestFakeBloomFilter_SetAddErr(t *testing.T) {
	f := NewFakeBloomFilter()
	f.SetAddErr(errors.New("redis down"))
	_, err := f.Add(context.Background(), "hash1")
	if err == nil {
		t.Error("expected error from SetAddErr")
	}
}

func TestFakeBloomFilter_SetExistErr(t *testing.T) {
	f := NewFakeBloomFilter()
	f.SetExistErr(errors.New("redis down"))
	ok, err := f.MightContain(context.Background(), "hash1")
	if err == nil {
		t.Error("expected error from SetExistErr")
	}
	if !ok {
		t.Error("fail-open should return true on error")
	}
}

func TestFakeBloomFilter_ContainsCount(t *testing.T) {
	f := NewFakeBloomFilter()
	if f.ContainsCount() != 0 {
		t.Error("empty filter should have count 0")
	}
	f.Add(context.Background(), "h1")
	f.Add(context.Background(), "h2")
	if f.ContainsCount() != 2 {
		t.Errorf("count: got %d, want 2", f.ContainsCount())
	}
}

// ---------------------------------------------------------------------------
// RedisBloomFilter with mock runner
// ---------------------------------------------------------------------------

type mockRedisCmd struct {
	reserveErr  error
	addResult   bool
	addErr      error
	existResult bool
	existErr    error
	delResult   int64
	delErr      error

	addCalls     int
	existCalls   int
	delCalls     int
	reserveCalls int
}

func (m *mockRedisCmd) BFReserve(_ context.Context, _ string, _ float64, _ int64) error {
	m.reserveCalls++
	return m.reserveErr
}
func (m *mockRedisCmd) BFAdd(_ context.Context, _ string, _ string) (bool, error) {
	m.addCalls++
	return m.addResult, m.addErr
}
func (m *mockRedisCmd) BFExists(_ context.Context, _ string, _ string) (bool, error) {
	m.existCalls++
	return m.existResult, m.existErr
}
func (m *mockRedisCmd) Del(_ context.Context, _ ...string) (int64, error) {
	m.delCalls++
	return m.delResult, m.delErr
}

func TestRedisBloomFilter_Add(t *testing.T) {
	mock := &mockRedisCmd{addResult: true}
	f := NewRedisBloomFilter(mock, "ep1", nil)
	ok, err := f.Add(context.Background(), "hash1")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !ok {
		t.Error("expected new=true")
	}
}

func TestRedisBloomFilter_MightContain(t *testing.T) {
	mock := &mockRedisCmd{existResult: true}
	f := NewRedisBloomFilter(mock, "ep1", nil)
	ok, err := f.MightContain(context.Background(), "hash1")
	if err != nil {
		t.Fatalf("MightContain: %v", err)
	}
	if !ok {
		t.Error("expected true")
	}
}

func TestRedisBloomFilter_MightContainFailOpen(t *testing.T) {
	mock := &mockRedisCmd{existErr: errors.New("connection refused")}
	f := NewRedisBloomFilter(mock, "ep1", nil)
	ok, err := f.MightContain(context.Background(), "hash1")
	if err == nil {
		t.Error("expected error propagated")
	}
	if !ok {
		t.Error("fail-open: should return true on error")
	}
}

func TestRedisBloomFilter_AddRetryOnReserve(t *testing.T) {
	// Stateful mock: first BFAdd fails, second succeeds after reserve.
	mock := &statefulBloomMock{
		addResults: []addResult{
			{err: errors.New("WRONGTYPE")},
			{created: true},
		},
	}
	f := NewRedisBloomFilter(mock, "ep1", nil)
	ok, err := f.Add(context.Background(), "hash1")
	if err != nil {
		t.Fatalf("Add after reserve: %v", err)
	}
	if !ok {
		t.Error("expected new=true after reserve+retry")
	}
	if mock.addCalls < 2 {
		t.Errorf("BFAdd calls: got %d, want >= 2", mock.addCalls)
	}
	if mock.reserveCalls < 1 {
		t.Errorf("BFReserve calls: got %d, want >= 1", mock.reserveCalls)
	}
}

type addResult struct {
	created bool
	err     error
}

type statefulBloomMock struct {
	addResults    []addResult
	addCalls      int
	reserveCalls  int
	delCalls      int
}

func (m *statefulBloomMock) BFReserve(_ context.Context, _ string, _ float64, _ int64) error {
	m.reserveCalls++
	return nil
}
func (m *statefulBloomMock) BFAdd(_ context.Context, _ string, _ string) (bool, error) {
	if m.addCalls < len(m.addResults) {
		r := m.addResults[m.addCalls]
		m.addCalls++
		return r.created, r.err
	}
	m.addCalls++
	return true, nil
}
func (m *statefulBloomMock) BFExists(_ context.Context, _ string, _ string) (bool, error) {
	return false, nil
}
func (m *statefulBloomMock) Del(_ context.Context, _ ...string) (int64, error) {
	m.delCalls++
	return 1, nil
}

func TestRedisBloomFilter_ResetCallsDelAndReserve(t *testing.T) {
	mock := &mockRedisCmd{}
	f := NewRedisBloomFilter(mock, "ep1", nil)
	err := f.Reset(context.Background())
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if mock.delCalls != 1 {
		t.Errorf("Del calls: got %d, want 1", mock.delCalls)
	}
	if mock.reserveCalls != 1 {
		t.Errorf("BFReserve calls: got %d, want 1", mock.reserveCalls)
	}
}

func TestRedisBloomFilter_KeyNaming(t *testing.T) {
	mock := &mockRedisCmd{}
	f := NewRedisBloomFilter(mock, "endpoint-abc", nil)
	if f.key != "aegis:cas:bloom:endpoint-abc" {
		t.Errorf("unexpected key: %s", f.key)
	}
}

// ---------------------------------------------------------------------------
// decodeHex — internal helper
// ---------------------------------------------------------------------------

func TestDecodeHex_Valid(t *testing.T) {
	b, ok := decodeHex("0a1b2c3d")
	if !ok {
		t.Fatal("decodeHex should succeed")
	}
	if len(b) != 4 {
		t.Errorf("length: got %d, want 4", len(b))
	}
	if b[0] != 0x0a || b[1] != 0x1b || b[2] != 0x2c || b[3] != 0x3d {
		t.Errorf("bytes: %v", b)
	}
}

func TestDecodeHex_OddLength(t *testing.T) {
	_, ok := decodeHex("abc")
	if ok {
		t.Error("odd-length hex should fail")
	}
}

func TestDecodeHex_InvalidChars(t *testing.T) {
	_, ok := decodeHex("zzzz")
	if ok {
		t.Error("invalid hex chars should fail")
	}
}

func TestDecodeHex_Empty(t *testing.T) {
	b, ok := decodeHex("")
	if !ok {
		t.Error("empty string should decode to empty slice")
	}
	if len(b) != 0 {
		t.Errorf("expected empty slice, got %d bytes", len(b))
	}
}

func TestDecodeHex_Uppercase(t *testing.T) {
	b, ok := decodeHex("ABCD")
	if !ok {
		t.Fatal("uppercase hex should work")
	}
	if b[0] != 0xAB || b[1] != 0xCD {
		t.Errorf("bytes: %v", b)
	}
}

// ---------------------------------------------------------------------------
// GCConfig defaults
// ---------------------------------------------------------------------------

func TestGCConfig_ApplyDefaultsZero(t *testing.T) {
	cfg := GCConfig{}
	cfg.applyDefaults()
	if cfg.Interval != 5*time.Minute {
		t.Errorf("default interval: got %v, want 5m", cfg.Interval)
	}
	if cfg.BatchSize != 1000 {
		t.Errorf("default batch size: got %d, want 1000", cfg.BatchSize)
	}
}

func TestGCConfig_PreservesCustomValues(t *testing.T) {
	cfg := GCConfig{Interval: 10 * time.Second, BatchSize: 500}
	cfg.applyDefaults()
	if cfg.Interval != 10*time.Second {
		t.Errorf("custom interval should be preserved: %v", cfg.Interval)
	}
	if cfg.BatchSize != 500 {
		t.Errorf("custom batch size should be preserved: %d", cfg.BatchSize)
	}
}
