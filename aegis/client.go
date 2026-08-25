package aegis

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aegis-dev/aegis/internal/fastcdc"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Client — the main SDK entry point
// ---------------------------------------------------------------------------

// Client is the Aegis SDK client for uploading files.
type Client struct {
	endpoint   string
	tenantID   string
	apiToken   string
	httpClient *http.Client
}

// Option configures the client.
type Option func(*Client)

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(c *http.Client) Option {
	return func(cl *Client) { cl.httpClient = c }
}

// WithTimeout sets the HTTP client timeout.
func WithTimeout(d time.Duration) Option {
	return func(cl *Client) { cl.httpClient.Timeout = d }
}

// NewClient creates a new Aegis SDK client.
func NewClient(endpoint, tenantID, apiToken string, opts ...Option) *Client {
	c := &Client{
		endpoint: strings.TrimRight(endpoint, "/"),
		tenantID: tenantID,
		apiToken: apiToken,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// ---------------------------------------------------------------------------
// UploadOptions / UploadResult
// ---------------------------------------------------------------------------

// UploadOptions configures a file upload.
type UploadOptions struct {
	FilePath   string  // Local file path
	TargetPath string  // Server-side path (e.g., "/Documents/report.pdf")
	ParentID   *string // Parent directory node ID (nil = root)
	NodeID     *string // Existing node ID (nil = create new)
	MimeType   string  // Override MIME type (auto-detected if empty)

	ProgressCallback func(UploadProgress)
	OnChunkComplete  func(ChunkInfo)
}

// UploadProgress reports upload progress.
type UploadProgress struct {
	BytesUploaded          int64
	TotalBytes             int64
	PercentComplete        float64
	EstimatedTimeRemaining time.Duration
	CurrentBitrate         float64 // bytes per second
}

// UploadResult is the successful outcome of an upload.
type UploadResult struct {
	VersionID     string
	VersionNumber int
	NodeID        string
	SessionID     string
	BytesUploaded int64
	Deduplicated  int64 // bytes not re-uploaded (CAS hit)
	Duration      time.Duration
}

// ---------------------------------------------------------------------------
// UploadFile — the high-level upload API
// ---------------------------------------------------------------------------

// UploadFile uploads a local file to Aegis. It handles chunking, deduplication,
// parallel upload, and commit automatically.
func (c *Client) UploadFile(ctx context.Context, opts UploadOptions) (*UploadResult, error) {
	start := time.Now()

	// 1. Open file.
	file, err := os.Open(opts.FilePath)
	if err != nil {
		return nil, fmt.Errorf("aegis: open file: %w", err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("aegis: stat file: %w", err)
	}
	totalSize := stat.Size()

	// 2. Detect MIME type.
	mimeType := opts.MimeType
	if mimeType == "" {
		mimeType = detectMIMEType(opts.FilePath)
	}

	// 3. Chunk the file with FastCDC (consumes the reader).
	chunker := fastcdc.New(file)
	chunks, err := chunker.ChunkAll()
	if err != nil {
		return nil, fmt.Errorf("aegis: chunk file: %w", err)
	}
	if len(chunks) == 0 {
		return nil, fmt.Errorf("aegis: file produced no chunks")
	}

	// Convert to protocol ChunkInfo.
	chunkInfos := make([]ChunkInfo, len(chunks))
	for i, ch := range chunks {
		chunkInfos[i] = ChunkInfo{
			BlockHash: ch.HashHex,
			SizeBytes: int64(ch.Size),
		}
	}

	// 4. Call /initiate.
	fileName := filepath.Base(opts.FilePath)
	initResp, err := c.initiateUpload(ctx, InitiateRequest{
		TenantID:  c.tenantID,
		FileName:  fileName,
		ParentID:  opts.ParentID,
		NodeID:    opts.NodeID,
		TotalSize: totalSize,
		Chunks:    chunkInfos,
	})
	if err != nil {
		return nil, fmt.Errorf("aegis: initiate: %w", err)
	}

	// 5. Build upload URL lookup.
	urlMap := make(map[string]UploadURL, len(initResp.UploadURLs))
	for _, u := range initResp.UploadURLs {
		urlMap[u.BlockHash] = u
	}

	// 6. Compute bytes stats.
	var bytesUploaded int64
	var deduplicated int64
	for _, ch := range chunks {
		if _, ok := urlMap[ch.HashHex]; ok {
			bytesUploaded += int64(ch.Size)
		} else {
			deduplicated += int64(ch.Size)
		}
	}

	// 7. Upload missing chunks in parallel.
	// Re-open file for reading chunk data (chunker consumed the original reader).
	if len(initResp.UploadURLs) > 0 {
		file2, err := os.Open(opts.FilePath)
		if err != nil {
			return nil, fmt.Errorf("aegis: reopen file for upload: %w", err)
		}
		defer file2.Close()

		uploaded, err := c.uploadChunksParallel(ctx, file2, chunks, urlMap, opts, totalSize)
		if err != nil {
			return nil, fmt.Errorf("aegis: upload chunks: %w", err)
		}
		bytesUploaded = uploaded
	} else {
		deduplicated = totalSize
		bytesUploaded = 0
	}

	// 8. Compute full-file SHA-256.
	file3, err := os.Open(opts.FilePath)
	if err != nil {
		return nil, fmt.Errorf("aegis: reopen file for hash: %w", err)
	}
	defer file3.Close()

	fileHash, err := fastcdc.ComputeFileSHA256FromReader(file3)
	if err != nil {
		return nil, fmt.Errorf("aegis: compute file hash: %w", err)
	}

	// 9. Build BlockMeta for commit.
	blocks := make([]BlockMeta, len(chunks))
	for i, ch := range chunks {
		blocks[i] = BlockMeta{
			BlockHash:  ch.HashHex,
			ChunkIndex: i,
			Offset:     ch.Offset,
			SizeBytes:  int64(ch.Size),
		}
	}

	// 10. Call /commit.
	commitResp, err := c.commitUpload(ctx, CommitRequest{
		SessionID:     initResp.SessionID,
		ContentSHA256: fileHash,
		MimeType:      mimeType,
		Blocks:        blocks,
	})
	if err != nil {
		return nil, fmt.Errorf("aegis: commit: %w", err)
	}

	return &UploadResult{
		VersionID:     commitResp.VersionID,
		VersionNumber: commitResp.VersionNumber,
		NodeID:        initResp.NodeID,
		SessionID:     initResp.SessionID,
		BytesUploaded: bytesUploaded,
		Deduplicated:  deduplicated,
		Duration:      time.Since(start),
	}, nil
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

func (c *Client) initiateUpload(ctx context.Context, req InitiateRequest) (*InitiateResponse, error) {
	var resp InitiateResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/ingest/initiate", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) commitUpload(ctx context.Context, req CommitRequest) (*CommitResponse, error) {
	var resp CommitResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/ingest/commit", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, reqBody, respBody interface{}) error {
	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiToken)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var apiErr APIError
		if err := json.NewDecoder(resp.Body).Decode(&apiErr); err != nil {
			return fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, apiErr.Error)
	}

	if respBody != nil {
		if err := json.NewDecoder(resp.Body).Decode(respBody); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// MIME type detection
// ---------------------------------------------------------------------------

func detectMIMEType(filePath string) string {
	ext := filepath.Ext(filePath)
	if ext != "" {
		mimeType := mime.TypeByExtension(ext)
		if mimeType != "" {
			return mimeType
		}
	}
	return "application/octet-stream"
}

// Ensure uuid import is used.
var _ = uuid.New
