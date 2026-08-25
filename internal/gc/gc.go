// Package gc implements the generational mark-and-sweep garbage collection
// pipeline for Aegis. It runs three phases on different schedules:
//
//  Phase 1 (hourly): Expired session cleanup — marks stale upload sessions
//   as expired and recycles their space.
//
//  Phase 2 (daily): Orphan detection + tombstone emission — finds CAS blocks
//   with ref_count = 0 older than the 7-day safety window, publishes tombstone
//   events to the "cas-tombstones" topic, then soft-deletes the DB rows.
//
//  Phase 3 (out-of-band): Tombstone consumer — a separate worker reads
//   tombstone events and deletes blobs from object storage, then confirms
//   hard deletion.
//
// Safety mechanisms:
//   - Double-check ref_count = 0 before every delete
//   - Dry-run mode for testing
//   - Rate limiting (max blocks/hour)
//   - Audit trail via structured logs
//   - Monitoring alerts on abnormal deletion rates
package gc

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// Config controls the GC pipeline behavior.
type Config struct {
	SessionCleanupInterval time.Duration // Phase 1 interval (default 1h)
	BlockSweepInterval     time.Duration // Phase 2 interval (default 24h)
	SafetyWindow           time.Duration // Min block age before GC (default 7d)
	BatchSize              int           // Blocks per batch (default 1000)
	MaxBlocksPerHour       int           // Rate limit (default 5000)
	DryRun                 bool          // Log only, no actual deletes
}

func (c *Config) applyDefaults() {
	if c.SessionCleanupInterval <= 0 {
		c.SessionCleanupInterval = 1 * time.Hour
	}
	if c.BlockSweepInterval <= 0 {
		c.BlockSweepInterval = 24 * time.Hour
	}
	if c.SafetyWindow <= 0 {
		c.SafetyWindow = 7 * 24 * time.Hour
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 1000
	}
	if c.MaxBlocksPerHour <= 0 {
		c.MaxBlocksPerHour = 5000
	}
}

// ---------------------------------------------------------------------------
// Store — abstracts the DB operations needed by GC (avoids importing cas/ingress)
// ---------------------------------------------------------------------------

// SessionInfo is a minimal upload session record for GC.
type SessionInfo struct {
	ID        string
	TenantID  string
	ExpiresAt time.Time
}

// BlockInfo is a minimal CAS block record for GC.
type BlockInfo struct {
	BlockHash string
	TenantID  string
	SizeBytes int32
	RefCount  int64
}

// Store abstracts the database operations the GC pipeline needs.
type Store interface {
	// GetExpiredSessions returns sessions past their expiry that haven't
	// been completed.
	GetExpiredSessions(ctx context.Context, limit int) ([]SessionInfo, error)

	// MarkSessionExpired sets is_completed on an expired session.
	MarkSessionExpired(ctx context.Context, sessionID string) error

	// FindOrphanedBlocks returns blocks with ref_count = 0 older than
	// the safety window, limited to batchSize.
	FindOrphanedBlocks(ctx context.Context, safetyWindow time.Duration, batchSize int) ([]BlockInfo, error)

	// DoubleCheckBlock re-queries a single block to confirm ref_count = 0.
	DoubleCheckBlock(ctx context.Context, blockHash string) (bool, error)

	// HardDeleteBlocks removes blocks by hash (only if ref_count = 0).
	// Returns the number actually deleted.
	HardDeleteBlocks(ctx context.Context, hashes []string) (int, error)
}

// ---------------------------------------------------------------------------
// TombstonePublisher — abstracts event publishing for test fakes.
// ---------------------------------------------------------------------------

// TombstonePublisher emits tombstone events to a topic.
type TombstonePublisher interface {
	PublishBlockTombstone(ctx context.Context, topic string, event TombstoneEvent) error
}

// TombstoneEvent is the CDC payload for block deletion.
type TombstoneEvent struct {
	BlockHash string `json:"block_hash"`
	TenantID  string `json:"tenant_id"`
	SizeBytes int32  `json:"size_bytes"`
	Timestamp string `json:"timestamp"`
	Reason    string `json:"reason"`
}

// MarshalTombstone serializes a TombstoneEvent to JSON bytes.
func MarshalTombstone(e TombstoneEvent) ([]byte, error) {
	return json.Marshal(e)
}

// UnmarshalTombstone deserializes a TombstoneEvent from JSON bytes.
func UnmarshalTombstone(data []byte) (TombstoneEvent, error) {
	var e TombstoneEvent
	err := json.Unmarshal(data, &e)
	return e, err
}

