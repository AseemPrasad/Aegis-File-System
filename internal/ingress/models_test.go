package ingress

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var errUnknown = errors.New("some unknown error")

// ---------------------------------------------------------------------------
// HTTPStatus — error → HTTP code mapping
// ---------------------------------------------------------------------------

func TestHTTPStatus(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{nil, 200},
		{ErrUnauthorized, 401},
		{ErrQuotaExceeded, 402},
		{ErrSessionTenantMismatch, 403},
		{ErrTenantNotFound, 404},
		{ErrSessionNotFound, 404},
		{ErrNodeNotFound, 404},
		{ErrBadRequest, 400},
		{ErrSessionCompleted, 409},
		{ErrSessionExpired, 410},
		{ErrNodeNotFile, 400},
		{ErrDBUnavailable, 503},
		{ErrSerialization, 503},
		{errUnknown, 500},
	}
	for _, tt := range tests {
		got := HTTPStatus(tt.err)
		if got != tt.want {
			t.Errorf("HTTPStatus(%v) = %d, want %d", tt.err, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// InitiateRequest.Validate
// ---------------------------------------------------------------------------

func validInitiate() InitiateRequest {
	return InitiateRequest{
		TenantID:  "tenant-1",
		FileName:  "test.txt",
		TotalSize: 1024,
		Chunks: []ChunkInfo{
			{BlockHash: strings.Repeat("a", 64), SizeBytes: 1024},
		},
	}
}

func TestInitiateRequest_ValidateValid(t *testing.T) {
	req := validInitiate()
	if err := req.Validate(); err != nil {
		t.Errorf("expected valid, got: %v", err)
	}
}

func TestInitiateRequest_MissingTenantID(t *testing.T) {
	req := validInitiate()
	req.TenantID = ""
	if err := req.Validate(); err == nil {
		t.Error("expected error for missing tenant_id")
	}
}

func TestInitiateRequest_MissingFileName(t *testing.T) {
	req := validInitiate()
	req.FileName = ""
	if err := req.Validate(); err == nil {
		t.Error("expected error for missing file_name")
	}
}

func TestInitiateRequest_ZeroSize(t *testing.T) {
	req := validInitiate()
	req.TotalSize = 0
	if err := req.Validate(); err == nil {
		t.Error("expected error for zero total_size")
	}
}

func TestInitiateRequest_NegativeSize(t *testing.T) {
	req := validInitiate()
	req.TotalSize = -1
	if err := req.Validate(); err == nil {
		t.Error("expected error for negative total_size")
	}
}

func TestInitiateRequest_EmptyChunks(t *testing.T) {
	req := validInitiate()
	req.Chunks = nil
	if err := req.Validate(); err == nil {
		t.Error("expected error for empty chunks")
	}
}

func TestInitiateRequest_BadBlockHash(t *testing.T) {
	req := validInitiate()
	req.Chunks[0].BlockHash = "short"
	if err := req.Validate(); err == nil {
		t.Error("expected error for short block hash")
	}
}

func TestInitiateRequest_NonHexBlockHash(t *testing.T) {
	req := validInitiate()
	req.Chunks[0].BlockHash = strings.Repeat("g", 64) // 'g' is not hex
	if err := req.Validate(); err == nil {
		t.Error("expected error for non-hex block hash")
	}
}

func TestInitiateRequest_ZeroChunkSize(t *testing.T) {
	req := validInitiate()
	req.Chunks[0].SizeBytes = 0
	if err := req.Validate(); err == nil {
		t.Error("expected error for zero chunk size")
	}
}

func TestInitiateRequest_NegativeChunkSize(t *testing.T) {
	req := validInitiate()
	req.Chunks[0].SizeBytes = -1
	if err := req.Validate(); err == nil {
		t.Error("expected error for negative chunk size")
	}
}

func TestInitiateRequest_MultipleChunks(t *testing.T) {
	req := validInitiate()
	req.Chunks = []ChunkInfo{
		{BlockHash: strings.Repeat("a", 64), SizeBytes: 512},
		{BlockHash: strings.Repeat("b", 64), SizeBytes: 512},
	}
	if err := req.Validate(); err != nil {
		t.Errorf("multiple valid chunks should pass: %v", err)
	}
}

func TestInitiateRequest_SecondChunkBadHash(t *testing.T) {
	req := validInitiate()
	req.Chunks = []ChunkInfo{
		{BlockHash: strings.Repeat("a", 64), SizeBytes: 512},
		{BlockHash: "bad", SizeBytes: 512},
	}
	if err := req.Validate(); err == nil {
		t.Error("expected error for bad hash in second chunk")
	}
}

// ---------------------------------------------------------------------------
// CommitRequest.Validate
// ---------------------------------------------------------------------------

func validCommit() CommitRequest {
	return CommitRequest{
		SessionID:     "sess-123",
		ContentSHA256: strings.Repeat("a", 64),
		Blocks: []BlockMeta{
			{
				BlockHash:  strings.Repeat("b", 64),
				ChunkIndex: 0,
				Offset:     0,
				SizeBytes:  1024,
			},
		},
	}
}

func TestCommitRequest_ValidateValid(t *testing.T) {
	req := validCommit()
	if err := req.Validate(); err != nil {
		t.Errorf("expected valid, got: %v", err)
	}
}

func TestCommitRequest_MissingSessionID(t *testing.T) {
	req := validCommit()
	req.SessionID = ""
	if err := req.Validate(); err == nil {
		t.Error("expected error for missing session_id")
	}
}

func TestCommitRequest_BadContentHash(t *testing.T) {
	req := validCommit()
	req.ContentSHA256 = "short"
	if err := req.Validate(); err == nil {
		t.Error("expected error for short content hash")
	}
}

func TestCommitRequest_BadBlockHash(t *testing.T) {
	req := validCommit()
	req.Blocks[0].BlockHash = "bad"
	if err := req.Validate(); err == nil {
		t.Error("expected error for bad block hash")
	}
}

func TestCommitRequest_ZeroBlockSize(t *testing.T) {
	req := validCommit()
	req.Blocks[0].SizeBytes = 0
	if err := req.Validate(); err == nil {
		t.Error("expected error for zero block size")
	}
}

// ---------------------------------------------------------------------------
// MustDecodeHash
// ---------------------------------------------------------------------------

func TestMustDecodeHash_Valid(t *testing.T) {
	h := strings.Repeat("ab", 32) // 64 hex chars
	b := MustDecodeHash(h)
	if len(b) != 32 {
		t.Errorf("length: got %d, want 32", len(b))
	}
}

func TestMustDecodeHash_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for invalid hex")
		}
	}()
	MustDecodeHash("not-hex!")
}

// ---------------------------------------------------------------------------
// Config defaults
// ---------------------------------------------------------------------------

func TestConfig_ApplyDefaults(t *testing.T) {
	cfg := Config{}
	cfg.applyDefaults()
	if cfg.MaxBodySize != 100<<20 {
		t.Errorf("default MaxBodySize: got %d, want %d", cfg.MaxBodySize, 100<<20)
	}
	if cfg.GracePeriod != 10*time.Second {
		t.Errorf("default GracePeriod: got %v, want 10s", cfg.GracePeriod)
	}
}
