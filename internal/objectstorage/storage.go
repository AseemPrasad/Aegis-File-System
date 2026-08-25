// Package objectstorage provides the storage abstraction layer for Aegis
// binary data. Clients stream directly to S3/MinIO/R2 via pre-signed URLs
// without touching the ingestion engine, enabling 1GB/s+ aggregate throughput.
//
// Architecture:
//
//	IngressServer → ObjectStorageClient.GenerateUploadURL → client PUTs to S3
//	IngressServer → ObjectStorageClient.VerifyBlock       → ETag match check
//	GC Worker     → ObjectStorageClient.GetBlockMetadata  → existence proof
//	GC Worker     → ObjectStorageClient.DeleteBlock       → sweep unreferenced
//
// PROMPT 4.2 scope.
package objectstorage

import (
	"context"
	"errors"
	"io"
	"time"
)

// ---------------------------------------------------------------------------
// Error sentinels
// ---------------------------------------------------------------------------

var (
	// ErrBlockNotFound is returned when a block does not exist in object storage.
	ErrBlockNotFound = errors.New("objectstorage: block not found")
	// ErrETagMismatch is returned when the stored ETag does not match expected.
	ErrETagMismatch = errors.New("objectstorage: ETag mismatch")
	// ErrUploadFailed is returned when pre-signed URL generation fails.
	ErrUploadFailed = errors.New("objectstorage: upload URL generation failed")
	// ErrDeleteFailed is returned when block deletion fails.
	ErrDeleteFailed = errors.New("objectstorage: block deletion failed")
	// ErrStorageUnavailable is returned when the backend is unreachable.
	ErrStorageUnavailable = errors.New("objectstorage: storage unavailable")
)

// ---------------------------------------------------------------------------
// Core interface
// ---------------------------------------------------------------------------

// StorageTier classifies blocks for lifecycle management.
type StorageTier string

const (
	TierHot  StorageTier = "HOT"  // < 7 days, S3 Standard / Ceph
	TierWarm StorageTier = "WARM" // 7-30 days, S3 Standard-IA / Ceph
	TierCold StorageTier = "COLD" // > 30 days, S3 Glacier / Ceph Archive
)

// BlockMetadata holds object-storage-side metadata for a block.
type BlockMetadata struct {
	BlockHash     string
	ETag          string // MD5 hex or multipart ETag
	SizeBytes     int64
	StorageTier   StorageTier
	LastModified  time.Time
	IsVerified    bool // true after ETag verification passed
}

// ObjectStorageClient abstracts direct-to-blob PUT workflows.
// All methods are safe for concurrent use.
type ObjectStorageClient interface {
	// GenerateUploadURL returns a pre-signed PUT URL valid for 15 minutes.
	// The URL allows the client to stream data directly to object storage
	// without touching the application server.
	GenerateUploadURL(ctx context.Context, tenantID string, blockHash string, sizeBytes int64) (string, error)

	// VerifyBlock checks that a block exists and its ETag matches the expected
	// hash. Called after upload to confirm integrity before marking verified.
	VerifyBlock(ctx context.Context, blockHash string, expectedETag string) error

	// GetBlockMetadata retrieves object-side metadata (used during GC).
	GetBlockMetadata(ctx context.Context, blockHash string) (*BlockMetadata, error)

	// GetBlockReader returns a reader for the block content (for downloads).
	GetBlockReader(ctx context.Context, blockHash string) (io.ReadCloser, int64, error)

	// DeleteBlock removes a block from object storage (GC sweep).
	DeleteBlock(ctx context.Context, blockHash string) error

	// ApplyLifecycleConfigures storage lifecycle policies on the bucket.
	ApplyLifecycle(ctx context.Context, policy LifecyclePolicy) error
}

// ---------------------------------------------------------------------------
// Lifecycle policies
// ---------------------------------------------------------------------------

// LifecycleRule defines one transition rule for tiered storage.
type LifecycleRule struct {
	ID            string
	Prefix        string
	TransitionDay int           // days after creation to transition
	TargetTier    StorageTier   // target storage class
}

// LifecyclePolicy is the collection of rules applied to a bucket.
type LifecyclePolicy struct {
	Rules                           []LifecycleRule
	AbortIncompleteMultipartDays    int // abort incomplete multipart after N days
	NoncurrentVersionExpirationDays int // expire noncurrent versions after N days
}

// DefaultLifecyclePolicy returns the standard Aegis lifecycle configuration.
func DefaultLifecyclePolicy() LifecyclePolicy {
	return LifecyclePolicy{
		Rules: []LifecycleRule{
			{
				ID:            "tier-warm",
				Prefix:        "",
				TransitionDay: 7,
				TargetTier:    TierWarm,
			},
			{
				ID:            "tier-cold",
				Prefix:        "",
				TransitionDay: 30,
				TargetTier:    TierCold,
			},
		},
		AbortIncompleteMultipartDays:    1,
		NoncurrentVersionExpirationDays: 1,
	}
}

// ---------------------------------------------------------------------------
// Upload URL response
// ---------------------------------------------------------------------------

// UploadURL contains the pre-signed URL and metadata for a block upload.
type UploadURL struct {
	BlockHash string
	URL       string
	Headers   map[string]string // additional headers the client must send
	ExpiresAt time.Time
}