// ---------------------------------------------------------------------------
// BlobDeleter — abstracts blob deletion for test fakes.
// ---------------------------------------------------------------------------

// BlobDeleter removes blobs from object storage.
type BlobDeleter interface {
	DeleteBlock(ctx context.Context, blockHash string) error
}

// ---------------------------------------------------------------------------
// Metrics — GC-specific Prometheus collectors
// ---------------------------------------------------------------------------

type Metrics struct {
	SessionsExpired   func() // called per expired session
	BlocksTombstoned  func() // called per tombstone emitted
	BlocksDeleted     func() // called per successful blob delete
	DeletionFailed    func() // called per failed blob delete
	RateLimitHit      func() // called when rate limit blocks deletion
	DoubleCheckSaved  func() // called when double-check prevents a deletion
}

// nopMetrics returns a Metrics that does nothing.
func nopMetrics() *Metrics {
	return &Metrics{
		SessionsExpired:  func() {},
		BlocksTombstoned: func() {},
		BlocksDeleted:    func() {},
		DeletionFailed:   func() {},
		RateLimitHit:     func() {},
		DoubleCheckSaved: func() {},
	}
}

// ---------------------------------------------------------------------------
// AuditEntry — structured audit trail for every deletion action
// ---------------------------------------------------------------------------

type AuditEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Action    string    `json:"action"` // "session_expired", "tombstone_emitted", "blob_deleted", "blob_delete_failed"
	BlockHash string    `json:"block_hash,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	TenantID  string    `json:"tenant_id,omitempty"`
	SizeBytes int32     `json:"size_bytes,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	Detail    string    `json:"detail,omitempty"`
}

// ---------------------------------------------------------------------------
// GarbageCollector — the generational mark-and-sweep pipeline
// ---------------------------------------------------------------------------

// GarbageCollector orchestrates the 3-phase GC pipeline.
type GarbageCollector struct {
	store      Store
	publisher  TombstonePublisher
	blob       BlobDeleter
	metrics    *Metrics
	logger     *slog.Logger
	cfg        Config

	auditLog   []AuditEntry
	auditMu    sync.Mutex

	deletionCount atomic.Int64 // blocks deleted this hour (rate limiting)
	rateResetAt   time.Time

	stopCh chan struct{}
}

// New creates a GarbageCollector. All dependencies are required.
func New(
	store Store,
	publisher TombstonePublisher,
	blob BlobDeleter,
	metrics *Metrics,
	lg *slog.Logger,
	cfg Config,
) *GarbageCollector {
	if lg == nil {
		lg = slog.Default()
	}
	if metrics == nil {
		metrics = nopMetrics()
	}
	cfg.applyDefaults()
	return &GarbageCollector{
		store:     store,
		publisher: publisher,
		blob:      blob,
		metrics:   metrics,
		logger:    lg,
		cfg:       cfg,
		stopCh:    make(chan struct{}),
	}
}

// Start launches both background loops. Safe to call multiple times.
func (gc *GarbageCollector) Start(ctx context.Context) {
	go gc.sessionCleanupLoop(ctx)
	go gc.blockSweepLoop(ctx)
	gc.logger.Info("gc pipeline started",
		"session_cleanup", gc.cfg.SessionCleanupInterval,
		"block_sweep", gc.cfg.BlockSweepInterval,
		"safety_window", gc.cfg.SafetyWindow,
		"dry_run", gc.cfg.DryRun,
	)
}

// Stop signals both background loops to exit.
func (gc *GarbageCollector) Stop() {
	select {
	case gc.stopCh <- struct{}{}:
	default:
	}
}

// AuditLog returns a copy of the audit trail (for testing / inspection).
func (gc *GarbageCollector) AuditLog() []AuditEntry {
	gc.auditMu.Lock()
	defer gc.auditMu.Unlock()
	out := make([]AuditEntry, len(gc.auditLog))
	copy(out, gc.auditLog)
	return out
}

// ---------------------------------------------------------------------------
// Phase 1: Expired Session Cleanup (hourly)
// ---------------------------------------------------------------------------

func (gc *GarbageCollector) sessionCleanupLoop(ctx context.Context) {
	t := time.NewTicker(gc.cfg.SessionCleanupInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-gc.stopCh:
			return
		case <-t.C:
			gc.cleanupExpiredSessions(ctx)
		}
	}
}

