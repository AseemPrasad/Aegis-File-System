package ingress

import (
	"context"
	"log/slog"
)

// ---------------------------------------------------------------------------
// EventBus — CDC event publisher for downstream consumers.
// ---------------------------------------------------------------------------

// ChunkDetail describes one block in the committed manifest for CDC consumers.
type ChunkDetail struct {
	Index      int   `json:"index"`
	BlockHash  string `json:"block_hash"`
	SizeBytes  int64  `json:"size_bytes"`
	OffsetBytes int64 `json:"offset_bytes"`
}

// FileCommittedEvent is the CDC payload published after a successful commit.
type FileCommittedEvent struct {
	EventID       string        `json:"event_id"`
	EventType     string        `json:"event_type"` // "VERSION_COMMITTED"
	TenantID      string        `json:"tenant_id"`
	NodeID        string        `json:"node_id"`
	VersionID     string        `json:"version_id"`
	VersionNumber int           `json:"version_number"`
	TotalSize     int64         `json:"total_size_bytes"`
	MimeType      string        `json:"mime_type"`
	ContentSHA256 string        `json:"content_sha256"`
	CreatedAt     string        `json:"created_at"` // RFC 3339
	Chunks        []ChunkDetail `json:"chunks"`
	// BlockHashes is kept for backward compatibility with legacy consumers.
	BlockHashes []string `json:"block_hashes,omitempty"`
}

// BlockTombstoneEvent is the CDC payload emitted when a CAS block is
// scheduled for deletion (ref_count = 0, past safety window).
type BlockTombstoneEvent struct {
	BlockHash string `json:"block_hash"`
	TenantID  string `json:"tenant_id"`
	SizeBytes int32  `json:"size_bytes"`
	Timestamp string `json:"timestamp"` // RFC 3339
	Reason    string `json:"reason"`    // "orphan_gc", "session_expired"
}

// EventBus publishes CDC events after successful commits.
type EventBus interface {
	PublishFileCommitted(ctx context.Context, event FileCommittedEvent) error
	PublishBlockTombstone(ctx context.Context, topic string, event BlockTombstoneEvent) error
}

// NoopBus discards events. Used when KAFKA_BROKERS is unset (dev profile).
type NoopBus struct {
	Logger *slog.Logger
}

func (b *NoopBus) PublishFileCommitted(_ context.Context, event FileCommittedEvent) error {
	if b.Logger != nil {
		b.Logger.Info("CDC event (noop)",
			"tenant", event.TenantID,
			"node", event.NodeID,
			"version", event.VersionNumber)
	}
	return nil
}

func (b *NoopBus) PublishBlockTombstone(_ context.Context, topic string, event BlockTombstoneEvent) error {
	if b.Logger != nil {
		b.Logger.Info("tombstone event (noop)",
			"topic", topic,
			"hash", event.BlockHash,
			"reason", event.Reason)
	}
	return nil
}
