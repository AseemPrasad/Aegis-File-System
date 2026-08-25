package ingress

import (
	"context"
	"log/slog"
)

// ---------------------------------------------------------------------------
// EventBus — CDC event publisher for downstream consumers.
// ---------------------------------------------------------------------------

// FileCommittedEvent is the CDC payload published after a successful commit.
type FileCommittedEvent struct {
	TenantID     string   `json:"tenant_id"`
	NodeID       string   `json:"node_id"`
	VersionID    string   `json:"version_id"`
	VersionNumber int     `json:"version_number"`
	TotalSize    int64    `json:"total_size_bytes"`
	ContentSHA256 string  `json:"content_sha256"`
	BlockHashes  []string `json:"block_hashes"`
}

// EventBus publishes CDC events after successful commits.
type EventBus interface {
	PublishFileCommitted(ctx context.Context, event FileCommittedEvent) error
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