// CleanupExpiredSessions finds and marks stale upload sessions. Exposed
// for manual triggering / testing.
func (gc *GarbageCollector) cleanupExpiredSessions(ctx context.Context) {
	sessions, err := gc.store.GetExpiredSessions(ctx, gc.cfg.BatchSize)
	if err != nil {
		gc.logger.Error("gc: get expired sessions failed", "err", err)
		return
	}
	for _, s := range sessions {
		if err := gc.store.MarkSessionExpired(ctx, s.ID); err != nil {
			gc.logger.Warn("gc: mark session expired failed", "id", s.ID, "err", err)
			continue
		}
		gc.metrics.SessionsExpired()
		gc.audit(AuditEntry{
			Timestamp: time.Now(),
			Action:    "session_expired",
			SessionID: s.ID,
			TenantID:  s.TenantID,
			Reason:    "stale upload not committed within grace period",
		})
		gc.logger.Info("gc: expired session recycled",
			"session_id", s.ID, "tenant_id", s.TenantID)
	}
}

// ---------------------------------------------------------------------------
// Phase 2: Orphan Detection + Tombstone Emission (daily)
// ---------------------------------------------------------------------------

func (gc *GarbageCollector) blockSweepLoop(ctx context.Context) {
	t := time.NewTicker(gc.cfg.BlockSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-gc.stopCh:
			return
		case <-t.C:
			gc.sweepOrphanedBlocks(ctx)
		}
	}
}

// SweepOrphanedBlocks performs a full sweep pass. Exposed for testing.
func (gc *GarbageCollector) sweepOrphanedBlocks(ctx context.Context) {
	// Reset rate limiter every hour.
	if time.Since(gc.rateResetAt) > time.Hour {
		gc.deletionCount.Store(0)
		gc.rateResetAt = time.Now()
	}

	blocks, err := gc.store.FindOrphanedBlocks(ctx, gc.cfg.SafetyWindow, gc.cfg.BatchSize)
	if err != nil {
		gc.logger.Error("gc: find orphaned blocks failed", "err", err)
		return
	}
	if len(blocks) == 0 {
		return
	}

	gc.logger.Info("gc: found orphaned blocks", "count", len(blocks))

	for i := 0; i < len(blocks); i += gc.cfg.BatchSize {
		batch := blocks[i:min(i+gc.cfg.BatchSize, len(blocks))]
		gc.processBatch(ctx, batch)
	}
}

func (gc *GarbageCollector) processBatch(ctx context.Context, batch []BlockInfo) {
	for _, block := range batch {
		// Rate limiting check.
		if int(gc.deletionCount.Load()) >= gc.cfg.MaxBlocksPerHour {
			gc.metrics.RateLimitHit()
			gc.logger.Warn("gc: rate limit reached, deferring remaining blocks",
				"deleted", gc.deletionCount.Load(),
				"limit", gc.cfg.MaxBlocksPerHour)
			return
		}

		// Double-check: re-verify ref_count = 0 before proceeding.
		still, err := gc.store.DoubleCheckBlock(ctx, block.BlockHash)
		if err != nil {
			gc.logger.Warn("gc: double-check failed", "hash", block.BlockHash, "err", err)
			continue
		}
		if !still {
			// Block was re-referenced between FindOrphanedBlocks and now.
			gc.metrics.DoubleCheckSaved()
			gc.audit(AuditEntry{
				Timestamp: time.Now(),
				Action:    "double_check_saved",
				BlockHash: block.BlockHash,
				TenantID:  block.TenantID,
				Detail:    "block re-referenced before deletion, skipping",
			})
			continue
		}

		// Emit tombstone event.
		tombstone := TombstoneEvent{
			BlockHash: block.BlockHash,
			TenantID:  block.TenantID,
			SizeBytes: block.SizeBytes,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Reason:    "orphan_gc",
		}

		if gc.cfg.DryRun {
			gc.audit(AuditEntry{
				Timestamp: time.Now(),
				Action:    "dry_run_tombstone",
				BlockHash: block.BlockHash,
				TenantID:  block.TenantID,
				SizeBytes: block.SizeBytes,
				Reason:    "dry_run",
			})
			gc.logger.Info("gc: dry run — would emit tombstone",
				"hash", block.BlockHash, "tenant", block.TenantID)
			continue
		}

		if err := gc.publisher.PublishBlockTombstone(ctx, "cas-tombstones", tombstone); err != nil {
			gc.logger.Warn("gc: publish tombstone failed", "hash", block.BlockHash, "err", err)
			continue
		}
		gc.metrics.BlocksTombstoned()
		gc.audit(AuditEntry{
			Timestamp: time.Now(),
			Action:    "tombstone_emitted",
			BlockHash: block.BlockHash,
			TenantID:  block.TenantID,
			SizeBytes: block.SizeBytes,
			Reason:    "orphan_gc",
		})

		// Delete from blob store.
		if gc.blob != nil {
			if err := gc.blob.DeleteBlock(ctx, block.BlockHash); err != nil {
				gc.metrics.DeletionFailed()
				gc.logger.Warn("gc: blob delete failed", "hash", block.BlockHash, "err", err)
				gc.audit(AuditEntry{
					Timestamp: time.Now(),
					Action:    "blob_delete_failed",
					BlockHash: block.BlockHash,
					TenantID:  block.TenantID,
					Detail:    err.Error(),
				})
				continue
			}
			gc.metrics.BlocksDeleted()
			gc.audit(AuditEntry{
				Timestamp: time.Now(),
				Action:    "blob_deleted",
				BlockHash: block.BlockHash,
				TenantID:  block.TenantID,
				SizeBytes: block.SizeBytes,
			})
		}

		// Hard-delete from DB (only if still ref_count = 0).
		deleted, err := gc.store.HardDeleteBlocks(ctx, []string{block.BlockHash})
		if err != nil {
			gc.logger.Warn("gc: hard delete failed", "hash", block.BlockHash, "err", err)
			continue
		}
		if deleted > 0 {
			gc.deletionCount.Add(1)
		}
	}
}

