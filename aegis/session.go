package aegis

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/aegis-dev/aegis/internal/fastcdc"
)

// ---------------------------------------------------------------------------
// Session — pause/resume support via local JSON persistence
// ---------------------------------------------------------------------------

// SessionState captures the state of an upload so it can be resumed later.
type SessionState struct {
	SessionID   string          `json:"session_id"`
	FilePath    string          `json:"file_path"`
	NodeID      string          `json:"node_id"`
	TotalSize   int64           `json:"total_size"`
	MimeType    string          `json:"mime_type"`
	Chunks      []fastcdc.Chunk `json:"chunks"`
	Uploaded    []string        `json:"uploaded"`    // block hashes of already-uploaded chunks
	CreatedAt   time.Time       `json:"created_at"`
	ExpiresAt   time.Time       `json:"expires_at"`
}

// SaveSession writes the session state to a JSON file.
func SaveSession(state *SessionState, path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create session file: %w", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(state); err != nil {
		return fmt.Errorf("encode session: %w", err)
	}
	return nil
}

// LoadSession reads a session state from a JSON file.
func LoadSession(path string) (*SessionState, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open session file: %w", err)
	}
	defer f.Close()

	var state SessionState
	if err := json.NewDecoder(f).Decode(&state); err != nil {
		return nil, fmt.Errorf("decode session: %w", err)
	}
	return &state, nil
}

// ResumeUpload resumes an upload from a saved session. Only missing chunks
// (those not in state.Uploaded) are re-uploaded.
func (c *Client) ResumeUpload(ctx context.Context, state *SessionState, opts UploadOptions) (*UploadResult, error) {
	start := time.Now()

	// Open file for chunking.
	file, err := os.Open(state.FilePath)
	if err != nil {
		return nil, fmt.Errorf("aegis: open file: %w", err)
	}
	defer file.Close()

	// Re-chunk to get fresh hashes.
	chunker := fastcdc.New(file)
	chunks, err := chunker.ChunkAll()
	if err != nil {
		return nil, fmt.Errorf("aegis: re-chunk file: %w", err)
	}
	if len(chunks) == 0 {
		return nil, fmt.Errorf("aegis: file produced no chunks")
	}

	// Build set of already-uploaded hashes.
	uploadedSet := make(map[string]bool, len(state.Uploaded))
	for _, h := range state.Uploaded {
		uploadedSet[h] = true
	}

	// Identify missing chunks.
	var missing []fastcdc.Chunk
	for _, ch := range chunks {
		if !uploadedSet[ch.HashHex] {
			missing = append(missing, ch)
		}
	}

	if len(missing) == 0 {
		// All chunks uploaded — just commit.
		return c.commitSession(ctx, state, chunks, opts)
	}

	// Call /initiate with only missing chunks.
	missingInfos := make([]ChunkInfo, len(missing))
	for i, ch := range missing {
		missingInfos[i] = ChunkInfo{
			BlockHash: ch.HashHex,
			SizeBytes: int64(ch.Size),
		}
	}

	initResp, err := c.initiateUpload(ctx, InitiateRequest{
		TenantID:  c.tenantID,
		FileName:  filepath.Base(state.FilePath),
		NodeID:    &state.NodeID,
		TotalSize: state.TotalSize,
		Chunks:    missingInfos,
	})
	if err != nil {
		return nil, fmt.Errorf("aegis: initiate resume: %w", err)
	}

	// Build upload URL map.
	urlMap := make(map[string]UploadURL, len(initResp.UploadURLs))
	for _, u := range initResp.UploadURLs {
		urlMap[u.BlockHash] = u
	}

	// Upload missing chunks.
	file2, err := os.Open(state.FilePath)
	if err != nil {
		return nil, fmt.Errorf("aegis: reopen file: %w", err)
	}
	defer file2.Close()

	var bytesUploaded int64
	if len(initResp.UploadURLs) > 0 {
		uploaded, err := c.uploadChunksParallel(ctx, file2, missing, urlMap, opts, state.TotalSize)
		if err != nil {
			return nil, fmt.Errorf("aegis: upload missing chunks: %w", err)
		}
		bytesUploaded = uploaded
	}

	// Update uploaded list.
	allUploaded := make([]string, 0, len(chunks))
	for _, ch := range chunks {
		allUploaded = append(allUploaded, ch.HashHex)
	}

	// Save updated session.
	state.Uploaded = allUploaded
	state.SessionID = initResp.SessionID
	if opts.FilePath != "" {
		state.FilePath = opts.FilePath
	}

	// Commit.
	commitResp, err := c.commitUpload(ctx, CommitRequest{
		SessionID:     initResp.SessionID,
		ContentSHA256: "", // computed server-side if empty
		MimeType:      state.MimeType,
		Blocks:        buildBlockMetas(chunks),
	})
	if err != nil {
		return nil, fmt.Errorf("aegis: commit resume: %w", err)
	}

	return &UploadResult{
		VersionID:     commitResp.VersionID,
		VersionNumber: commitResp.VersionNumber,
		NodeID:        initResp.NodeID,
		SessionID:     initResp.SessionID,
		BytesUploaded: bytesUploaded,
		Duration:      time.Since(start),
	}, nil
}

// commitSession commits when all chunks are already uploaded.
func (c *Client) commitSession(ctx context.Context, state *SessionState, chunks []fastcdc.Chunk, opts UploadOptions) (*UploadResult, error) {
	start := time.Now()

	// Compute file hash.
	file, err := os.Open(state.FilePath)
	if err != nil {
		return nil, fmt.Errorf("aegis: open for hash: %w", err)
	}
	defer file.Close()

	fileHash, err := fastcdc.ComputeFileSHA256FromReader(file)
	if err != nil {
		return nil, fmt.Errorf("aegis: compute hash: %w", err)
	}

	commitResp, err := c.commitUpload(ctx, CommitRequest{
		SessionID:     state.SessionID,
		ContentSHA256: fileHash,
		MimeType:      state.MimeType,
		Blocks:        buildBlockMetas(chunks),
	})
	if err != nil {
		return nil, fmt.Errorf("aegis: commit: %w", err)
	}

	return &UploadResult{
		VersionID:     commitResp.VersionID,
		VersionNumber: commitResp.VersionNumber,
		NodeID:        state.NodeID,
		SessionID:     state.SessionID,
		Duration:      time.Since(start),
	}, nil
}

// buildBlockMetas converts chunks to BlockMeta slice.
func buildBlockMetas(chunks []fastcdc.Chunk) []BlockMeta {
	blocks := make([]BlockMeta, len(chunks))
	for i, ch := range chunks {
		blocks[i] = BlockMeta{
			BlockHash:  ch.HashHex,
			ChunkIndex: i,
			Offset:     ch.Offset,
			SizeBytes:  int64(ch.Size),
		}
	}
	return blocks
}


