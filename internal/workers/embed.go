package workers

import (
	"context"
	"fmt"
	"strings"

	"github.com/aegis-dev/aegis/internal/derivation"
)

// ---------------------------------------------------------------------------
// Vector Embed Worker — computes ML embeddings for search.
// ---------------------------------------------------------------------------

// VectorEmbedWorker computes vector embeddings for searchable content.
type VectorEmbedWorker struct {
	embedder Embedder
	reader   derivation.BlockReader
}

// NewVectorEmbedWorker creates a vector embedding worker.
func NewVectorEmbedWorker(embedder Embedder, reader derivation.BlockReader) *VectorEmbedWorker {
	return &VectorEmbedWorker{embedder: embedder, reader: reader}
}

func (w *VectorEmbedWorker) Name() string { return "vector-embed" }

// ShouldProcess returns true for embeddable content (images, video, text, PDFs).
func (w *VectorEmbedWorker) ShouldProcess(event derivation.Event) bool {
	return IsEmbeddableMimeType(event.MimeType)
}

// Process downloads the first chunk and computes a vector embedding.
func (w *VectorEmbedWorker) Process(ctx context.Context, event derivation.Event) (derivation.ProcessingResult, error) {
	if len(event.Chunks) == 0 {
		return derivation.ProcessingResult{}, fmt.Errorf("vector-embed: no chunks in event")
	}

	data, err := w.reader.ReadBlock(ctx, event.TenantID, event.Chunks[0].BlockHash)
	if err != nil {
		return derivation.ProcessingResult{}, fmt.Errorf("vector-embed: read block: %w", err)
	}

	embedding, err := w.embedder.Embed(strings.NewReader(string(data)))
	if err != nil {
		return derivation.ProcessingResult{}, fmt.Errorf("vector-embed: embed: %w", err)
	}

	r := derivation.NewSuccessResult(event.VersionID, "vector-embed", map[string]interface{}{
		"embedding_dim":    len(embedding),
		"source_bytes":     len(data),
		"source_mime_type": event.MimeType,
	})
	return r, nil
}

// Ensure VectorEmbedWorker implements Worker at compile time.
var _ derivation.Worker = (*VectorEmbedWorker)(nil)
