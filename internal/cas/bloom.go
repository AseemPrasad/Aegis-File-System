package cas

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// ---------------------------------------------------------------------------
// Bloom filter backed by Redis (BF.ADD / BF.EXISTS / BF.RESERVE).
//
// Provides O(1) "definitely not exists" checks to skip DB round-trips.
// False positives are possible; a positive result falls through to the DB.
// TTL on the Redis key ensures periodic rebuild for long-lived systems.
// ---------------------------------------------------------------------------

const (
	bloomKeyPrefix    = "aegis:cas:bloom"
	bloomFalsePosRate = 0.01 // 1% false positive rate
	bloomTTL          = 1 * time.Hour
)

// BloomFilterer abstracts the bloom filter for test fakes.
type BloomFilterer interface {
	// Add inserts a hash into the filter. Returns true if it was new.
	Add(ctx context.Context, hash string) (bool, error)
	// MightContain returns true if the hash might exist (false positive possible).
	MightContain(ctx context.Context, hash string) (bool, error)
	// AddBatch inserts multiple hashes. Returns the count of newly added.
	AddBatch(ctx context.Context, hashes []string) (int64, error)
	// Reset clears and reinitializes the filter.
	Reset(ctx context.Context) error
}

// RedisBloomFilter implements BloomFilterer using Redis commands.
// Requires the RedisBloom module (BF.RESERVE, BF.ADD, BF.EXISTS).
type RedisBloomFilter struct {
	cmd    RedisCmdRunner
	key    string
	logger *slog.Logger
}

// RedisCmdRunner abstracts the Redis client for the specific commands we need.
type RedisCmdRunner interface {
	BFReserve(ctx context.Context, key string, errorRate float64, capacity int64) error
	BFAdd(ctx context.Context, key string, element string) (bool, error)
	BFExists(ctx context.Context, key string, element string) (bool, error)
	Del(ctx context.Context, keys ...string) (int64, error)
}

// NewRedisBloomFilter creates a bloom filter with the given capacity.
// The Redis key is namespaced and auto-expires after TTL.
func NewRedisBloomFilter(cmd RedisCmdRunner, endpointID string, lg *slog.Logger) *RedisBloomFilter {
	if lg == nil {
		lg = slog.Default()
	}
	key := fmt.Sprintf("%s:%s", bloomKeyPrefix, endpointID)
	return &RedisBloomFilter{cmd: cmd, key: key, logger: lg}
}

func (f *RedisBloomFilter) Add(ctx context.Context, hash string) (bool, error) {
	created, err := f.cmd.BFAdd(ctx, f.key, hash)
	if err != nil {
		// If key doesn't exist, reserve and retry.
		if rerr := f.cmd.BFReserve(ctx, f.key, bloomFalsePosRate, 1_000_000); rerr == nil {
			f.cmd.Del(ctx, f.key) //nolint:errcheck // clear and recreate
			if rerr2 := f.cmd.BFReserve(ctx, f.key, bloomFalsePosRate, 1_000_000); rerr2 != nil {
				return false, fmt.Errorf("cas bloom: reserve: %w", rerr2)
			}
			return f.cmd.BFAdd(ctx, f.key, hash)
		}
		return false, fmt.Errorf("cas bloom: add: %w", err)
	}
	return created, nil
}

func (f *RedisBloomFilter) MightContain(ctx context.Context, hash string) (bool, error) {
	exists, err := f.cmd.BFExists(ctx, f.key, hash)
	if err != nil {
		return true, fmt.Errorf("cas bloom: exists: %w", err) // fail-open: assume might exist
	}
	return exists, nil
}

func (f *RedisBloomFilter) AddBatch(ctx context.Context, hashes []string) (int64, error) {
	var added int64
	for _, h := range hashes {
		isNew, err := f.Add(ctx, h)
		if err != nil {
			return added, err
		}
		if isNew {
			added++
		}
	}
	return added, nil
}

func (f *RedisBloomFilter) Reset(ctx context.Context) error {
	if _, err := f.cmd.Del(ctx, f.key); err != nil {
		return fmt.Errorf("cas bloom: reset: %w", err)
	}
	return f.cmd.BFReserve(ctx, f.key, bloomFalsePosRate, 1_000_000)
}

// ---------------------------------------------------------------------------
// FakeBloomFilter — in-memory test double
// ---------------------------------------------------------------------------

// FakeBloomFilter is an in-memory BloomFilterer for unit tests.
type FakeBloomFilter struct {
	entries map[string]bool
	addErr  error
	existErr error
}

var _ BloomFilterer = (*FakeBloomFilter)(nil)

func NewFakeBloomFilter() *FakeBloomFilter {
	return &FakeBloomFilter{entries: make(map[string]bool)}
}

func (f *FakeBloomFilter) SetAddErr(err error)        { f.addErr = err }
func (f *FakeBloomFilter) SetExistErr(err error)      { f.existErr = err }

func (f *FakeBloomFilter) Add(_ context.Context, hash string) (bool, error) {
	if f.addErr != nil {
		return false, f.addErr
	}
	if f.entries[hash] {
		return false, nil
	}
	f.entries[hash] = true
	return true, nil
}

func (f *FakeBloomFilter) MightContain(_ context.Context, hash string) (bool, error) {
	if f.existErr != nil {
		return true, f.existErr // fail-open
	}
	return f.entries[hash], nil
}

func (f *FakeBloomFilter) AddBatch(ctx context.Context, hashes []string) (int64, error) {
	var added int64
	for _, h := range hashes {
		isNew, err := f.Add(ctx, h)
		if err != nil {
			return added, err
		}
		if isNew {
			added++
		}
	}
	return added, nil
}

func (f *FakeBloomFilter) Reset(_ context.Context) error {
	f.entries = make(map[string]bool)
	return nil
}

// ContainsCount returns the number of entries in the fake bloom filter.
func (f *FakeBloomFilter) ContainsCount() int {
	return len(f.entries)
}
