package events

import (
	"context"

	"github.com/aegis-dev/aegis/internal/ingress"
)

// Topic Names
const (
	TopicFileCommits    = "aegis.file.commits"
	TopicDerivationTasks = "aegis.derivation.tasks"
	TopicGCTombstones   = "aegis.gc.tombstones"
	TopicAuditEvents    = "aegis.audit.events"
	TopicDerivationDLQ  = "aegis.derivation.dlq"
)

// EventPublisher defines the contract for producing events onto streaming buses.
type EventPublisher interface {
	PublishFileCommitted(ctx context.Context, event ingress.FileCommittedEvent) error
	PublishBlockTombstone(ctx context.Context, topic string, event ingress.BlockTombstoneEvent) error
	Close() error
}

// DerivationTaskEvent represents a task payload dispatched onto Kafka derivation topics.
type DerivationTaskEvent struct {
	EventID   string `json:"event_id"`
	TenantID  string `json:"tenant_id"`
	VersionID string `json:"version_id"`
	NodeID    string `json:"node_id"`
	BlockHash string `json:"block_hash"`
	MimeType  string `json:"mime_type"`
	SizeBytes int64  `json:"size_bytes"`
	Timestamp string `json:"timestamp"`
}
