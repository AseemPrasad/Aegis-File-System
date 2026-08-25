package main

import (
	"context"
	"fmt"
	"io"

	"github.com/aegis-dev/aegis/internal/database"
	"github.com/aegis-dev/aegis/internal/workers"
)

// ---------------------------------------------------------------------------
// dbBlockReader bridges database.DatabaseClient to derivation.BlockReader.
// ---------------------------------------------------------------------------

type dbBlockReader struct {
	db *database.DatabaseClient
}

func (r *dbBlockReader) ReadBlock(ctx context.Context, _, blockHash string) ([]byte, error) {
	rows, err := r.db.QueryWithMetrics(ctx, "derivation", "read_block", "system",
		`SELECT size_bytes FROM cas_blocks WHERE block_hash = $1`, blockHash)
	if err != nil {
		return nil, fmt.Errorf("read block: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, fmt.Errorf("block not found: %s", blockHash)
	}

	var sizeBytes int64
	if err := rows.Scan(&sizeBytes); err != nil {
		return nil, fmt.Errorf("scan block size: %w", err)
	}

	// Placeholder: actual content fetched from object storage via BlobStore.
	return nil, fmt.Errorf("block content not available via DB; use object storage reader")
}

// ---------------------------------------------------------------------------
// No-op tool implementations — compile-time placeholders for external deps.
// Replace with real ClamAV/Tesseract/FFmpeg clients in production.
// ---------------------------------------------------------------------------

type noopScanner struct{}

func (s *noopScanner) Scan(_ io.Reader) (workers.ScanResult, error) {
	return workers.ScanResult{Status: "CLEAN"}, nil
}

type noopTextExtractor struct{}

func (e *noopTextExtractor) ExtractText(_ io.Reader) (string, error) {
	return "", nil
}

type noopVideoProcessor struct{}

func (p *noopVideoProcessor) GenerateThumbnail(_ io.Reader) ([]byte, string, error) {
	return nil, "image/jpeg", nil
}

func (p *noopVideoProcessor) GenerateVariants(_ io.Reader) ([]workers.VideoVariant, error) {
	return nil, nil
}

type noopEmbedder struct{}

func (e *noopEmbedder) Embed(_ io.Reader) ([]float32, error) {
	return nil, nil
}
