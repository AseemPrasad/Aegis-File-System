package workers

import "io"

// ---------------------------------------------------------------------------
// Abstract tool interfaces — injected into workers for testability.
// ---------------------------------------------------------------------------

// Scanner scans content for malware (e.g., ClamAV).
type Scanner interface {
	Scan(ctx io.Reader) (ScanResult, error)
}

// ScanResult is the output of a malware scan.
type ScanResult struct {
	Status    string // "CLEAN" or "INFECTED"
	VirusName string // set when INFECTED
}

// TextExtractor extracts text from documents (e.g., Tesseract OCR).
type TextExtractor interface {
	ExtractText(ctx io.Reader) (string, error)
}

// VideoProcessor handles video transformations (e.g., FFmpeg).
type VideoProcessor interface {
	GenerateThumbnail(ctx io.Reader) ([]byte, string, error) // data, contentType, error
	GenerateVariants(ctx io.Reader) ([]VideoVariant, error)
}

// VideoVariant is a transcoded version of a video.
type VideoVariant struct {
	Label    string // e.g., "720p", "480p"
	MimeType string
	Data     []byte
}

// Embedder computes vector embeddings for ML search.
type Embedder interface {
	Embed(ctx io.Reader) ([]float32, error)
}

// ---------------------------------------------------------------------------
// MIME type classification helpers
// ---------------------------------------------------------------------------

// IsImageMimeType returns true for common image types.
func IsImageMimeType(mime string) bool {
	switch {
	case mime == "image/jpeg", mime == "image/png", mime == "image/gif",
		mime == "image/webp", mime == "image/tiff", mime == "image/bmp",
		mime == "image/svg+xml":
		return true
	case mime == "application/pdf":
		return true // PDFs are OCR-able
	}
	return false
}

// IsVideoMimeType returns true for common video types.
func IsVideoMimeType(mime string) bool {
	switch mime {
	case "video/mp4", "video/webm", "video/ogg", "video/quicktime",
		"video/x-msvideo", "video/x-matroska", "video/mpeg":
		return true
	}
	return false
}

// IsAudioMimeType returns true for common audio types.
func IsAudioMimeType(mime string) bool {
	switch mime {
	case "audio/mpeg", "audio/ogg", "audio/wav", "audio/webm",
		"audio/flac", "audio/aac":
		return true
	}
	return false
}

// IsEmbeddableMimeType returns true for types that benefit from vector embeddings.
func IsEmbeddableMimeType(mime string) bool {
	return IsImageMimeType(mime) || IsVideoMimeType(mime) || IsAudioMimeType(mime) ||
		mime == "application/pdf" || mime == "text/plain" || mime == "text/markdown"
}
