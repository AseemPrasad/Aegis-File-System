package derivation

import "context"

// Worker processes CDC events for a specific derivation (e.g., virus scan, OCR).
type Worker interface {
	// Name returns the unique worker identifier (e.g., "clamav", "ocr").
	Name() string

	// ShouldProcess returns true if this worker handles the event's mime type.
	ShouldProcess(event Event) bool

	// Process executes the derivation logic. Called only when ShouldProcess
	// returns true. Must be safe for concurrent use.
	Process(ctx context.Context, event Event) (ProcessingResult, error)
}

// BlockReader abstracts reading a block's content from object storage.
type BlockReader interface {
	ReadBlock(ctx context.Context, tenantID, blockHash string) ([]byte, error)
}
