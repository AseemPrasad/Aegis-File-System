package gc

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aegis-dev/aegis/internal/lock"
)

// LeaderGCSweeper wraps GarbageCollector with cluster leader election and lock-protected two-phase sweeps.
type LeaderGCSweeper struct {
	gc      *GarbageCollector
	elector *lock.LeaderElection
	locker  lock.Locker
	logger  *slog.Logger
}

func NewLeaderGCSweeper(gc *GarbageCollector, elector *lock.LeaderElection, locker lock.Locker, lg *slog.Logger) *LeaderGCSweeper {
	if lg == nil {
		lg = slog.Default()
	}
	return &LeaderGCSweeper{
		gc:      gc,
		elector: elector,
		locker:  locker,
		logger:  lg,
	}
}

// RunLeaderSweep executes two-phase race-free deletion sweep if this node holds active leadership.
func (s *LeaderGCSweeper) RunLeaderSweep(ctx context.Context) error {
	if s.elector != nil && !s.elector.IsLeader() {
		s.logger.Debug("skipping gc sweep; node is follower")
		return nil
	}

	s.logger.Info("executing leader-elected race-free gc sweep")

	// Phase 1: Mark Phase (Find orphaned blocks older than safety window)
	blocks, err := s.gc.store.FindOrphanedBlocks(ctx, s.gc.cfg.SafetyWindow, s.gc.cfg.BatchSize)
	if err != nil {
		return fmt.Errorf("failed to fetch orphan blocks: %w", err)
	}

	// Phase 2: Lock-Protected Sweep Phase (Acquire lock per block, verify ref_count, delete)
	for _, block := range blocks {
		resourceKey := fmt.Sprintf("gc:block:%s", block.BlockHash)

		// Acquire distributed lock per block hash
		lockTok, err := s.locker.Acquire(ctx, resourceKey, 10*time.Second)
		if err != nil {
			s.logger.Debug("skipping locked block", "hash", block.BlockHash)
			continue
		}

		// Double-check reference count under lock protection
		isStillOrphan, err := s.gc.store.DoubleCheckBlock(ctx, block.BlockHash)
		if err == nil && isStillOrphan {
			if s.gc.publisher != nil {
				_ = s.gc.publisher.PublishBlockTombstone(ctx, "cas-tombstones", TombstoneEvent{
					BlockHash: block.BlockHash,
					TenantID:  block.TenantID,
					SizeBytes: block.SizeBytes,
					Timestamp: time.Now().Format(time.RFC3339),
					Reason:    "orphan_gc",
				})
			}
			_, _ = s.gc.store.HardDeleteBlocks(ctx, []string{block.BlockHash})
		}

		_ = s.locker.Release(ctx, lockTok)
	}

	return nil
}
