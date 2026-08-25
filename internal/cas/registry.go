// Package cas implements the Content-Addressable Storage registry layer for
// Aegis. It manages the lifecycle of deduplicated binary blocks: existence
// checks, ref-count visibility, tier promotion/demotion, orphan detection,
// and garbage collection.
//
// The DB triggers (manifest_block_added / manifest_block_removed) remain the
// single source of truth for ref_count mutations. This package wraps them
// with application-level tiering, bloom-filter acceleration, and metrics.
package cas

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ---------------------------------------------------------------------------
// Error sentinels
// ---------------------------------------------------------------------------

var (
	ErrBlockNotFound = errors.New("cas: block not found")
	ErrAlreadyExists = errors.New("cas: block already exists")
)

// ---------------------------------------------------------------------------
// Tier thresholds (ref_count boundaries for automatic tier transitions)
// ---------------------------------------------------------------------------

const (
	TierThresholdWarm = 10  // ref_count >= 10 → WARM
	TierThresholdHot  = 100 // ref_count > 100 → HOT
	GCSafetyWindow    = 7 * 24 * time.Hour
)

// ---------------------------------------------------------------------------
// BlockInfo describes one CAS block row.
// ---------------------------------------------------------------------------

type BlockInfo struct {
	BlockHash  string
	TenantID   string
	SizeBytes  int32
	RefCount   int64
	StorageTier string
	Verified   bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ---------------------------------------------------------------------------
// Registry interface — abstracts DB for test fakes.
// ---------------------------------------------------------------------------

// Registry provides CAS registry operations.
type Registry interface {
	// EnsureBlock inserts a CAS block row if it doesn't exist, or increments
	// ref_count if it does. Returns (isNew, error). The DB trigger handles
	// ref_count bump on manifest insert; this method ensures the cas_blocks
	// row exists with size_bytes populated.
	EnsureBlock(ctx context.Context, blockHash []byte, tenantID string, sizeBytes int32) (bool, error)

	// BatchQueryExisting returns a map of hex(block_hash) → true for all
	// hashes that exist in cas_blocks (cross-tenant).
	BatchQueryExisting(ctx context.Context, hashes [][]byte) (map[string]bool, error)

	// GetBlock retrieves metadata for a single block.
	GetBlock(ctx context.Context, blockHash []byte) (*BlockInfo, error)

	// FindOrphans returns blocks with ref_count = 0 older than the safety
	// window, limited to batchSize rows.
	FindOrphans(ctx context.Context, batchSize int) ([]BlockInfo, error)

	// DeleteBlocks removes blocks by hash (only if ref_count = 0).
	// Returns number actually deleted.
	DeleteBlocks(ctx context.Context, hashes [][]byte) (int, error)

	// GetStorageStats returns aggregate statistics.
	GetStorageStats(ctx context.Context) (*StorageStats, error)

	// UpdateTier recalculates and applies the storage tier for a block based
	// on its current ref_count.
	UpdateTier(ctx context.Context, blockHash []byte) error
}

// ---------------------------------------------------------------------------
// StorageStats — aggregate CAS metrics
// ---------------------------------------------------------------------------

type StorageStats struct {
	TotalBlocks   int64
	TotalBytes    int64
	OrphanBlocks  int64
	OrphanBytes   int64
	HotBlocks     int64
	WarmBlocks    int64
	ColdBlocks    int64
	UnverifiedBlocks int64
	AvgRefCount   float64
	MaxRefCount   int64
}

// ---------------------------------------------------------------------------
// PgRegistry — production implementation backed by pgx
// ---------------------------------------------------------------------------

// DBTx abstracts pgx.Tx for testability.
type DBTx interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// TxFactory creates a new write transaction.
type TxFactory func(ctx context.Context) (DBTx, error)

// ExecFunc is a standalone executor (wraps DatabaseClient.ExecWithMetrics).
type ExecFunc func(ctx context.Context, op, tenant, sql string, args ...any) (pgconn.CommandTag, error)

// QueryFunc is a standalone query executor (wraps DatabaseClient.QueryWithMetrics).
type QueryFunc func(ctx context.Context, op, tenant, sql string, args ...any) (pgx.Rows, error)

// PgRegistry implements Registry backed by PostgreSQL.
type PgRegistry struct {
	beginTx TxFactory
	exec    ExecFunc
	query   QueryFunc
	logger  *slog.Logger
}

// NewPgRegistry builds a PgRegistry from the provided exec/query functions
// and a transaction factory.
func NewPgRegistry(beginTx TxFactory, exec ExecFunc, query QueryFunc, lg *slog.Logger) *PgRegistry {
	if lg == nil {
		lg = slog.Default()
	}
	return &PgRegistry{
		beginTx: beginTx,
		exec:    exec,
		query:   query,
		logger:  lg,
	}
}

// EnsureBlock inserts a new CAS row or increments ref_count on an existing one.
// Returns true if the block was newly inserted.
func (r *PgRegistry) EnsureBlock(ctx context.Context, blockHash []byte, tenantID string, sizeBytes int32) (bool, error) {
	tag, err := r.exec(ctx, "cas_ensure_block", tenantID,
		`INSERT INTO cas_blocks (block_hash, tenant_id, size_bytes, ref_count)
		 VALUES ($1, $2, $3, 1)
		 ON CONFLICT (block_hash) DO NOTHING`,
		blockHash, tenantID, sizeBytes,
	)
	if err != nil {
		return false, fmt.Errorf("cas: ensure block: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (r *PgRegistry) BatchQueryExisting(ctx context.Context, hashes [][]byte) (map[string]bool, error) {
	if len(hashes) == 0 {
		return map[string]bool{}, nil
	}
	rows, err := r.query(ctx, "cas_batch_query", "",
		`SELECT block_hash FROM cas_blocks WHERE block_hash = ANY($1)`, hashes)
	if err != nil {
		return nil, fmt.Errorf("cas: batch query: %w", err)
	}
	defer rows.Close()

	result := make(map[string]bool, len(hashes))
	for rows.Next() {
		var h []byte
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		result[hex.EncodeToString(h)] = true
	}
	return result, rows.Err()
}

func (r *PgRegistry) GetBlock(ctx context.Context, blockHash []byte) (*BlockInfo, error) {
	rows, err := r.query(ctx, "cas_get_block", "",
		`SELECT block_hash, tenant_id::text, size_bytes, ref_count,
		        storage_tier, verified, created_at, updated_at
		 FROM cas_blocks WHERE block_hash = $1`, blockHash)
	if err != nil {
		return nil, fmt.Errorf("cas: get block: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, ErrBlockNotFound
	}
	var b BlockInfo
	var hashBytes []byte
	if err := rows.Scan(&hashBytes, &b.TenantID, &b.SizeBytes, &b.RefCount,
		&b.StorageTier, &b.Verified, &b.CreatedAt, &b.UpdatedAt); err != nil {
		return nil, err
	}
	b.BlockHash = hex.EncodeToString(hashBytes)
	return &b, rows.Err()
}

func (r *PgRegistry) FindOrphans(ctx context.Context, batchSize int) ([]BlockInfo, error) {
	if batchSize <= 0 {
		batchSize = 1000
	}
	if batchSize > 10000 {
		batchSize = 10000
	}
	rows, err := r.query(ctx, "cas_find_orphans", "",
		`SELECT block_hash, tenant_id::text, size_bytes, ref_count,
		        storage_tier, verified, created_at, updated_at
		 FROM cas_blocks
		 WHERE ref_count = 0
		   AND created_at < NOW() - $1::interval
		 ORDER BY created_at
		 LIMIT $2`,
		fmt.Sprintf("%d seconds", int(GCSafetyWindow.Seconds())), batchSize,
	)
	if err != nil {
		return nil, fmt.Errorf("cas: find orphans: %w", err)
	}
	defer rows.Close()

	var orphans []BlockInfo
	for rows.Next() {
		var b BlockInfo
		var hashBytes []byte
		if err := rows.Scan(&hashBytes, &b.TenantID, &b.SizeBytes, &b.RefCount,
			&b.StorageTier, &b.Verified, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, err
		}
		b.BlockHash = hex.EncodeToString(hashBytes)
		orphans = append(orphans, b)
	}
	return orphans, rows.Err()
}

func (r *PgRegistry) DeleteBlocks(ctx context.Context, hashes [][]byte) (int, error) {
	if len(hashes) == 0 {
		return 0, nil
	}
	tag, err := r.exec(ctx, "cas_delete_blocks", "",
		`DELETE FROM cas_blocks WHERE block_hash = ANY($1) AND ref_count = 0`, hashes)
	if err != nil {
		return 0, fmt.Errorf("cas: delete blocks: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (r *PgRegistry) GetStorageStats(ctx context.Context) (*StorageStats, error) {
	rows, err := r.query(ctx, "cas_stats", "",
		`SELECT
			COUNT(*),
			COALESCE(SUM(size_bytes), 0),
			COUNT(*) FILTER (WHERE ref_count = 0),
			COALESCE(SUM(size_bytes) FILTER (WHERE ref_count = 0), 0),
			COUNT(*) FILTER (WHERE storage_tier = 'HOT'),
			COUNT(*) FILTER (WHERE storage_tier = 'WARM'),
			COUNT(*) FILTER (WHERE storage_tier = 'COLD'),
			COUNT(*) FILTER (WHERE verified = FALSE),
			COALESCE(AVG(ref_count), 0),
			COALESCE(MAX(ref_count), 0)
		 FROM cas_blocks`)
	if err != nil {
		return nil, fmt.Errorf("cas: storage stats: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return &StorageStats{}, nil
	}
	var s StorageStats
	if err := rows.Scan(
		&s.TotalBlocks, &s.TotalBytes,
		&s.OrphanBlocks, &s.OrphanBytes,
		&s.HotBlocks, &s.WarmBlocks, &s.ColdBlocks,
		&s.UnverifiedBlocks, &s.AvgRefCount, &s.MaxRefCount,
	); err != nil {
		return nil, err
	}
	return &s, rows.Err()
}

func (r *PgRegistry) UpdateTier(ctx context.Context, blockHash []byte) error {
	_, err := r.exec(ctx, "cas_update_tier", "",
		`UPDATE cas_blocks
		 SET storage_tier = CASE
		        WHEN ref_count > $1 THEN 'HOT'
		        WHEN ref_count >= $2 THEN 'WARM'
		        ELSE 'COLD'
		     END,
		     updated_at = NOW()
		 WHERE block_hash = $3`,
		TierThresholdHot, TierThresholdWarm, blockHash,
	)
	return err
}
