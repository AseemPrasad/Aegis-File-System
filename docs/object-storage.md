# Object Storage Abstraction & Direct PUT Workflows

> PROMPT 4.2 — Binary Data Plane

## Overview

The `internal/objectstorage` package provides a backend-agnostic abstraction for
direct-to-blob uploads. Clients stream data directly to S3/MinIO/R2 via
pre-signed PUT URLs without touching the ingestion engine, enabling 1 GB/s+
aggregate throughput.

### Architecture

```
┌────────────┐     pre-signed URL      ┌──────────────┐
│ IngressSrv │ ──────────────────────▸  │ Client (SDK) │
│            │ ◂────────────────────── │              │
│            │     ETag + session_id    │   PUT block  │
└─────┬──────┘                          └──────┬───────┘
      │ VerifyBlock (ETag compare)              │ direct PUT
      ▼                                         ▼
┌──────────────┐                    ┌──────────────────┐
│ ObjectStore  │                    │ S3 / MinIO / R2  │
│ (Postgres)   │                    │ (bucket)         │
└──────────────┘                    └──────────────────┘
```

## Interface

```go
type ObjectStorageClient interface {
    GenerateUploadURL(ctx, tenantID, blockHash string, sizeBytes int64) (string, error)
    VerifyBlock(ctx, blockHash, expectedETag string) error
    GetBlockMetadata(ctx, blockHash string) (*BlockMetadata, error)
    GetBlockReader(ctx, blockHash string) (io.ReadCloser, int64, error)
    DeleteBlock(ctx, blockHash string) error
    ApplyLifecycle(ctx, policy LifecyclePolicy) error
}
```

A smaller `BlobStore` interface is used by the ingress server for upload+verify
only:

```go
type BlobStore interface {
    GenerateUploadURL(ctx, tenantID, blockHash string, sizeBytes int64) (string, error)
    VerifyBlock(ctx, blockHash, expectedETag string) error
}
```

## Backends

### AWS S3 (`internal/objectstorage/s3.go`)

Uses the official AWS SDK v2 (`aws-sdk-go-v2`).

| Config field   | Env var            | Description                         |
|---------------|--------------------|-------------------------------------|
| `Endpoint`    | `AEGIS_S3_ENDPOINT`| S3 endpoint (for S3-compatible)     |
| `Region`      | `AEGIS_S3_REGION`  | AWS region (default `us-east-1`)    |
| `BucketName`  | `AEGIS_S3_BUCKET`  | Bucket name (required)              |
| `KMSKeyID`    | —                  | KMS key for SSE-KMS encryption      |

Features:
- Server-side encryption (SSE-KMS when `KMSKeyID` is set, else SSE-S3)
- ETag verification via `HeadObject`
- Lifecycle policy via `PutBucketLifecycleConfiguration`
- Storage class detection (STANDARD, STANDARD_IA, GLACIER)

### MinIO (`internal/objectstorage/minio.go`)

Uses `minio-go/v7`.

| Config field | Env var              | Description                     |
|-------------|----------------------|---------------------------------|
| `Endpoint`  | `AEGIS_S3_ENDPOINT`  | `host:port` (required)          |
| `AccessKey` | `AEGIS_S3_ACCESS_KEY`| MinIO access key                |
| `SecretKey` | `AEGIS_S3_SECRET_KEY`| MinIO secret key                |
| `Bucket`    | `AEGIS_S3_BUCKET`    | Bucket name (auto-created)      |
| `UseSSL`    | `AEGIS_S3_USE_SSL`   | `true` for HTTPS                |

Features:
- Auto-creates bucket on startup
- PostPolicy pre-signed URLs (15 min TTL)
- SSE-S3 encryption by default
- Lifecycle via `SetBucketLifecycle`

## Lifecycle Policies

Default Aegis lifecycle (applied via `DefaultLifecyclePolicy()`):

| Rule ID       | Transition Day | Target Tier | Description                  |
|---------------|---------------|-------------|------------------------------|
| `tier-warm`   | 7 days        | WARM        | S3 Standard-IA / Ceph warm   |
| `tier-cold`   | 30 days       | COLD        | S3 Glacier / Ceph archive    |

Abort incomplete multipart uploads after 1 day.

## Block Verification Flow

1. Client uploads block to pre-signed URL → S3 returns ETag
2. Client sends ETag in `CommitRequest.Blocks[].etag`
3. `handleCommit` calls `BlobStore.VerifyBlock(hash, etag)`
4. On match: `MarkBlockVerified(hash)` → `cas_blocks.verified = TRUE`
5. On mismatch: log warning, block remains unverified

Schema addition (`cas_blocks`):
```sql
verified BOOLEAN NOT NULL DEFAULT FALSE
```

## Storage Tiers

| Tier  | Constant  | Duration    | AWS S3 Class     |
|-------|-----------|-------------|------------------|
| HOT   | `TierHot` | < 7 days    | S3 Standard      |
| WARM  | `TierWarm`| 7-30 days   | S3 Standard-IA   |
| COLD  | `TierCold`| > 30 days   | S3 Glacier       |

## Ingress Integration

`IngressServer` accepts an optional `BlobStore`:

```go
srv := ingress.NewIngressServer(store, tokenSigner, blobStore, events, metrics, cfg, logger)
```

- When `blobStore != nil`: initiate handler uses `blobStore.GenerateUploadURL`
- When `blobStore == nil`: falls back to `TokenSigner` (edge PoP HMAC URLs)

## Environment Variables

| Variable                | Default    | Description                          |
|------------------------|------------|--------------------------------------|
| `AEGIS_STORAGE_BACKEND`| `""`       | `"s3"` or `"minio"` (empty = edge)   |
| `AEGIS_S3_ENDPOINT`    | —          | S3/MinIO endpoint URL                |
| `AEGIS_S3_REGION`      | `us-east-1`| AWS region                           |
| `AEGIS_S3_BUCKET`      | —          | Bucket name                          |
| `AEGIS_S3_ACCESS_KEY`  | —          | Access key (MinIO)                   |
| `AEGIS_S3_SECRET_KEY`  | —          | Secret key (MinIO)                   |
| `AEGIS_S3_USE_SSL`     | `false`    | Use HTTPS for MinIO                  |

## Test Doubles

`FakeObjectStorage` (in `storage_test.go`) provides a fully in-memory
implementation for unit tests, with:
- `SeedBlock(hash, data)` — pre-populate blocks
- `SetGenerateErr/SetVerifyErr/SetDeleteErr` — inject failures
- `CallLog()` — inspect method call sequence
