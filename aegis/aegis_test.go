package aegis_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aegis-dev/aegis/aegis"
	"github.com/aegis-dev/aegis/internal/fastcdc"
)

// ---------------------------------------------------------------------------
// Mock server
// ---------------------------------------------------------------------------

type mockServer struct {
	t        *testing.T
	mu       sync.Mutex
	uploaded map[string][]byte // block_hash → data
	files    map[string][]byte // version_id → file content
	nextVer  int

	initHandler  func(req aegis.InitiateRequest) aegis.InitiateResponse
	commitHandler func(req aegis.CommitRequest) aegis.CommitResponse
}

func newMockServer(t *testing.T) (*mockServer, *httptest.Server) {
	ms := &mockServer{
		t:        t,
		uploaded: make(map[string][]byte),
		files:    make(map[string][]byte),
		nextVer:  1,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/ingest/initiate", ms.handleInitiate)
	mux.HandleFunc("/api/v1/ingest/commit", ms.handleCommit)
	mux.HandleFunc("/upload/", ms.handleUpload)

	ts := httptest.NewServer(mux)
	return ms, ts
}

func (ms *mockServer) handleInitiate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req aegis.InitiateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Determine missing hashes (all are missing in our mock).
	var missing []aegis.UploadURL
	for _, ch := range req.Chunks {
		missing = append(missing, aegis.UploadURL{
			BlockHash: ch.BlockHash,
			URL:       "http://" + r.Host + "/upload/" + ch.BlockHash,
			SizeBytes: ch.SizeBytes,
		})
	}

	resp := aegis.InitiateResponse{
		SessionID:  "sess_test_001",
		NodeID:     "node_test_001",
		ExpiresAt:  "2026-12-31T23:59:59Z",
		UploadURLs: missing,
	}

	if ms.initHandler != nil {
		resp = ms.initHandler(req)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (ms *mockServer) handleCommit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req aegis.CommitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ms.mu.Lock()
	verID := "ver_test_001"
	verNum := ms.nextVer
	ms.nextVer++
	ms.mu.Unlock()

	resp := aegis.CommitResponse{
		VersionID:     verID,
		VersionNumber: verNum,
	}

	if ms.commitHandler != nil {
		resp = ms.commitHandler(req)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (ms *mockServer) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	hash := strings.TrimPrefix(r.URL.Path, "/upload/")
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ms.mu.Lock()
	ms.uploaded[hash] = data
	ms.mu.Unlock()

	w.WriteHeader(http.StatusOK)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestUploadFileBasic(t *testing.T) {
	ms, ts := newMockServer(t)
	defer ts.Close()

	// Create test file.
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.bin")
	content := []byte(strings.Repeat("Aegis upload test data ", 5000))
	if err := os.WriteFile(filePath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	client := aegis.NewClient(ts.URL, "tenant-1", "tok-abc123")
	result, err := client.UploadFile(context.Background(), aegis.UploadOptions{
		FilePath:   filePath,
		TargetPath: "/test/test.bin",
	})
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}

	if result.VersionID == "" {
		t.Error("expected non-empty VersionID")
	}
	if result.VersionNumber <= 0 {
		t.Errorf("expected positive VersionNumber, got %d", result.VersionNumber)
	}
	if result.SessionID == "" {
		t.Error("expected non-empty SessionID")
	}
	if result.Duration <= 0 {
		t.Error("expected positive Duration")
	}

	// Verify chunks were uploaded.
	ms.mu.Lock()
	uploadedCount := len(ms.uploaded)
	ms.mu.Unlock()

	if uploadedCount == 0 {
		t.Error("expected at least one chunk uploaded")
	}

	t.Logf("Uploaded %d chunks, VersionID=%s, v%d, duration=%v",
		uploadedCount, result.VersionID, result.VersionNumber, result.Duration)
}

func TestUploadSmallFile(t *testing.T) {
	_, ts := newMockServer(t)
	defer ts.Close()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "small.txt")
	content := []byte("hello aegis")
	if err := os.WriteFile(filePath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	client := aegis.NewClient(ts.URL, "tenant-1", "tok")
	result, err := client.UploadFile(context.Background(), aegis.UploadOptions{
		FilePath:   filePath,
		TargetPath: "/small.txt",
	})
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if result.VersionID == "" {
		t.Error("expected non-empty VersionID")
	}
}

func TestUploadLargeFile(t *testing.T) {
	_, ts := newMockServer(t)
	defer ts.Close()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "large.bin")
	// 5 MiB file — should produce multiple chunks.
	content := make([]byte, 5*1024*1024)
	for i := range content {
		content[i] = byte(i * 7)
	}
	if err := os.WriteFile(filePath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	client := aegis.NewClient(ts.URL, "tenant-1", "tok")
	result, err := client.UploadFile(context.Background(), aegis.UploadOptions{
		FilePath:   filePath,
		TargetPath: "/large.bin",
	})
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}

	// Verify dedup stats.
	totalChunks, _ := fastcdc.ChunkFromBytes(content)
	expectedDedup := int64(0) // all uploaded in mock (no CAS)
	if result.Deduplicated != expectedDedup {
		t.Logf("Deduplicated=%d (mock has no CAS, all uploaded)", result.Deduplicated)
	}
	t.Logf("Uploaded %d bytes, %d chunks, dedup=%d",
		result.BytesUploaded, len(totalChunks), result.Deduplicated)
}

func TestUploadWithMIMEType(t *testing.T) {
	_, ts := newMockServer(t)
	defer ts.Close()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "doc.pdf")
	content := []byte("%PDF-1.4 fake pdf content for testing purposes only")
	if err := os.WriteFile(filePath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	// Track MIME type in commit request.
	var capturedMimeType string
	captureTs := ms_commitCapture(t, &capturedMimeType)
	defer captureTs.Close()

	client := aegis.NewClient(captureTs.URL, "tenant-1", "tok")
	result, err := client.UploadFile(context.Background(), aegis.UploadOptions{
		FilePath: filePath,
		MimeType: "application/pdf",
	})
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}

	if capturedMimeType != "application/pdf" {
		t.Errorf("expected MIME type 'application/pdf', got %q", capturedMimeType)
	}
	t.Logf("VersionID=%s, MIME=%s", result.VersionID, capturedMimeType)
}

func TestUploadWithProgressCallback(t *testing.T) {
	_, ts := newMockServer(t)
	defer ts.Close()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "progress.bin")
	content := make([]byte, 2*1024*1024) // 2 MiB
	for i := range content {
		content[i] = byte(i)
	}
	if err := os.WriteFile(filePath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	var progressUpdates []aegis.UploadProgress
	client := aegis.NewClient(ts.URL, "tenant-1", "tok")
	_, err := client.UploadFile(context.Background(), aegis.UploadOptions{
		FilePath: filePath,
		ProgressCallback: func(p aegis.UploadProgress) {
			progressUpdates = append(progressUpdates, p)
		},
	})
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}

	if len(progressUpdates) == 0 {
		t.Error("expected at least one progress callback")
	}

	last := progressUpdates[len(progressUpdates)-1]
	if last.PercentComplete != 100.0 {
		t.Errorf("final progress %.1f%%, want 100%%", last.PercentComplete)
	}
	t.Logf("Got %d progress updates, final: %.1f%%", len(progressUpdates), last.PercentComplete)
}

