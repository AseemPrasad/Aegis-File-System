package main

import (
	"context"
	"fmt"
	"time"

	"github.com/aegis-dev/aegis/internal/database"
	"github.com/aegis-dev/aegis/internal/gc"
	"github.com/aegis-dev/aegis/internal/ingress"
)

// gcStoreAdapter bridges database.DatabaseClient to gc.Store.
type gcStoreAdapter struct {
	db *database.DatabaseClient
}

func (a *gcStoreAdapter) GetExpiredSessions(ctx context.Context, limit int) ([]gc.SessionInfo, error) {
	rows, err := a.db.QueryWithMetrics(ctx, "gc", "get_expired_sessions", "system",
		`SELECT id, tenant_id, expires_at
		 FROM upload_sessions
		 WHERE status != 'COMPLETED'
		   AND expires_at < now()
		 ORDER BY expires_at ASC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("get expired sessions: %w", err)
	}
	defer rows.Close()

	var sessions []gc.SessionInfo
	for rows.Next() {
		var s gc.SessionInfo
		if err := rows.Scan(&s.ID, &s.TenantID, &s.ExpiresAt); err != nil {
			return nil, fmt.Errorf("scan expired session: %w", err)
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

func (a *gcStoreAdapter) MarkSessionExpired(ctx context.Context, sessionID string) error {
	_, err := a.db.ExecWithMetrics(ctx, "gc", "mark_session_expired", "system",
		`UPDATE upload_sessions SET status = 'EXPIRED' WHERE id = $1`, sessionID)
	return err
}

func (a *gcStoreAdapter) FindOrphanedBlocks(ctx context.Context, safetyWindow time.Duration, batchSize int) ([]gc.BlockInfo, error) {
	rows, err := a.db.QueryWithMetrics(ctx, "gc", "find_orphaned_blocks", "system",
		`SELECT block_hash, tenant_id, size_bytes, ref_count
		 FROM cas_blocks
		 WHERE ref_count = 0
		   AND created_at < now() - $1::interval
		 ORDER BY created_at ASC
		 LIMIT $2`,
		fmt.Sprintf("%ds", int(safetyWindow.Seconds())), batchSize)
	if err != nil {
		return nil, fmt.Errorf("find orphaned blocks: %w", err)
	}
	defer rows.Close()

	var blocks []gc.BlockInfo
	for rows.Next() {
		var b gc.BlockInfo
		if err := rows.Scan(&b.BlockHash, &b.TenantID, &b.SizeBytes, &b.RefCount); err != nil {
			return nil, fmt.Errorf("scan orphaned block: %w", err)
		}
		blocks = append(blocks, b)
	}
	return blocks, rows.Err()
}

// DoubleCheckBlock re-queries a single block's ref_count. Returns true if
// the block still has ref_count = 0 (safe to delete).
func (a *gcStoreAdapter) DoubleCheckBlock(ctx context.Context, blockHash string) (bool, error) {
	rows, err := a.db.QueryWithMetrics(ctx, "gc", "double_check_block", "system",
		`SELECT ref_count FROM cas_blocks WHERE block_hash = $1`, blockHash)
	if err != nil {
		return false, fmt.Errorf("double check block: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return false, rows.Err()
	}
	var refCount int64
	if err := rows.Scan(&refCount); err != nil {
		return false, fmt.Errorf("scan ref_count: %w", err)
	}
	return refCount == 0, nil
}

func (a *gcStoreAdapter) HardDeleteBlocks(ctx context.Context, hashes []string) (int, error) {
	tag, err := a.db.ExecWithMetrics(ctx, "gc", "hard_delete_blocks", "system",
		`DELETE FROM cas_blocks WHERE block_hash = ANY($1) AND ref_count = 0`, hashes)
	if err != nil {
		return 0, fmt.Errorf("hard delete blocks: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ---------------------------------------------------------------------------
// gcPublisherAdapter bridges ingress.EventBus to gc.TombstonePublisher
// ---------------------------------------------------------------------------

type gcPublisherAdapter struct {
	bus ingress.EventBus
}

func (a *gcPublisherAdapter) PublishBlockTombstone(ctx context.Context, topic string, event gc.TombstoneEvent) error {
	return a.bus.PublishBlockTombstone(ctx, topic, ingress.BlockTombstoneEvent{
		BlockHash: event.BlockHash,
		TenantID:  event.TenantID,
		SizeBytes: event.SizeBytes,
		Timestamp: event.Timestamp,
		Reason:    event.Reason,
	})
}

// blobDeleterAdapter adapts a DeleteBlock function to gc.BlobDeleter.
type blobDeleterAdapter struct {
	deleteFn func(ctx context.Context, blockHash string) error
}

func (a *blobDeleterAdapter) DeleteBlock(ctx context.Context, blockHash string) error {
	return a.deleteFn(ctx, blockHash)
}
