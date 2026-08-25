package cas

import (
	"context"
	"log/slog"
	"time"
)

// GCWorker periodically sweeps orphaned CAS blocks (ref_count = 0, older than
// safety window) and deletes them from both the DB registry and blob storage.
type GCWorker struct {
	registry   Registry
	blobDeleter func(ctx context.Context, blockHash string) error // optional
	metrics    *CASMetrics
	logger     *slog.Logger
	interval   time.Duration
	batchSize  int
	stopCh     chan struct{}
}

// GCConfig configures the garbage collection worker.
type GCConfig struct {
	Interval  time.Duration // sweep interval (default 5m)
	BatchSize int           // blocks per sweep (default 1000)
}

func (c *GCConfig) applyDefaults() {
	if c.Interval <= 0 {
		c.Interval = 5 * time.Minute
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 1000
	}
}

// NewGCWorker creates a GC worker. blobDeleter can be nil (DB-only cleanup).
func NewGCWorker(
	registry Registry,
	blobDeleter func(ctx context.Context, blockHash string) error,
	metrics *CASMetrics,
	lg *slog.Logger,
	cfg GCConfig,
) *GCWorker {
	if lg == nil {
		lg = slog.Default()
	}
	cfg.applyDefaults()
	return &GCWorker{
		registry:    registry,
		blobDeleter: blobDeleter,
		metrics:     metrics,
		logger:      lg,
		interval:    cfg.Interval,
		batchSize:   cfg.BatchSize,
		stopCh:      make(chan struct{}),
	}
}

// Start launches the background GC loop. Safe to call multiple times.
func (w *GCWorker) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(w.interval)
		defer t.Stop()
		w.logger.Info("gc worker started", "interval", w.interval, "batch", w.batchSize)
		for {
			select {
			case <-ctx.Done():
				w.logger.Info("gc worker stopping (context cancelled)")
				return
			case <-w.stopCh:
				w.logger.Info("gc worker stopped")
				return
			case <-t.C:
				w.sweep(ctx)
			}
		}
	}()
}

// Stop signals the GC loop to exit.
func (w *GCWorker) Stop() {
	select {
	case w.stopCh <- struct{}{}:
	default:
	}
}

// Sweep performs a single GC pass. Exposed for manual triggering / testing.
func (w *GCWorker) sweep(ctx context.Context) {
	start := time.Now()

	orphans, err := w.registry.FindOrphans(ctx, w.batchSize)
	if err != nil {
		w.logger.Error("gc: find orphans failed", "err", err)
		return
	}
	if len(orphans) == 0 {
		return
	}

	hashes := make([][]byte, 0, len(orphans))
	for _, o := range orphans {
		b, ok := decodeHex(o.BlockHash)
		if !ok {
			w.logger.Warn("gc: invalid block hash", "hash", o.BlockHash)
			continue
		}
		hashes = append(hashes, b)
	}

	// Best-effort blob deletion before DB removal.
	if w.blobDeleter != nil {
		for _, o := range orphans {
			if derr := w.blobDeleter(ctx, o.BlockHash); derr != nil {
				w.logger.Warn("gc: blob delete failed", "hash", o.BlockHash, "err", derr)
			}
		}
	}

	deleted, err := w.registry.DeleteBlocks(ctx, hashes)
	if err != nil {
		w.logger.Error("gc: delete blocks failed", "err", err)
		return
	}

	dur := time.Since(start)
	w.logger.Info("gc sweep completed",
		"found", len(orphans),
		"deleted", deleted,
		"duration_ms", dur.Milliseconds(),
	)

	if w.metrics != nil && w.metrics.GCSweepsTotal != nil {
		w.metrics.GCSweepsTotal.Inc()
		w.metrics.GCBlocksDeleted.Add(float64(deleted))
		w.metrics.GCLastSweepDuration.Observe(dur.Seconds())
	}
}

func decodeHex(s string) ([]byte, bool) {
	if len(s)%2 != 0 {
		return nil, false
	}
	b := make([]byte, len(s)/2)
	for i := 0; i < len(s); i += 2 {
		var hi, lo byte
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			hi = c - '0'
		case c >= 'a' && c <= 'f':
			hi = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			hi = c - 'A' + 10
		default:
			return nil, false
		}
		switch c := s[i+1]; {
		case c >= '0' && c <= '9':
			lo = c - '0'
		case c >= 'a' && c <= 'f':
			lo = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			lo = c - 'A' + 10
		default:
			return nil, false
		}
		b[i/2] = hi<<4 | lo
	}
	return b, true
}
