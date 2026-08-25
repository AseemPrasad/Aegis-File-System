package workers

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestIsAudioMimeType(t *testing.T) {
	tests := []struct {
		mime string
		want bool
	}{
		{"audio/mpeg", true},
		{"audio/ogg", true},
		{"audio/wav", true},
		{"audio/webm", true},
		{"audio/flac", true},
		{"audio/aac", true},
		{"video/mp4", false},
		{"text/plain", false},
		{"application/pdf", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsAudioMimeType(tt.mime); got != tt.want {
			t.Errorf("IsAudioMimeType(%q) = %v, want %v", tt.mime, got, tt.want)
		}
	}
}

func TestIsEmbeddableMimeType(t *testing.T) {
	tests := []struct {
		mime string
		want bool
	}{
		{"image/jpeg", true},
		{"video/mp4", true},
		{"audio/mpeg", true},
		{"application/pdf", true},
		{"text/plain", true},
		{"text/markdown", true},
		{"application/zip", false},
		{"application/octet-stream", false},
	}
	for _, tt := range tests {
		if got := IsEmbeddableMimeType(tt.mime); got != tt.want {
			t.Errorf("IsEmbeddableMimeType(%q) = %v, want %v", tt.mime, got, tt.want)
		}
	}
}

func TestVectorEmbedWorker_EmbedError(t *testing.T) {
	embedder := NewFakeEmbedder()
	embedder.EmbedErr = fmt.Errorf("model offline")
	blockHash := strings.Repeat("ee", 32)
	reader := testBlockReader(blockHash, []byte("embeddable content"))
	worker := NewVectorEmbedWorker(embedder, reader)

	event := testEvent("v1", "image/png", blockHash)
	_, err := worker.Process(context.Background(), event)
	if err == nil {
		t.Error("expected error from embedder")
	}
}

func TestVectorEmbedWorker_ReadBlockError(t *testing.T) {
	embedder := NewFakeEmbedder()
	reader := NewFakeBlockReader()
	reader.ReadErr = fmt.Errorf("storage down")
	worker := NewVectorEmbedWorker(embedder, reader)

	event := testEvent("v1", "image/png", strings.Repeat("ee", 32))
	_, err := worker.Process(context.Background(), event)
	if err == nil {
		t.Error("expected error from block reader")
	}
}

func TestOCRWorker_ReadBlockError(t *testing.T) {
	extractor := NewFakeTextExtractor()
	reader := NewFakeBlockReader()
	reader.ReadErr = fmt.Errorf("storage down")
	worker := NewOCRWorker(extractor, reader)

	event := testEvent("v1", "image/png", strings.Repeat("cc", 32))
	_, err := worker.Process(context.Background(), event)
	if err == nil {
		t.Error("expected error from block reader")
	}
}

func TestFFmpegWorker_ReadBlockError(t *testing.T) {
	processor := NewFakeVideoProcessor()
	reader := NewFakeBlockReader()
	reader.ReadErr = fmt.Errorf("storage down")
	worker := NewFFmpegWorker(processor, reader)

	event := testEvent("v1", "video/mp4", strings.Repeat("dd", 32))
	_, err := worker.Process(context.Background(), event)
	if err == nil {
		t.Error("expected error from block reader")
	}
}
