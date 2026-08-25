package workers

import (
	"context"
	"fmt"
	"strings"

	"github.com/aegis-dev/aegis/internal/derivation"
)

// ---------------------------------------------------------------------------
// FFmpeg Worker — video thumbnail + variant generation.
// ---------------------------------------------------------------------------

// FFmpegWorker generates thumbnails and bitrate variants for video files.
type FFmpegWorker struct {
	processor VideoProcessor
	reader    derivation.BlockReader
}

// NewFFmpegWorker creates an FFmpeg derivation worker.
func NewFFmpegWorker(processor VideoProcessor, reader derivation.BlockReader) *FFmpegWorker {
	return &FFmpegWorker{processor: processor, reader: reader}
}

func (w *FFmpegWorker) Name() string { return "ffmpeg" }

// ShouldProcess returns true for video mime types.
func (w *FFmpegWorker) ShouldProcess(event derivation.Event) bool {
	return IsVideoMimeType(event.MimeType)
}

// Process downloads the video, generates thumbnail + variants.
func (w *FFmpegWorker) Process(ctx context.Context, event derivation.Event) (derivation.ProcessingResult, error) {
	if len(event.Chunks) == 0 {
		return derivation.ProcessingResult{}, fmt.Errorf("ffmpeg: no chunks in event")
	}

	data, err := w.reader.ReadBlock(ctx, event.TenantID, event.Chunks[0].BlockHash)
	if err != nil {
		return derivation.ProcessingResult{}, fmt.Errorf("ffmpeg: read block: %w", err)
	}

	// Generate thumbnail.
	thumbData, thumbMime, err := w.processor.GenerateThumbnail(strings.NewReader(string(data)))
	if err != nil {
		return derivation.ProcessingResult{}, fmt.Errorf("ffmpeg: thumbnail: %w", err)
	}

	// Generate variants.
	variants, err := w.processor.GenerateVariants(strings.NewReader(string(data)))
	if err != nil {
		return derivation.ProcessingResult{}, fmt.Errorf("ffmpeg: variants: %w", err)
	}

	variantLabels := make([]string, len(variants))
	for i, v := range variants {
		variantLabels[i] = v.Label
	}

	r := derivation.NewSuccessResult(event.VersionID, "ffmpeg", map[string]interface{}{
		"thumbnail_size_bytes": len(thumbData),
		"thumbnail_mime_type":  thumbMime,
		"variant_count":        len(variants),
		"variant_labels":       variantLabels,
		"source_duration_hint": "unknown",
	})
	return r, nil
}

// Ensure FFmpegWorker implements Worker at compile time.
var _ derivation.Worker = (*FFmpegWorker)(nil)
