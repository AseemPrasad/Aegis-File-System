# Aegis File System — All Features Deep-Dive

> **Purpose:** Complete reverse-engineering of every feature as a complete system.
> **Generated:** August 2026

---

## Table of Contents

- [Feature 1: File Ingestion Pipeline](#feature-1-file-ingestion-pipeline)
- [Feature 2: Content-Addressable Storage (CAS)](#feature-2-content-addressable-storage-cas)
- [Feature 3: HMAC Authentication & Pre-Signed URLs](#feature-3-hmac-authentication--pre-signed-urls)
- [Feature 4: Database Client Architecture](#feature-4-database-client-architecture)
- [Feature 5: Garbage Collection Pipeline](#feature-5-garbage-collection-pipeline)
- [Feature 6: FastCDC Content-Defined Chunking](#feature-6-fastcdc-content-defined-chunking)
- [Feature 7: Pipeline Derivation Workers](#feature-7-pipeline-derivation-workers)
- [Feature 8: Multi-Tenancy & Namespace Management](#feature-8-multi-tenancy--namespace-management)
- [Feature 9: Rate Limiting & CORS Middleware](#feature-9-rate-limiting--cors-middleware)
- [Feature 10: Object Storage Abstraction](#feature-10-object-storage-abstraction)
- [Feature 11: Observability Stack](#feature-11-observability-stack)
- [Feature 12: SDK Client Library](#feature-12-sdk-client-library)
- [Feature 13: Service Wiring & Lifecycle](#feature-13-service-wiring--lifecycle)

---

# Feature 1: File Ingestion Pipeline

## 1. Feature Overview

**What it does:** The ingestion pipeline is the core value stream of Aegis. It accepts files from clients, deduplicates content at the block level, stores blocks directly to S3 via pre-signed URLs, and atomically commits file versions with manifest records. The pipeline spans three API calls: `Initiate` (plan the upload), `Upload` (PUT blocks to S3), and `Commit` (finalize the version).

**Who uses it:** Any client uploading files — web applications, CLI tools, SDK consumers, or automated pipelines.

**What problem it solves:** Secure, deduplicated, multi-tenant file ingestion at scale. Without this, every upload would be a full proxy through the server (memory-intensive), duplicates would waste storage, and there would be no audit trail.

**Important business rules:**
- Tenant quota enforcement (file size + storage limits)
- CAS deduplication is cross-tenant (identical content shared)
- Upload sessions expire (15 minutes) and are reaped hourly
- Commit is atomic (transaction: lock session → version → CAS ensure → manifest → complete)
- Events published post-commit trigger derivation workers
- Block verification (ETag check) is best-effort after commit

## 2. Entry Point

**API Endpoints:**
- `POST /api/v1/ingest/initiate` → `internal/ingress/initiate.go:handleInitiate`
- `PUT /api/v1/files/upload/{session_id}/{block_idx}` → S3 direct (pre-signed URL)
- `POST /api/v1/ingest/commit` → `internal/ingress/commit.go:handleCommit`

**Route registration:** `internal/ingress/server.go:SetupRoutes` (line 108)
```go
mux.HandleFunc("POST /api/v1/ingest/initiate", s.wrap(s.handleInitiate))
mux.HandleFunc("POST /api/v1/ingest/commit", s.wrap(s.handleCommit))
```

## 3. Complete Execution Trace

### Initiate Flow

```
Entry: POST /api/v1/ingest/initiate
  │
  ├─ server.go:wrap()
  │   ├─ Panic recovery (defer)
  │   ├─ authenticate() → Bearer token check
  │   └─ MaxBytesReader (100MB default)
  │
  ├─ initiate.go:handleInitiate()
  │   ├─ decodeJSON(r, &req) → InitiateRequest
  │   ├─ req.Validate() → structural checks (hex hashes, positive sizes)
  │   ├─ uuid.Parse(req.TenantID)
  │   │
  │   ├─ [STEP 1] store.GetTenantQuota(ctx, tenantID)
  │   │   └─ PgStore → db.QueryWithMetrics("SELECT storage_quota_bytes, used_bytes FROM tenants")
  │   │   └─ Check: quota.StorageQuotaBytes > 0 && used+requested > quota → 402
  │   │
  │   ├─ [STEP 2] File node creation/validation
  │   │   ├─ If req.NodeID set: store.ValidateFileNode(ctx, tenantID, nodeID)
  │   │   │   └─ PgStore → db.QueryWithMetrics("SELECT type FROM namespace_nodes WHERE node_id=$1 AND tenant_id=$2")
  │   │   │   └─ Must be type='FILE', else ErrNodeNotFile
  │   │   └─ If req.NodeID nil: store.CreateFileNode(ctx, tenantID, parentID, name)
  │   │       └─ PgStore → db.BeginWriteTx → INSERT INTO namespace_nodes → Commit
  │   │       └─ Returns new nodeID
  │   │
  │   ├─ [STEP 3] Batch CAS dedup
  │   │   ├─ Build hashes [][]byte from req.Chunks[].BlockHash
  │   │   └─ store.BatchQueryExistingCAS(ctx, hashes)
  │   │       └─ PgStore → db.QueryWithMetrics("SELECT block_hash FROM cas_blocks WHERE block_hash = ANY($1)")
  │   │       └─ Returns map[string]bool (hex → exists)
  │   │
  │   ├─ [STEP 4] Compute missing blocks + mint URLs
  │   │   ├─ For each chunk: if existing[hash] → CAS hit (skip)
  │   │   │   └─ metrics.CASHitsTotal.Inc()
  │   │   └─ If not existing:
  │   │       ├─ metrics.UploadsTotal.Inc()
  │   │       ├─ If blob != nil: blob.GenerateUploadURL(ctx, tenantID, hash, size)
  │   │       │   └─ S3Client.GenerateUploadURL → presigned PUT URL
  │   │       └─ Else: tokens.GeneratePreSignedURL(ctx, tenantID, hash, endpointID)
  │   │           └─ TokenGenerator.SignClaims → HMAC token URL
  │   │       └─ Append to uploadURLs
  │   │
  │   ├─ [STEP 5] Create upload session
  │   │   ├─ Extract client IP (X-Forwarded-For or RemoteAddr)
  │   │   └─ store.CreateUploadSession(ctx, tenantID, nodeID, totalSize, chunks, ip, ua)
  │   │       └─ PgStore → db.BeginWriteTx → INSERT INTO upload_sessions → Commit
  │   │       └─ Returns sessionID, expiresAt
  │   │
  │   └─ [RESPONSE] InitiateResponse{SessionID, NodeID, ExpiresAt, UploadURLs}
  │       └─ writeJSON(w, 200, resp)
```

### Commit Flow

```
Entry: POST /api/v1/ingest/commit
  │
  ├─ server.go:wrap() → authenticate → MaxBytesReader
  │
  ├─ commit.go:handleCommit()
  │   ├─ decodeJSON(r, &req) → CommitRequest
  │   ├─ req.Validate() → session_id required, content_sha256 64 hex, blocks valid
  │   ├─ uuid.Parse(req.SessionID), hex.DecodeString(req.ContentSHA256)
  │   │
  │   ├─ [PRE-FLIGHT] store.GetSession(ctx, sessionID)
  │   │   └─ PgStore → db.QueryWithMetrics("SELECT ... FROM upload_sessions WHERE session_id=$1")
  │   │   └─ Check: status != "COMPLETED" (409), not expired (410)
  │   │
  │   ├─ [ATOMIC COMMIT] store.CommitFile(ctx, tenantID, sessionID, contentSHA256, blocks)
  │   │   └─ PgStore.CommitFile (store.go lines 190-310):
  │   │       ├─ db.BeginWriteTx(ctx)
  │   │       ├─ [1] SELECT ... FOR UPDATE on upload_sessions (lock session)
  │   │       ├─ [2] Validate: status, expiry, tenant match, block count
  │   │       ├─ [3] SELECT 1 FROM namespace_nodes WHERE node_id=$1 FOR UPDATE (lock node)
  │   │       ├─ [4] SELECT next_version_number($1) (fenced via FOR UPDATE)
  │   │       ├─ [5] INSERT INTO file_versions (version_id, version_number, total_size_bytes, content_sha256, created_by)
  │   │       ├─ [6] INSERT INTO cas_blocks ... ON CONFLICT DO NOTHING (ensure CAS rows)
  │   │       ├─ [7] INSERT INTO file_manifest_blocks (triggers bump ref_count)
  │   │       ├─ [8] UPDATE upload_sessions SET status='COMPLETED'
  │   │       └─ [9] tx.Commit() → catches serialization error (40001)
  │   │
  │   ├─ [POST-COMMIT] store.BumpCacheGeneration(ctx, tenantID) → best-effort Redis INCR
  │   │
  │   ├─ [POST-COMMIT] Block verification (best-effort)
  │   │   └─ For each block: blob.VerifyBlock(ctx, hash, etag) → store.MarkBlockVerified
  │   │
  │   ├─ [POST-COMMIT] Event publication
  │   │   └─ events.PublishFileCommitted(ctx, FileCommittedEvent{...})
  │   │       ├─ NoopBus: logs event
  │   │       └─ derivationBridge: sends to WorkerPool via channel
  │   │
  │   └─ [RESPONSE] CommitResponse{VersionID, VersionNumber}
  │       └─ writeJSON(w, 201, resp)
```

## 4. Data Flow

```
InitiateRequest JSON
  → decodeJSON → InitiateRequest struct
  → Validate() → structural validation (hex, sizes)
  → uuid.Parse → tenantID, nodeID
  → Store.GetTenantQuota → TenantQuota{StorageQuotaBytes, UsedBytes}
  → Store.BatchQueryExistingCAS → map[string]bool
  → BlobStore/TokenSigner → pre-signed URL strings
  → Store.CreateUploadSession → sessionID (UUID)
  → InitiateResponse JSON

CommitRequest JSON
  → decodeJSON → CommitRequest struct
  → Validate() → structural validation
  → uuid.Parse → sessionID
  → hex.DecodeString → contentSHA256 ([]byte)
  → Store.GetSession → SessionRecord{SessionID, TenantID, NodeID, Status, TotalSize, ExpectedChunks, ExpiresAt}
  → Store.CommitFile (atomic transaction):
      SessionRecord → FOR UPDATE lock
      → next_version_number (fenced)
      → INSERT file_versions → versionID (UUID)
      → INSERT cas_blocks ON CONFLICT DO NOTHING
      → INSERT file_manifest_blocks (trigger → ref_count++)
      → UPDATE upload_sessions SET status='COMPLETED'
  → versionID (string), versionNumber (int)
  → CommitResponse JSON
```

## 5. Architecture

**Controller/Handler:** `internal/ingress/initiate.go`, `commit.go` — parse requests, orchestrate store calls, format responses.

**Service/Use Case:** `PgStore.CommitFile` in `store.go` — the atomic commit transaction is the core business logic.

**Domain Logic:** CAS deduplication (cross-tenant), version numbering (fenced, gapless), quota enforcement.

**Repository:** `PgStore` wraps `database.DatabaseClient` for all DB operations.

**Database:** PostgreSQL with triggers for ref_count, ltree for namespace, ltree-based lineage.

**External Services:** S3/MinIO (blob storage via `BlobStore` interface), Redis (cache generation bump).

**Infrastructure:** Rate limiter, CORS, audit logging middleware.

## 6. Design Decisions

**Why pre-signed URLs instead of proxy uploads?**
- Server never handles file bytes → constant memory per request
- S3 handles durability/availability directly
- Supports arbitrarily large files without server buffering
- Tradeoff: more complex auth (HMAC tokens), client must handle S3 directly

**Why atomic commit with FOR UPDATE locks?**
- Prevents race conditions on version numbering (gapless versions)
- Prevents double-commit (session lock + status check)
- Prevents concurrent commits to same node (node lock)
- Tradeoff: serializes commits per node (acceptable for file upload pattern)

**Why trigger-maintained ref_count?**
- Application code NEVER writes ref_count — triggers on file_manifest_blocks handle it
- Guarantees consistency even with concurrent operations
- Atomic within the same transaction as the INSERT/DELETE
- Tradeoff: harder to understand, requires stored procedures

**Why batch CAS query instead of per-block?**
- Single DB round-trip for N blocks vs N round-trips
- O(N) vs O(N²) for the dedup check phase
- Tradeoff: larger query payload, but blocks are small (hashes only)

## 7. Failure Scenarios

| Scenario | What Happens |
|----------|-------------|
| **Invalid JSON** | `decodeJSON` returns `ErrBadRequest` → 400 |
| **Invalid hex hash** | `Validate()` catches non-64-char or non-hex → 400 |
| **Tenant not found** | `GetTenantQuota` returns `ErrTenantNotFound` → 404 |
| **Quota exceeded** | `handleInitiate` returns `ErrQuotaExceeded` → 402 |
| **Node not found** | `ValidateFileNode` returns `ErrNodeNotFound` → 404 |
| **Node is DIRECTORY** | `ValidateFileNode` returns `ErrNodeNotFile` → 400 |
| **DB down** | `ErrDBUnavailable` → 503 |
| **Session not found** | `GetSession` returns `ErrSessionNotFound` → 404 |
| **Session already completed** | `ErrSessionCompleted` → 409 |
| **Session expired** | `ErrSessionExpired` → 410 |
| **Tenant mismatch** | `ErrSessionTenantMismatch` → 403 |
| **Block count mismatch** | `ErrBadRequest` with expected/got counts → 400 |
| **Serialization failure** | pgcode 40001 → `ErrSerialization` → 503 (retryable) |
| **S3 upload fails** | Client retries (server not involved in upload) |
| **Event publish fails** | Logged, commit already succeeded (best-effort) |
| **Cache bump fails** | Logged at error, TTL expiry is backstop |
| **Panic in handler** | `wrap()` defer recovers → 500 + structured log |

## 8. Security

- **Authentication:** Bearer token check in `wrap()` → `authenticate()` (constant-time comparison)
- **Authorization:** Tenant ID from token/request, all queries scoped to tenant_id
- **Validation:** Hex hash validation, size validation, node ownership validation
- **Sensitive data:** Client IP logged (X-Forwarded-For), user agent logged
- **Trust boundaries:** Pre-signed URLs bypass server auth (S3 validates HMAC)
- **Attack surfaces:** Large body (mitigated by MaxBytesReader), hash collision (SHA-256), session fixation (UUID random)

## 9. Performance

- **Initiate:** Quota check (1 query) + file node (1 query) + batch CAS (1 query) + session create (1 query) = 4 DB round-trips
- **Commit:** Session get (1) + atomic transaction (6-8 queries) = 7-9 DB round-trips total
- **Bloom filter:** Not used in current code (batch CAS query goes directly to DB)
- **Caching:** Cache generation bump after commit (Redis INCR)
- **Scalability:** Pre-signed URLs mean uploads don't hit server; server only handles initiate + commit

## 10. Testing

- **FakeStore:** In-memory test double for all Store methods
- **SLA tests:** `internal/ingress/sla_test.go` — P95 latency, throughput, CAS dedup, concurrent commits
- **Chaos tests:** `internal/ingress/chaos_test.go` — DB down, panic recovery, overload, invalid JSON, concurrent
- **Handler tests:** Unit tests for initiate/commit handlers with FakeStore

## 11. Alternative Design

**Alternative 1: Proxy uploads (server buffers bytes)**
- Simpler client code, but server memory scales with file size × concurrent uploads
- Rejected because: 10,000 concurrent 100MB files = 1TB server memory

**Alternative 2: Per-block CAS queries**
- Simpler code, but N round-trips per initiate for N blocks
- Rejected because: 100-block file = 100 DB queries instead of 1

**Alternative 3: Optimistic version numbering (no FOR UPDATE)**
- Higher concurrency, but version gaps possible
- Rejected because: business requires gapless, dense version numbers

## 12. Learning Questions

### Beginner
1. What are the three API calls in the ingestion pipeline?
2. What is the purpose of the upload session?
3. How does the server authenticate requests?
4. What happens when a client uploads a file that already exists in CAS?
5. What is the MaxBytesReader used for?
6. How does the server handle panics in handlers?
7. What status codes are returned for different error types?
8. What is the purpose of the `wrap()` function?
9. How does the server extract the client's IP address?
10. What happens if the database is down during initiate?

### Intermediate
1. Why does `CommitFile` use `FOR UPDATE` locks on both the session and the node?
2. How does the trigger-maintained ref_count prevent race conditions?
3. What is the purpose of `BumpCacheGeneration` after commit?
4. How does the `next_version_number` function guarantee gapless versions?
5. Why is block verification (ETag check) done best-effort after commit?
6. What is the difference between `BlobStore` and `TokenSigner`?
7. How does the session reaper work and why is it needed?
8. What is the purpose of `MustDecodeHash` and why does it panic?
9. How does the commit transaction handle serialization failures?
10. Why are CAS block inserts done with `ON CONFLICT DO NOTHING`?

### Advanced
1. Trace the complete data flow from client upload to derivation worker processing.
2. Analyze the consistency guarantees of the atomic commit transaction.
3. Design a distributed version of the ingestion pipeline that maintains gapless versioning.
4. Explain how the cache generation invalidation model achieves O(1) subtree invalidation.
5. What would happen if the event publication failed after a successful commit?
6. How would you implement cross-region CAS deduplication?
7. Analyze the failure modes when both PostgreSQL and Redis are unavailable simultaneously.
8. Design a backpressure mechanism for the ingestion pipeline under extreme load.
9. How would you implement progressive file upload (resumable uploads)?
10. What are the implications of removing the FOR UPDATE lock on namespace_nodes?

## 13. Implementation Exercise

**Exercise: Implement a simplified version of `handleInitiate`**

Requirements:
1. Accept a JSON body with `tenant_id`, `file_name`, and `chunks` (array of `{hash, size}`)
2. Validate the request (hex hashes, positive sizes)
3. Check tenant quota (mock a Store interface)
4. Query existing CAS blocks (mock)
5. Return pre-signed URLs for missing blocks (mock URL generation)
6. Create an upload session (mock)
7. Return session_id + upload URLs

Constraints:
- Do NOT look at `initiate.go` while implementing
- Use the same error sentinel pattern (`var ErrX = errors.New(...)`)
- Use the same `HTTPStatus()` mapping pattern
- Use the same `decodeJSON()` helper pattern
- Write at least 3 test cases using a FakeStore

---

# Feature 2: Content-Addressable Storage (CAS)

## 1. Feature Overview

**What it does:** CAS is the storage abstraction where every block is identified by its SHA-256 hash. Identical content from any tenant is stored once. Reference counting tracks how many file versions reference each block. Garbage collection reclaims unreferenced blocks after a safety window.

**Who uses it:** The ingestion pipeline (ensure/query blocks), GC engine (find/delete orphans), derivation workers (read block metadata).

**What problem it solves:** Eliminates duplicate storage across tenants. A 1GB file uploaded by 100 tenants uses 1GB of storage, not 100GB.

**Important business rules:**
- Cross-tenant deduplication (block_hash is global, not tenant-scoped)
- ref_count maintained exclusively by database triggers (application never writes it)
- 7-day safety window before GC can reclaim orphaned blocks
- Storage tiers: HOT (ref_count > 100), WARM (10-100), COLD (< 10)
- Bloom filter provides O(1) "definitely not exists" check (1% false positive rate)

## 2. Entry Point

- **CAS ensure:** `internal/cas/registry.go:PgRegistry.EnsureBlock` — inserts block row or no-ops
- **CAS query:** `internal/ingress/store.go:PgStore.BatchQueryExistingCAS` — batch existence check
- **CAS stats:** `internal/cas/registry.go:PgRegistry.GetStorageStats` — aggregate metrics
- **CAS tiering:** `internal/cas/registry.go:PgRegistry.UpdateTier` — recalculate storage tier
- **Bloom filter:** `internal/cas/bloom.go:RedisBloomFilter.MightContain` — fast negative check

## 3. Complete Execution Trace

```
Block arrives for dedup check:
  │
  ├─ [Bloom filter check] bloom.MightContain(ctx, hash)
  │   ├─ Redis: BF.EXISTS aegis:cas:bloom:{endpoint_id} {hash}
  │   ├─ [false] → definitely not in CAS → skip to "new block" path
  │   └─ [true] → might exist → fall through to DB query
  │
  ├─ [DB query] store.BatchQueryExistingCAS(ctx, hashes)
  │   └─ SELECT block_hash FROM cas_blocks WHERE block_hash = ANY($1)
  │   └─ Returns map[string]bool
  │
  ├─ [New block] → ensure CAS row exists
  │   └─ PgRegistry.EnsureBlock(ctx, hash, tenantID, sizeBytes)
  │       └─ INSERT INTO cas_blocks (block_hash, tenant_id, size_bytes, ref_count)
  │          VALUES ($1, $2, $3, 1) ON CONFLICT (block_hash) DO NOTHING
  │       └─ Returns (isNew bool, error)
  │
  └─ [Existing block] → ref_count incremented by trigger on manifest insert
```

## 4. Data Flow

```
BlockHash (hex string from client)
  → hex.DecodeString → []byte (32 bytes)
  → Bloom filter: BF.EXISTS (Redis command)
  → DB query: SELECT block_hash FROM cas_blocks WHERE block_hash = ANY($1)
  → map[string]bool (hex → exists)
  → If new: INSERT INTO cas_blocks → trigger sets ref_count = 1
  → If existing: INSERT INTO file_manifest_blocks → trigger increments ref_count
```

## 5. Architecture

**Registry interface** (`internal/cas/registry.go`): Abstracts all CAS operations for testability.
- `EnsureBlock` — insert if new
- `BatchQueryExisting` — batch existence check
- `GetBlock` — single block metadata
- `FindOrphans` — blocks with ref_count = 0
- `DeleteBlocks` — remove orphans
- `GetStorageStats` — aggregate metrics
- `UpdateTier` — recalculate storage tier

**PgRegistry:** Production implementation backed by PostgreSQL.

**BloomFilterer interface** (`internal/cas/bloom.go`): Abstracts bloom filter for test fakes.
- `Add` — insert hash
- `MightContain` — probabilistic existence check
- `AddBatch` — batch insert
- `Reset` — clear filter

**RedisBloomFilter:** Production implementation using Redis Bloom module.

## 6. Design Decisions

**Why cross-tenant dedup?**
- Maximizes deduplication ratio (identical files across tenants)
- Block identity is content-based, not tenant-based
- Tradeoff: tenant_id on cas_blocks is attribution only, not identity

**Why trigger-maintained ref_count?**
- Atomic with the manifest insert/delete (same transaction)
- No application code can accidentally corrupt ref_count
- Tradeoff: requires stored procedures, harder to debug

**Why bloom filter before DB query?**
- Redis BF.EXISTS is O(1) vs DB query O(N)
- 1% false positive rate acceptable (falls through to DB)
- Reduces DB load by ~90% for repeat uploads
- Tradeoff: 1-hour TTL means brief window of false negatives after filter reset

**Why 7-day safety window?**
- Allows client-side error recovery (re-upload within window)
- Prevents premature deletion during concurrent operations
- Mirrors key rotation overlap window (7 days)

## 7. Failure Scenarios

| Scenario | What Happens |
|----------|-------------|
| **Redis down** | Bloom filter fails-open → all hashes fall through to DB query |
| **DB down** | `ErrDBUnavailable` → 503 |
| **Hash collision** | SHA-256 collision is computationally infeasible |
| **Concurrent EnsureBlock** | `ON CONFLICT DO NOTHING` — second insert is no-op |
| **ref_count goes negative** | CHECK constraint prevents it (ref_count >= 0) |
| **GC deletes active block** | Double-check before delete + safety window prevents this |

## 8. Security

- Block hashes are SHA-256 (cryptographic hash)
- Cross-tenant dedup doesn't leak data (blocks are opaque bytes)
- Bloom filter keys are namespaced per endpoint
- GC only deletes blocks with ref_count = 0 AND age > 7 days

## 9. Performance

- **Bloom filter:** O(1) per hash, Redis sub-millisecond
- **Batch CAS query:** Single DB round-trip for N hashes
- **EnsureBlock:** Single INSERT with ON CONFLICT (no lock contention)
- **Storage stats:** Aggregation query (periodic refresh every 30s)
- **Tiering:** UPDATE based on ref_count thresholds (background)

## 10. Testing

- **FakeBloomFilter:** In-memory bloom filter for tests
- **FakeStore:** Mock CAS operations
- **Unit tests:** EnsureBlock, BatchQueryExisting, FindOrphans, DeleteBlocks
- **Integration tests:** Full CAS lifecycle with PostgreSQL

## 11. Alternative Design

**Alternative 1: Tenant-scoped CAS (block_hash includes tenant_id)**
- Simpler isolation, but no cross-tenant dedup
- Rejected because: dedup ratio is the primary value proposition

**Alternative 2: Application-maintained ref_count**
- Simpler code, but race conditions possible
- Rejected because: triggers guarantee atomicity

**Alternative 3: Redis-only CAS (no PostgreSQL)**
- Faster, but no persistence guarantee
- Rejected because: CAS registry is the source of truth

## 12. Learning Questions

### Beginner
1. What is content-addressable storage?
2. How does cross-tenant deduplication work?
3. What is the bloom filter's role in CAS?
4. What are the three storage tiers?
5. How is ref_count maintained?
6. What is the safety window and why does it exist?
7. How does EnsureBlock handle duplicate inserts?
8. What metrics are tracked for CAS?
9. How does the bloom filter fail (open or closed)?
10. What happens to blocks when ref_count reaches 0?

### Intermediate
1. Trace the complete dedup check pipeline from bloom filter to DB.
2. Explain the trigger-based ref_count mechanism in detail.
3. How does the 7-day safety window interact with key rotation?
4. What is the relationship between CAS tiers and S3 lifecycle policies?
5. How would you implement batch bloom filter operations efficiently?
6. Analyze the consistency guarantees of the ON CONFLICT DO NOTHING pattern.
7. How does the bloom filter TTL affect dedup accuracy?
8. What is the memory footprint of the bloom filter for 1M blocks?
9. How would you implement CAS dedup across multiple regions?
10. Design a monitoring dashboard for CAS health.

### Advanced
1. Design a distributed CAS system with eventual consistency.
2. Analyze the ABA problem in ref_count management.
3. How would you implement CAS with erasure coding instead of replication?
4. Design a CAS system that supports partial block deduplication.
5. How would you handle CAS during a PostgreSQL primary failover?
6. Analyze the tradeoffs between bloom filter false positive rate and memory.
7. Design a CAS system for petabyte-scale storage.
8. How would you implement CAS with immutable blocks (no ref_count)?
9. Analyze the security implications of cross-tenant dedup.
10. Design a CAS system that supports content-defined encryption.

## 13. Implementation Exercise

**Exercise: Implement a simplified CAS registry**

Requirements:
1. Implement a `Registry` interface with `EnsureBlock`, `GetBlock`, `FindOrphans`, `DeleteBlocks`
2. Use an in-memory map as the backing store
3. Implement ref_count increment/decrement
4. Implement orphan detection (ref_count = 0, age > threshold)
5. Write tests for all operations

Constraints:
- Do NOT look at `registry.go` while implementing
- Use the same error sentinel pattern
- Handle concurrent access safely
- Implement at least 5 test cases

---

# Feature 3: HMAC Authentication & Pre-Signed URLs

## 1. Feature Overview

**What it does:** The auth system mints time-limited, HMAC-signed pre-signed URLs that allow clients to upload blocks directly to S3 without proxying through the server. Each URL is single-use (nonce-based replay protection), scoped to a specific tenant/block/endpoint, and expires after 15 minutes.

**Who uses it:** Clients uploading files (receive URLs from Initiate), S3 (validates URLs on upload).

**What problem it solves:** Secure, stateless upload authorization without server-side session state or proxying.

**Important business rules:**
- Token TTL: 15 minutes (configurable)
- Nonce: 16 random bytes, consumed exactly once (SETNX semantics)
- Key rotation: 7-day overlap window for zero-downtime rotation
- Tenant isolation: key is per-tenant, tenant ID is in the signed message
- Constant-time signature comparison (crypto/hmac.Equal)

## 2. Entry Point

- **Token generation:** `internal/auth/hmac.go:TokenGenerator.GeneratePreSignedURL` — mints URL
- **Token validation:** `internal/auth/hmac.go:TokenGenerator.Validate` — full gauntlet
- **Key management:** `internal/auth/keys.go:StaticKMS` — in-memory KMS for dev/tests

## 3. Complete Execution Trace

### Token Generation

```
TokenGenerator.GeneratePreSignedURL(ctx, tenantID, blockHash, endpointID)
  │
  ├─ [1] KMS.SigningKey(ctx, tenantID) → (version, key, error)
  │   └─ StaticKMS → returns current version + random 32-byte key
  │
  ├─ [2] Generate 16 random bytes → nonce
  │   └─ crypto/rand.Read(nonce)
  │
  ├─ [3] Build Claims{
  │   TenantID, BlockHash, ExpiryUnix (now+TTL),
  │   KeyVersion, EndpointID, NonceHex
  │   }
  │
  ├─ [4] Claims.valid() → structural validation
  │   ├─ BlockHash: 64 lowercase hex chars
  │   ├─ EndpointID: alphanumeric + underscore/hyphen, ≤ 64 chars
  │   ├─ KeyVersion: 1..1_000_000
  │   └─ NonceHex: 32 lowercase hex chars
  │
  ├─ [5] Compute signature
  │   ├─ message = "aegis1:{ver}:{tenant}:{hash}:{exp}:{endpoint}:{nonce}"
  │   └─ sig = hex(HMAC_SHA256(key, message))
  │
  └─ [6] Build URL
      └─ {base}/chunks/{block_hash}?tenant={tenant}&ts={exp}&kv={ver}&ep={endpoint}&nonce={nonce}&sig={sig}
```

### Token Validation

```
TokenGenerator.Validate(ctx, SignedToken)
  │
  ├─ [1] STRUCTURE — Claims.valid()
  │   └─ Format checks on all fields
  │
  ├─ [2] FRESHNESS — expiry check
  │   ├─ expired: ExpiryUnix + skew < now → ErrExpired
  │   └─ too far: ExpiryUnix > now + TTL + skew → ErrTooFarInFuture
  │
  ├─ [3] KEY RESOLUTION
  │   └─ KMS.VerificationKey(ctx, tenantID, version) → (key, active, error)
  │       └─ Active if: v == current OR v < current AND superseded_at + 7d > now
  │
  ├─ [4] AUTHENTICITY — constant-time HMAC comparison
  │   ├─ want = HMAC_SHA256(key, message)
  │   ├─ got = hex.DecodeString(signatureHex)
  │   └─ hmac.Equal(want, got) → ErrBadSignature if mismatch
  │
  ├─ [5] REPLAY — consume nonce exactly once
  │   └─ NonceStore.Consume(ctx, nonceHex, ttl) → (ok, error)
  │       ├─ Redis: SETNX key 1 EX ttl → true if first
  │       └─ InMemory: map check + insert → true if first
  │   └─ false → ErrReplay
  │
  └─ [6] OWNERSHIP (optional, backend-only)
      └─ BlockOwnershipChecker.OwnsBlock(ctx, tenantID, blockHash)
```

## 4. Data Flow

```
TenantID (UUID) + BlockHash (hex) + EndpointID (string)
  → KMS.SigningKey → (version int, key []byte)
  → crypto/rand → nonce []byte (16 bytes)
  → Claims{...} → message bytes (colon-joined)
  → HMAC-SHA256(key, message) → signature []byte (32 bytes)
  → SignedToken{Claims, SignatureHex}
  → url.Values → URL string

On validation:
  URL string → ParsePreSignedURL → SignedToken
  → Claims.valid() → structural check
  → freshness check → timestamp comparison
  → KMS.VerificationKey → (key, active)
  → computeMAC(key, message) → want signature
  → hmac.Equal(want, got) → authenticity
  → NonceStore.Consume → replay protection
```

## 5. Architecture

**TokenGenerator** (auth/hmac.go): Core HMAC token system.
- `GeneratePreSignedURL` — mint URL
- `SignClaims` — compute signed claims
- `Validate` — full validation gauntlet
- `ValidateToken` — convenience wrapper

**KMSClient interface** (auth/keys.go): Abstracts key management.
- `SigningKey` — current signing key
- `VerificationKey` — specific version verification

**StaticKMS** (auth/keys.go): In-memory KMS for dev/tests.
- `Provision` — register tenant with key
- `Rotate` — generate new key version

**NonceStore interface** (auth/replay.go): Anti-replay nonce storage.
- `Consume` — atomic consume (SETNX semantics)

**RedisNonceStore** (auth/replay.go): Production Redis-backed store.

**InMemoryNonceStore** (auth/replay.go): Process-local store for tests.

## 6. Design Decisions

**Why HMAC instead of JWT/JWE?**
- Simpler: no JWK/JWKS infrastructure needed
- Faster: SHA-256 HMAC is ~10x faster than RSA signing
- Sufficient: we only need authenticity, not encryption or complex claims
- Tradeoff: no standard token format, custom parsing needed

**Why nonce-based replay protection?**
- Single-use tokens prevent replay attacks
- Redis SETNX is atomic (handles concurrent validators)
- TTL auto-cleans old nonces
- Tradeoff: requires Redis for distributed deployments

**Why 7-day key rotation overlap?**
- Mirrors CAS safety window (consistency)
- Allows in-flight pre-signed URLs to survive rotation
- Prevents service disruption during rotation
- Tradeoff: must maintain old keys for 7 days

**Why constant-time comparison?**
- Prevents timing attacks on signature verification
- crypto/hmac.Equal is constant-time regardless of input
- Tradeoff: slightly slower than early-exit comparison

## 7. Failure Scenarios

| Scenario | What Happens |
|----------|-------------|
| **Expired token** | `ErrExpired` → 401 |
| **Replay attack** | `ErrReplay` → 401 |
| **Wrong key version** | `ErrUnknownKeyVer` → 401 |
| **Signature mismatch** | `ErrBadSignature` → 401 |
| **Nonce store down** | `ErrNonceUnavailable` → 401 |
| **KMS down** | Error propagates → 401 |
| **Malformed URL** | `ErrMalformed` → 401 |
| **Future expiry too far** | `ErrTooFarInFuture` → 401 |
| **Tenant doesn't own block** | `ErrNotTenantBlock` → 401 |

## 8. Security

- **Authentication:** HMAC-SHA256 signature (key known only to server)
- **Authorization:** Tenant ID in signed message (cross-tenant replay fails twice)
- **Replay protection:** Nonce consumed exactly once (SETNX)
- **Freshness:** 15-minute expiry + clock skew tolerance
- **Key isolation:** Per-tenant keys (cross-tenant key access impossible)
- **Constant-time comparison:** Prevents timing attacks
- **Domain separation:** "aegis1:" prefix prevents signature reuse across protocols

## 9. Performance

- **Token generation:** HMAC-SHA256 (~1μs), nonce generation (~1μs), Redis SETNX (~1ms)
- **Token validation:** HMAC-SHA256 (~1μs), nonce consume (~1ms), key lookup (in-memory or Redis)
- **Total latency:** ~2-5ms per validation (Redis-dominated)

## 10. Testing

- **StaticKMS:** Deterministic key management for golden-vector tests
- **InMemoryNonceStore:** No Redis dependency for unit tests
- **WithClock:** Custom time source for testing expiry
- **Test cases:** Valid token, expired token, replay, wrong key, malformed input

## 11. Alternative Design

**Alternative 1: JWT tokens**
- Standard format, but heavier (RSA/ECDSA signing, JWK infrastructure)
- Rejected because: HMAC is sufficient and faster

**Alternative 2: Session-based auth (server stores session)**
- Simpler, but requires server-side state for every upload
- Rejected because: pre-signed URLs eliminate server-side state

**Alternative 3: IP-based restrictions**
- Simple, but breaks behind NAT/load balancers
- Rejected because: IP is not a reliable identity signal

## 12. Learning Questions

### Beginner
1. What is the purpose of pre-signed URLs?
2. How does HMAC authentication work?
3. What is a nonce and why is it needed?
4. What is the token TTL and why is it 15 minutes?
5. How does key rotation work?
6. What is the difference between `SigningKey` and `VerificationKey`?
7. How does the `StaticKMS` differ from production KMS?
8. What is domain separation in the context of HMAC?
9. How does constant-time comparison prevent timing attacks?
10. What happens when the nonce store is unavailable?

### Intermediate
1. Trace the complete token generation flow from request to URL.
2. Explain the 5 security properties enforced in Validate.
3. How does the 7-day key rotation overlap work?
4. Analyze the security implications of cross-tenant key isolation.
5. What is the memory footprint of the InMemoryNonceStore?
6. How would you implement nonce cleanup without Redis TTL?
7. Explain the difference between `Validate` and `ValidateToken`.
8. How does the `BlockOwnershipChecker` provide defense in depth?
9. What is the impact of clock skew on token validation?
10. Design a monitoring system for auth failures.

### Advanced
1. Design a distributed auth system without Redis (for edge PoPs).
2. Analyze the security implications of the "aegis1:" domain separator.
3. How would you implement key rotation with zero downtime?
4. Design a token system that supports partial block uploads.
5. Analyze the tradeoffs between HMAC and digital signatures for this use case.
6. How would you implement auth for WebSocket connections?
7. Design a token revocation system for compromised keys.
8. Analyze the security of the nonce-based replay protection under high concurrency.
9. How would you implement multi-region auth with eventual consistency?
10. Design a token system that supports delegated upload authority.

## 13. Implementation Exercise

**Exercise: Implement a simplified HMAC token system**

Requirements:
1. Implement `TokenGenerator` with `GeneratePreSignedURL` and `Validate`
2. Use HMAC-SHA256 for signing
3. Implement nonce-based replay protection (in-memory)
4. Implement key rotation with overlap window
5. Write tests for valid tokens, expired tokens, replay, wrong key

Constraints:
- Do NOT look at `hmac.go` while implementing
- Use the same `Claims` struct pattern
- Implement at least 5 test cases
- Handle edge cases (malformed input, missing fields)

---

# Feature 4: Database Client Architecture

## 1. Feature Overview

**What it does:** The DatabaseClient provides pooled, observed, cached, and failover-aware access to PostgreSQL. It manages four connection pool tiers (write, metadata, read replicas, analytical), a circuit breaker for primary failover, Redis-backed namespace caching, and comprehensive Prometheus metrics.

**Who uses it:** All internal packages that need database access (ingress, CAS, GC, derivation).

**What problem it solves:** Connection pool exhaustion, query routing, failover, caching, and observability — without each package reimplementing these concerns.

**Important business rules:**
- Four pool tiers with explicit routing (no accidental writes on read connections)
- Circuit breaker: 3 consecutive failures → open → failover to replicas
- Cache-aside with generation-based invalidation (O(1) subtree invalidation)
- Bounded-queue overflow: acquire timeout → fail fast (no pileups)
- Slow query logging (>50ms threshold)

## 2. Entry Point

- **Construction:** `internal/database/client.go:NewDatabaseClient` — builds all pools
- **Query routing:** `QueryWithMetrics` / `ExecWithMetrics` — instrumented execution
- **Transaction:** `BeginWriteTx` — pooled write transaction
- **Caching:** `GetNodeMetadataCached` — cache-aside with singleflight

## 3. Complete Execution Trace

### Query Execution

```
QueryWithMetrics(ctx, OpMetadata, "get_tenant_quota", tenantID, sql, args...)
  │
  ├─ [1] pickPool(class) → *pgxpool.Pool
  │   ├─ OpWrite → writePool (gated by circuit breaker)
  │   ├─ OpRead → readTarget() → round-robin replica
  │   ├─ OpMetadata → metadata pool
  │   └─ OpAnalytical → analytical pool
  │
  ├─ [2] queryOnce(ctx, pool, sql, args...)
  │   ├─ pool.Acquire(ctx) with AcquireTimeout (250ms default)
  │   ├─ conn.Query(ctx, sql, args...)
  │   └─ Return releasedRows (auto-release on Close/Next exhaustion)
  │
  ├─ [3] metrics.observeQuery(class, operation, duration, err)
  │   └─ Prometheus histogram + error counter
  │
  ├─ [4] slowGate(class, operation, tenantID, duration)
  │   └─ If duration > 50ms: increment slowQueries counter + structured log
  │
  ├─ [5] logOperation(ctx, class, operation, tenantID, start, err)
  │   └─ Structured JSON log line
  │
  └─ [6] On transport error (OpRead only):
      ├─ breaker.RecordFailure()
      ├─ nextReplica() → next read pool
      └─ queryOnce(ctx, next, sql, args...) → single retry
```

### Transaction Execution

```
BeginWriteTx(ctx)
  │
  ├─ acquire(ctx, OpWrite)
  │   ├─ breaker.Allow() → false? → ErrPrimaryUnavailable
  │   └─ writePool.Acquire(ctx) with AcquireTimeout
  │
  ├─ conn.Begin(ctx)
  │   └─ Return pooledTx{Tx, release}
  │
  └─ On Commit/Rollback:
      └─ release() → conn.Release() → connection returns to pool
```

### Cache-Aside

```
GetNodeMetadataCached(ctx, tenantID, nodeID, loader)
  │
  ├─ [1] cache.generation(ctx, tenantID) → current generation
  │   └─ Redis GET aegis:ns:gen:{tenantID} → uint64
  │
  ├─ [2] cache.Get(ctx, key) under live generation
  │   └─ Redis GET aegis:ns:{tenant}:g{N}:{node} → JSON payload
  │   ├─ [HIT] → Unmarshal → return NodeMeta (lock-free)
  │   └─ [MISS] → fall through
  │
  ├─ [3] singleflight.Do(key, loader)
  │   ├─ First goroutine: run loader(ctx) → DB query
  │   ├─ Concurrent goroutines: block on first result
  │   └─ Loader result: SET with PX TTL → cache
  │
  └─ [4] Return NodeMeta
```

## 4. Data Flow

```
Query string + args
  → pickPool(class) → *pgxpool.Pool
  → pool.Acquire(ctx) → *pgxpool.Conn
  → conn.Query(ctx, sql, args...) → pgx.Rows
  → releasedRows{Rows, onDone} (auto-release wrapper)
  → caller iterates rows → done() on exhaustion
  → connection returns to pool

Cached data:
  → Redis GET → JSON bytes → json.Unmarshal → NodeMeta
  → Miss: loader(ctx) → DB query → NodeMeta → json.Marshal → Redis SET
```

## 5. Architecture

**Pool Tiers:**
- **Write:** Small pool pinned to primary. Strict ordering. Only pool permitted to mutate state.
- **Read:** Larger pool set, round-robin across replicas. Transparent failover on primary failure.
- **Metadata:** Control-plane queries (listing, ACL resolution). Isolated from hot-path traffic.
- **Analytical:** Isolated reporting pool pointed at replicas. Heavy aggregations cannot starve transactional work.

**Circuit Breaker:** Gates traffic to primary. Reads reroute to replicas when open; writes fail fast.

**Cache:** Redis-backed, generation-based invalidation. Mutations bump generation after commit. O(1) subtree invalidation.

**Metrics:** Query duration histogram, acquire-wait histogram, acquired/released counters, error counters, slow-query counter.

## 6. Design Decisions

**Why four pool tiers?**
- Prevents connection exhaustion (different traffic patterns compete for connections)
- Write pool is small (serialized mutations reduce row lock thrashing)
- Metadata pool isolates control-plane from hot-path
- Analytical pool prevents heavy aggregations from starving transactions
- Tradeoff: more complex configuration, but necessary for production reliability

**Why circuit breaker instead of retry?**
- Retries on a failing primary waste connections and time
- Circuit breaker stops sending traffic immediately
- Failover to replicas is transparent for reads
- Tradeoff: writes fail fast (no retry), which is correct (no stale-tolerant writes)

**Why generation-based cache invalidation?**
- O(1) invalidation of entire subtree (no SCAN/DEL)
- Mutations bump generation after commit (immediate invalidation)
- TTL is garbage collection, not coherency mechanism
- Tradeoff: requires Redis for generation counter

**Why bounded-queue overflow?**
- Deterministic p99 latency (no unbounded queuing)
- Fail fast with ErrPoolTimeout instead of pileup
- Tradeoff: some requests rejected under extreme load (correct behavior)

## 7. Failure Scenarios

| Scenario | What Happens |
|----------|-------------|
| **Primary down** | Circuit breaker opens → reads failover to replicas, writes fail fast |
| **All replicas down** | `ErrPrimaryUnavailable` → 503 |
| **Pool exhausted** | `ErrPoolTimeout` → 503 |
| **Redis down** | Cache bypass → direct DB queries |
| **Slow query** | Logged at warn level, slowQueries counter incremented |
| **Connection leak** | `releasedRows.done()` auto-releases on exhaustion |
| **Serialization failure** | pgcode 40001 → `ErrSerialization` → 503 (retryable) |

## 8. Security

- **Tenant isolation:** All queries scoped to tenant_id
- **Connection encryption:** TLS (configured via DSN)
- **Least privilege:** aegis_app has DML only, aegis_readonly has SELECT only
- **Secrets:** DSN contains password, masked in logs

## 9. Performance

- **Pool sizing:** cores*2 min, cores*4 max per pool, global budget of 96 connections
- **Acquire timeout:** 250ms (bounded queue)
- **Health check:** 500ms interval (detection budget = interval × threshold ≈ 1.5s)
- **Cache hit:** Single Redis GET (~1ms)
- **Cache miss:** Singleflight collapse → one DB query + Redis SET
- **Slow query threshold:** 50ms

## 10. Testing

- **SkipStartupPing:** Allows tests with dead primary
- **waitReservoir:** Fixed-size ring for p95 acquire wait
- **Metrics:** Process-wide collectors registered once
- **Leak detection:** Acquired/released counters must return to zero

## 11. Alternative Design

**Alternative 1: Single pool with read/write splitting**
- Simpler, but connections can be exhausted by mixed traffic
- Rejected because: production load requires isolation

**Alternative 2: Proxy-based routing (PgBouncer)**
- External dependency, but simpler application code
- Rejected because: want full control over pool behavior

**Alternative 3: No caching (direct DB queries)**
- Simpler, but higher DB load for metadata queries
- Rejected because: metadata queries are hot path

## 12. Learning Questions

### Beginner
1. What are the four pool tiers and what is each used for?
2. How does the circuit breaker work?
3. What is the purpose of the cache-aside pattern?
4. What is singleflight and why is it needed?
5. How does the releasedRows wrapper prevent connection leaks?
6. What is the acquire timeout and why is it bounded?
7. How does the slow query logger work?
8. What metrics are tracked for database operations?
9. How does the health loop detect primary failures?
10. What happens when Redis is unavailable?

### Intermediate
1. Trace the complete query routing logic from OpClass to pool selection.
2. Explain the circuit breaker state machine (Closed → Open → Half-Open).
3. How does generation-based cache invalidation achieve O(1) subtree invalidation?
4. Analyze the consistency guarantees of the cache-aside pattern.
5. What is the impact of pool sizing on performance?
6. How does the waitReservoir compute p95 without Prometheus quantile queries?
7. Explain the difference between transport errors and SQL errors in the failover logic.
8. How does the analytical pool prevent starvation of transactional work?
9. What is the memory footprint of the NamespaceCache?
10. Design a monitoring dashboard for database health.

### Advanced
1. Design a multi-region database client with conflict resolution.
2. Analyze the tradeoffs between connection pooling and connection multiplexing.
3. How would you implement read-your-owns-writes consistency with replicas?
4. Design a database client that supports automatic query parallelization.
5. Analyze the security implications of connection string exposure.
6. How would you implement database client metrics without Prometheus?
7. Design a circuit breaker that adapts to different failure modes.
8. Analyze the performance impact of TLS on connection pooling.
9. How would you implement connection pool warming for cold starts?
10. Design a database client that supports zero-downtime schema migrations.

## 13. Implementation Exercise

**Exercise: Implement a simplified connection pool with circuit breaker**

Requirements:
1. Implement a `Pool` struct with `Acquire`, `Release`, `Stats`
2. Implement a `CircuitBreaker` with `Allow`, `RecordSuccess`, `RecordFailure`
3. Implement round-robin read routing
4. Implement acquire timeout
5. Write tests for pool exhaustion, circuit breaker states, failover

Constraints:
- Do NOT look at `client.go` or `failover.go` while implementing
- Use the same atomic operations pattern
- Implement at least 5 test cases
- Handle concurrent access safely

---

# Feature 5: Garbage Collection Pipeline

## 1. Feature Overview

**What it does:** The GC pipeline runs three phases on different schedules:
1. **Phase 1 (hourly):** Expired session cleanup — marks stale upload sessions as expired
2. **Phase 2 (daily):** Orphan detection + tombstone emission — finds CAS blocks with ref_count = 0 older than 7 days, publishes tombstone events, soft-deletes DB rows
3. **Phase 3 (out-of-band):** Tombstone consumer — deletes blobs from object storage

**Who uses it:** Background process (started in main.go), no user-facing interface.

**What problem it solves:** Reclaims storage from failed/cancelled uploads and deleted files.

**Important business rules:**
- 7-day safety window before GC can reclaim blocks
- Double-check ref_count = 0 before every delete
- Rate limiting: max 5,000 blocks/hour
- Dry-run mode for testing
- Audit trail via structured logs

## 2. Entry Point

- **Session cleanup:** `internal/gc/gc.go:GarbageCollector.sessionCleanupLoop` — hourly
- **Block sweep:** `internal/gc/gc.go:GarbageCollector.blockSweepLoop` — daily
- **Start/Stop:** `internal/gc/gc.go:GarbageCollector.Start/Stop`

## 3. Complete Execution Trace

### Phase 1: Session Cleanup

```
sessionCleanupLoop (hourly)
  │
  ├─ store.GetExpiredSessions(ctx, limit=100)
  │   └─ gcStoreAdapter → db.QueryWithMetrics("SELECT ... FROM upload_sessions WHERE status != 'COMPLETED' AND expires_at < now()")
  │
  └─ For each session:
      ├─ store.MarkSessionExpired(ctx, sessionID)
      │   └─ gcStoreAdapter → db.ExecWithMetrics("UPDATE upload_sessions SET status = 'EXPIRED'")
      └─ metrics.SessionsExpired()
```

### Phase 2: Block Sweep

```
blockSweepLoop (daily)
  │
  ├─ [1] store.FindOrphanedBlocks(ctx, safetyWindow=7d, batchSize=1000)
  │   └─ gcStoreAdapter → db.QueryWithMetrics("SELECT ... FROM cas_blocks WHERE ref_count = 0 AND created_at < now() - 7d")
  │
  ├─ [2] For each orphan:
  │   ├─ [Rate limit check] deletionCount > MaxBlocksPerHour → skip
  │   ├─ [Double-check] store.DoubleCheckBlock(ctx, blockHash)
  │   │   └─ gcStoreAdapter → db.QueryWithMetrics("SELECT ref_count FROM cas_blocks WHERE block_hash=$1")
  │   │   └─ If ref_count != 0 → skip (block was re-referenced)
  │   │
  │   ├─ [Tombstone event] publisher.PublishBlockTombstone(ctx, "cas-tombstones", event)
  │   │   └─ gcPublisherAdapter → ingress.EventBus.PublishBlockTombstone
  │   │
  │   ├─ [Blob deletion] blob.DeleteBlock(ctx, blockHash)
  │   │   └─ blobDeleterAdapter → S3Client.Delete
  │   │
  │   └─ [DB deletion] store.HardDeleteBlocks(ctx, hashes)
  │       └─ gcStoreAdapter → db.ExecWithMetrics("DELETE FROM cas_blocks WHERE block_hash = ANY($1) AND ref_count = 0")
  │
  └─ [3] Record metrics + audit trail
```

## 4. Data Flow

```
Expired sessions:
  upload_sessions (status != 'COMPLETED', expires_at < now())
  → SessionInfo{id, tenantID, expiresAt}
  → UPDATE status = 'EXPIRED'

Orphaned blocks:
  cas_blocks (ref_count = 0, created_at < 7d ago)
  → BlockInfo{hash, tenantID, sizeBytes, refCount}
  → Double-check: SELECT ref_count → confirm 0
  → TombstoneEvent → Kafka/Redis
  → BlobDeleter.DeleteBlock → S3 DELETE
  → DELETE FROM cas_blocks WHERE ref_count = 0
```

## 5. Architecture

**Store interface** (gc/store.go): Abstracts DB operations for GC.
- `GetExpiredSessions` — find stale sessions
- `MarkSessionExpired` — mark session as expired
- `FindOrphanedBlocks` — find blocks with ref_count = 0
- `DoubleCheckBlock` — re-verify ref_count before delete
- `HardDeleteBlocks` — remove blocks

**TombstonePublisher interface** (gc/publisher.go): Abstracts event publishing.
- `PublishBlockTombstone` — emit deletion event

**BlobDeleter interface** (gc/deleter.go): Abstracts blob deletion.
- `DeleteBlock` — remove blob from object storage

**gcStoreAdapter** (cmd/ingest/gc_adapter.go): Bridges DatabaseClient to gc.Store.

## 6. Design Decisions

**Why 3-phase pipeline?**
- Phase 1 (sessions) is fast and frequent (hourly)
- Phase 2 (blocks) is slow and infrequent (daily)
- Phase 3 (blobs) is out-of-band (separate worker)
- Tradeoff: more complex, but each phase can be tuned independently

**Why double-check before delete?**
- Prevents deleting blocks that were re-referenced between FindOrphans and Delete
- Race condition: a commit could add a reference between the two queries
- Tradeoff: extra DB query per block (acceptable for safety)

**Why rate limiting?**
- Prevents GC from overwhelming the database
- Max 5,000 blocks/hour keeps GC impact bounded
- Tradeoff: orphans live longer, but system stability preserved

**Why tombstone events?**
- Decouples DB deletion from blob deletion
- Allows out-of-band processing
- Enables audit trail
- Tradeoff: eventual consistency between DB and blob storage

## 7. Failure Scenarios

| Scenario | What Happens |
|----------|-------------|
| **DB down** | FindOrphans fails → GC stops, orphans accumulate |
| **S3 down** | Blob deletion fails → logged, DB row deleted (blob orphaned) |
| **Kafka down** | Tombstone publish fails → logged, DB row deleted |
| **Rate limit hit** | Block skipped, rateLimitHit counter incremented |
| **Double-check fails** | Block skipped, doubleCheckSaved counter incremented |
| **Concurrent commit** | Double-check catches ref_count > 0 → block saved |

## 8. Security

- **Audit trail:** Every deletion action logged with structured JSON
- **Dry-run mode:** Log only, no actual deletes (for testing)
- **Rate limiting:** Prevents abuse (GC cannot delete more than 5,000 blocks/hour)
- **Double-check:** Prevents accidental deletion of active blocks

## 9. Performance

- **Session cleanup:** O(100) sessions per hour (bounded by limit)
- **Block sweep:** O(1000) blocks per sweep (bounded by batchSize)
- **Rate limit:** 5,000 blocks/hour maximum
- **DB queries:** 2 per block (find + double-check) + 1 delete per batch

## 10. Testing

- **Dry-run mode:** Test without actual deletes
- **FakeStore:** Mock all DB operations
- **Metrics callbacks:** Verify correct counters
- **Audit trail:** Verify structured log output

## 11. Alternative Design

**Alternative 1: Trigger-based GC (no background worker)**
- Simpler, but GC runs in the same transaction as the delete
- Rejected because: GC should not block user requests

**Alternative 2: Reference counting only (no safety window)**
- Simpler, but race conditions possible
- Rejected because: safety window prevents premature deletion

**Alternative 3: Eventual consistency (no double-check)**
- Simpler, but can delete active blocks
- Rejected because: correctness is paramount

## 12. Learning Questions

### Beginner
1. What are the three phases of the GC pipeline?
2. What is the safety window and why does it exist?
3. How does double-check prevent accidental deletion?
4. What is a tombstone event?
5. How does rate limiting affect GC behavior?
6. What happens when GC cannot delete a block?
7. How does session cleanup work?
8. What is dry-run mode?
9. How does GC handle S3 failures?
10. What metrics are tracked for GC?

### Intermediate
1. Trace the complete block sweep flow from discovery to deletion.
2. Explain the race condition that double-check prevents.
3. How does the 3-phase pipeline improve system stability?
4. Analyze the consistency guarantees between DB and blob storage.
5. What is the impact of GC rate limiting on storage reclamation?
6. How would you implement GC for a distributed system?
7. Explain the audit trail requirements for GC operations.
8. How does GC interact with the CAS bloom filter?
9. What is the memory footprint of the GC worker?
10. Design a monitoring dashboard for GC health.

### Advanced
1. Design a distributed GC system with exactly-once deletion semantics.
2. Analyze the tradeoffs between eager and lazy GC.
3. How would you implement GC for a system with erasure coding?
4. Design a GC system that supports rollback of deletions.
5. Analyze the security implications of GC audit trails.
6. How would you implement GC for a multi-region system?
7. Design a GC system that adapts to storage pressure.
8. Analyze the performance impact of GC on foreground traffic.
9. How would you implement GC for a system with immutable blocks?
10. Design a GC system that supports compliance retention policies.

## 13. Implementation Exercise

**Exercise: Implement a simplified GC pipeline**

Requirements:
1. Implement `GarbageCollector` with `sessionCleanupLoop` and `blockSweepLoop`
2. Implement rate limiting (max blocks per hour)
3. Implement double-check before delete
4. Implement dry-run mode
5. Write tests for all operations

Constraints:
- Do NOT look at `gc.go` while implementing
- Use the same interface pattern (Store, Publisher, Deleter)
- Implement at least 5 test cases
- Handle concurrent access safely

---

# Feature 6: FastCDC Content-Defined Chunking

## 1. Feature Overview

**What it does:** FastCDC splits files into content-defined chunks using a rolling hash (Gear hash) and normalized two-phase masking. Chunks are then SHA-256 hashed for content addressing. This ensures that small insertions/deletions only affect nearby chunks, maximizing deduplication.

**Who uses it:** Derivation workers (for re-chunking), SDK (for parallel upload), reference Rust implementation.

**What problem it solves:** Fixed-size chunking produces poor dedup when content shifts (e.g., inserting a byte at the beginning shifts all chunk boundaries). Content-defined chunking anchors boundaries to content, so changes only affect local chunks.

**Important business rules:**
- Min chunk: 64KB, Average chunk: 1MB, Max chunk: 4MB
- Gear hash uses a 256-entry lookup table
- Normalized two-phase masking determines cut points
- SHA-256 for content addressing (not the Gear hash)

## 2. Entry Point

- **Chunking:** `internal/fastcdc/fastcdc.go:FastCDC.Split` — split reader into chunks
- **Rust reference:** `crates/fastcdc/src/lib.rs` — reference implementation

## 3. Complete Execution Trace

```
FastCDC.Split(ctx, reader, cfg)
  │
  ├─ [1] Initialize state
  │   ├─ gearTable[256] → precomputed rolling hash table
  │   ├─ mask = normalize(avgChunk) → bit mask for cut points
  │   └─ state = {offset: 0, fp: 0, gearHash: 0}
  │
  ├─ [2] Read and process bytes
  │   ├─ Read chunk of data from reader
  │   ├─ For each byte:
  │   │   ├─ gearHash = (gearHash << 1) + gearTable[byte]
  │   │   ├─ fp = gearHash & mask
  │   │   ├─ If fp == 0 → potential cut point
  │   │   │   └─ Check min/max constraints
  │   │   └─ If cut point found:
  │   │       ├─ Compute SHA-256 of chunk bytes
  │   │       └─ Emit Chunk{Offset, Size, Hash}
  │   └─ Continue until reader exhausted
  │
  └─ [3] Return []Chunk
```

## 4. Data Flow

```
io.Reader (file content)
  → byte-by-byte processing
  → Gear hash (rolling, O(1) per byte)
  → Normalized mask (determines cut probability)
  → Cut point detection (min/max constraints)
  → SHA-256 of chunk bytes
  → []Chunk{Offset, Size, Hash}
```

## 5. Architecture

**FastCDC struct** (fastcdc/fastcdc.go): Core algorithm.
- `Split` — main entry point
- `readUntilCut` — read bytes until cut point
- `findCut` — detect cut point using Gear hash

**Gear hash:** Rolling hash using 256-entry lookup table. O(1) per byte.

**Normalized two-phase masking:** Determines cut probability based on average chunk size. mask = (1 << log2(avgChunk)) - 1.

**SHA-256:** Content addressing (not the Gear hash).

## 6. Design Decisions

**Why Gear hash instead of Rabin fingerprint?**
- Gear hash is ~10x faster (simple table lookup vs polynomial)
- Similar distribution quality for cut points
- Tradeoff: slightly less uniform distribution (acceptable)

**Why normalized two-phase masking?**
- Deterministic cut points (same content → same chunks)
- Average chunk size is statistically accurate
- Min/max constraints prevent degenerate chunks
- Tradeoff: more complex than simple modulo

**Why SHA-256 for content addressing?**
- Cryptographic hash (collision resistance)
- Standard, well-understood
- Tradeoff: slower than non-crypto hashes (acceptable for storage)

**Why 64KB min, 1MB avg, 4MB max?**
- 64KB min: small enough for dedup, large enough to avoid excessive metadata
- 1MB avg: good balance between dedup ratio and metadata overhead
- 4MB max: prevents huge chunks from dominating storage

## 7. Failure Scenarios

| Scenario | What Happens |
|----------|-------------|
| **Reader error** | Partial chunks returned, error propagated |
| **Empty file** | No chunks returned |
| **Very small file** | Single chunk (below min size) |
| **Corrupt data** | Different chunks (content-defined) |
| **Memory pressure** | Chunks streamed, not buffered entirely |

## 8. Security

- **SHA-256:** Cryptographically secure content addressing
- **No secrets in chunking:** Algorithm is public, security comes from hash
- **Input validation:** Reader errors propagated safely

## 9. Performance

- **Gear hash:** O(1) per byte (table lookup)
- **SHA-256:** O(n) per chunk (but chunks are large)
- **Total:** O(n) for file of n bytes
- **Memory:** O(chunk size) buffer (not entire file)

## 10. Testing

- **Reference Rust implementation:** Validates Go implementation
- **Known test vectors:** Expected chunks for known inputs
- **Boundary tests:** Min/max chunk sizes, empty files, single-byte files

## 11. Alternative Design

**Alternative 1: Fixed-size chunking**
- Simpler, but poor dedup when content shifts
- Rejected because: dedup ratio is primary value

**Alternative 2: Rabin fingerprint**
- Better distribution, but 10x slower
- Rejected because: Gear hash is sufficient

**Alternative 3: Content-defined chunking with CDC-based dedup**
- More complex, but better dedup for certain workloads
- Rejected because: standard FastCDC is sufficient

## 12. Learning Questions

### Beginner
1. What is content-defined chunking?
2. How does the Gear hash work?
3. What is normalized two-phase masking?
4. Why is SHA-256 used for content addressing?
5. What are the min/avg/max chunk sizes?
6. How does FastCDC handle small files?
7. What is the difference between Gear hash and SHA-256?
8. How does FastCDC achieve O(1) per byte?
9. What happens when the reader returns an error?
10. How does FastCDC handle empty files?

### Intermediate
1. Trace the complete chunking algorithm from reader to chunks.
2. Explain the normalized two-phase masking in detail.
3. How do min/max constraints prevent degenerate chunks?
4. Analyze the dedup ratio for different chunk sizes.
5. What is the memory footprint of FastCDC?
6. How does FastCDC handle content insertions/deletions?
7. Explain the relationship between Gear hash and cut points.
8. How would you implement streaming chunking for very large files?
9. What is the impact of chunk size on dedup ratio?
10. Design a benchmark for FastCDC performance.

### Advanced
1. Design a parallel FastCDC implementation for multi-core systems.
2. Analyze the statistical properties of Gear hash distribution.
3. How would you implement FastCDC for a distributed system?
4. Design a FastCDC variant that adapts chunk sizes to content type.
5. Analyze the security implications of content-defined chunking.
6. How would you implement FastCDC with encryption?
7. Design a FastCDC system that supports partial chunk updates.
8. Analyze the tradeoffs between chunk size and dedup ratio.
9. How would you implement FastCDC for streaming data?
10. Design a FastCDC system that supports content-defined encryption.

## 13. Implementation Exercise

**Exercise: Implement a simplified FastCDC chunker**

Requirements:
1. Implement `FastCDC` with `Split` method
2. Implement Gear hash (256-entry table)
3. Implement normalized two-phase masking
4. Implement SHA-256 content addressing
5. Write tests for known inputs

Constraints:
- Do NOT look at `fastcdc.go` while implementing
- Use the same min/avg/max parameters
- Implement at least 5 test cases
- Handle edge cases (empty file, single byte, very large file)

---

# Feature 7: Pipeline Derivation Workers

## 1. Feature Overview

**What it does:** Derivation workers process files after upload: virus scanning (ClamAV), OCR text extraction, video thumbnail generation, and vector embedding. Workers are event-driven (consume from Kafka/Redpanda), tool-based (each worker is a specific tool), and support retry with exponential backoff and dead-letter queue.

**Who uses it:** Background process (started in main.go when `AEGIS_DERIVATION_ENABLED=true`).

**What problem it solves:** Automatic post-upload processing without blocking the ingestion pipeline.

**Important business rules:**
- Workers are optional (noop implementations for dev)
- Events published after successful commit trigger workers
- Retry with exponential backoff for transient failures
- Dead-letter queue for failed jobs
- Results stored in `derivation_results` table

## 2. Entry Point

- **Event publication:** `internal/ingress/commit.go:handleCommit` → `events.PublishFileCommitted`
- **Event consumption:** `cmd/ingest/derivation_bridge.go:derivationBridge.consume`
- **Worker execution:** `internal/derivation/worker.go:WorkerPool.ProcessEvent`

## 3. Complete Execution Trace

```
Post-commit event:
  │
  ├─ [1] ingress.CommitFile → events.PublishFileCommitted(ctx, event)
  │   └─ derivationBridge.PublishFileCommitted → channel (buffered, 256)
  │
  ├─ [2] derivationBridge.consume(ctx)
  │   └─ Loop: select { case evt := <-events → pool.ProcessEvent(ctx, evt) }
  │
  ├─ [3] WorkerPool.ProcessEvent(ctx, event)
  │   ├─ Determine MIME type from event.MimeType
  │   ├─ Select workers for MIME type
  │   └─ For each worker:
  │       ├─ RetryWithBackoff(ctx, maxRetries, func() error {
  │       │   └─ worker.Process(ctx, event)
  │       │       ├─ Read block content (via BlockReader)
  │       │       ├─ Execute tool (ClamAV/OCR/FFmpeg/Embed)
  │       │       └─ Store result (via ResultStore)
  │       │   })
  │   │   └─ On failure after retries: DLQ.Add(event, err)
  │
  └─ [4] Results stored in derivation_results table
```

## 4. Data Flow

```
FileCommittedEvent (from ingress)
  → derivationBridge (channel buffer)
  → WorkerPool.ProcessEvent
  → MIME type classification
  → Worker selection (ClamAV, OCR, FFmpeg, Vector)
  → BlockReader.ReadBlock → []byte (content)
  → Tool execution → result
  → ResultStore.Put → derivation_results table
  → On failure: DLQ.Add → dead letter queue
```

## 5. Architecture

**EventBus interface** (ingress/events.go): Abstracts event publishing.
- `PublishFileCommitted` — emit commit event
- `PublishBlockTombstone` — emit deletion event

**derivationBridge** (cmd/ingest/derivation_bridge.go): Bridges ingress events to worker pool.
- Implements `ingress.EventBus`
- Forwards events via buffered channel (256 capacity)
- Background consumer goroutine

**WorkerPool** (internal/derivation/pool.go): Manages worker execution.
- Selects workers by MIME type
- Executes with retry/backoff
- Manages DLQ for failures

**Worker interface** (internal/derivation/worker.go): Individual processing tool.
- `Process(ctx, event)` — process one event
- `MimeTypes()` — supported MIME types

## 6. Design Decisions

**Why event-driven instead of synchronous?**
- Ingestion pipeline is not blocked by derivation
- Workers can scale independently
- Failed derivations don't affect uploads
- Tradeoff: eventual consistency (results appear later)

**Why tool-based workers?**
- Each worker is independent (ClamAV, OCR, FFmpeg, Vector)
- Easy to add new workers
- Easy to disable specific workers
- Tradeoff: more complex than monolithic processor

**Why retry with backoff?**
- Transient failures (network, S3) are common
- Exponential backoff prevents thundering herd
- Max retries prevents infinite loops
- Tradeoff: delayed processing for transient failures

**Why dead-letter queue?**
- Failed jobs preserved for debugging
- Prevents poison pills from blocking the queue
- Manual inspection/reprocessing possible
- Tradeoff: requires operational tooling

## 7. Failure Scenarios

| Scenario | What Happens |
|----------|-------------|
| **Worker crash** | Event redelivered (Kafka semantics) |
| **Tool failure** | Retry with backoff → DLQ after max retries |
| **Block not found** | Worker returns error → retry |
| **DB down** | Result storage fails → retry |
| **Channel full** | Event dropped, warning logged |
| **Invalid MIME type** | No workers selected, event skipped |

## 8. Security

- **Tool isolation:** Each worker runs independently
- **No secrets in events:** Events contain metadata, not content
- **Audit trail:** Derivation results logged with worker name and status

## 9. Performance

- **Channel buffer:** 256 events (configurable)
- **Worker concurrency:** Configurable pool size
- **Retry backoff:** Exponential (1s, 2s, 4s, ...)
- **DLQ:** Memory-backed (dev) or Kafka-backed (prod)

## 10. Testing

- **Noop implementations:** ClamAV, OCR, FFmpeg, Vector (dev profile)
- **FakeResultStore:** In-memory result storage
- **MemoryDLQ:** In-memory dead-letter queue
- **Unit tests:** Worker selection, retry logic, DLQ behavior

## 11. Alternative Design

**Alternative 1: Synchronous derivation**
- Simpler, but blocks ingestion pipeline
- Rejected because: derivation latency would affect upload latency

**Alternative 2: Monolithic processor**
- Simpler, but harder to add new tools
- Rejected because: tool-based workers are more flexible

**Alternative 3: No DLQ (retry forever)**
- Simpler, but poison pills block the queue
- Rejected because: DLQ prevents queue starvation

## 12. Learning Questions

### Beginner
1. What are derivation workers and what do they do?
2. How are workers triggered after upload?
3. What is the difference between retry and DLQ?
4. What MIME types are supported?
5. How does the derivationBridge work?
6. What happens when a worker fails?
7. How are results stored?
8. What is the channel buffer size?
9. How are workers selected for a given file?
10. What happens when derivation is disabled?

### Intermediate
1. Trace the complete derivation flow from event to result.
2. Explain the retry with backoff algorithm.
3. How does the DLQ prevent queue starvation?
4. Analyze the consistency guarantees of event-driven derivation.
5. What is the impact of channel buffer size on performance?
6. How would you implement worker scaling?
7. Explain the MIME type classification logic.
8. How does the derivationBridge handle channel overflow?
9. What is the memory footprint of the worker pool?
10. Design a monitoring dashboard for derivation health.

### Advanced
1. Design a distributed derivation system with exactly-once semantics.
2. Analyze the tradeoffs between event-driven and polling-based derivation.
3. How would you implement derivation for a multi-region system?
4. Design a derivation system that supports dependency chains between workers.
5. Analyze the security implications of derivation workers.
6. How would you implement derivation with rollback support?
7. Design a derivation system that adapts to load.
8. Analyze the performance impact of derivation on foreground traffic.
9. How would you implement derivation for streaming data?
10. Design a derivation system that supports custom user-defined workers.

## 13. Implementation Exercise

**Exercise: Implement a simplified derivation pipeline**

Requirements:
1. Implement `WorkerPool` with `ProcessEvent` method
2. Implement a simple worker (e.g., word count)
3. Implement retry with backoff
4. Implement dead-letter queue
5. Write tests for all operations

Constraints:
- Do NOT look at `worker.go` while implementing
- Use the same interface pattern (Worker, ResultStore, DLQ)
- Implement at least 5 test cases
- Handle concurrent access safely

---

# Feature 8: Multi-Tenancy & Namespace Management

## 1. Feature Overview

**What it does:** Aegis provides strict multi-tenant isolation through namespace trees (ltree paths), per-tenant quotas, per-tenant encryption keys, and role-based ACLs. Each tenant has a root directory, and files/folders form a tree under it.

**Who uses it:** All clients (each request is scoped to a tenant).

**What problem it solves:** Secure isolation between tenants with shared infrastructure.

**Important business rules:**
- Namespace tree uses PostgreSQL ltree extension
- Lineage paths are trigger-computed (never caller-supplied)
- ACLs are role-based (OWNER, EDITOR, VIEWER) with inheritance via ancestor lineage
- Quota enforcement at initiate time (file size + storage)
- One root per tenant (enforced by unique index)

## 2. Entry Point

- **Namespace creation:** `internal/ingress/store.go:PgStore.CreateFileNode` — create file node
- **Namespace validation:** `internal/ingress/store.go:PgStore.ValidateFileNode` — validate node ownership
- **Quota check:** `internal/ingress/store.go:PgStore.GetTenantQuota` — check tenant quota

## 3. Complete Execution Trace

### File Node Creation

```
CreateFileNode(ctx, tenantID, parentID, name)
  │
  ├─ db.BeginWriteTx(ctx)
  │
  ├─ If parentID != nil:
  │   └─ INSERT INTO namespace_nodes (tenant_id, parent_id, name, type)
  │      VALUES ($1, $2, $3, 'FILE') RETURNING node_id
  │
  ├─ If parentID == nil:
  │   └─ INSERT INTO namespace_nodes (tenant_id, parent_id, name, type)
  │      VALUES ($1, NULL, $2, 'FILE') RETURNING node_id
  │
  └─ tx.Commit(ctx)
      └─ Trigger trg_nodes_lineage computes lineage_path
```

### Quota Check

```
GetTenantQuota(ctx, tenantID)
  │
  └─ SELECT storage_quota_bytes, used_bytes FROM tenants WHERE tenant_id = $1
      └─ Returns TenantQuota{StorageQuotaBytes, UsedBytes}
      └─ Check: quota.StorageQuotaBytes > 0 && used+requested > quota → 402
```

## 4. Data Flow

```
InitiateRequest{TenantID, FileName, ParentID}
  → uuid.Parse → tenantID, parentID
  → GetTenantQuota → TenantQuota{StorageQuotaBytes, UsedBytes}
  → Quota check: used + requested > quota → 402
  → CreateFileNode → nodeID (UUID)
  → INSERT INTO namespace_nodes → trigger computes lineage_path
```

## 5. Architecture

**Schema** (db/schema.sql):
- `tenants` — tenant root with quota and KMS key
- `namespace_nodes` — hierarchical tree (ltree lineage_path)
- `acl_entries` — role-based permissions
- `file_versions` — immutable file revisions
- `cas_blocks` — content-addressable blocks

**Triggers:**
- `trg_nodes_lineage` — computes lineage_path on INSERT/UPDATE
- `manifest_block_added/removed` — maintains ref_count on cas_blocks

## 6. Design Decisions

**Why ltree for namespace hierarchy?**
- Native PostgreSQL support
- Efficient ancestor/descendant queries (@>, <@)
- GIST index for fast path lookups
- Simpler than recursive CTEs

**Why trigger-computed lineage_path?**
- Prevents broken lineage states (unrepresentable)
- Always consistent with parent_id
- No application code can corrupt lineage
- Tradeoff: harder to understand, requires stored procedures

**Why one root per tenant?**
- Simplifies ACL inheritance (all paths start from root)
- Prevents disconnected namespace trees
- Enforced by unique index: `uq_tenant_root`

**Why role-based ACLs (OWNER/EDITOR/VIEWER)?**
- Simple, coarse-grained permissions
- Fine-grained bitmasks belong above this layer
- Inheritance via ancestor lineage (ltree)

## 7. Failure Scenarios

| Scenario | What Happens |
|----------|-------------|
| **Tenant not found** | `ErrTenantNotFound` → 404 |
| **Quota exceeded** | `ErrQuotaExceeded` → 402 |
| **Node not found** | `ErrNodeNotFound` → 404 |
| **Node is DIRECTORY** | `ErrNodeNotFile` → 400 |
| **Duplicate root** | Unique index violation → error |
| **Cycle detection** | ltree prevents cycles structurally |

## 8. Security

- **Tenant isolation:** All queries scoped to tenant_id
- **ACL enforcement:** Role-based access control
- **Quota enforcement:** Prevents storage abuse
- **Audit trail:** All mutations logged

## 9. Performance

- **ltree queries:** O(log N) with GIST index
- **Quota check:** Single SELECT per initiate
- **ACL resolution:** Walk ancestor lineage (ltree @> operator)

## 10. Testing

- **FakeStore:** Mock namespace operations
- **Schema tests:** Verify trigger behavior
- **ACL tests:** Verify role inheritance

## 11. Alternative Design

**Alternative 1: Path-based namespace (strings)**
- Simpler, but no hierarchy queries
- Rejected because: ltree provides native hierarchy support

**Alternative 2: Adjacency list (parent_id only)**
- Simpler, but recursive queries needed
- Rejected because: ltree is more efficient

**Alternative 3: Materialized path (stored in column)**
- Similar to ltree, but no PostgreSQL support
- Rejected because: ltree is native and indexed

## 12. Learning Questions

### Beginner
1. What is the namespace tree and how is it structured?
2. How does ltree work for hierarchy queries?
3. What is the lineage_path and who computes it?
4. What are the ACL roles and how do they work?
5. How does quota enforcement work?
6. What is the one-root-per-tenant constraint?
7. How does the trigger compute lineage_path?
8. What happens when a tenant is deleted?
9. How does ACL inheritance work?
10. What is the acl_epoch column used for?

### Intermediate
1. Trace the complete namespace query flow from path to node.
2. Explain the ltree @> operator for ancestor queries.
3. How does the lineage trigger prevent broken states?
4. Analyze the performance of ACL resolution via ltree.
5. What is the impact of acl_epoch on cache invalidation?
6. How would you implement namespace migration?
7. Explain the CHECK constraint on namespace_nodes.
8. How does the unique index prevent duplicate roots?
9. What is the memory footprint of ltree indexes?
10. Design a monitoring dashboard for namespace health.

### Advanced
1. Design a distributed namespace system with conflict resolution.
2. Analyze the tradeoffs between ltree and recursive CTEs.
3. How would you implement namespace versioning?
4. Design a namespace system that supports soft deletes with recovery.
5. Analyze the security implications of ACL inheritance.
6. How would you implement cross-tenant namespace sharing?
7. Design a namespace system that supports namespace fusion/splitting.
8. Analyze the performance impact of deep namespace trees.
9. How would you implement namespace replication?
10. Design a namespace system that supports compliance retention.

## 13. Implementation Exercise

**Exercise: Implement a simplified namespace system**

Requirements:
1. Implement a `Namespace` struct with `CreateNode`, `GetNode`, `ListChildren`
2. Implement path-based hierarchy (strings, not ltree)
3. Implement basic ACL checking
4. Implement quota enforcement
5. Write tests for all operations

Constraints:
- Do NOT look at `store.go` or `schema.sql` while implementing
- Use the same error sentinel pattern
- Implement at least 5 test cases
- Handle concurrent access safely

---

# Feature 9: Rate Limiting & CORS Middleware

## 1. Feature Overview

**What it does:** Per-tenant token bucket rate limiting (1000 RPS default, burst 2000) and configurable CORS headers. Rate limiting prevents abuse; CORS allows browser-based clients.

**Who uses it:** All HTTP requests (rate limiting), browser-based clients (CORS).

**What problem it solves:** Prevents abuse (rate limiting) and enables cross-origin requests (CORS).

**Important business rules:**
- Rate limit: 1000 RPS per tenant, burst 2000
- CORS: configurable allowed origins, methods, headers
- Rate limit key: X-Tenant-ID header (or "default")
- 429 response with Retry-After header on rate limit exceeded

## 2. Entry Point

- **Rate limiting:** `internal/ingress/ratelimit.go:RateLimiter.Allow` — check rate limit
- **CORS:** `internal/ingress/cors.go:CORSMiddleware` — add CORS headers

## 3. Complete Execution Trace

### Rate Limiting

```
Request arrives:
  │
  ├─ RateLimiter.AllowHTTP(next).ServeHTTP(w, r)
  │   ├─ tenantID = r.Header.Get("X-Tenant-ID") or "default"
  │   ├─ Allow(tenantID) → bool
  │   │   ├─ getBucket(tenantID) → *tokenBucket
  │   │   │   └─ Create bucket if not exists (lazy initialization)
  │   │   └─ bucket.allow(now) → bool
  │   │       ├─ Refill tokens based on elapsed time
  │   │       ├─ If tokens >= 1: consume 1 token, return true
  │   │       └─ If tokens < 1: return false
  │   ├─ If false: 429 + Retry-After header
  │   └─ If true: next.ServeHTTP(w, r)
```

### CORS

```
Request arrives:
  │
  ├─ CORSMiddleware(cfg, next).ServeHTTP(w, r)
  │   ├─ origin = r.Header.Get("Origin")
  │   ├─ If origin not in allowedOrigins: pass through
  │   ├─ If origin allowed:
  │   │   ├─ Set Access-Control-Allow-Origin
  │   │   ├─ Set Access-Control-Allow-Methods
  │   │   ├─ Set Access-Control-Allow-Headers
  │   │   ├─ Set Access-Control-Max-Age
  │   │   ├─ If AllowCredentials: set Access-Control-Allow-Credentials
  │   │   └─ If OPTIONS: 204 No Content
  │   └─ next.ServeHTTP(w, r)
```

## 4. Data Flow

```
Request → RateLimiter.AllowHTTP → CORS middleware → Handler
  │
  ├─ Rate limiter: tenantID → tokenBucket → allow/deny
  └─ CORS: origin → allowedOrigins check → headers
```

## 5. Architecture

**RateLimiter** (ratelimit.go): Per-tenant token bucket.
- `Allow(tenantID)` — check rate limit
- `AllowHTTP(next)` — HTTP middleware wrapper
- `getBucket(tenantID)` — lazy bucket creation

**tokenBucket** (ratelimit.go): Single-tenant token bucket.
- `allow(now)` — consume one token

**CORSConfig** (cors.go): CORS configuration.
- `AllowedOrigins` — whitelist of origins
- `AllowedMethods` — allowed HTTP methods
- `AllowedHeaders` — allowed request headers
- `MaxAge` — preflight cache seconds

## 6. Design Decisions

**Why token bucket instead of sliding window?**
- Token bucket allows bursts (burst = 2000)
- Simpler implementation (no Redis needed for single-process)
- Memory-efficient (one bucket per tenant)
- Tradeoff: not distributed (single-process only)

**Why per-tenant rate limiting?**
- Prevents one tenant from starving others
- Fair resource allocation
- Tradeoff: requires tenant identification (X-Tenant-ID header)

**Why configurable CORS?**
- Production: restrict to actual frontend domain
- Development: allow localhost origins
- Tradeoff: more complex configuration

## 7. Failure Scenarios

| Scenario | What Happens |
|----------|-------------|
| **Rate limit exceeded** | 429 + Retry-After header |
| **Missing tenant ID** | Uses "default" tenant |
| **Redis down** | Rate limiter is in-memory (no Redis dependency) |
| **CORS origin not allowed** | No CORS headers (pass through) |
| **Preflight request** | 204 No Content with CORS headers |

## 8. Security

- **Rate limiting:** Prevents abuse (DoS, brute force)
- **CORS:** Prevents unauthorized cross-origin requests
- **Tenant isolation:** Rate limits per tenant
- **No secrets:** Rate limiter is in-memory (no external dependencies)

## 9. Performance

- **Rate limiter:** O(1) per request (token bucket check)
- **CORS:** O(1) per request (origin lookup)
- **Memory:** O(N) where N = number of active tenants
- **No Redis dependency:** Rate limiter is in-memory

## 10. Testing

- **NewRateLimiterWithClock:** Custom time source for testing
- **FakeStore:** Mock tenant identification
- **Test cases:** Rate limit exceeded, CORS preflight, origin allowed/blocked

## 11. Alternative Design

**Alternative 1: Redis-based rate limiting**
- Distributed, but requires Redis
- Rejected because: single-process rate limiting is sufficient

**Alternative 2: IP-based rate limiting**
- Simpler, but breaks behind NAT/load balancers
- Rejected because: tenant-based is more accurate

**Alternative 3: No CORS (server-side only)**
- Simpler, but breaks browser-based clients
- Rejected because: CORS is required for web applications

## 12. Learning Questions

### Beginner
1. What is token bucket rate limiting?
2. How does per-tenant rate limiting work?
3. What is CORS and why is it needed?
4. What is a preflight request?
5. How does the rate limiter handle bursts?
6. What happens when the rate limit is exceeded?
7. How does the CORS middleware handle OPTIONS requests?
8. What is the Retry-After header?
9. How does the rate limiter identify tenants?
10. What is the default rate limit configuration?

### Intermediate
1. Trace the complete rate limiting flow from request to response.
2. Explain the token bucket algorithm in detail.
3. How does the CORS middleware handle preflight requests?
4. Analyze the memory footprint of the rate limiter.
5. What is the impact of burst size on rate limiting?
6. How would you implement distributed rate limiting?
7. Explain the CORS configuration options.
8. How does the rate limiter handle tenant ID changes?
9. What is the performance impact of rate limiting?
10. Design a monitoring dashboard for rate limiting.

### Advanced
1. Design a distributed rate limiting system with Redis.
2. Analyze the tradeoffs between token bucket and sliding window.
3. How would you implement rate limiting for WebSocket connections?
4. Design a rate limiting system that adapts to load.
5. Analyze the security implications of rate limiting.
6. How would you implement rate limiting for a multi-region system?
7. Design a rate limiting system that supports priority queues.
8. Analyze the performance impact of CORS on high-traffic sites.
9. How would you implement rate limiting for GraphQL APIs?
10. Design a rate limiting system that supports user-defined rules.

## 13. Implementation Exercise

**Exercise: Implement a simplified rate limiter**

Requirements:
1. Implement `RateLimiter` with `Allow` method
2. Implement token bucket algorithm
3. Implement per-tenant isolation
4. Implement burst support
5. Write tests for all operations

Constraints:
- Do NOT look at `ratelimit.go` while implementing
- Use the same interface pattern
- Implement at least 5 test cases
- Handle concurrent access safely

---

# Feature 10: Object Storage Abstraction

## 1. Feature Overview

**What it does:** Abstracts S3/MinIO for blob storage with pre-signed URL generation, upload, download, delete, and integrity checks. Supports region-specific storage classes and lifecycle policies.

**Who uses it:** Ingress server (pre-signed URLs), GC engine (blob deletion), derivation workers (block reading).

**What problem it solves:** Vendor-agnostic blob storage with consistent API.

**Important business rules:**
- Pre-signed URLs: 15-minute expiry
- Integrity checks: ETag verification on upload/download
- Storage classes: STANDARD, STANDARD_IA, GLACIER (region-specific)
- Lifecycle policies: transition to IA after 30d, Glacier after 90d

## 2. Entry Point

- **Pre-signed URL:** `internal/objectstorage/s3.go:S3Client.GenerateUploadURL`
- **Upload:** `internal/objectstorage/s3.go:S3Client.Upload`
- **Delete:** `internal/objectstorage/s3.go:S3Client.Delete`
- **Verify:** `internal/objectstorage/s3.go:S3Client.VerifyBlock`

## 3. Complete Execution Trace

### Pre-Signed URL Generation

```
GenerateUploadURL(ctx, tenantID, blockHash, sizeBytes)
  │
  ├─ Determine storage class based on region
  │   └─ us-east-1: STANDARD, eu-west-1: STANDARD_IA, etc.
  │
  ├─ Build S3 key: {tenantID}/{blockHash}
  │
  ├─ Create S3 presigned PutObject request
  │   ├─ Bucket: configured bucket
  │   ├─ Key: {tenantID}/{blockHash}
  │   ├─ Expires: 15 minutes
  │   └─ ContentType: application/octet-stream
  │
  └─ Return presigned URL string
```

### Block Verification

```
VerifyBlock(ctx, blockHash, expectedETag)
  │
  ├─ HeadObject(ctx, bucket, key)
  │   └─ Get ETag from S3 response
  │
  ├─ Compare ETag with expectedETag
  │   ├─ Match: return nil (verified)
  │   └─ Mismatch: return error (integrity failure)
```

## 4. Data Flow

```
TenantID + BlockHash + SizeBytes
  → S3 key: {tenantID}/{blockHash}
  → Presigned PutObject URL (15min expiry)
  → Client uploads directly to S3
  → S3 returns ETag
  → VerifyBlock: HeadObject → compare ETag
```

## 5. Architecture

**ObjectStorageClient interface** (storage.go): Abstracts all blob operations.
- `GenerateUploadURL` — presigned upload URL
- `Upload` — direct upload
- `Download` — direct download
- `Delete` — blob deletion
- `VerifyBlock` — ETag verification
- `GetPresignedURL` — presigned download URL

**S3Client** (s3.go): AWS S3 implementation.
- Uses AWS SDK v2
- Region-specific storage classes
- Lifecycle policies

**MinIOClient** (minio.go): MinIO implementation.
- Uses MinIO Go client
- For development/edge deployments

## 6. Design Decisions

**Why pre-signed URLs instead of proxy?**
- Server never handles file bytes (constant memory)
- S3 handles durability/availability
- Supports arbitrarily large files
- Tradeoff: more complex auth (HMAC tokens)

**Why ETag verification?**
- Confirms bit-perfect upload
- Detects corruption during upload
- Tradeoff: extra HeadObject call (best-effort)

**Why region-specific storage classes?**
- Cost optimization (IA/Glacier cheaper)
- Compliance (data residency requirements)
- Tradeoff: more complex configuration

## 7. Failure Scenarios

| Scenario | What Happens |
|----------|-------------|
| **S3 down** | Pre-signed URL generation fails → 500 |
| **Upload fails** | Client retries (server not involved) |
| **ETag mismatch** | VerifyBlock returns error → logged |
| **Bucket not found** | S3 returns error → 500 |
| **Permission denied** | S3 returns error → 500 |

## 8. Security

- **Pre-signed URLs:** Time-limited, single-use
- **No server-side bytes:** Server never sees file content
- **Bucket policies:** Least-privilege access
- **Encryption at rest:** S3 SSE-S3/KMS

## 9. Performance

- **Pre-signed URL:** ~1ms generation
- **Upload:** Direct to S3 (bypasses server)
- **Download:** Direct from S3 (bypasses server)
- **Verification:** ~100ms (HeadObject call)

## 10. Testing

- **MinIO:** Local development stack
- **Mock S3:** For unit tests
- **Integration tests:** Full upload/download cycle

## 11. Alternative Design

**Alternative 1: Proxy uploads**
- Simpler, but server memory scales with file size
- Rejected because: pre-signed URLs are more efficient

**Alternative 2: Local filesystem**
- Simpler, but no durability/availability
- Rejected because: S3 provides 11 9's durability

**Alternative 3: Custom blob storage**
- More control, but reimplementation of S3 features
- Rejected because: S3 is mature and reliable

## 12. Learning Questions

### Beginner
1. What is the ObjectStorageClient interface?
2. How do pre-signed URLs work?
3. What is an ETag and why is it verified?
4. What are storage classes and lifecycle policies?
5. How does the S3Client differ from MinIOClient?
6. What happens when S3 is unavailable?
7. How does the server avoid handling file bytes?
8. What is the pre-signed URL expiry time?
9. How does the GC engine delete blobs?
10. What is the bucket naming convention?

### Intermediate
1. Trace the complete upload flow from pre-signed URL to verification.
2. Explain the region-specific storage class logic.
3. How do lifecycle policies affect cost?
4. Analyze the consistency guarantees of S3 uploads.
5. What is the impact of pre-signed URL expiry on uploads?
6. How would you implement multi-region blob storage?
7. Explain the MinIO configuration options.
8. How does the S3Client handle concurrent uploads?
9. What is the memory footprint of the S3Client?
10. Design a monitoring dashboard for blob storage.

### Advanced
1. Design a distributed blob storage system with consistency guarantees.
2. Analyze the tradeoffs between S3 and custom blob storage.
3. How would you implement blob storage with erasure coding?
4. Design a blob storage system that supports partial uploads.
5. Analyze the security implications of pre-signed URLs.
6. How would you implement blob storage for a multi-region system?
7. Design a blob storage system that supports content-defined encryption.
8. Analyze the performance impact of blob storage on foreground traffic.
9. How would you implement blob storage for streaming data?
10. Design a blob storage system that supports compliance retention.

## 13. Implementation Exercise

**Exercise: Implement a simplified blob storage client**

Requirements:
1. Implement `ObjectStorageClient` interface with local filesystem
2. Implement pre-signed URL generation (HTTP server)
3. Implement upload/download/delete
4. Implement integrity checks (checksum)
5. Write tests for all operations

Constraints:
- Do NOT look at `s3.go` while implementing
- Use the same interface pattern
- Implement at least 5 test cases
- Handle concurrent access safely

---

# Feature 11: Observability Stack

## 1. Feature Overview

**What it does:** Comprehensive observability via Prometheus metrics, structured logging, and distributed tracing. Metrics cover ingestion, database, CAS, and GC operations. Structured logs provide audit trails. Jaeger integration enables request tracing.

**Who uses it:** Operators, developers, SRE teams.

**What problem it solves:** Visibility into system behavior for debugging, optimization, and alerting.

**Important business rules:**
- Prometheus metrics: counters, histograms, gauges
- Structured JSON logging: request/response, errors, timing
- Slow query logging: >50ms threshold
- Health checks: /healthz, /readyz, /startupz

## 2. Entry Point

- **Metrics:** `internal/ingress/metrics.go:NewIngestMetrics` — ingestion metrics
- **DB metrics:** `internal/database/metrics.go:newMetrics` — database metrics
- **CAS metrics:** `internal/cas/metrics.go:NewCASMetrics` — CAS metrics
- **Logging:** `internal/ingress/server.go:LoggingMiddleware` — request logging

## 3. Complete Execution Trace

### Metrics Collection

```
Request arrives:
  │
  ├─ LoggingMiddleware
  │   ├─ Start timer
  │   ├─ next.ServeHTTP(sw, r)
  │   └─ Log: method, path, status, duration_ms, remote
  │
  ├─ IngestMetrics
  │   ├─ InitiateTotal.WithLabelValues(status).Inc()
  │   ├─ InitiateLatency.WithLabelValues(status).Observe(duration)
  │   ├─ CASHitsTotal.Inc() (if dedup)
  │   ├─ UploadsTotal.Inc() (if new block)
  │   └─ QuotaExceeded.Inc() (if quota exceeded)
  │
  └─ DatabaseMetrics
      ├─ queryDuration.WithLabelValues(pool, op).Observe(duration)
      ├─ acquireWait.Observe(acquireDuration)
      ├─ acquired.Inc() / released.Inc()
      ├─ errors.WithLabelValues(pool, op).Inc() (on error)
      └─ slowQueries.Inc() (if duration > 50ms)
```

### Health Checks

```
GET /healthz → 200 {"status": "ok"}
GET /readyz → check store.Ping(ctx)
  ├─ Success: 200 {"status": "ready"}
  └─ Failure: 503 {"error": "database unreachable"}
```

## 4. Data Flow

```
Request → Metrics → Prometheus → Grafana dashboards
Request → Structured logs → JSON output → log aggregation
Request → Tracing → Jaeger → distributed trace visualization
```

## 5. Architecture

**Prometheus metrics:**
- `aegis_ingest_initiate_total` — initiate calls by status
- `aegis_ingest_commit_total` — commit calls by status
- `aegis_ingest_initiate_duration_seconds` — initiate latency
- `aegis_ingest_commit_duration_seconds` — commit latency
- `aegis_ingest_cas_hits_total` — CAS dedup hits
- `aegis_ingest_uploads_total` — new uploads
- `aegis_db_query_duration_seconds` — DB query latency
- `aegis_db_acquire_wait_seconds` — connection acquire wait
- `aegis_cas_total_blocks` — total CAS blocks
- `aegis_cas_gc_blocks_deleted_total` — GC blocks deleted

**Structured logging:**
- JSON format (slog.NewJSONHandler)
- Request/response logging
- Error logging with stack traces
- Slow query logging (>50ms)

**Health checks:**
- `/healthz` — liveness (is process alive?)
- `/readyz` — readiness (can accept traffic?)
- `/startupz` — startup (initialization complete?)

## 6. Design Decisions

**Why Prometheus over StatsD?**
- Pull-based (no push configuration)
- Rich query language (PromQL)
- Native Kubernetes integration
- Tradeoff: requires scraping endpoint

**Why structured JSON logging?**
- Machine-parseable
- Easy to filter/search
- Consistent format across components
- Tradeoff: harder to read manually

**Why slow query logging?**
- Identifies performance bottlenecks
- Alerts on degraded queries
- Tradeoff: more log volume

## 7. Failure Scenarios

| Scenario | What Happens |
|----------|-------------|
| **Prometheus down** | Metrics not scraped (buffered in Prometheus) |
| **Jaeger down** | Traces dropped (best-effort) |
| **Log aggregation down** | Logs lost (stdout only) |
| **Health check fails** | K8s restarts pod |

## 8. Security

- **Metrics:** No sensitive data in metric names/labels
- **Logs:** Sensitive data masked (DSN, passwords)
- **Health checks:** No authentication (internal only)

## 9. Performance

- **Metrics:** O(1) per request (counter/histogram update)
- **Logging:** O(1) per request (JSON serialization)
- **Tracing:** O(1) per request (span creation)

## 10. Testing

- **Prometheus:** Local development stack
- **Grafana:** Dashboard visualization
- **Jaeger:** Distributed tracing
- **Test cases:** Metric labels, log format, health check responses

## 11. Alternative Design

**Alternative 1: StatsD**
- Simpler, but pull-based is better for Kubernetes
- Rejected because: Prometheus is the standard

**Alternative 2: ELK stack**
- More powerful, but heavier
- Rejected because: structured JSON + Prometheus is sufficient

**Alternative 3: No tracing**
- Simpler, but debugging distributed systems is harder
- Rejected because: Jaeger provides critical visibility

## 12. Learning Questions

### Beginner
1. What are the three pillars of observability?
2. How do Prometheus metrics work?
3. What is structured JSON logging?
4. What is the difference between /healthz and /readyz?
5. How does slow query logging work?
6. What metrics are tracked for ingestion?
7. How does Jaeger distributed tracing work?
8. What is a histogram in Prometheus?
9. How do Grafana dashboards visualize metrics?
10. What is the purpose of the /metrics endpoint?

### Intermediate
1. Trace the complete metrics flow from request to Prometheus.
2. Explain the Prometheus pull-based model.
3. How does structured logging improve debugging?
4. Analyze the performance impact of metrics collection.
5. What is the retention policy for Prometheus metrics?
6. How would you implement custom metrics?
7. Explain the Grafana dashboard configuration.
8. How does Jaeger handle trace propagation?
9. What is the memory footprint of the metrics system?
10. Design a monitoring dashboard for system health.

### Advanced
1. Design a distributed tracing system without Jaeger.
2. Analyze the tradeoffs between pull and push metrics.
3. How would you implement metrics for a multi-region system?
4. Design a logging system that supports complex queries.
5. Analyze the security implications of metrics exposure.
6. How would you implement metrics for streaming data?
7. Design a tracing system that supports sampling.
8. Analyze the performance impact of tracing on high-traffic systems.
9. How would you implement metrics for serverless functions?
10. Design an observability system that supports AI-powered anomaly detection.

## 13. Implementation Exercise

**Exercise: Implement a simplified metrics system**

Requirements:
1. Implement `Metrics` struct with counter and histogram
2. Implement Prometheus-compatible output
3. Implement structured JSON logging
4. Implement health checks
5. Write tests for all operations

Constraints:
- Do NOT look at `metrics.go` while implementing
- Use the same interface pattern
- Implement at least 5 test cases
- Handle concurrent access safely

---

# Feature 12: SDK Client Library

## 1. Feature Overview

**What it does:** The `aegis/` package provides a Go client SDK for uploading files. It handles the complete upload lifecycle: initiate, parallel block upload with concurrency control, commit, and session persistence for pause/resume.

**Who uses it:** Go applications uploading files to Aegis.

**What problem it solves:** Simplifies client-side upload logic with automatic retry, parallel uploads, and pause/resume support.

**Important business rules:**
- Parallel uploads with semaphore (max 10 concurrent)
- Exponential backoff retry on transient errors
- Pause/resume via SessionState JSON persistence
- ReadAt for concurrent file reads (no file locking)

## 2. Entry Point

- **Upload:** `aegis/client.go:Client.UploadFile` — main upload method
- **Parallel upload:** `aegis/upload.go:parallelUpload` — concurrent block upload
- **Session persistence:** `aegis/session.go:SaveSession/LoadSession` — pause/resume

## 3. Complete Execution Trace

```
Client.UploadFile(ctx, filePath, tenantID, parentID)
  │
  ├─ [1] Open file, compute SHA-256
  │   ├─ os.Open(filePath)
  │   ├─ io.ReadAll → compute SHA-256
  │   └─ fastcdc.Split → []Chunk (content-defined chunks)
  │
  ├─ [2] Initiate upload
  │   └─ POST /api/v1/ingest/initiate
  │       └─ Returns: sessionID, uploadURLs
  │
  ├─ [3] Parallel block upload
  │   └─ parallelUpload(ctx, file, uploadURLs, maxConcurrency=10)
  │       ├─ semaphore := make(chan struct{}, 10)
  │       ├─ For each uploadURL:
  │       │   ├─ semaphore <- struct{}{} (acquire)
  │       │   ├─ go func() {
  │       │   │   ├─ file.ReadAt(buf, offset) → read block
  │       │   │   ├─ HTTP PUT uploadURL → buf
  │       │   │   ├─ Retry on transient error (exponential backoff)
  │       │   │   └─ semaphore <- struct{}{} (release)
  │       │   │ }()
  │       └─ Wait for all goroutines
  │
  ├─ [4] Commit upload
  │   └─ POST /api/v1/ingest/commit
  │       └─ Returns: versionID, versionNumber
  │
  └─ [5] Return Result{VersionID, VersionNumber}
```

### Pause/Resume

```
Pause:
  ├─ SaveSession(ctx, filePath, sessionState)
  │   └─ json.Marshal(sessionState) → write to file
  └─ return

Resume:
  ├─ LoadSession(ctx, filePath)
  │   └─ read file → json.Unmarshal → SessionState
  ├─ Initiate (with existing sessionID if valid)
  ├─ Skip already-uploaded blocks
  ├─ Upload remaining blocks
  └─ Commit
```

## 4. Data Flow

```
FilePath → os.Open → io.ReaderAt
  → fastcdc.Split → []Chunk{Offset, Size, Hash}
  → POST /api/v1/ingest/initiate → InitiateResponse{SessionID, UploadURLs}
  → parallelUpload → HTTP PUT to each URL
  → POST /api/v1/ingest/commit → CommitResponse{VersionID, VersionNumber}
  → Result{VersionID, VersionNumber}
```

## 5. Architecture

**Client** (aegis/client.go): Main SDK entry point.
- `UploadFile` — main upload method
- `newRequest` — HTTP request helper

**parallelUpload** (aegis/upload.go): Concurrent block upload.
- Semaphore for concurrency control
- ReadAt for concurrent file reads
- Retry with exponential backoff

**SessionState** (aegis/session.go): Pause/resume persistence.
- `SaveSession` — persist to file
- `LoadSession` — restore from file

## 6. Design Decisions

**Why parallel uploads?**
- Large files benefit from parallelism
- S3 supports concurrent uploads
- Tradeoff: more complex error handling

**Why ReadAt instead of Seek?**
- ReadAt is safe for concurrent use (no file locking)
- Multiple goroutines can read different offsets simultaneously
- Tradeoff: requires buffer allocation per read

**Why pause/resume?**
- Large uploads may be interrupted
- Resuming from scratch wastes bandwidth
- Tradeoff: requires session persistence

**Why exponential backoff?**
- Prevents thundering herd on retry
- Handles transient failures gracefully
- Tradeoff: delayed retry for permanent failures

## 7. Failure Scenarios

| Scenario | What Happens |
|----------|-------------|
| **File not found** | Return error immediately |
| **Network error** | Retry with backoff → fail after max retries |
| **S3 error** | Retry with backoff → fail after max retries |
| **Session expired** | Re-initiate upload |
| **Invalid hash** | Server returns 400 → return error |

## 8. Security

- **Authentication:** Bearer token in request header
- **Authorization:** Tenant ID from token
- **No secrets in SDK:** Token passed by caller
- **HTTPS:** All communication encrypted

## 9. Performance

- **Parallel uploads:** 10 concurrent blocks (configurable)
- **ReadAt:** O(1) per read (no seeking)
- **Retry:** Exponential backoff (1s, 2s, 4s, ...)
- **Session persistence:** O(1) JSON write

## 10. Testing

- **Mock server:** HTTP test server for SDK tests
- **Test files:** Known test vectors
- **Integration tests:** Full upload cycle

## 11. Alternative Design

**Alternative 1: Sequential uploads**
- Simpler, but slower for large files
- Rejected because: parallel uploads are significantly faster

**Alternative 2: Streaming uploads**
- No file seeking, but more complex
- Rejected because: ReadAt is simpler and sufficient

**Alternative 3: No pause/resume**
- Simpler, but wastes bandwidth on interruption
- Rejected because: pause/resume is critical for large files

## 12. Learning Questions

### Beginner
1. What is the Aegis SDK and what does it do?
2. How does parallel upload work?
3. What is pause/resume and why is it needed?
4. How does the SDK handle retries?
5. What is ReadAt and why is it used?
6. How does the SDK compute file hashes?
7. What is the semaphore pattern?
8. How does the SDK handle large files?
9. What is the default concurrency limit?
10. How does the SDK authenticate with the server?

### Intermediate
1. Trace the complete upload flow from file to commit.
2. Explain the parallel upload algorithm in detail.
3. How does the SDK handle transient vs permanent errors?
4. Analyze the performance impact of concurrency limits.
5. What is the memory footprint of the SDK?
6. How would you implement resumable uploads?
7. Explain the session persistence format.
8. How does the SDK handle very large files (>10GB)?
9. What is the impact of chunk size on upload performance?
10. Design a monitoring dashboard for SDK uploads.

### Advanced
1. Design a distributed upload system with consistency guarantees.
2. Analyze the tradeoffs between parallel and sequential uploads.
3. How would you implement upload for a multi-region system?
4. Design an SDK that supports custom chunking strategies.
5. Analyze the security implications of pre-signed URLs in the SDK.
6. How would you implement upload for streaming data?
7. Design an SDK that supports upload priority queues.
8. Analyze the performance impact of retry on upload throughput.
9. How would you implement upload for a serverless environment?
10. Design an SDK that supports user-defined upload policies.

## 13. Implementation Exercise

**Exercise: Implement a simplified upload SDK**

Requirements:
1. Implement `Client` with `UploadFile` method
2. Implement parallel upload with semaphore
3. Implement retry with exponential backoff
4. Implement session persistence
5. Write tests for all operations

Constraints:
- Do NOT look at `client.go` while implementing
- Use the same interface pattern
- Implement at least 5 test cases
- Handle concurrent access safely

---

# Feature 13: Service Wiring & Lifecycle

## 1. Feature Overview

**What it does:** The `cmd/ingest/main.go` wires together all components: database, Redis, auth, store, events, metrics, object storage, derivation workers, GC pipeline, and HTTP server. It handles graceful shutdown, signal handling, and component lifecycle.

**Who uses it:** Operators deploying the service.

**What problem it solves:** Single binary deployment with all components wired together.

**Important business rules:**
- Graceful shutdown on SIGINT/SIGTERM
- Component lifecycle: start order matters (DB → Redis → Auth → Store → Events → Workers → GC → HTTP)
- Environment variable configuration (20+ variables)
- Optional components (derivation, S3/MinIO)

## 2. Entry Point

- **Service start:** `cmd/ingest/main.go:main`
- **Graceful shutdown:** Signal handler + `httpSrv.Shutdown`

## 3. Complete Execution Trace

```
main()
  │
  ├─ [1] Logger setup
  │   └─ slog.NewJSONHandler(os.Stdout, level)
  │
  ├─ [2] Signal handling
  │   └─ signal.NotifyContext(ctx, SIGINT, SIGTERM)
  │
  ├─ [3] Database
  │   └─ database.NewDatabaseClient(ctx, cfg, logger)
  │       └─ Creates 4 pool tiers + circuit breaker + cache
  │
  ├─ [4] Redis (nonces)
  │   └─ redis.NewClient(&redis.Options{...})
  │
  ├─ [5] Auth (HMAC tokens)
  │   ├─ auth.NewStaticKMS(time.Now)
  │   ├─ kms.Provision(uuid.Nil, 1, make([]byte, 32))
  │   ├─ auth.NewRedisNonceStore(rdb)
  │   └─ auth.NewTokenGenerator(kms, nonces, ...)
  │
  ├─ [6] Store (pgx adapter)
  │   └─ ingress.NewPgStore(dbClient, logger)
  │
  ├─ [7] Events (noop or Kafka)
  │   └─ var events ingress.EventBus = &ingress.NoopBus{...}
  │
  ├─ [8] Metrics
  │   └─ ingress.NewIngestMetrics(reg)
  │
  ├─ [9] Object storage (optional)
  │   ├─ S3: objectstorage.NewS3Client(ctx, cfg, logger)
  │   └─ MinIO: objectstorage.NewMinIOClient(cfg, logger)
  │
  ├─ [10] Derivation pipeline (optional)
  │   ├─ workers.NewClamAVWorker(...)
  │   ├─ workers.NewOCRWorker(...)
  │   ├─ workers.NewFFmpegWorker(...)
  │   ├─ workers.NewVectorEmbedWorker(...)
  │   ├─ derivation.NewWorkerPool(workers, cfg)
  │   └─ newDerivationBridge(pool, logger)
  │
  ├─ [11] Ingress server
  │   └─ ingress.NewIngressServer(store, signer, blob, events, metrics, cfg, logger)
  │
  ├─ [12] Session reaper
  │   └─ srv.StartSessionReaper(interval)
  │
  ├─ [13] CAS metrics + GC pipeline
  │   ├─ cas.NewCASMetrics(reg)
  │   ├─ cas.NewPgRegistryFromClient(dbClient, logger)
  │   ├─ gc.New(store, publisher, blob, metrics, logger, cfg)
  │   └─ gcPipeline.Start(ctx)
  │
  ├─ [14] HTTP server
  │   ├─ mux := http.NewServeMux()
  │   ├─ srv.SetupRoutes(mux)
  │   ├─ mux.Handle("GET /metrics", promhttp.Handler())
  │   └─ httpSrv := &http.Server{...}
  │
  └─ [15] Graceful shutdown
      ├─ <-ctx.Done()
      ├─ httpSrv.Shutdown(shutdownCtx)
      ├─ gcPipeline.Stop()
      ├─ derivationBridge.Stop()
      └─ dbClient.Close()
```

## 4. Data Flow

```
Environment variables → Config structs → Component construction
  → Component wiring → HTTP server → Request handling
  → Graceful shutdown → Component teardown
```

## 5. Architecture

**Components:**
- **Database:** PostgreSQL connection pools + circuit breaker + cache
- **Redis:** Nonce store + cache backend
- **Auth:** HMAC token generator + KMS + nonce store
- **Store:** PostgreSQL adapter for ingress operations
- **Events:** Event bus (noop or Kafka)
- **Metrics:** Prometheus collectors
- **Object Storage:** S3/MinIO client
- **Derivation:** Worker pool + tool implementations
- **GC:** Garbage collection pipeline
- **HTTP:** Server with middleware chain

## 6. Design Decisions

**Why single binary?**
- Simpler deployment (one binary, one container)
- No inter-service communication overhead
- Tradeoff: harder to scale individual components

**Why environment variables?**
- 12-factor app compliance
- Kubernetes-friendly (ConfigMaps, Secrets)
- Tradeoff: more configuration surface

**Why optional components?**
- Development: minimal dependencies (noop events, no S3)
- Production: full stack (Kafka, S3, derivation)
- Tradeoff: more configuration complexity

## 7. Failure Scenarios

| Scenario | What Happens |
|----------|-------------|
| **DB init fails** | Service exits immediately |
| **Redis init fails** | Service exits immediately |
| **S3 init fails** | Service exits immediately |
| **Signal received** | Graceful shutdown initiated |
| **Component panic** | Caught by middleware recovery |

## 8. Security

- **Secrets:** Environment variables (Kubernetes Secrets)
- **No hardcoded secrets:** All secrets from environment
- **Graceful shutdown:** In-flight requests complete

## 9. Performance

- **Startup time:** ~1-2 seconds (pool warm-up)
- **Shutdown time:** ~10 seconds (grace period)
- **Memory:** Pool connections + cache + metrics

## 10. Testing

- **Integration tests:** Full stack with Docker Compose
- **Unit tests:** Individual components
- **Chaos tests:** Component failures

## 11. Alternative Design

**Alternative 1: Microservices**
- More scalable, but more complex
- Rejected because: monolith is simpler for this scale

**Alternative 2: Configuration file**
- More structured, but less Kubernetes-friendly
- Rejected because: environment variables are the standard

**Alternative 3: Plugin architecture**
- More extensible, but more complex
- Rejected because: compile-time composition is simpler

## 12. Learning Questions

### Beginner
1. What components are wired together in main.go?
2. How does graceful shutdown work?
3. What environment variables are required?
4. How does the service handle signals?
5. What is the startup order of components?
6. How does the session reaper work?
7. What is the CAS metrics refresh interval?
8. How does the HTTP server handle requests?
9. What is the grace period for shutdown?
10. How does the service handle optional components?

### Intermediate
1. Trace the complete startup flow from main() to listening.
2. Explain the component dependency graph.
3. How does the service handle component failures during startup?
4. Analyze the memory footprint of all components.
5. What is the impact of pool sizing on startup time?
6. How would you implement hot reload of configuration?
7. Explain the signal handling logic.
8. How does the service handle in-flight requests during shutdown?
9. What is the monitoring strategy for all components?
10. Design a deployment pipeline for the service.

### Advanced
1. Design a zero-downtime deployment strategy.
2. Analyze the tradeoffs between monolith and microservices for this system.
3. How would you implement multi-region deployment?
4. Design a configuration management system for all components.
5. Analyze the security implications of environment variable configuration.
6. How would you implement canary deployments?
7. Design a service mesh integration for the ingress server.
8. Analyze the performance impact of all components on request latency.
9. How would you implement serverless deployment?
10. Design a chaos engineering strategy for all components.

## 13. Implementation Exercise

**Exercise: Implement a simplified service wiring**

Requirements:
1. Implement `main()` with component construction
2. Implement graceful shutdown
3. Implement signal handling
4. Implement health checks
5. Write tests for startup/shutdown

Constraints:
- Do NOT look at `main.go` while implementing
- Use the same environment variable pattern
- Implement at least 5 test cases
- Handle component failures gracefully

---

# Appendix: Cross-Feature Relationships

```
┌─────────────────────────────────────────────────────────────────┐
│                    CLIENT (aegis SDK)                            │
│  UploadFile → Initiate → PUT blocks → Commit → Derive          │
└──────────┬──────────────────────────────────────┬───────────────┘
           │                                      │
           ▼                                      ▼
┌──────────────────────┐           └──────────────────────────────┐
│   INGRESS SERVER     │           │    DERIVATION WORKERS        │
│  - Auth (HMAC)       │           │  - EventConsumer             │
│  - Rate Limiting     │◄──────────│  - PipelineRegistry          │
│  - CORS              │  publish  │  - ToolExecutor              │
│  - Audit Logging     │           │  - Retry/DLQ                 │
│  - Initiate/Commit   │           └──────────────┬───────────────┘
└──────────┬───────────┘                          │
           │                                      ▼
           │                           ┌──────────────────────────┐
           │                           │   DERIVATION RESULTS     │
           │                           │  (namespace_nodes with   │
           │                           │   derived_from edges)    │
           │                           └──────────────────────────┘
           │
           ▼
┌──────────────────────────────────────────────────────────────────┐
│                       STORAGE LAYER                              │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────────────┐   │
│  │  PostgreSQL   │  │    Redis     │  │   S3 / MinIO         │   │
│  │  - Namespace  │  │  - Cache     │  │  - CAS Blocks        │   │
│  │  - Versions   │  │  - Bloom     │  │  - Lifecycle         │   │
│  │  - Sessions   │  │  - Sessions  │  │  - Integrity Checks  │   │
│  │  - Audit      │  │  - Nonces    │  │  - Storage Classes   │   │
│  │  - GC         │  │  - Rate Limit│  │                      │   │
│  └──────────────┘  └──────────────┘  └──────────────────────┘   │
└──────────────────────────────────────────────────────────────────┘
```

---

*This analysis was generated by reverse-engineering the Aegis repository across all source files. It represents the current state of the codebase as of August 2026.*