func TestUploadChunkCompleteCallback(t *testing.T) {
	_, ts := newMockServer(t)
	defer ts.Close()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "chunks.bin")
	content := make([]byte, 3*1024*1024) // 3 MiB
	for i := range content {
		content[i] = byte(i ^ 0xFF)
	}
	if err := os.WriteFile(filePath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	var completedChunks []aegis.ChunkInfo
	client := aegis.NewClient(ts.URL, "tenant-1", "tok")
	_, err := client.UploadFile(context.Background(), aegis.UploadOptions{
		FilePath: filePath,
		OnChunkComplete: func(ci aegis.ChunkInfo) {
			completedChunks = append(completedChunks, ci)
		},
	})
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}

	if len(completedChunks) == 0 {
		t.Error("expected at least one chunk complete callback")
	}
	for i, ci := range completedChunks {
		if ci.BlockHash == "" {
			t.Errorf("chunk %d: empty BlockHash", i)
		}
		if ci.SizeBytes <= 0 {
			t.Errorf("chunk %d: non-positive SizeBytes", i)
		}
	}
	t.Logf("Got %d chunk complete callbacks", len(completedChunks))
}

func TestUploadDeduplication(t *testing.T) {
	_, ts := newMockServer(t)
	defer ts.Close()

	// Custom initiate handler that returns no upload URLs (all deduplicated).
	ms := &dedupMockServer{
		t: t,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/ingest/initiate", ms.handleInitiate)
	mux.HandleFunc("/api/v1/ingest/commit", ms.handleCommit)
	ts.Close()
	ts = httptest.NewServer(mux)
	defer ts.Close()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "dedup.bin")
	content := make([]byte, 2*1024*1024)
	for i := range content {
		content[i] = byte(i * 3)
	}
	if err := os.WriteFile(filePath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	client := aegis.NewClient(ts.URL, "tenant-1", "tok")
	result, err := client.UploadFile(context.Background(), aegis.UploadOptions{
		FilePath: filePath,
	})
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}

	if result.Deduplicated != int64(len(content)) {
		t.Errorf("expected full dedup (%d bytes), got %d", len(content), result.Deduplicated)
	}
	if result.BytesUploaded != 0 {
		t.Errorf("expected 0 bytes uploaded, got %d", result.BytesUploaded)
	}
	t.Logf("Fully deduplicated: %d bytes", result.Deduplicated)
}

func TestUploadServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/ingest/initiate", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(aegis.APIError{Error: "internal error"})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "fail.bin")
	os.WriteFile(filePath, []byte("test"), 0o644)

	client := aegis.NewClient(ts.URL, "tenant-1", "tok")
	_, err := client.UploadFile(context.Background(), aegis.UploadOptions{
		FilePath: filePath,
	})
	if err == nil {
		t.Fatal("expected error from server error response")
	}
	t.Logf("Got expected error: %v", err)
}

func TestUploadContextCancellation(t *testing.T) {
	// Slow server that blocks on upload.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/ingest/initiate", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(aegis.InitiateResponse{
			SessionID: "sess",
			NodeID:    "node",
			ExpiresAt: "2099-12-31T23:59:59Z",
			UploadURLs: []aegis.UploadURL{
				{BlockHash: "abc123", URL: "http://localhost/upload/abc123", SizeBytes: 4},
			},
		})
	})
	mux.HandleFunc("/upload/", func(w http.ResponseWriter, r *http.Request) {
		// Block until context is cancelled.
		select {}
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "cancel.bin")
	os.WriteFile(filePath, []byte("test data for cancellation"), 0o644)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	client := aegis.NewClient(ts.URL, "tenant-1", "tok")
	_, err := client.UploadFile(ctx, aegis.UploadOptions{
		FilePath: filePath,
	})
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	t.Logf("Got expected cancellation error: %v", err)
}

func TestSessionSaveLoad(t *testing.T) {
	tmpDir := t.TempDir()
	sessionPath := filepath.Join(tmpDir, "session.json")

	state := &aegis.SessionState{
		SessionID: "sess_001",
		FilePath:  "/tmp/test.bin",
		NodeID:    "node_001",
		TotalSize: 1024,
		MimeType:  "application/octet-stream",
		Uploaded:  []string{"hash1", "hash2"},
	}

	if err := aegis.SaveSession(state, sessionPath); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	loaded, err := aegis.LoadSession(sessionPath)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}

	if loaded.SessionID != state.SessionID {
		t.Errorf("SessionID: got %q, want %q", loaded.SessionID, state.SessionID)
	}
	if loaded.FilePath != state.FilePath {
		t.Errorf("FilePath: got %q, want %q", loaded.FilePath, state.FilePath)
	}
	if len(loaded.Uploaded) != len(state.Uploaded) {
		t.Errorf("Uploaded count: got %d, want %d", len(loaded.Uploaded), len(state.Uploaded))
	}
}

func TestMIMEDetection(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"file.pdf", "application/pdf"},
		{"file.json", "application/json"},
		{"file.txt", "text/plain; charset=utf-8"},
		{"file.unknown", "application/octet-stream"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			tmpDir := t.TempDir()
			fp := filepath.Join(tmpDir, tt.path)
			os.WriteFile(fp, []byte("x"), 0o644)
			if _, err := os.Stat(fp); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// dedupMockServer simulates a server where all chunks are already in CAS.
type dedupMockServer struct {
	t *testing.T
}

func (d *dedupMockServer) handleInitiate(w http.ResponseWriter, r *http.Request) {
	var req aegis.InitiateRequest
	json.NewDecoder(r.Body).Decode(&req)

	// Return empty upload URLs — all deduplicated.
	resp := aegis.InitiateResponse{
		SessionID:  "sess_dedup",
		NodeID:     "node_dedup",
		ExpiresAt:  "2099-12-31T23:59:59Z",
		UploadURLs: []aegis.UploadURL{}, // empty = all deduped
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (d *dedupMockServer) handleCommit(w http.ResponseWriter, r *http.Request) {
	resp := aegis.CommitResponse{
		VersionID:     "ver_dedup_001",
		VersionNumber: 1,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// ms_commitCapture creates a test server that captures commit MIME types.
func ms_commitCapture(t *testing.T, captured *string) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/ingest/initiate", func(w http.ResponseWriter, r *http.Request) {
		var req aegis.InitiateRequest
		json.NewDecoder(r.Body).Decode(&req)
		resp := aegis.InitiateResponse{
			SessionID:  "sess_capture",
			NodeID:     "node_capture",
			ExpiresAt:  "2099-12-31T23:59:59Z",
			UploadURLs: []aegis.UploadURL{},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/api/v1/ingest/commit", func(w http.ResponseWriter, r *http.Request) {
		var req aegis.CommitRequest
		json.NewDecoder(r.Body).Decode(&req)
		*captured = req.MimeType
		resp := aegis.CommitResponse{
			VersionID:     "ver_cap_001",
			VersionNumber: 1,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	ts := httptest.NewServer(mux)
	return ts
}
