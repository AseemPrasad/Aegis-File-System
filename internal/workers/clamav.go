package workers

import (
	"context"
	"fmt"
	"strings"

	"github.com/aegis-dev/aegis/internal/derivation"
)

// ---------------------------------------------------------------------------
// ClamAV Worker — malware scanning for all file types.
// ---------------------------------------------------------------------------

// ClamAVWorker scans committed files for malware using a Scanner (e.g., ClamAV).
type ClamAVWorker struct {
	scanner Scanner
	reader  derivation.BlockReader
}

// NewClamAVWorker creates a ClamAV derivation worker.
func NewClamAVWorker(scanner Scanner, reader derivation.BlockReader) *ClamAVWorker {
	return &ClamAVWorker{scanner: scanner, reader: reader}
}

func (w *ClamAVWorker) Name() string { return "clamav" }

// ShouldProcess returns true — all files should be scanned.
func (w *ClamAVWorker) ShouldProcess(_ derivation.Event) bool { return true }

// Process downloads the first chunk and scans it for malware.
func (w *ClamAVWorker) Process(ctx context.Context, event derivation.Event) (derivation.ProcessingResult, error) {
	if len(event.Chunks) == 0 {
		return derivation.ProcessingResult{}, fmt.Errorf("clamav: no chunks in event")
	}

	// Download first chunk for scanning.
	data, err := w.reader.ReadBlock(ctx, event.TenantID, event.Chunks[0].BlockHash)
	if err != nil {
		return derivation.ProcessingResult{}, fmt.Errorf("clamav: read block: %w", err)
	}

	// Scan.
	result, err := w.scanner.Scan(strings.NewReader(string(data)))
	if err != nil {
		return derivation.ProcessingResult{}, fmt.Errorf("clamav: scan: %w", err)
	}

	if result.Status == "INFECTED" {
		r := derivation.NewFailedResult(event.VersionID, "clamav",
			fmt.Errorf("malware detected: %s", result.VirusName))
		return r, nil
	}

	s := derivation.NewSuccessResult(event.VersionID, "clamav", map[string]interface{}{
		"scan_status":   result.Status,
		"scanned_bytes": len(data),
	})
	return s, nil
}

// ScanAllChunks downloads and scans all chunks (more thorough but slower).
func (w *ClamAVWorker) ScanAllChunks(ctx context.Context, event derivation.Event) (derivation.ProcessingResult, error) {
	var totalScanned int
	for _, chunk := range event.Chunks {
		data, err := w.reader.ReadBlock(ctx, event.TenantID, chunk.BlockHash)
		if err != nil {
			return derivation.ProcessingResult{}, fmt.Errorf("clamav: read block %s: %w", chunk.BlockHash, err)
		}
		result, err := w.scanner.Scan(strings.NewReader(string(data)))
		if err != nil {
			return derivation.ProcessingResult{}, fmt.Errorf("clamav: scan block %s: %w", chunk.BlockHash, err)
		}
		if result.Status == "INFECTED" {
			r := derivation.NewFailedResult(event.VersionID, "clamav",
				fmt.Errorf("malware detected in chunk %d: %s", chunk.Index, result.VirusName))
			return r, nil
		}
		totalScanned += len(data)
	}

	s := derivation.NewSuccessResult(event.VersionID, "clamav", map[string]interface{}{
		"scan_status":    "CLEAN",
		"chunks_scanned": len(event.Chunks),
		"total_bytes":    totalScanned,
	})
	return s, nil
}

// Ensure ClamAVWorker implements Worker at compile time.
var _ derivation.Worker = (*ClamAVWorker)(nil)