func (gc *GarbageCollector) audit(entry AuditEntry) {
	gc.auditMu.Lock()
	defer gc.auditMu.Unlock()
	gc.auditLog = append(gc.auditLog, entry)
}

// ---------------------------------------------------------------------------
// TombstoneConsumer — Phase 3: out-of-band blob deletion worker
// ---------------------------------------------------------------------------

// TombstoneMessage represents a message from the tombstone topic.
type TombstoneMessage struct {
	Key   string
	Value []byte
}

// MessageSource abstracts a Kafka-like consumer for tombstones.
type MessageSource interface {
	ReadMessage(ctx context.Context) (TombstoneMessage, error)
	CommitMessage(ctx context.Context, msg TombstoneMessage) error
}

// TombstoneConsumer reads tombstone events and deletes blobs from object storage.
type TombstoneConsumer struct {
	source   MessageSource
	blob     BlobDeleter
	store    Store
	metrics  *Metrics
	logger   *slog.Logger
	dryRun   bool
}

// NewTombstoneConsumer creates a tombstone consumer.
func NewTombstoneConsumer(
	source MessageSource,
	blob BlobDeleter,
	store Store,
	metrics *Metrics,
	lg *slog.Logger,
	dryRun bool,
) *TombstoneConsumer {
	if lg == nil {
		lg = slog.Default()
	}
	if metrics == nil {
		metrics = nopMetrics()
	}
	return &TombstoneConsumer{
		source:  source,
		blob:    blob,
		store:   store,
		metrics: metrics,
		logger:  lg,
		dryRun:  dryRun,
	}
}

// Run reads and processes tombstone messages until ctx is cancelled.
func (tc *TombstoneConsumer) Run(ctx context.Context) {
	tc.logger.Info("tombstone consumer started", "dry_run", tc.dryRun)
	for {
		select {
		case <-ctx.Done():
			tc.logger.Info("tombstone consumer stopped")
			return
		default:
		}

		msg, err := tc.source.ReadMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			tc.logger.Warn("tombstone consumer: read failed", "err", err)
			time.Sleep(time.Second)
			continue
		}

		tc.processMessage(ctx, msg)
	}
}

func (tc *TombstoneConsumer) processMessage(ctx context.Context, msg TombstoneMessage) {
	event, err := UnmarshalTombstone(msg.Value)
	if err != nil {
		tc.logger.Warn("tombstone consumer: unmarshal failed", "err", err)
		return
	}

	if tc.dryRun {
		tc.logger.Info("tombstone consumer: dry run — would delete",
			"hash", event.BlockHash, "tenant", event.TenantID)
		_ = tc.source.CommitMessage(ctx, msg)
		return
	}

	// Delete from blob storage.
	if err := tc.blob.DeleteBlock(ctx, event.BlockHash); err != nil {
		tc.metrics.DeletionFailed()
		tc.logger.Warn("tombstone consumer: blob delete failed",
			"hash", event.BlockHash, "err", err)
		return // don't commit — will retry on restart
	}
	tc.metrics.BlocksDeleted()

	// Hard-delete from DB.
	deleted, _ := tc.store.HardDeleteBlocks(ctx, []string{event.BlockHash})
	if deleted > 0 {
		tc.logger.Info("tombstone consumer: block fully deleted",
			"hash", event.BlockHash, "tenant", event.TenantID)
	}

	_ = tc.source.CommitMessage(ctx, msg)
}


