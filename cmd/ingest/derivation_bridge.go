package main

import (
	"context"
	"log/slog"

	"github.com/aegis-dev/aegis/internal/derivation"
	"github.com/aegis-dev/aegis/internal/ingress"
)

// ---------------------------------------------------------------------------
// derivationBridge bridges ingress.EventBus → derivation.WorkerPool.
// ---------------------------------------------------------------------------

// derivationBridge implements ingress.EventBus by forwarding FileCommittedEvents
// to the derivation WorkerPool via an in-process channel.
type derivationBridge struct {
	pool   *derivation.WorkerPool
	logger *slog.Logger
	events chan derivation.Event
	stopCh chan struct{}
}

func newDerivationBridge(pool *derivation.WorkerPool, logger *slog.Logger) *derivationBridge {
	return &derivationBridge{
		pool:   pool,
		logger: logger,
		events: make(chan derivation.Event, 256),
		stopCh: make(chan struct{}),
	}
}

// Start launches the background consumer. Call before accepting traffic.
func (b *derivationBridge) Start(ctx context.Context) {
	go b.consume(ctx)
}

// Stop signals the consumer to drain and exit.
func (b *derivationBridge) Stop() {
	close(b.stopCh)
}

func (b *derivationBridge) PublishFileCommitted(_ context.Context, event ingress.FileCommittedEvent) error {
	chunks := make([]derivation.ChunkDetail, len(event.Chunks))
	for i, c := range event.Chunks {
		chunks[i] = derivation.ChunkDetail{
			Index:       c.Index,
			BlockHash:   c.BlockHash,
			SizeBytes:   c.SizeBytes,
			OffsetBytes: c.OffsetBytes,
		}
	}

	devt := derivation.Event{
		EventID:       event.EventID,
		EventType:     event.EventType,
		VersionID:     event.VersionID,
		NodeID:        event.NodeID,
		TenantID:      event.TenantID,
		TotalSize:     event.TotalSize,
		MimeType:      event.MimeType,
		ContentSHA256: event.ContentSHA256,
		CreatedAt:     event.CreatedAt,
		Chunks:        chunks,
	}

	select {
	case b.events <- devt:
	default:
		b.logger.Warn("CDC channel full, dropping event",
			"version_id", event.VersionID,
			"queue_depth", len(b.events))
	}
	return nil
}

func (b *derivationBridge) PublishBlockTombstone(_ context.Context, _ string, _ ingress.BlockTombstoneEvent) error {
	return nil
}

func (b *derivationBridge) consume(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			b.drain()
			return
		case <-b.stopCh:
			b.drain()
			return
		case evt := <-b.events:
			b.pool.ProcessEvent(ctx, evt)
		}
	}
}

func (b *derivationBridge) drain() {
	for {
		select {
		case evt := <-b.events:
			b.pool.ProcessEvent(context.Background(), evt)
		default:
			return
		}
	}
}
