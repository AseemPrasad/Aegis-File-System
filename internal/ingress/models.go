// Package ingress implements the stateless ingestion engine for Aegis:
// HandleInitiate (CAS dedup, pre-signed URLs, quota) and HandleCommit
// (atomic version + manifest + ref-count via trigger, CDC publish).
//
// PROMPT 4.1 scope.
package ingress

import (
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// ---------------------------------------------------------------------------
// Error sentinels — handlers map these to HTTP status codes.
// ---------------------------------------------------------------------------

var (
	// ErrUnauthorized is returned when the bearer token is missing or invalid.
	ErrUnauthorized = errors.New("ingress: unauthorized")
	// ErrBadRequest is returned for malformed or incomplete request payloads.
	ErrBadRequest = errors.New("ingress: bad request")
	// ErrTenantNotFound is returned when the tenant_id does not exist.
	ErrTenantNotFound = errors.New("ingress: tenant not found")
	// ErrQuotaExceeded is returned when used + requested exceeds quota.
	ErrQuotaExceeded = errors.New("ingress: storage quota exceeded")
	// ErrSessionNotFound is returned when the session_id is unknown.
	ErrSessionNotFound = errors.New("ingress: upload session not found")
	// ErrSessionExpired is returned when the session has passed its expiry.
	ErrSessionExpired = errors.New("ingress: upload session expired")
	// ErrSessionCompleted is returned on duplicate commit attempts.
	ErrSessionCompleted = errors.New("ingress: upload session already completed")
	// ErrSessionTenantMismatch is returned when tenant doesn't own the session.
	ErrSessionTenantMismatch = errors.New("ingress: session tenant mismatch")
	// ErrNodeNotFound is returned when the referenced file node doesn't exist.
	ErrNodeNotFound = errors.New("ingress: file node not found")
	// ErrNodeNotFile is returned when a non-FILE node is referenced.
	ErrNodeNotFile = errors.New("ingress: node is not a FILE")
	// ErrDBUnavailable is returned when the circuit breaker is open or pool saturated.
	ErrDBUnavailable = errors.New("ingress: database unavailable")
	// ErrSerialization is returned on a serialization failure (retryable).
	ErrSerialization = errors.New("ingress: serialization failure")
)

// HTTPStatus maps an error to the appropriate HTTP status code.
func HTTPStatus(err error) int {
	switch {
	case err == nil:
		return 200
	case errors.Is(err, ErrUnauthorized):
		return 401
	case errors.Is(err, ErrQuotaExceeded):
		return 402
	case errors.Is(err, ErrSessionTenantMismatch):
		return 403
	case errors.Is(err, ErrTenantNotFound), errors.Is(err, ErrSessionNotFound),
		errors.Is(err, ErrNodeNotFound):
		return 404
	case errors.Is(err, ErrBadRequest):
		return 400
	case errors.Is(err, ErrSessionCompleted):
		return 409
	case errors.Is(err, ErrSessionExpired):
		return 410
	case errors.Is(err, ErrNodeNotFile):
		return 400
	case errors.Is(err, ErrDBUnavailable), errors.Is(err, ErrSerialization):
		return 503
	default:
		return 500
	}
}

// ---------------------------------------------------------------------------
// Request / response types
// ---------------------------------------------------------------------------

// InitiateRequest is the JSON body for POST /api/v1/ingest/initiate.
type InitiateRequest struct {
	TenantID  string      `json:"tenant_id"`
	FileName  string      `json:"file_name"`
	ParentID  *string     `json:"parent_id,omitempty"` // nil = root
	NodeID    *string     `json:"node_id,omitempty"`   // nil = create new
	TotalSize int64       `json:"total_size_bytes"`
	Chunks    []ChunkInfo `json:"chunks"`
}

// ChunkInfo describes one content-defined chunk for dedup lookup.
type ChunkInfo struct {
	BlockHash string `json:"block_hash"` // lowercase hex SHA-256, 64 chars
	SizeBytes int64  `json:"size_bytes"`
}

// Validate checks structural invariants. Does not touch the database.
func (r *InitiateRequest) Validate() error {
	if r.TenantID == "" {
		return fmt.Errorf("%w: tenant_id required", ErrBadRequest)
	}
	if r.FileName == "" {
		return fmt.Errorf("%w: file_name required", ErrBadRequest)
	}
	if r.TotalSize <= 0 {
		return fmt.Errorf("%w: total_size_bytes must be positive", ErrBadRequest)
	}
	if len(r.Chunks) == 0 {
		return fmt.Errorf("%w: chunks array must not be empty", ErrBadRequest)
	}
	for i, c := range r.Chunks {
		if len(c.BlockHash) != 64 {
			return fmt.Errorf("%w: chunk[%d].block_hash must be 64 hex chars", ErrBadRequest, i)
		}
		if c.SizeBytes <= 0 {
			return fmt.Errorf("%w: chunk[%d].size_bytes must be positive", ErrBadRequest, i)
		}
	}
	return nil
}

// InitiateResponse is the JSON body returned by HandleInitiate.
type InitiateResponse struct {
	SessionID  string      `json:"session_id"`
	NodeID     string      `json:"node_id"`
	ExpiresAt  string      `json:"expires_at"` // RFC 3339
	UploadURLs []UploadURL `json:"upload_urls"`
}

// UploadURL is one pre-signed URL for a block that must be uploaded.
type UploadURL struct {
	BlockHash string `json:"block_hash"`
	URL       string `json:"url"`
	SizeBytes int64  `json:"size_bytes"`
}

// CommitRequest is the JSON body for POST /api/v1/ingest/commit.
type CommitRequest struct {
	SessionID     string      `json:"session_id"`
	ContentSHA256 string      `json:"content_sha256"` // hex, 64 chars
	MimeType      string      `json:"mime_type,omitempty"` // optional; defaults to "application/octet-stream"
	Blocks        []BlockMeta `json:"blocks"`
}

// BlockMeta describes one block in the committed manifest.
type BlockMeta struct {
	BlockHash string `json:"block_hash"` // hex SHA-256
	ChunkIndex int    `json:"chunk_index"`
	Offset     int64  `json:"offset_bytes"`
	SizeBytes  int64  `json:"size_bytes"`
	ETag       string `json:"etag,omitempty"` // MD5 ETag from object store (for verification)
}

// Validate checks structural invariants.
func (r *CommitRequest) Validate() error {
	if r.SessionID == "" {
		return fmt.Errorf("%w: session_id required", ErrBadRequest)
	}
	if len(r.ContentSHA256) != 64 {
		return fmt.Errorf("%w: content_sha256 must be 64 hex chars", ErrBadRequest)
	}
	for i, b := range r.Blocks {
		if len(b.BlockHash) != 64 {
			return fmt.Errorf("%w: block[%d].block_hash must be 64 hex chars", ErrBadRequest, i)
		}
		if b.SizeBytes <= 0 {
			return fmt.Errorf("%w: block[%d].size_bytes must be positive", ErrBadRequest, i)
		}
	}
	return nil
}

// CommitResponse is the JSON body returned by HandleCommit.
type CommitResponse struct {
	VersionID     string `json:"version_id"`
	VersionNumber int    `json:"version_number"`
}

// ---------------------------------------------------------------------------
// Internal types shared between store.go and handlers
// ---------------------------------------------------------------------------

// TenantQuota holds quota information for a tenant.
type TenantQuota struct {
	StorageQuotaBytes int64
	UsedBytes         int64
}

// SessionRecord is the row data from an upload session.
type SessionRecord struct {
	SessionID       string
	TenantID        string
	NodeID          string
	Status          string
	TotalSize       int64
	ExpectedChunks  int
	ExpiresAt       time.Time
}

// MustDecodeHash is a helper that decodes a hex string to bytes, panicking
// on invalid input (called only with caller-validated hex).
func MustDecodeHash(h string) []byte {
	b, err := hex.DecodeString(h)
	if err != nil {
		panic("ingress: invalid hex hash: " + err.Error())
	}
	return b
}
