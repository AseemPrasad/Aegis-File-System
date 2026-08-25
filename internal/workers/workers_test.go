package workers

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aegis-dev/aegis/internal/derivation"
)

func testEvent(versionID, mimeType, blockHash string) derivation.Event {
	return derivation.Event{
		EventID:       "evt-test-001",
		EventType:     "VERSION_COMMITTED",
		VersionID:     versionID,
		NodeID:        "node-001",
		TenantID:      "tenant-001",
		TotalSize:     1024,
		MimeType:      mimeType,
		ContentSHA256: strings.Repeat("ab", 32),
		CreatedAt:     "2025-08-24T12:34:56Z",
		Chunks: []derivation.ChunkDetail{
			{Index: 0, BlockHash: blockHash, SizeBytes: 1024, OffsetBytes: 0},
		},
	}
}

func testBlockReader(hash string, data []byte) *FakeBlockReader {
	r := NewFakeBlockReader()
	r.PutBlock(hash, data)
	return r
}

func TestClamAVWorker_CleanFile(t *testing.T) {
	scanner := NewFakeScanner()
	blockHash := strings.Repeat("aa", 32)
	reader := testBlockReader(blockHash, []byte("safe content here"))
	worker := NewClamAVWorker(scanner, reader)

	event := testEvent("v1", "application/octet-stream", blockHash)
	result, err := worker.Process(context.Background(), event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != derivation.StatusSuccess {
		t.Errorf("status = %q, want SUCCESS", result.Status)
	}
	if result.WorkerName != "clamav" {
		t.Errorf("worker = %q, want clamav", result.WorkerName)
	}
}

func TestClamAVWorker_InfectedFile(t *testing.T) {
	scanner := NewFakeScanner()
	blockHash := strings.Repeat("bb", 32)
	eicar := "X5O!P%@AP[4" + "\x5c" + "PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*"
	reader := testBlockReader(blockHash, []byte(eicar))
	worker := NewClamAVWorker(scanner, reader)

	event := testEvent("v1", "application/octet-stream", blockHash)
	result, err := worker.Process(context.Background(), event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != derivation.StatusFailed {
		t.Errorf("status = %q, want FAILED for infected file", result.Status)
	}
}

func TestClamAVWorker_NoChunks(t *testing.T) {
	scanner := NewFakeScanner()
	worker := NewClamAVWorker(scanner, NewFakeBlockReader())

	event := derivation.Event{VersionID: "v1", Chunks: nil}
	_, err := worker.Process(context.Background(), event)
	if err == nil {
		t.Error("expected error for empty chunks")
	}
}

func TestClamAVWorker_ShouldProcess(t *testing.T) {
	worker := NewClamAVWorker(NewFakeScanner(), NewFakeBlockReader())
	if !worker.ShouldProcess(testEvent("v1", "application/pdf", "aa")) {
		t.Error("clamav should process all mime types")
	}
	if !worker.ShouldProcess(testEvent("v1", "video/mp4", "aa")) {
		t.Error("clamav should process video")
	}
}

func TestClamAVWorker_ScanAllChunks(t *testing.T) {
	scanner := NewFakeScanner()
	hash1 := strings.Repeat("aa", 32)
	hash2 := strings.Repeat("bb", 32)
	reader := NewFakeBlockReader()
	reader.PutBlock(hash1, []byte("chunk 1 content"))
	reader.PutBlock(hash2, []byte("chunk 2 content"))
	worker := NewClamAVWorker(scanner, reader)

	event := derivation.Event{
		VersionID: "v1",
		TenantID:  "tenant-001",
		Chunks: []derivation.ChunkDetail{
			{Index: 0, BlockHash: hash1, SizeBytes: 16, OffsetBytes: 0},
			{Index: 1, BlockHash: hash2, SizeBytes: 16, OffsetBytes: 16},
		},
	}
	result, err := worker.ScanAllChunks(context.Background(), event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != derivation.StatusSuccess {
		t.Errorf("status = %q, want SUCCESS", result.Status)
	}
}

func TestOCRWorker_ProcessImage(t *testing.T) {
	extractor := NewFakeTextExtractor()
	blockHash := strings.Repeat("cc", 32)
	reader := testBlockReader(blockHash, []byte("fake image bytes"))
	worker := NewOCRWorker(extractor, reader)

	event := testEvent("v1", "image/png", blockHash)
	result, err := worker.Process(context.Background(), event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != derivation.StatusSuccess {
		t.Errorf("status = %q, want SUCCESS", result.Status)
	}
}

func TestOCRWorker_SkipsVideo(t *testing.T) {
	worker := NewOCRWorker(NewFakeTextExtractor(), NewFakeBlockReader())
	if worker.ShouldProcess(testEvent("v1", "video/mp4", "aa")) {
		t.Error("ocr should skip video")
	}
}

func TestOCRWorker_SkipsText(t *testing.T) {
	worker := NewOCRWorker(NewFakeTextExtractor(), NewFakeBlockReader())
	if worker.ShouldProcess(testEvent("v1", "text/plain", "aa")) {
		t.Error("ocr should skip text")
	}
}

func TestOCRWorker_ProcessesPDF(t *testing.T) {
	worker := NewOCRWorker(NewFakeTextExtractor(), NewFakeBlockReader())
	if !worker.ShouldProcess(testEvent("v1", "application/pdf", "aa")) {
		t.Error("ocr should process PDF")
	}
}

func TestOCRWorker_NoChunks(t *testing.T) {
	worker := NewOCRWorker(NewFakeTextExtractor(), NewFakeBlockReader())
	_, err := worker.Process(context.Background(), derivation.Event{VersionID: "v1"})
	if err == nil {
		t.Error("expected error for empty chunks")
	}
}

func TestFFmpegWorker_ProcessVideo(t *testing.T) {
	processor := NewFakeVideoProcessor()
	blockHash := strings.Repeat("dd", 32)
	reader := testBlockReader(blockHash, []byte("fake video bytes"))
	worker := NewFFmpegWorker(processor, reader)

	event := testEvent("v1", "video/mp4", blockHash)
	result, err := worker.Process(context.Background(), event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != derivation.StatusSuccess {
		t.Errorf("status = %q, want SUCCESS", result.Status)
	}
}

func TestFFmpegWorker_SkipsImage(t *testing.T) {
	worker := NewFFmpegWorker(NewFakeVideoProcessor(), NewFakeBlockReader())
	if worker.ShouldProcess(testEvent("v1", "image/png", "aa")) {
		t.Error("ffmpeg should skip image")
	}
}

func TestFFmpegWorker_SkipsText(t *testing.T) {
	worker := NewFFmpegWorker(NewFakeVideoProcessor(), NewFakeBlockReader())
	if worker.ShouldProcess(testEvent("v1", "text/plain", "aa")) {
		t.Error("ffmpeg should skip text")
	}
}

func TestFFmpegWorker_ProcessesWebM(t *testing.T) {
	worker := NewFFmpegWorker(NewFakeVideoProcessor(), NewFakeBlockReader())
	if !worker.ShouldProcess(testEvent("v1", "video/webm", "aa")) {
		t.Error("ffmpeg should process video/webm")
	}
}

func TestFFmpegWorker_NoChunks(t *testing.T) {
	worker := NewFFmpegWorker(NewFakeVideoProcessor(), NewFakeBlockReader())
	_, err := worker.Process(context.Background(), derivation.Event{VersionID: "v1"})
	if err == nil {
		t.Error("expected error for empty chunks")
	}
}

func TestVectorEmbedWorker_Process(t *testing.T) {
	embedder := NewFakeEmbedder()
	blockHash := strings.Repeat("ee", 32)
	reader := testBlockReader(blockHash, []byte("embeddable content"))
	worker := NewVectorEmbedWorker(embedder, reader)

	event := testEvent("v1", "image/png", blockHash)
	result, err := worker.Process(context.Background(), event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != derivation.StatusSuccess {
		t.Errorf("status = %q, want SUCCESS", result.Status)
	}
}

func TestVectorEmbedWorker_SkipsBinary(t *testing.T) {
	worker := NewVectorEmbedWorker(NewFakeEmbedder(), NewFakeBlockReader())
	if worker.ShouldProcess(testEvent("v1", "application/zip", "aa")) {
		t.Error("embed should skip zip files")
	}
}

func TestVectorEmbedWorker_ProcessesText(t *testing.T) {
	worker := NewVectorEmbedWorker(NewFakeEmbedder(), NewFakeBlockReader())
	if !worker.ShouldProcess(testEvent("v1", "text/plain", "aa")) {
		t.Error("embed should process text")
	}
}

func TestVectorEmbedWorker_ProcessesPDF(t *testing.T) {
	worker := NewVectorEmbedWorker(NewFakeEmbedder(), NewFakeBlockReader())
	if !worker.ShouldProcess(testEvent("v1", "application/pdf", "aa")) {
		t.Error("embed should process PDF")
	}
}

func TestVectorEmbedWorker_NoChunks(t *testing.T) {
	worker := NewVectorEmbedWorker(NewFakeEmbedder(), NewFakeBlockReader())
	_, err := worker.Process(context.Background(), derivation.Event{VersionID: "v1"})
	if err == nil {
		t.Error("expected error for empty chunks")
	}
}

func TestClamAVWorker_ScanError(t *testing.T) {
	scanner := NewFakeScanner()
	scanner.ScanErr = fmt.Errorf("scanner offline")
	blockHash := strings.Repeat("aa", 32)
	reader := testBlockReader(blockHash, []byte("content"))
	worker := NewClamAVWorker(scanner, reader)

	event := testEvent("v1", "application/octet-stream", blockHash)
	_, err := worker.Process(context.Background(), event)
	if err == nil {
		t.Error("expected error from scanner")
	}
}

func TestOCRWorker_ExtractError(t *testing.T) {
	extractor := NewFakeTextExtractor()
	extractor.ExtractErr = fmt.Errorf("tesseract not installed")
	blockHash := strings.Repeat("cc", 32)
	reader := testBlockReader(blockHash, []byte("image data"))
	worker := NewOCRWorker(extractor, reader)

	event := testEvent("v1", "image/png", blockHash)
	_, err := worker.Process(context.Background(), event)
	if err == nil {
		t.Error("expected error from extractor")
	}
}

func TestFFmpegWorker_ProcessError(t *testing.T) {
	processor := NewFakeVideoProcessor()
	processor.ProcessErr = fmt.Errorf("ffmpeg not found")
	blockHash := strings.Repeat("dd", 32)
	reader := testBlockReader(blockHash, []byte("video data"))
	worker := NewFFmpegWorker(processor, reader)

	event := testEvent("v1", "video/mp4", blockHash)
	_, err := worker.Process(context.Background(), event)
	if err == nil {
		t.Error("expected error from processor")
	}
}

func TestClamAVWorker_ReadBlockError(t *testing.T) {
	reader := NewFakeBlockReader()
	reader.ReadErr = fmt.Errorf("storage unavailable")
	worker := NewClamAVWorker(NewFakeScanner(), reader)

	event := testEvent("v1", "application/octet-stream", strings.Repeat("aa", 32))
	_, err := worker.Process(context.Background(), event)
	if err == nil {
		t.Error("expected error from block reader")
	}
}

func TestIsImageMimeType(t *testing.T) {
	tests := []struct{ mime string; want bool }{
		{"image/jpeg", true},
		{"image/png", true},
		{"application/pdf", true},
		{"video/mp4", false},
		{"text/plain", false},
		{"application/zip", false},
	}
	for _, tt := range tests {
		if got := IsImageMimeType(tt.mime); got != tt.want {
			t.Errorf("IsImageMimeType(%q) = %v, want %v", tt.mime, got, tt.want)
		}
	}
}

func TestIsVideoMimeType(t *testing.T) {
	tests := []struct{ mime string; want bool }{
		{"video/mp4", true},
		{"video/webm", true},
		{"image/png", false},
		{"text/plain", false},
	}
	for _, tt := range tests {
		if got := IsVideoMimeType(tt.mime); got != tt.want {
			t.Errorf("IsVideoMimeType(%q) = %v, want %v", tt.mime, got, tt.want)
		}
	}
}

func TestTruncateText(t *testing.T) {
	short := truncateText("hello", 10)
	if short != "hello" {
		t.Errorf("truncateText(short) = %q, want hello", short)
	}
	long := truncateText("this is a very long text that should be truncated", 10)
	if len(long) <= 10 {
		t.Errorf("truncateText(long) should be longer than 10 chars, got %d", len(long))
	}
}
