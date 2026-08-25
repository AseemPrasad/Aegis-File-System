package workers

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// ---------------------------------------------------------------------------
// Fake tool implementations for testing
// ---------------------------------------------------------------------------

// FakeScanner simulates a malware scanner.
type FakeScanner struct {
	Infected map[string]string // blockHash → virus name (empty = clean)
	ScanErr  error
}

func NewFakeScanner() *FakeScanner {
	return &FakeScanner{Infected: make(map[string]string)}
}

func (s *FakeScanner) Scan(r io.Reader) (ScanResult, error) {
	if s.ScanErr != nil {
		return ScanResult{}, s.ScanErr
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return ScanResult{}, fmt.Errorf("read: %w", err)
	}
	content := string(data)

	// Check for EICAR test string.
	if strings.Contains(content, "X5O!P%@AP[4\\PZX54(P^)7CC)7}$EICAR") {
		return ScanResult{Status: "INFECTED", VirusName: "EICAR-Test-File"}, nil
	}

	// Check fake infection map by content.
	for hash, virus := range s.Infected {
		if strings.Contains(content, hash) {
			return ScanResult{Status: "INFECTED", VirusName: virus}, nil
		}
	}

	return ScanResult{Status: "CLEAN"}, nil
}

// FakeTextExtractor simulates OCR text extraction.
type FakeTextExtractor struct {
	Texts   map[string]string // blockHash → extracted text
	ExtractErr error
}

func NewFakeTextExtractor() *FakeTextExtractor {
	return &FakeTextExtractor{Texts: make(map[string]string)}
}

func (e *FakeTextExtractor) ExtractText(r io.Reader) (string, error) {
	if e.ExtractErr != nil {
		return "", e.ExtractErr
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	// Return fake extracted text based on content hash.
	content := string(data)
	if text, ok := e.Texts[content]; ok {
		return text, nil
	}
	return fmt.Sprintf("extracted-text-from-%d-bytes", len(data)), nil
}

// FakeVideoProcessor simulates FFmpeg video processing.
type FakeVideoProcessor struct {
	Thumbnails map[string][]byte // blockHash → thumbnail data
	Variants   map[string][]VideoVariant
	ProcessErr error
}

func NewFakeVideoProcessor() *FakeVideoProcessor {
	return &FakeVideoProcessor{
		Thumbnails: make(map[string][]byte),
		Variants:   make(map[string][]VideoVariant),
	}
}

func (p *FakeVideoProcessor) GenerateThumbnail(r io.Reader) ([]byte, string, error) {
	if p.ProcessErr != nil {
		return nil, "", p.ProcessErr
	}
	data, _ := io.ReadAll(r)
	key := fmt.Sprintf("thumb-%d", len(data))
	if thumb, ok := p.Thumbnails[string(data)]; ok {
		return thumb, "image/jpeg", nil
	}
	return []byte("fake-thumbnail-" + key), "image/jpeg", nil
}

func (p *FakeVideoProcessor) GenerateVariants(r io.Reader) ([]VideoVariant, error) {
	if p.ProcessErr != nil {
		return nil, p.ProcessErr
	}
	data, _ := io.ReadAll(r)
	key := fmt.Sprintf("var-%d", len(data))
	if variants, ok := p.Variants[string(data)]; ok {
		return variants, nil
	}
	return []VideoVariant{
		{Label: "720p", MimeType: "video/mp4", Data: []byte("variant-720p-" + key)},
		{Label: "480p", MimeType: "video/mp4", Data: []byte("variant-480p-" + key)},
	}, nil
}

// FakeEmbedder simulates vector embedding computation.
type FakeEmbedder struct {
	Vectors  map[string][]float32
	EmbedErr error
}

func NewFakeEmbedder() *FakeEmbedder {
	return &FakeEmbedder{Vectors: make(map[string][]float32)}
}

func (e *FakeEmbedder) Embed(r io.Reader) ([]float32, error) {
	if e.EmbedErr != nil {
		return nil, e.EmbedErr
	}
	data, _ := io.ReadAll(r)
	key := string(data)
	if vec, ok := e.Vectors[key]; ok {
		return vec, nil
	}
	// Return a fake embedding vector.
	return []float32{0.1, 0.2, 0.3, 0.4, 0.5}, nil
}

// ---------------------------------------------------------------------------
// FakeBlockReader — satisfies derivation.BlockReader for tests.
// ---------------------------------------------------------------------------

// FakeBlockReader returns pre-loaded block content.
type FakeBlockReader struct {
	Blocks  map[string][]byte // blockHash → content
	ReadErr error
}

func NewFakeBlockReader() *FakeBlockReader {
	return &FakeBlockReader{Blocks: make(map[string][]byte)}
}

func (r *FakeBlockReader) ReadBlock(_ context.Context, _, blockHash string) ([]byte, error) {
	if r.ReadErr != nil {
		return nil, r.ReadErr
	}
	data, ok := r.Blocks[blockHash]
	if !ok {
		return nil, fmt.Errorf("block not found: %s", blockHash)
	}
	return data, nil
}

// PutBlock adds block content for testing.
func (r *FakeBlockReader) PutBlock(hash string, data []byte) {
	r.Blocks[hash] = data
}
