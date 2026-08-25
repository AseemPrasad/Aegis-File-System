package workers

import (
	"context"
	"fmt"
	"strings"

	"github.com/aegis-dev/aegis/internal/derivation"
)

// ---------------------------------------------------------------------------
// OCR Worker — text extraction from images and PDFs.
// ---------------------------------------------------------------------------

// OCRWorker extracts text from images and PDFs using a TextExtractor.
type OCRWorker struct {
	extractor TextExtractor
	reader    derivation.BlockReader
}

// NewOCRWorker creates an OCR derivation worker.
func NewOCRWorker(extractor TextExtractor, reader derivation.BlockReader) *OCRWorker {
	return &OCRWorker{extractor: extractor, reader: reader}
}

func (w *OCRWorker) Name() string { return "ocr" }

// ShouldProcess returns true for images and PDFs.
func (w *OCRWorker) ShouldProcess(event derivation.Event) bool {
	return IsImageMimeType(event.MimeType)
}

// Process downloads the first chunk and extracts text.
func (w *OCRWorker) Process(ctx context.Context, event derivation.Event) (derivation.ProcessingResult, error) {
	if len(event.Chunks) == 0 {
		return derivation.ProcessingResult{}, fmt.Errorf("ocr: no chunks in event")
	}

	data, err := w.reader.ReadBlock(ctx, event.TenantID, event.Chunks[0].BlockHash)
	if err != nil {
		return derivation.ProcessingResult{}, fmt.Errorf("ocr: read block: %w", err)
	}

	text, err := w.extractor.ExtractText(strings.NewReader(string(data)))
	if err != nil {
		return derivation.ProcessingResult{}, fmt.Errorf("ocr: extract text: %w", err)
	}

	r := derivation.NewSuccessResult(event.VersionID, "ocr", map[string]interface{}{
		"extracted_text_length":  len(text),
		"extracted_text_preview": truncateText(text, 500),
		"source_mime_type":       event.MimeType,
	})
	return r, nil
}

// Ensure OCRWorker implements Worker at compile time.
var _ derivation.Worker = (*OCRWorker)(nil)

// truncateText returns the first n runes of text.
func truncateText(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}
