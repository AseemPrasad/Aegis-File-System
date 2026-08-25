package derivation

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Dead Letter Queue — captures permanently failed events for investigation.
// ---------------------------------------------------------------------------

// DeadLetterEntry is one failed event with context.
type DeadLetterEntry struct {
	Event     Event           `json:"event"`
	Worker    string          `json:"worker"`
	Error     string          `json:"error"`
	Attempts  int             `json:"attempts"`
	CreatedAt time.Time       `json:"created_at"`
}

// DeadLetterQueue abstracts DLQ behavior. The in-memory implementation is
// used for dev/testing; a Kafka-backed implementation would be swapped in
// for production.
type DeadLetterQueue interface {
	Enqueue(ctx context.Context, entry DeadLetterEntry) error
	Drain(ctx context.Context, limit int) ([]DeadLetterEntry, error)
	Size() int
}

// ---------------------------------------------------------------------------
// MemoryDLQ — in-memory dead letter queue for dev/testing.
// ---------------------------------------------------------------------------

// MemoryDLQ is a thread-safe in-memory DLQ.
type MemoryDLQ struct {
	mu      sync.Mutex
	entries []DeadLetterEntry
	logger  *slog.Logger
}

// NewMemoryDLQ creates a new in-memory DLQ.
func NewMemoryDLQ(logger *slog.Logger) *MemoryDLQ {
	if logger == nil {
		logger = slog.Default()
	}
	return &MemoryDLQ{logger: logger}
}

// Enqueue adds a failed event to the DLQ.
func (q *MemoryDLQ) Enqueue(_ context.Context, entry DeadLetterEntry) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	q.entries = append(q.entries, entry)
	q.logger.Warn("DLQ: event enqueued",
		"worker", entry.Worker,
		"version_id", entry.Event.VersionID,
		"error", entry.Error,
		"attempts", entry.Attempts)
	return nil
}

// Drain returns up to limit entries and removes them from the queue.
func (q *MemoryDLQ) Drain(_ context.Context, limit int) ([]DeadLetterEntry, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if limit <= 0 || limit > len(q.entries) {
		limit = len(q.entries)
	}
	out := make([]DeadLetterEntry, limit)
	copy(out, q.entries[:limit])
	q.entries = q.entries[limit:]
	return out, nil
}

// Size returns the number of entries in the DLQ.
func (q *MemoryDLQ) Size() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.entries)
}
