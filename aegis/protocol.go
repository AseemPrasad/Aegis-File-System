// Package aegis provides a Go client SDK for the Aegis content-addressed
// file storage system.
//
// Usage:
//
//	client := aegis.NewClient("https://aegis.example.com", tenantID, apiToken)
//	result, err := client.UploadFile(ctx, aegis.UploadOptions{
//	    FilePath:   "/path/to/file.pdf",
//	    TargetPath: "/Documents/report.pdf",
//	})
package aegis

// ---------------------------------------------------------------------------
// Protocol types — mirrors the server-side API contracts.
// ---------------------------------------------------------------------------

// InitiateRequest is the POST body for /api/v1/ingest/initiate.
type InitiateRequest struct {
	TenantID  string      `json:"tenant_id"`
	FileName  string      `json:"file_name"`
	ParentID  *string     `json:"parent_id,omitempty"`
	NodeID    *string     `json:"node_id,omitempty"`
	TotalSize int64       `json:"total_size_bytes"`
	Chunks    []ChunkInfo `json:"chunks"`
}

// ChunkInfo describes one content-defined chunk for dedup lookup.
type ChunkInfo struct {
	BlockHash string `json:"block_hash"` // lowercase hex SHA-256, 64 chars
	SizeBytes int64  `json:"size_bytes"`
}

// InitiateResponse is the response from /api/v1/ingest/initiate.
type InitiateResponse struct {
	SessionID  string      `json:"session_id"`
	NodeID     string      `json:"node_id"`
	ExpiresAt  string      `json:"expires_at"` // RFC 3339
	UploadURLs []UploadURL `json:"upload_urls"`
}

// UploadURL is a pre-signed URL for uploading one block.
type UploadURL struct {
	BlockHash string `json:"block_hash"`
	URL       string `json:"url"`
	SizeBytes int64  `json:"size_bytes"`
}

// CommitRequest is the POST body for /api/v1/ingest/commit.
type CommitRequest struct {
	SessionID     string      `json:"session_id"`
	ContentSHA256 string      `json:"content_sha256"` // hex, 64 chars
	MimeType      string      `json:"mime_type,omitempty"`
	Blocks        []BlockMeta `json:"blocks"`
}

// BlockMeta describes one block in the committed manifest.
type BlockMeta struct {
	BlockHash  string `json:"block_hash"`
	ChunkIndex int    `json:"chunk_index"`
	Offset     int64  `json:"offset_bytes"`
	SizeBytes  int64  `json:"size_bytes"`
	ETag       string `json:"etag,omitempty"`
}

// CommitResponse is the response from /api/v1/ingest/commit.
type CommitResponse struct {
	VersionID     string `json:"version_id"`
	VersionNumber int    `json:"version_number"`
}

// APIError is the error response body from the server.
type APIError struct {
	Error string `json:"error"`
}
