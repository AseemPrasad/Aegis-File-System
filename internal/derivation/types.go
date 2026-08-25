// Package derivation implements the CDC event-driven derivation pipeline.
//
// When a file version is committed, downstream workers (ClamAV scanner, OCR,
// FFmpeg, vector embedding) are notified via CDC events. Each worker decides
// whether to process the event based on mime type, then persists results.
//
// Architecture:
//
//	CDC event → WorkerPool → Worker.ShouldProcess → Worker.Process → ResultStore
//	                          ↓ (failure)                ↓ (permanent)
//	                          Retry (exponential)     Dead Letter Queue
package derivation

import "time"

// ---------------------------------------------------------------------------
// Event types — mirrors the CDC schema from ingress but decoupled for workers.
// ---------------------------------------------------------------------------

// ChunkDetail describes one block in the committed manifest.
type ChunkDetail struct {
	Index       int   `json:"index"`
	BlockHash   string `json:"block_hash"`
	SizeBytes   int64  `json:"size_bytes"`
	OffsetBytes int64  `json:"offset_bytes"`
}

// Event is the CDC event consumed by derivation workers.
type Event struct {
	EventID       string        `json:"event_id"`
	EventType     string        `json:"event_type"`
	VersionID     string        `json:"version_id"`
	NodeID        string        `json:"node_id"`
	TenantID      string        `json:"tenant_id"`
	TotalSize     int64         `json:"total_size_bytes"`
	MimeType      string        `json:"mime_type"`
	ContentSHA256 string        `json:"content_sha256"`
	CreatedAt     string        `json:"created_at"`
	Chunks        []ChunkDetail `json:"chunks"`
}

// ---------------------------------------------------------------------------
// Processing results
// ---------------------------------------------------------------------------

// WorkerStatus represents the outcome of a derivation worker.
type WorkerStatus string

const (
	StatusSuccess WorkerStatus = "SUCCESS"
	StatusFailed  WorkerStatus = "FAILED"
	StatusSkipped WorkerStatus = "SKIPPED"
)

// ProcessingResult is the output of a derivation worker for one event.
type ProcessingResult struct {
	ResultID   string                 `json:"result_id"`
	VersionID  string                 `json:"version_id"`
	WorkerName string                 `json:"worker_name"`
	Status     WorkerStatus           `json:"status"`
	ResultData map[string]interface{} `json:"result_data,omitempty"`
	Error      string                 `json:"error,omitempty"`
	CreatedAt  time.Time              `json:"created_at"`
}

// NewSuccessResult creates a successful result.
func NewSuccessResult(versionID, workerName string, data map[string]interface{}) ProcessingResult {
	return ProcessingResult{
		VersionID:  versionID,
		WorkerName: workerName,
		Status:     StatusSuccess,
		ResultData: data,
		CreatedAt:  time.Now().UTC(),
	}
}

// NewFailedResult creates a failed result.
func NewFailedResult(versionID, workerName string, err error) ProcessingResult {
	result := ProcessingResult{
		VersionID:  versionID,
		WorkerName: workerName,
		Status:     StatusFailed,
		CreatedAt:  time.Now().UTC(),
	}
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

// NewSkippedResult creates a skipped result (worker doesn't handle this mime type).
func NewSkippedResult(versionID, workerName string) ProcessingResult {
	return ProcessingResult{
		VersionID:  versionID,
		WorkerName: workerName,
		Status:     StatusSkipped,
		CreatedAt:  time.Now().UTC(),
	}
}
