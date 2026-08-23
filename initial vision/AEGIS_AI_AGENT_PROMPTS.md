# PROJECT AEGIS: SYSTEMATIC AI AGENT PROMPTS FOR PRODUCTION IMPLEMENTATION
## Complete Step-by-Step Blueprint with Quality Assurance at Every Stage

---

## PHASE 1: FOUNDATION & ARCHITECTURAL SETUP

### PROMPT 1.1: Core Architecture & Design Document
**Context & Objective:**
You are architecting a production-grade, exabyte-capable distributed storage engine called Project Aegis. This system must separate binary I/O operations from metadata state management to prevent resource starvation. Read and internalize the complete Project Aegis specification. Your first task is to create a comprehensive architectural validation document.

**Task:**
- [ ] Create a detailed system architecture diagram (text-based, structured markdown) showing:
  - The dual-plane separation (Data Plane vs. Control/Metadata Plane)
  - Every component and its responsibilities
  - The three immutable invariants and how they're enforced
  - Data flow paths for upload, commit, retrieval, and GC operations
  - Cross-plane communication boundaries
  
- [ ] For each of the three invariants (Bit-Perfect CAS, Acyclic Namespace, Cryptographic Ingress), create:
  - A formal definition in first-order logic or pseudocode
  - How it's enforced in code/schema
  - Failure scenarios if violated and their consequences
  - Testing strategies to verify it's maintained

- [ ] Document all integration points:
  - Client ↔ Edge Layer
  - Edge ↔ Ingress Engine
  - Ingress ↔ Metadata Store
  - Metadata Store ↔ CAS
  - CAS ↔ Derivation Workers
  - Derivation Workers ↔ Event Bus
  
- [ ] Create a detailed SLA matrix with:
  - Every guarantee (Durability, Latency, RPO, RTO)
  - The architectural component responsible for each
  - How it's measured and verified
  - Fallback behavior if SLA is breached

**Acceptance Criteria:**
- Every component has a clear, single responsibility
- No circular dependencies between planes
- All data flows are acyclic and traceable
- Failure modes are documented for every component
- Integration contract between components is explicit

**Output Format:**
```markdown
# Project Aegis: Architectural Validation Document

## System Topology
[ASCII diagram with all components]

## Invariant 1: Bit-Perfect CAS
- Definition: [formal statement]
- Enforcement: [code paths]
- Testing: [test scenarios]

## Integration Points
- [Component A] → [Component B]: [contract]

## SLA Guarantees
- [Guarantee]: [Component Responsible]: [Verification Method]
```

---

### PROMPT 1.2: Development Environment & Dependency Selection
**Context & Objective:**
You are setting up the complete development and production environment for Project Aegis. Every dependency choice must be justified by the architectural requirements. Speed, safety, and correctness are non-negotiable.

**Task:**
- [ ] Create a complete `Cargo.toml` (Rust ingestion engine) with:
  - tokio (async runtime with work-stealing scheduler)
  - sha2 (SHA-256 cryptography)
  - serde + serde_json (serialization)
  - tonic + prost (gRPC for inter-service communication)
  - uuid (UUID generation)
  - tracing + tracing-subscriber (structured logging)
  - criterion (benchmarking)
  - proptest (property-based testing)
  - justification for each major dependency including:
    - Why this over alternatives
    - Version pinning strategy
    - Security considerations
    - Performance impact

- [ ] Create a complete `go.mod` (Go orchestration engine) with:
  - pgx (PostgreSQL driver with connection pooling)
  - google/uuid
  - gin or echo (HTTP routing)
  - gRPC libraries
  - Redis client
  - Kafka/Redpanda client
  - justification for each including memory and GC impact

- [ ] Create infrastructure-as-code manifests for:
  - PostgreSQL 16+ cluster (with ltree extension)
  - Redis/DragonflyDB cluster
  - Redpanda cluster
  - MinIO/S3 object storage
  - Include: networking, RBAC, encryption at rest, monitoring

- [ ] Document the complete CI/CD pipeline:
  - Lint checks (clippy for Rust, golangci-lint for Go)
  - Unit test suites with coverage targets (> 85%)
  - Integration tests against real PostgreSQL + MinIO
  - Performance benchmarking gates (p95 latency targets)
  - Security scanning (SAST, dependency vulnerability scanning)
  - Container image building and scanning

- [ ] Create environment configuration templates for:
  - Local development (in-memory stores allowed)
  - Integration testing (Docker Compose with single-node databases)
  - Staging (multi-region simulation)
  - Production (multi-AZ, replicated databases)

**Acceptance Criteria:**
- All dependencies have documented justifications
- No dependency contradicts the design invariants
- CI/CD pipeline catches regressions before production
- Every environment can be provisioned from templates
- Security baseline is met for all components

**Output Format:**
```toml
# Cargo.toml with detailed comments
[dependencies]
tokio = { version = "1.x", features = ["full"], doc = "Why tokio: Zero-copy work stealing..." }
```

---

## PHASE 2: DATABASE SCHEMA & RELATIONAL CONTROL PLANE

### PROMPT 2.1: PostgreSQL Schema Design with ltree Hierarchy
**Context & Objective:**
The metadata control plane is built on PostgreSQL 16+ with the ltree extension for hierarchical path management. This is the single source of truth for all file state, versioning, and access control. Schema design here is critical—poor design leads to N+1 queries, deadlocks, and SLA violations.

**Task:**
- [ ] Create a complete, production-ready PostgreSQL schema including:
  - All tables from the specification (tenants, namespace_nodes, file_versions, cas_blocks, file_manifest_blocks, upload_sessions)
  - Additional tables for:
    - Audit logs (who accessed what, when)
    - ACL (Access Control Lists) with role-based permissions
    - Version metadata (mime type, size, owner, last accessed)
    - Session metadata (client IP, user agent, retry counts)
  
  - For every table:
    - Explicit column constraints (NOT NULL, UNIQUE, FOREIGN KEY with ON DELETE CASCADE/RESTRICT)
    - Default values (NOW(), gen_random_uuid())
    - Comment annotations explaining purpose
    - Partition strategy if table exceeds 10GB

- [ ] Create all indexes with detailed reasoning:
  - `idx_namespace_lineage`: GIST index on ltree for ancestor queries
  - `idx_namespace_parent`: B-tree for fast sibling lookups
  - `idx_cas_tenant_created`: For generational GC sweeps
  - `idx_file_manifest_version`: For fast chunk lookups during reads
  - Document query patterns each index supports

- [ ] Create the ltree-based cycle detection system:
  - Document ltree operators: `<@` (is ancestor), `@>` (has descendant), `~` (matches pattern)
  - Write the `move_directory()` stored procedure exactly as specified
  - Add comments explaining the pessimistic locking strategy
  - Create test cases verifying cycle detection rejects invalid moves:
    - Moving /A/B into /A/B/C (self-containment)
    - Moving /root into /root/sub (creates ancestor loop)
    - Concurrent moves to same parent (no race condition)

- [ ] Create materialized view for fast access pattern queries:
  - Tenant storage usage (used_bytes aggregation)
  - Most recent versions per file
  - Chunk reference statistics (hot/cold storage tiers)
  - Query planning hints to prevent full table scans

- [ ] Design and implement transaction isolation strategy:
  - Document which operations require READ COMMITTED vs SERIALIZABLE
  - Create deadlock prevention guidelines
  - Write conflict resolution strategies for concurrent operations
  - Include retry logic with exponential backoff

- [ ] Create comprehensive SQL tests for:
  - Schema integrity (referential constraints)
  - ltree cycle detection with pathological inputs
  - Concurrent inserts to namespace_nodes (no duplicates)
  - Cascade deletes (file deletion → version deletion → block cleanup)
  - ACL epoch incrementing on directory moves

**Acceptance Criteria:**
- Schema enforces all invariants at the database level
- All queries have documented execution plans (EXPLAIN ANALYZE)
- No N+1 query patterns exist in the codebase
- Deadlock scenarios are documented with prevention strategies
- Index selectivity is > 95% for all queries
- Tests achieve 100% code coverage for schema logic

**Output Format:**
```sql
-- Project Aegis: PostgreSQL Schema
-- Version: 1.0
-- Invariants: Bit-Perfect CAS, Acyclic Namespace, Cryptographic Ingress

-- Enable ltree extension for hierarchical queries
CREATE EXTENSION IF NOT EXISTS ltree;

CREATE TABLE tenants (
  tenant_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  -- [Full schema with comments]
  CONSTRAINT check_storage_quota CHECK (storage_quota_bytes >= 0)
);

-- Indexes with query patterns documented
CREATE INDEX idx_namespace_lineage ON namespace_nodes USING GIST (lineage_path)
  -- Supports: Find all descendants of /A/B: SELECT * WHERE lineage_path <@ 'root.A.B'
  -- Supports: Check no ancestor of /new/path is in {set}: WHERE lineage_path @> ANY(...)
;

-- Stored procedures with invariant documentation
CREATE OR REPLACE FUNCTION move_directory(...) RETURNS VOID AS $$
  -- INVARIANT: After this procedure, the namespace graph remains acyclic
  -- INVARIANT: All descendants have updated lineage_path values
  -- INVARIANT: ACL epochs are incremented for cache invalidation
$$;
```

---

### PROMPT 2.2: Connection Pooling & Database Client Design
**Context & Objective:**
Database connections are expensive and finite. Poor pooling leads to connection exhaustion and SLA violations. The design must support:
- 1000+ concurrent client connections
- Sub-5ms query latency (p95)
- Automatic failover to replica on primary failure
- Explicit separation of metadata vs. analytical queries

**Task:**
- [ ] Design a connection pool architecture:
  - Create separate pools for:
    - WRITE operations (small pool, strict ordering)
    - READ operations (larger pool, load-balanced across replicas)
    - ANALYTICAL operations (isolated, doesn't contend with transactional)
  - Document pool sizing strategy:
    - Minimum connections: (number of CPU cores) * 2
    - Maximum connections: (number of CPU cores) * 4
    - Overflow behavior (queue or fail fast)
    - Connection timeout and validation strategies

- [ ] Implement in Go (pgx-based):
  ```go
  type DatabaseClient struct {
    writePool *pgxpool.Pool      // For INSERT/UPDATE/DELETE
    readPool  *pgxpool.Pool      // For SELECT (load-balanced)
    metadata  *pgxpool.Pool      // For control plane
    
    // Metrics for observability
    poolStats struct {
      connectionsAcquired uint64
      connectionsReleased uint64
      waitTimeP95         time.Duration
    }
  }
  ```

- [ ] Create query result caching layer:
  - Use Redis for caching namespace metadata (TTL: 5 minutes)
  - Cache invalidation on:
    - move_directory() calls
    - ACL epoch increments
    - file_versions inserts
  - Implement cache-aside pattern with lock-free reads

- [ ] Implement observability:
  - Query execution time histograms (p50, p95, p99)
  - Connection pool saturation metrics
  - Slow query logging (queries > 50ms)
  - Query error rate tracking per operation type
  - Implement structured logging: `{"operation": "move_directory", "tenant_id": "...", "duration_ms": 12}`

- [ ] Design failover strategy:
  - Detect primary failure via connection timeouts + health checks
  - Automatic redirect to read-only replica
  - Circuit breaker pattern to prevent cascade failures
  - Document RPO (Recovery Point Objective) and RTO guarantees

- [ ] Create comprehensive tests:
  - Connection pool under load (simulate 1000 concurrent clients)
  - Query behavior under connection exhaustion
  - Failover behavior during primary failure
  - Cache coherency under concurrent writes
  - Connection leak detection

**Acceptance Criteria:**
- Connection pool never exhausts under design load
- p95 query latency stays < 5ms
- Cache invalidation is immediate (no stale reads)
- Failover is automatic and transparent
- No connection leaks in long-running processes

**Output Format:**
```go
// project-aegis/internal/database/client.go

type DatabaseClient struct {
  // Connection pools with metrics
  // Query result caching
  // Failover logic
}

// Query execution with observability
func (c *DatabaseClient) QueryWithMetrics(ctx context.Context, query string, args ...interface{}) (*pgx.Rows, error) {
  // Measure latency
  // Track results
  // Handle failover
}
```

---

## PHASE 3: CORE ALGORITHMS & CRYPTOGRAPHIC COMPONENTS

### PROMPT 3.1: FastCDC Implementation (Rust) with SIMD Optimization
**Context & Objective:**
FastCDC is the deduplication engine. Gear-hash fingerprinting enables content-aware chunk boundaries that shift with modifications. This single component can 10x the deduplication ratio vs. fixed-size chunks. Performance is critical: must process > 1GB/s on modern CPUs.

**Task:**
- [ ] Implement the complete FastCDC algorithm in Rust:
  ```rust
  pub struct FastCDCStream<R: Read> {
    reader: R,
    buffer: Vec<u8>,
    global_offset: u64,
    fingerprint: u32,
    gear_matrix: &'static [u32; 256],
  }
  
  impl<R: Read> FastCDCStream<R> {
    pub fn next_chunk(&mut self) -> io::Result<Option<ChunkDescriptor>> {
      // Two-phase masking (sub-average + post-average)
      // Dynamic cut-point detection
      // SHA-256 hashing of chunk
    }
  }
  ```

- [ ] Implement SIMD optimizations:
  - Use `packed_simd` for AVX-512 / ARM NEON where available
  - Vectorize the gear-matrix lookups (process 4-8 bytes per iteration)
  - Benchmark: should achieve 2-4GB/s on modern CPUs
  - Graceful fallback to scalar implementation on unsupported hardware

- [ ] Implement the exact two-phase masking algorithm:
  - **Phase 1 (Sub-average)**: MIN_CHUNK (64KB) to AVG_CHUNK (1MB) with MASK_S (0x0003_FFFF)
  - **Phase 2 (Post-average)**: AVG_CHUNK (1MB) to MAX_CHUNK (4MB) with MASK_L (0x0007_FFFF)
  - Document why two masks: sub-average finds boundaries faster, post-average ensures max chunk size
  - Verify fingerprint computation: `fingerprint = (fingerprint << 1).wrapping_add(GEAR_MATRIX[byte])`

- [ ] Create chunk descriptor output:
  ```rust
  pub struct ChunkDescriptor {
    pub hash: [u8; 32],           // SHA-256 of chunk
    pub offset: u64,              // Byte offset in input stream
    pub length: usize,            // Chunk size
    pub content_features: u32,    // Optional: min/max/entropy for analytics
  }
  ```

- [ ] Handle edge cases:
  - Files smaller than MIN_CHUNK: emit single chunk
  - Empty files: return None
  - Extremely repetitive data (runs of identical bytes): mask still works correctly
  - Chunk stream ending mid-buffer: flush remaining bytes as final chunk

- [ ] Create comprehensive benchmarks:
  ```rust
  #[bench]
  fn bench_fastcdc_1gb_random(b: &mut Bencher) { ... }
  #[bench]
  fn bench_fastcdc_with_modifications(b: &mut Bencher) { ... }
  #[bench]
  fn bench_fastcdc_vs_fixed_size_deduplication(b: &mut Bencher) { ... }
  ```
  - Verify 2-4GB/s throughput
  - Verify > 50% better deduplication vs. fixed-size chunks
  - Measure SIMD speedup over scalar implementation

- [ ] Create comparative tests:
  - Same file with single 10MB insertion: FastCDC should rehash < 10% of content vs. fixed-size 100%
  - Identical files from two clients: FastCDC should produce identical chunks
  - Random binary data: verify chunk size distribution (should cluster around 1MB)

- [ ] Create the Gear Matrix generation tool:
  - Document the pseudorandom number generator used
  - Provide way to verify Gear Matrix is correct
  - Include alternative Gear Matrices for different chunk size targets

**Acceptance Criteria:**
- FastCDC throughput >= 2GB/s on i7/Ryzen
- Deduplication ratio > 50% better than fixed-size chunks
- Chunk boundaries are content-aware (verified by modification tests)
- No crashes on any input (fuzz tested)
- SIMD optimization provides > 2x speedup over scalar

**Output Format:**
```rust
// project-aegis/crates/fastcdc/src/lib.rs

pub const MIN_CHUNK: usize = 64 * 1024;      // 64 KB
pub const AVG_CHUNK: usize = 1024 * 1024;    // 1 MB
pub const MAX_CHUNK: usize = 4 * 1024 * 1024; // 4 MB
pub const MASK_S: u32 = 0x0003_FFFF;         // Sub-average threshold
pub const MASK_L: u32 = 0x0007_FFFF;         // Post-average threshold

// Gear matrix (256-entry lookup table)
static GEAR_MATRIX: [u32; 256] = [...];

pub struct FastCDCStream<R: Read> {
  // Implementation with SIMD support
}

#[cfg(test)]
mod tests {
  // Verification tests for two-phase masking
  // Content-aware boundary tests
  // SIMD correctness tests
}
```

---

### PROMPT 3.2: Cryptographic Ingress & HMAC Token System
**Context & Objective:**
Pre-signed URLs with HMAC-SHA256 validation must prevent unauthorized uploads. Tokens are scoped to:
- Single tenant (tenant_id)
- Single block (block_hash)
- Time window (15-minute TTL)
- Specific storage endpoint (edge PoP)

This is the security perimeter. Any weakness here breaks multi-tenancy isolation.

**Task:**
- [ ] Design the HMAC token format:
  ```
  Token = HMAC_SHA256(
    key=KMS_key_for_tenant,
    message=fmt!("{}:{}:{}:{}", tenant_id, block_hash, timestamp, endpoint_id),
    digest=hex(result)
  )
  
  Pre-signed URL = https://blob-storage/chunks/{block_hash}?tenant={tenant_id}&ts={timestamp}&sig={token}
  ```

- [ ] Implement token generation in Go:
  ```go
  func (s *IngressServer) generatePreSignedURL(
    ctx context.Context,
    tenantID uuid.UUID,
    blockHash string,
    endpointID string,
  ) (string, error) {
    // Fetch tenant KMS key from key vault
    // Generate timestamp (now + 15 minutes)
    // Compute HMAC
    // Return signed URL
  }
  ```

- [ ] Implement token validation at edge:
  - Cloudflare Worker or Lambda@Edge validates HMAC
  - Re-computes: `HMAC_SHA256(kms_key, message)` 
  - Compares against submitted signature (constant-time comparison)
  - Rejects if: signature mismatch OR timestamp > 15 minutes old OR tenant doesn't own block_hash
  - Returns 401 Unauthorized (no request reaches backend)

- [ ] Implement KMS key rotation strategy:
  - Keys rotate every 90 days
  - New tokens use new key
  - Old tokens with old keys still valid for 7 days (overlap window)
  - Document versioning scheme: `kms_key_version:1:old_sig` vs `kms_key_version:2:new_sig`

- [ ] Implement anti-replay protection:
  - Token includes nonce (random 16 bytes) in HMAC computation
  - Each request increments nonce in Redis set
  - Rejects if nonce already used
  - TTL on Redis entries = 15 minutes (token validity window)

- [ ] Implement tenant isolation verification:
  - Create fuzz tests where attacker tries to:
    - Use Token_A (tenant A, block X) to upload to (tenant B, block Y)
    - Modify timestamp to extend token validity
    - Replay tokens
    - Forge signatures
  - All must fail with 401

- [ ] Create comprehensive tests:
  ```rust
  #[test]
  fn test_hmac_token_signature_valid() { ... }
  
  #[test]
  fn test_hmac_token_signature_tampered_fails() { ... }
  
  #[test]
  fn test_hmac_token_expired_fails() { ... }
  
  #[test]
  fn test_cross_tenant_token_fails() { ... }
  
  #[test]
  fn test_token_replay_fails() { ... }
  
  #[proptest]
  fn prop_hmac_signature_secure_against_all_modifications(
    token in arb_valid_hmac_token(),
  ) { ... }
  ```

- [ ] Document security properties:
  - HMAC provides authentication (verifies token came from server)
  - Timestamp provides freshness (token only valid for 15 minutes)
  - Nonce provides replay protection (can't use same token twice)
  - Tenant ID in HMAC provides multi-tenancy isolation (can't cross boundaries)
  - Edge validation provides defense-in-depth (invalid tokens never reach backend)

**Acceptance Criteria:**
- All HMAC signatures verified at edge (0 invalid requests reach backend)
- Tokens expire after 15 minutes (verified by tests)
- Replay attacks impossible (nonce validation)
- Cross-tenant attacks impossible (fuzz tests verify)
- Key rotation is seamless (overlap window allows old keys)

**Output Format:**
```go
// project-aegis/internal/auth/hmac.go

type TokenGenerator struct {
  kmsClient KMSClient
  cache     *redis.Client
}

func (tg *TokenGenerator) GeneratePreSignedURL(
  ctx context.Context,
  tenantID uuid.UUID,
  blockHash string,
) (string, error) {
  // Fetch KMS key
  // Generate timestamp + nonce
  // Compute HMAC-SHA256
  // Return signed URL
}

func (tg *TokenGenerator) ValidateToken(
  ctx context.Context,
  tenantID uuid.UUID,
  blockHash string,
  signature string,
  timestamp int64,
) (bool, error) {
  // Re-compute HMAC
  // Verify signature (constant-time comparison)
  // Check timestamp freshness
  // Check nonce hasn't been used
  // Return validation result
}
```

---

## PHASE 4: INGESTION ENGINE & DATA PLANE

### PROMPT 4.1: Stateless Ingestion Engine (Go/Rust Hybrid)
**Context & Objective:**
The ingestion engine is the hot path. It must handle:
- 10,000+ concurrent uploads
- > 1GB/s aggregate throughput
- Process HMAC tokens without blocking
- Stream chunks directly to object storage (bypass application memory)
- Maintain < 50ms latency for upload initiation

No state is stored in this service. All state lives in PostgreSQL (metadata) or object storage (binary data).

**Task:**
- [ ] Design the ingestion request lifecycle:
  ```
  1. Client → Ingress Gateway (HTTPS)
     - Auth: Bearer token (JWT for tenant)
  
  2. Client → FastCDC stream (local, on client machine)
     - Output: List of [block_hash, size] for all chunks
  
  3. Client → Ingestion Engine HandleInitiate() (HTTP/2)
     - Request: { session_id?, tenant_id, total_size, hashes: [block_hashes] }
     - Response: { session_id, missing_hashes: [blocks_client_must_send], upload_urls: {hash → signed_url} }
  
  4. Client → Object Storage (direct, via signed URLs)
     - Upload missing blocks directly (bypasses app server)
  
  5. Client → Ingestion Engine HandleCommit() (HTTP/2)
     - Request: { session_id, node_id, chunks: [{hash, offset, size}] }
     - Response: { status: "COMMITTED" }
  ```

- [ ] Implement HandleInitiate in Go:
  ```go
  func (s *IngressServer) HandleInitiate(w http.ResponseWriter, r *http.Request) {
    req := ParseInitiateRequest(r)
    
    // 1. Validate tenant_id (from JWT)
    tenant, err := s.db.GetTenant(r.Context(), req.TenantID)
    if err != nil || tenant == nil { return 401 }
    
    // 2. Check storage quota (used_bytes + total_size <= storage_quota_bytes)
    if tenant.UsedBytes + req.TotalSize > tenant.StorageQuotaBytes {
      return http.StatusPaymentRequired // 402
    }
    
    // 3. Batch query: which hashes already exist in CAS?
    // Use IN clause: SELECT block_hash FROM cas_blocks WHERE block_hash IN ($1, $2, ...) AND tenant_id = $3
    existingHashes := s.db.GetExistingBlocks(r.Context(), req.TenantID, req.Hashes)
    missingHashes := SetDifference(req.Hashes, existingHashes)
    
    // 4. Generate pre-signed URLs for missing hashes only
    uploadURLs := make(map[string]string)
    for _, hash := range missingHashes {
      url, _ := s.tokenGen.GeneratePreSignedURL(r.Context(), req.TenantID, hash)
      uploadURLs[hash] = url
    }
    
    // 5. Create session with 24-hour expiry
    sessionID := uuid.New()
    s.db.CreateUploadSession(r.Context(), &UploadSession{
      SessionID:     sessionID,
      TenantID:      req.TenantID,
      TotalSize:     req.TotalSize,
      ExpectedChunks: len(missingHashes),
      ExpiresAt:     time.Now().Add(24 * time.Hour),
    })
    
    // 6. Return response (200 OK with session + URLs)
    return RespondJSON(w, http.StatusOK, InitiateResponse{
      SessionID:     sessionID,
      MissingHashes: missingHashes,
      UploadURLs:    uploadURLs,
    })
  }
  ```

- [ ] Implement HandleCommit in Go:
  ```go
  func (s *IngressServer) HandleCommit(w http.ResponseWriter, r *http.Request) {
    req := ParseCommitRequest(r)
    
    // 1. Open transaction (READ COMMITTED isolation)
    tx, _ := s.db.BeginTx(r.Context(), &sql.TxOptions{
      Isolation: sql.LevelReadCommitted,
    })
    defer tx.Rollback()
    
    // 2. Verify session exists and hasn't expired (FOR UPDATE lock)
    session, expired := tx.GetUploadSessionForUpdate(r.Context(), req.SessionID)
    if expired { return 410 } // Gone
    
    // 3. Verify tenant ownership
    if session.TenantID != req.TenantID { return 403 } // Forbidden
    
    // 4. Get next version number for this file node
    nextVersion := tx.GetNextVersionNumber(r.Context(), req.NodeID) + 1
    
    // 5. Insert file_versions record
    versionID := uuid.New()
    contentHash, _ := hex.DecodeString(req.ContentHash)
    tx.InsertFileVersion(r.Context(), &FileVersion{
      VersionID:    versionID,
      NodeID:       req.NodeID,
      VersionNumber: nextVersion,
      TotalSizeBytes: req.TotalSize,
      ContentHash:  contentHash,
      CreatedBy:    req.TenantID, // Audit trail
      CreatedAt:    time.Now(),
    })
    
    // 6. For each chunk, upsert cas_blocks with ref_count increment
    for _, chunk := range req.Chunks {
      blockHash, _ := hex.DecodeString(chunk.BlockHash)
      tx.ExecContext(r.Context(), `
        INSERT INTO cas_blocks (block_hash, tenant_id, size_bytes, ref_count)
        VALUES ($1, $2, $3, 1)
        ON CONFLICT (block_hash) DO UPDATE SET ref_count = ref_count + 1
      `, blockHash, req.TenantID, chunk.SizeBytes)
    }
    
    // 7. Insert file_manifest_blocks (ordered Merkle manifest)
    for _, chunk := range req.Chunks {
      blockHash, _ := hex.DecodeString(chunk.BlockHash)
      tx.InsertManifestBlock(r.Context(), &ManifestBlock{
        VersionID: versionID,
        ChunkIndex: chunk.Index,
        BlockHash: blockHash,
        OffsetBytes: chunk.Offset,
        SizeBytes: chunk.SizeBytes,
      })
    }
    
    // 8. Mark session as completed
    tx.UpdateUploadSession(r.Context(), req.SessionID, true)
    
    // 9. Emit CDC event: VERSION_COMMITTED (to Kafka for async derivation)
    s.eventBus.Publish("file_versions", CDCEvent{
      Operation: "COMMIT",
      VersionID: versionID,
      NodeID: req.NodeID,
      TenantID: req.TenantID,
      Timestamp: time.Now(),
    })
    
    // 10. Commit transaction
    if err := tx.Commit(); err != nil {
      return 500
    }
    
    // 11. Return 201 Created
    w.WriteHeader(http.StatusCreated)
    RespondJSON(w, http.StatusCreated, map[string]string{
      "status": "COMMITTED",
      "version_id": versionID.String(),
    })
  }
  ```

- [ ] Implement advanced optimizations:
  - **Batch operations**: GetExistingBlocks uses IN clause (handles 1000+ hashes)
  - **Connection pooling**: Separate read/write pools to prevent contention
  - **Prepared statements**: All queries use parameterized statements
  - **Circuit breaker**: If database latency > 100ms, fast-fail (return 503)
  - **Request validation**: Reject requests > 100MB before querying database

- [ ] Implement comprehensive error handling:
  - Tenant not found: 403 Forbidden
  - Storage quota exceeded: 402 Payment Required
  - Session expired: 410 Gone
  - Session already completed: 409 Conflict
  - Transaction conflicts: Retry with exponential backoff
  - Database unavailable: 503 Service Unavailable
  - Document all HTTP status codes and recovery strategies

- [ ] Implement observability:
  ```go
  type IngestMetrics struct {
    InitiateRequestCount      prometheus.Counter
    InitiateLatency           prometheus.Histogram
    CommitRequestCount        prometheus.Counter
    CommitLatency             prometheus.Histogram
    ExistingBlocksCacheHitRate prometheus.Gauge
    SessionCreateFailures     prometheus.Counter
    DatabaseLatency           prometheus.Histogram
  }
  ```

- [ ] Create load tests:
  - Simulate 1000 concurrent upload sessions
  - Verify < 50ms p95 latency for HandleInitiate
  - Verify < 100ms p95 latency for HandleCommit
  - Verify no connection pool exhaustion
  - Simulate database lag (add 500ms latency): verify circuit breaker activates

**Acceptance Criteria:**
- HandleInitiate: < 50ms p95 latency with 1000 concurrent clients
- HandleCommit: < 100ms p95 latency with 1000 concurrent clients
- Zero state stored in service (stateless)
- All errors return appropriate HTTP status codes
- Connection pool never exhausts
- Load tests achieve > 1000 RPS

**Output Format:**
```go
// project-aegis/internal/ingress/server.go

type IngressServer struct {
  db        *DatabaseClient
  tokenGen  *TokenGenerator
  objectStore ObjectStorageClient
  eventBus  EventBusClient
  metrics   *IngestMetrics
}

func (s *IngressServer) HandleInitiate(w http.ResponseWriter, r *http.Request) {
  // Detailed implementation with all error handling
}

func (s *IngressServer) HandleCommit(w http.ResponseWriter, r *http.Request) {
  // Detailed implementation with all error handling
}

type InitiateRequest struct {
  TenantID  uuid.UUID `json:"tenant_id"`
  TotalSize int64     `json:"total_size"`
  Hashes    []string  `json:"hashes"`
}

type CommitRequest struct {
  SessionID uuid.UUID       `json:"session_id"`
  NodeID    uuid.UUID       `json:"node_id"`
  TenantID  uuid.UUID       `json:"tenant_id"`
  TotalSize int64           `json:"total_size"`
  ContentHash string        `json:"content_hash"`
  Chunks    []ChunkManifest `json:"chunks"`
}
```

---

### PROMPT 4.2: Object Storage Abstraction & Direct PUT Workflows
**Context & Objective:**
Binary data bypasses the application entirely. Direct-to-blob pre-signed URLs mean clients stream data to S3/MinIO/R2 without touching the ingestion engine. This is critical for throughput (1GB/s aggregate requires hardware acceleration).

**Task:**
- [ ] Design the ObjectStorageClient abstraction:
  ```go
  type ObjectStorageClient interface {
    // Generate pre-signed PUT URL valid for 15 minutes
    GenerateUploadURL(ctx context.Context, tenantID uuid.UUID, blockHash string) (string, error)
    
    // Verify block exists and matches hash (called during GC)
    GetBlockMetadata(ctx context.Context, blockHash string) (*BlockMetadata, error)
    
    // Retrieve block (for downloads)
    GetBlockReader(ctx context.Context, blockHash string) (io.Reader, error)
    
    // Delete block (called during GC sweep)
    DeleteBlock(ctx context.Context, blockHash string) error
  }
  ```

- [ ] Implement S3 backend:
  ```go
  type S3Client struct {
    s3Client *s3.Client
    bucketName string
    kmsKeyID string
  }
  
  func (c *S3Client) GenerateUploadURL(ctx context.Context, tenantID uuid.UUID, blockHash string) (string, error) {
    // Generate pre-signed URL with:
    // - 15-minute expiry
    // - Server-side encryption (KMS)
    // - Object tags for lifecycle policies
    presigner := s3.NewPresignClient(c.s3Client)
    request, _ := presigner.PresignPutObject(ctx, &s3.PutObjectInput{
      Bucket: aws.String(c.bucketName),
      Key:    aws.String(blockHash),
      ServerSideEncryption: types.ServerSideEncryptionAwsKms,
      SSEKMSKeyId: aws.String(c.kmsKeyID),
      Tagging: aws.String(fmt.Sprintf("tenant=%s", tenantID)),
    }, func(opts *s3.PresignOptions) {
      opts.Expires = time.Duration(15 * time.Minute)
    })
    
    return request.URL, nil
  }
  ```

- [ ] Implement MinIO backend (for on-premise deployments):
  ```go
  type MinIOClient struct {
    client *minio.Client
    bucket string
  }
  ```

- [ ] Design tiered storage strategy:
  - **HOT tier** (S3 Standard / Ceph): High-performance blocks, < 7 days old
  - **WARM tier** (S3 Standard-IA / Ceph): Medium-performance, 7-30 days old
  - **COLD tier** (S3 Glacier / Ceph Archive): Infrequent access, > 30 days old
  - Implement automatic tiering via object lifecycle policies
  - Document cost/performance tradeoffs

- [ ] Implement object lifecycle policies:
  ```json
  {
    "Rules": [
      {
        "Id": "Move to WARM",
        "Prefix": "blocks/",
        "Transitions": [
          {
            "StorageClass": "STANDARD_IA",
            "Days": 7
          }
        ]
      },
      {
        "Id": "Move to COLD",
        "Prefix": "blocks/",
        "Transitions": [
          {
            "StorageClass": "GLACIER",
            "Days": 30
          }
        ]
      },
      {
        "Id": "Abort Incomplete Multipart",
        "NoncurrentVersionExpirationInDays": 1,
        "AbortIncompleteMultipartUpload": {
          "DaysAfterInitiation": 1
        }
      }
    ]
  }
  ```

- [ ] Implement verification on ingestion:
  - Object storage computes ETag (MD5 of uploaded data)
  - Ingestion engine requests metadata after upload
  - Verifies block exists and ETag matches expected hash
  - Only then marks block as "verified" in cas_blocks table

- [ ] Create tests:
  - S3 integration tests with LocalStack
  - MinIO integration tests with containerized MinIO
  - Verify pre-signed URLs expire correctly
  - Verify blocks marked as verified only after confirmation
  - Verify object tagging works correctly
  - Test tiered storage transitions

**Acceptance Criteria:**
- Pre-signed URLs prevent direct database access
- All PUT requests are authenticated via HMAC
- Object lifecycle policies are enforced
- Block verification prevents corrupted uploads
- Tiered storage reduces costs without impacting performance

---

## PHASE 5: CONTENT-ADDRESSABLE STORAGE & DEDUPLICATION

### PROMPT 5.1: CAS Registry & Block Management
**Context & Objective:**
The cas_blocks table is the deduplication registry. Every unique chunk has one row. Multiple file versions can reference the same block via file_manifest_blocks. The ref_count tracks how many versions reference each block. When ref_count = 0, the block is orphaned and eligible for GC.

**Task:**
- [ ] Design the CAS registry lifecycle:
  ```
  1. Upload initiation: Check cas_blocks for existing hashes
  
  2. Upload completion: INSERT OR UPDATE cas_blocks
     INSERT INTO cas_blocks (...) VALUES (...)
     ON CONFLICT (block_hash) DO UPDATE SET ref_count = ref_count + 1
  
  3. File delete: Decrement ref_count
     UPDATE cas_blocks SET ref_count = ref_count - 1 WHERE block_hash IN (...)
  
  4. Generational GC (after 7 days): Delete blocks with ref_count = 0
     DELETE FROM cas_blocks WHERE ref_count = 0 AND created_at < NOW() - INTERVAL '7 days'
  ```

- [ ] Implement the CAS operations in PostgreSQL:
  ```sql
  -- Query 1: Check which blocks exist (used during HandleInitiate)
  SELECT block_hash FROM cas_blocks 
  WHERE block_hash = ANY($1::BYTEA[]) AND tenant_id = $2::UUID;
  
  -- Query 2: Increment ref_count on upload (used during HandleCommit)
  INSERT INTO cas_blocks (block_hash, tenant_id, size_bytes, ref_count, created_at)
  VALUES ($1::BYTEA, $2::UUID, $3::INT, 1, NOW())
  ON CONFLICT (block_hash) DO UPDATE 
  SET ref_count = ref_count + 1,
      storage_tier = CASE WHEN ref_count > 100 THEN 'HOT' ELSE 'WARM' END;
  
  -- Query 3: Decrement ref_count on file delete
  UPDATE cas_blocks 
  SET ref_count = ref_count - 1 
  WHERE block_hash IN (...) AND tenant_id = $1::UUID;
  
  -- Query 4: Find orphaned blocks eligible for GC
  SELECT block_hash, size_bytes FROM cas_blocks
  WHERE ref_count = 0 
    AND created_at < NOW() - INTERVAL '7 days'
    AND tenant_id = $1::UUID
  LIMIT $2 -- Batch size to avoid large transactions
  ```

- [ ] Implement storage tier automation:
  - Blocks with ref_count > 100: tier = 'HOT' (replicate to 3+ regions, high performance)
  - Blocks with 10 <= ref_count <= 100: tier = 'WARM' (2 regions, standard performance)
  - Blocks with ref_count < 10: tier = 'COLD' (1 region, infrequent access)
  - Transition tiers based on ref_count changes
  - Document cost/performance implications

- [ ] Implement bloom filter for fast existence checks:
  - Maintain a Bloom filter of all block_hashes in Redis
  - Update on every ref_count increment/decrement
  - Use for "might exist" checks before database query
  - If bloom says "doesn't exist", skip database query (guaranteed miss)
  - If bloom says "might exist", query database (possibility of false positive)
  - TTL on bloom filter: 1 hour (re-build periodically)

- [ ] Implement comprehensive metrics:
  ```go
  type CASMetrics struct {
    TotalBlocks         prometheus.Gauge
    TotalBytes          prometheus.Gauge
    RefCountDistribution prometheus.Histogram
    DeduplicationRatio  prometheus.Gauge // (Total content size) / (actual stored size)
    HotBlocks           prometheus.Gauge
    WarmBlocks          prometheus.Gauge
    ColdBlocks          prometheus.Gauge
  }
  ```

- [ ] Create tests:
  - Insert block, verify ref_count = 1
  - Insert same block again, verify ref_count = 2
  - Delete file referencing block, verify ref_count decrements
  - Verify orphaned blocks (ref_count = 0) exist after 7 days
  - Verify deduplication ratio calculation
  - Fuzz test: random sequence of inserts/deletes, verify ref_count always correct

**Acceptance Criteria:**
- ref_count is always correct (invariant maintained)
- Deduplication ratio is measurable and reported
- Bloom filter provides correct "might exist" results
- Storage tiering is automatic and transparent
- GC can safely delete blocks with ref_count = 0

---

## PHASE 6: GARBAGE COLLECTION & COMPACTION

### PROMPT 6.1: Generational Mark-and-Sweep GC Pipeline
**Context & Objective:**
Incomplete uploads leak data (client drops before commit). Deleted files leave orphan blocks. Without GC, storage is wasted. But GC must be conservative: only delete blocks that are DEFINITELY unreferenced, after sufficient waiting period.

**Task:**
- [ ] Design the GC pipeline:
  ```
  1. Incomplete Upload Cleanup (every hour):
     - Query upload_sessions WHERE expires_at < NOW() AND is_completed = FALSE
     - Mark as expired (soft delete)
     - Blocks in expired sessions become candidates for deletion
  
  2. Unreferenced Block Detection (every 24 hours):
     - SELECT cas_blocks WHERE ref_count = 0 AND created_at < NOW() - INTERVAL '7 days'
     - Batch into 1000-block chunks
     - Emit tombstone events to Kafka: cas-tombstones topic
  
  3. Storage Engine Deletion (out-of-band):
     - Kafka consumers read tombstones
     - DELETE from S3/MinIO/Ceph
     - Mark in cas_blocks table as deleted (soft delete, audit trail)
  ```

- [ ] Implement in Go:
  ```go
  type GarbageCollector struct {
    db            *DatabaseClient
    objectStore   ObjectStorageClient
    eventBus      EventBusClient
    batchSize     int // e.g., 1000 blocks per transaction
  }
  
  func (gc *GarbageCollector) CollectUnreferencedBlocks(ctx context.Context) error {
    // 1. Find unreferenced blocks
    blocks, err := gc.db.QueryUnreferencedBlocks(ctx, 7*24*time.Hour, gc.batchSize)
    if err != nil {
      return err
    }
    
    // 2. For each batch, emit tombstone events
    for i := 0; i < len(blocks); i += gc.batchSize {
      batch := blocks[i:min(i+gc.batchSize, len(blocks))]
      
      // Emit as CDC tombstone event
      for _, block := range batch {
        gc.eventBus.Publish("cas-tombstones", CASBlockDeletion{
          BlockHash:   block.Hash,
          TenantID:    block.TenantID,
          SizeBytes:   block.SizeBytes,
          Timestamp:   time.Now(),
        })
      }
      
      // Only delete after events are published (idempotency)
      err = gc.db.SoftDeleteBlocks(ctx, batch)
      if err != nil {
        return err
      }
    }
    
    return nil
  }
  ```

- [ ] Implement tombstone consumer (worker):
  ```go
  type TombstoneConsumer struct {
    objectStore ObjectStorageClient
    kafka       *kafka.Reader
  }
  
  func (tc *TombstoneConsumer) ConsumeTombstones(ctx context.Context) error {
    for {
      msg, err := tc.kafka.ReadMessage(ctx)
      if err != nil { return err }
      
      deletion := UnmarshalCASBlockDeletion(msg.Value)
      
      // Delete from object storage
      err = tc.objectStore.DeleteBlock(ctx, deletion.BlockHash)
      if err != nil {
        // Log, but continue (failed deletion is non-fatal, retry later)
        log.Warnf("Failed to delete block %s: %v", deletion.BlockHash, err)
        continue
      }
      
      // Success: mark message as committed
      tc.kafka.CommitMessages(ctx, msg)
    }
  }
  ```

- [ ] Implement safety mechanisms:
  - **Double-check before delete**: Query cas_blocks again before deletion to verify ref_count is still 0
  - **Dry-run mode**: Log what would be deleted without actually deleting (for testing)
  - **Rate limiting**: Process max N blocks/hour to avoid sudden capacity spikes
  - **Monitoring**: Alert if deletion rate > threshold (suggests bug in ref_count tracking)
  - **Audit trail**: Log every deletion with reason (expired session, file deleted, etc.)

- [ ] Implement incomplete session cleanup:
  ```go
  func (gc *GarbageCollector) CleanupExpiredSessions(ctx context.Context) error {
    // Mark expired sessions as cleaned
    sessions, err := gc.db.GetExpiredUploadSessions(ctx)
    if err != nil { return err }
    
    for _, session := range sessions {
      err := gc.db.MarkSessionExpired(ctx, session.ID)
      if err != nil {
        log.Warnf("Failed to mark session %s expired: %v", session.ID, err)
      }
      
      // Emit metric: incomplete upload dropped
      gc.metrics.IncompleteUploadsRecycled.Inc()
    }
    
    return nil
  }
  ```

- [ ] Create comprehensive tests:
  - Create upload session, don't commit, verify expiry after 24 hours
  - Verify blocks from expired sessions become candidates for GC
  - Create duplicate blocks, delete one file, verify ref_count decrements but not to 0
  - Delete all files referencing a block, verify ref_count = 0
  - Verify GC doesn't delete blocks created < 7 days ago (even if ref_count = 0)
  - Verify concurrent GC and normal upload operations (no race conditions)
  - Fuzz test: random sequence of uploads/deletes with concurrent GC

**Acceptance Criteria:**
- Expired sessions cleaned up within 1 hour
- Orphaned blocks detected within 24 hours
- Blocks safely deleted only after 7-day window
- No race conditions between GC and normal operations
- Audit trail logs every deletion
- Rate limiting prevents capacity spikes

---

## PHASE 7: METADATA SYNCHRONIZATION & DERIVATION WORKERS

### PROMPT 7.1: CDC Event Streaming & Change Data Capture
**Context & Objective:**
When a file version is committed, downstream workers need to know:
- **ClamAV scanner**: Scan for malware
- **OCR worker**: Extract text from images/PDFs
- **FFmpeg worker**: Transcode video
- **Vector embedding**: Compute ML embeddings for search

All of these must run **out-of-band** (after commit returns 201 Created) so client latency isn't affected.

**Task:**
- [ ] Design CDC event schema (Kafka topic: `file_versions`):
  ```json
  {
    "event_id": "uuid",
    "event_type": "VERSION_COMMITTED",
    "version_id": "uuid",
    "node_id": "uuid",
    "tenant_id": "uuid",
    "file_size_bytes": 1073741824,
    "mime_type": "video/mp4",
    "created_at": "2025-08-24T12:34:56Z",
    "chunks": [
      {
        "index": 0,
        "block_hash": "sha256:...",
        "size_bytes": 1048576,
        "offset_bytes": 0
      }
    ]
  }
  ```

- [ ] Implement CDC emission in HandleCommit:
  ```go
  // After transaction commit succeeds
  err = s.eventBus.Publish(ctx, "file_versions", CDCEvent{
    EventID:    uuid.New(),
    EventType:  "VERSION_COMMITTED",
    VersionID:  versionID,
    NodeID:     req.NodeID,
    TenantID:   req.TenantID,
    FileSizeBytes: req.TotalSize,
    MimeType:   "application/octet-stream", // or detected from filename
    CreatedAt:  time.Now(),
    Chunks:     req.Chunks,
  })
  ```

- [ ] Implement worker pool architecture:
  ```go
  type DerivationWorker struct {
    name      string // e.g., "clamav-scanner"
    kafka     *kafka.Reader
    processor DerivationProcessor
  }
  
  type DerivationProcessor interface {
    Process(ctx context.Context, event CDCEvent) (*ProcessingResult, error)
  }
  ```

- [ ] Implement specific workers:
  
  **ClamAV Scanner Worker**:
  ```go
  type ClamAVWorker struct {
    objectStore ObjectStorageClient
    clamav      *clamd.Clamd
  }
  
  func (w *ClamAVWorker) Process(ctx context.Context, event CDCEvent) (*ProcessingResult, error) {
    // 1. Download all chunks
    reader, err := w.objectStore.GetBlockReader(ctx, event.Chunks[0].BlockHash)
    if err != nil { return nil, err }
    
    // 2. Scan with ClamAV
    result, err := w.clamav.ScanStream(reader)
    if err != nil { return nil, err }
    
    // 3. Return result: clean/infected
    return &ProcessingResult{
      WorkerName: "clamav",
      Status:     result.Status, // "CLEAN" or "INFECTED"
      Timestamp:  time.Now(),
    }, nil
  }
  ```
  
  **OCR Worker**:
  ```go
  type OCRWorker struct {
    objectStore ObjectStorageClient
    ocr         *tesseract.Client
  }
  
  func (w *OCRWorker) Process(ctx context.Context, event CDCEvent) (*ProcessingResult, error) {
    if !isImageMimeType(event.MimeType) {
      return nil, fmt.Errorf("OCR requires image mime type, got %s", event.MimeType)
    }
    
    // Download, extract text, store in metadata
    reader, _ := w.objectStore.GetBlockReader(ctx, event.Chunks[0].BlockHash)
    text, _ := w.ocr.ExtractText(ctx, reader)
    
    return &ProcessingResult{
      WorkerName: "ocr",
      Text:       text,
      Timestamp:  time.Now(),
    }, nil
  }
  ```
  
  **FFmpeg Worker**:
  ```go
  type FFmpegWorker struct {
    objectStore ObjectStorageClient
  }
  
  func (w *FFmpegWorker) Process(ctx context.Context, event CDCEvent) (*ProcessingResult, error) {
    if !isVideoMimeType(event.MimeType) {
      return nil, fmt.Errorf("FFmpeg requires video mime type, got %s", event.MimeType)
    }
    
    // Download video, generate thumbnail + different bitrates
    // Store as new file versions with same node_id
    
    return &ProcessingResult{
      WorkerName: "ffmpeg",
      Thumbnails: []string{...},
      Variants:   []string{...},
      Timestamp:  time.Now(),
    }, nil
  }
  ```

- [ ] Implement result persistence:
  ```go
  type ProcessingResult struct {
    WorkerName string
    VersionID  uuid.UUID
    Status     string // "SUCCESS" / "FAILED"
    ResultData map[string]interface{}
    Timestamp  time.Time
  }
  
  // Store results in PostgreSQL
  CREATE TABLE derivation_results (
    result_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    version_id UUID NOT NULL REFERENCES file_versions(version_id),
    worker_name VARCHAR(64) NOT NULL,
    status VARCHAR(32) NOT NULL,
    result_data JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
  );
  
  CREATE INDEX idx_derivation_version ON derivation_results(version_id);
  CREATE INDEX idx_derivation_worker ON derivation_results(worker_name);
  ```

- [ ] Implement error handling & retries:
  - **Transient failures** (network timeout): Retry with exponential backoff (1s, 2s, 4s, 8s, 16s)
  - **Permanent failures** (malformed data): Log and skip (don't retry forever)
  - **Kafka offset management**: Only commit offset after successful processing
  - **Dead letter queue**: Send permanently failed events to separate Kafka topic for investigation

- [ ] Create comprehensive tests:
  - Emit VERSION_COMMITTED event, verify workers receive it
  - ClamAV worker correctly identifies malware (use EICAR test file)
  - OCR worker extracts text from PDF
  - FFmpeg worker generates thumbnail
  - Verify retry logic under transient failures
  - Verify concurrent workers don't interfere

**Acceptance Criteria:**
- CDC events emitted for all version commits
- All workers process events within SLA (ClamAV < 60s, OCR < 120s)
- Failed events are retried and logged
- Results persisted to database
- Dead letter queue captures permanently failed events

---

## PHASE 8: API LAYER & CLIENT INTERFACE

### PROMPT 8.1: Client SDK & Upload Protocol
**Context & Objective:**
Clients need a simple, idiomatic interface for uploading files. The SDK must:
- Automatically chunk files using FastCDC
- Implement intelligent retry logic
- Handle network failures gracefully
- Support progress reporting and pause/resume

**Task:**
- [ ] Design the SDK API (Go):
  ```go
  package aegis
  
  type Client struct {
    endpoint  string
    tenantID  uuid.UUID
    apiToken  string // JWT
  }
  
  func (c *Client) UploadFile(ctx context.Context, opts UploadOptions) (*UploadResult, error) {
    // High-level interface
    // Handles chunking, deduplication, manifest commit
  }
  
  type UploadOptions struct {
    FilePath        string
    TargetPath      string // e.g., "/Documents/report.pdf"
    ProgressCallback func(progress UploadProgress)
    OnChunkComplete  func(chunk ChunkInfo)
  }
  
  type UploadProgress struct {
    BytesUploaded   int64
    TotalBytes      int64
    PercentComplete float64
    EstimatedTimeRemaining time.Duration
    CurrentBitrate  float64
  }
  ```

- [ ] Implement the upload flow:
  ```go
  func (c *Client) UploadFile(ctx context.Context, opts UploadOptions) (*UploadResult, error) {
    // 1. Open file
    file, _ := os.Open(opts.FilePath)
    defer file.Close()
    
    // 2. Run FastCDC to get chunks
    fastcdc := fastcdc.NewStream(file)
    var chunks []ChunkInfo
    for {
      chunk, _ := fastcdc.NextChunk()
      if chunk == nil { break }
      chunks = append(chunks, chunk)
    }
    
    // 3. Call HandleInitiate
    hashes := make([]string, len(chunks))
    for i, chunk := range chunks {
      hashes[i] = chunk.HashHex
    }
    
    initiateResp, _ := c.initiateUpload(ctx, InitiateRequest{
      TotalSize: file.Size(),
      Hashes:    hashes,
    })
    
    // 4. Upload missing chunks in parallel
    missingHashes := make(map[string]ChunkInfo)
    for i, hash := range initiateResp.MissingHashes {
      missingHashes[hash] = chunks[i]
    }
    
    err := c.uploadChunksInParallel(ctx, missingHashes, initiateResp.UploadURLs, opts.ProgressCallback)
    if err != nil { return nil, err }
    
    // 5. Compute file content hash (SHA-256 of file)
    fileHash := computeFileSHA256(chunks)
    
    // 6. Call HandleCommit
    result, _ := c.commitUpload(ctx, CommitRequest{
      SessionID:   initiateResp.SessionID,
      NodeID:      uuid.New(), // or fetch existing node_id
      TargetPath:  opts.TargetPath,
      ContentHash: fileHash,
      Chunks:      chunks,
    })
    
    return result, nil
  }
  ```

- [ ] Implement retry logic:
  ```go
  func (c *Client) uploadChunksInParallel(
    ctx context.Context,
    chunks map[string]ChunkInfo,
    urls map[string]string,
    progressCb func(UploadProgress),
  ) error {
    semaphore := make(chan struct{}, 10) // Max 10 parallel uploads
    errChan := make(chan error, len(chunks))
    
    for hash, chunk := range chunks {
      go func(h string, ch ChunkInfo) {
        semaphore <- struct{}{}        // Acquire
        defer func() { <-semaphore }() // Release
        
        // Retry logic with exponential backoff
        var lastErr error
        for attempt := 0; attempt < 5; attempt++ {
          err := c.uploadChunk(ctx, urls[h], ch)
          if err == nil {
            progressCb(...)
            errChan <- nil
            return
          }
          lastErr = err
          
          // Exponential backoff: 1s, 2s, 4s, 8s, 16s
          backoff := time.Duration(math.Pow(2, float64(attempt))) * time.Second
          select {
          case <-time.After(backoff):
          case <-ctx.Done():
            errChan <- ctx.Err()
            return
          }
        }
        
        errChan <- fmt.Errorf("chunk %s failed after 5 retries: %w", h, lastErr)
      }(hash, chunk)
    }
    
    // Collect errors
    for i := 0; i < len(chunks); i++ {
      if err := <-errChan; err != nil {
        return err
      }
    }
    
    return nil
  }
  ```

- [ ] Implement pause/resume:
  ```go
  type UploadSession struct {
    sessionID uuid.UUID
    filePath  string
    chunks    []ChunkInfo
    uploaded  map[string]bool // Track which chunks are uploaded
  }
  
  func (c *Client) SaveSession(session *UploadSession) error {
    // Serialize to ~/.aegis/sessions/{sessionID}.json
    data, _ := json.Marshal(session)
    return os.WriteFile(path, data, 0600)
  }
  
  func (c *Client) ResumeSession(ctx context.Context, sessionID uuid.UUID) (*UploadResult, error) {
    // Load session from disk
    session, _ := c.LoadSession(sessionID)
    
    // Only upload missing chunks
    missingChunks := make([]ChunkInfo, 0)
    for _, chunk := range session.chunks {
      if !session.uploaded[chunk.HashHex] {
        missingChunks = append(missingChunks, chunk)
      }
    }
    
    // Continue from where we left off
    return c.continueUpload(ctx, session, missingChunks)
  }
  ```

- [ ] Create client tests:
  - Upload small file (< 1MB)
  - Upload large file (> 1GB) and verify parallelization
  - Upload duplicate file and verify zero bytes transferred
  - Simulate network failure and verify retry logic
  - Pause and resume upload
  - Verify progress reporting is accurate

**Acceptance Criteria:**
- SDK handles all failure modes gracefully
- Parallel uploads achieve > 100 MB/s on good networks
- Deduplication works transparently (no re-upload on duplicate)
- Pause/resume preserves session state
- Progress reporting is accurate

---

## PHASE 9: TESTING & VALIDATION

### PROMPT 9.1: Comprehensive Test Suite & Coverage
**Context & Objective:**
Production systems require bulletproof testing. Every component must be tested at unit, integration, and system levels. Fuzz testing catches edge cases that humans miss.

**Task:**
- [ ] Create test hierarchy:
  ```
  UNIT TESTS (Fast, isolated, no dependencies)
  ├── FastCDC algorithm tests
  ├── HMAC token generation tests
  ├── ltree cycle detection tests
  └── Database schema tests
  
  INTEGRATION TESTS (Real databases, slower, comprehensive)
  ├── Upload initiation workflow
  ├── Upload commit workflow
  ├── Garbage collection workflow
  ├── Concurrent operations (race condition detection)
  └── CDC event streaming
  
  SYSTEM TESTS (End-to-end, realistic workloads)
  ├── Upload 1GB file
  ├── Concurrent 1000 clients
  ├── Multi-region failover
  ├── Load shedding under peak
  └── SLA compliance verification
  
  FUZZ TESTS (Property-based, discover edge cases)
  ├── FastCDC with random binary data
  ├── ltree with adversarial paths
  ├── Directory move with concurrent modifications
  └── Concurrent uploads/deletes/GC
  ```

- [ ] Implement unit tests (Rust):
  ```rust
  #[cfg(test)]
  mod tests {
    use super::*;
    
    #[test]
    fn test_fastcdc_simple_file() {
      let data = b"Hello, World!";
      let mut stream = FastCDCStream::new(&data[..]);
      let chunk = stream.next_chunk().unwrap();
      assert!(chunk.is_some());
    }
    
    #[test]
    fn test_fastcdc_content_aware_boundaries() {
      // Insert 1MB in middle, verify chunk boundaries shift
      let mut original = vec![0u8; 1024 * 1024];
      let mut modified = original.clone();
      modified.insert(512 * 1024, 42); // Insert byte in middle
      
      let orig_chunks = chunk_file(&original);
      let modified_chunks = chunk_file(&modified);
      
      // Most chunks should be identical, only nearby chunks differ
      let identical = orig_chunks.iter()
        .zip(modified_chunks.iter())
        .filter(|(a, b)| a.hash == b.hash)
        .count();
      
      assert!(identical > 90); // 90% of chunks unchanged
    }
    
    #[test]
    fn test_hmac_signature_invalid_fails() {
      let signature = generate_hmac(&secret_key, "message");
      let tampered = &signature[..signature.len()-1]; // Remove last byte
      assert!(!verify_hmac(&secret_key, "message", tampered));
    }
    
    #[test]
    fn test_ltree_cycle_detection() {
      let root = ltree_path!["root"];
      let a = ltree_path!["root", "a"];
      let b = ltree_path!["root", "a", "b"];
      
      // b can't move into itself (cycle detection)
      assert!(is_cycle(&b, &b)); // target is descendant of source
      
      // b can't move into a (creates cycle: a → b → a)
      assert!(is_cycle(&a, &b)); // target is descendant of source
      
      // b can move into root (valid)
      assert!(!is_cycle(&root, &b));
    }
  }
  ```

- [ ] Implement integration tests (Go with testcontainers):
  ```go
  func TestUploadWorkflow(t *testing.T) {
    // Setup: spin up PostgreSQL container
    ctx := context.Background()
    postgres := setupPostgres(ctx, t)
    defer postgres.Terminate(ctx)
    
    minio := setupMinIO(ctx, t)
    defer minio.Terminate(ctx)
    
    redis := setupRedis(ctx, t)
    defer redis.Terminate(ctx)
    
    // Create clients
    db, _ := postgres.GetConnectionPool(ctx)
    objStore, _ := minio.GetS3Client()
    cache := redis.GetClient()
    
    server := &IngressServer{DB: db, ObjectStore: objStore, Cache: cache}
    
    // Test 1: Initiate upload
    sessionResp, err := server.HandleInitiate(ctx, &InitiateRequest{
      TenantID:  testTenantID,
      TotalSize: 1024 * 1024, // 1 MB
      Hashes:    []string{sha256([]byte("chunk1"))},
    })
    require.NoError(t, err)
    require.NotNil(t, sessionResp.SessionID)
    require.Len(t, sessionResp.MissingHashes, 1)
    
    // Test 2: Upload chunk to S3
    url := sessionResp.UploadURLs[sessionResp.MissingHashes[0]]
    err = uploadViaURL(url, []byte("chunk1_data"))
    require.NoError(t, err)
    
    // Test 3: Commit upload
    commitResp, err := server.HandleCommit(ctx, &CommitRequest{
      SessionID:   sessionResp.SessionID,
      TenantID:    testTenantID,
      NodeID:      testNodeID,
      TotalSize:   1024 * 1024,
      ContentHash: sha256([]byte("chunk1_data")),
      Chunks: []ChunkManifest{{
        Index:     0,
        BlockHash: sha256([]byte("chunk1_data")),
        Offset:    0,
        SizeBytes: 1024 * 1024,
      }},
    })
    require.NoError(t, err)
    require.Equal(t, http.StatusCreated, commitResp.StatusCode)
    
    // Test 4: Verify version created
    version, err := db.GetFileVersion(ctx, commitResp.VersionID)
    require.NoError(t, err)
    require.NotNil(t, version)
    require.Equal(t, version.TotalSizeBytes, int64(1024*1024))
  }
  
  func TestConcurrentUploads(t *testing.T) {
    // Simulate 1000 concurrent clients
    const numClients = 1000
    results := make(chan error, numClients)
    
    for i := 0; i < numClients; i++ {
      go func(clientID int) {
        err := simulateUpload(clientID)
        results <- err
      }(i)
    }
    
    for i := 0; i < numClients; i++ {
      err := <-results
      require.NoError(t, err)
    }
  }
  ```

- [ ] Implement fuzz tests:
  ```rust
  #[cfg(test)]
  mod fuzz_tests {
    use proptest::prelude::*;
    
    proptest! {
      #[test]
      fn prop_fastcdc_deterministic(
        data in prop::collection::vec(any::<u8>(), 0..10*1024*1024)
      ) {
        let chunks1 = chunk_data(&data);
        let chunks2 = chunk_data(&data);
        prop_assert_eq!(chunks1, chunks2); // Same input → same chunks
      }
      
      #[test]
      fn prop_ltree_cycle_detection_sound(
        paths in arb_ltree_paths()
      ) {
        for (source, target) in paths {
          let is_cycle = check_cycle(&source, &target);
          let is_actually_descendant = target.is_descendant_of(&source);
          prop_assert_eq!(is_cycle, is_actually_descendant);
        }
      }
    }
  }
  ```

- [ ] Create coverage reporting:
  ```bash
  # Generate coverage report
  cargo tarpaulin --out Html --output-dir coverage
  go test -cover ./...
  
  # Target: > 85% coverage on core paths
  # Exclude: error paths, logging, metrics
  ```

- [ ] Create load test suite:
  ```go
  func BenchmarkUploadThroughput(b *testing.B) {
    // Measure: uploads/second, bytes/second, latency distribution
    for i := 0; i < b.N; i++ {
      _ = simulateUpload()
    }
  }
  
  func BenchmarkDatabaseLatency(b *testing.B) {
    // Measure: query latency p50/p95/p99
    for i := 0; i < b.N; i++ {
      _ = db.GetExistingBlocks(context.Background(), hashes)
    }
  }
  
  // Run with: go test -bench=. -benchmem
  ```

**Acceptance Criteria:**
- Unit test coverage > 85%
- Integration tests cover happy path + 10 failure scenarios
- System tests verify SLA compliance
- Fuzz tests run for 1 hour without finding issues
- Load tests achieve > 1000 RPS
- All tests pass in CI/CD pipeline

---

## PHASE 10: DEPLOYMENT & OPERATIONS

### PROMPT 10.1: Kubernetes Deployment Manifests & Observability
**Context & Objective:**
Production deployment requires Kubernetes manifests for:
- Stateless ingestion engine (auto-scaling)
- PostgreSQL cluster (stateful, replicated)
- Redis cache (stateful, replicated)
- Derivation workers (horizontal scaling)
- Monitoring, logging, tracing

**Task:**
- [ ] Create Kubernetes manifests:
  ```yaml
  # ingestion-deployment.yaml
  apiVersion: apps/v1
  kind: Deployment
  metadata:
    name: aegis-ingestion
    namespace: aegis
  spec:
    replicas: 10  # Auto-scaling based on CPU/memory
    strategy:
      type: RollingUpdate
      rollingUpdate:
        maxUnavailable: 2
        maxSurge: 4
    selector:
      matchLabels:
        app: aegis-ingestion
    template:
      metadata:
        labels:
          app: aegis-ingestion
      spec:
        # Affinity: spread across nodes for redundancy
        affinity:
          podAntiAffinity:
            preferredDuringSchedulingIgnoredDuringExecution:
            - weight: 100
              podAffinityTerm:
                labelSelector:
                  matchExpressions:
                  - key: app
                    operator: In
                    values:
                    - aegis-ingestion
                topologyKey: kubernetes.io/hostname
        
        containers:
        - name: ingestion
          image: aegis-ingestion:latest
          ports:
          - containerPort: 8080
            name: http
          env:
          - name: DATABASE_URL
            valueFrom:
              secretKeyRef:
                name: aegis-secrets
                key: database-url
          - name: REDIS_URL
            valueFrom:
              secretKeyRef:
                name: aegis-secrets
                key: redis-url
          
          # Resource limits prevent starvation
          resources:
            requests:
              cpu: "500m"
              memory: "512Mi"
            limits:
              cpu: "2000m"
              memory: "2Gi"
          
          # Health checks
          livenessProbe:
            httpGet:
              path: /health
              port: 8080
            initialDelaySeconds: 10
            periodSeconds: 10
            timeoutSeconds: 5
          
          readinessProbe:
            httpGet:
              path: /ready
              port: 8080
            initialDelaySeconds: 5
            periodSeconds: 5
          
          # Graceful shutdown
          lifecycle:
            preStop:
              exec:
                command: ["/bin/sh", "-c", "sleep 15"]  # Allow in-flight requests to complete
  
  ---
  apiVersion: autoscaling/v2
  kind: HorizontalPodAutoscaler
  metadata:
    name: aegis-ingestion-hpa
  spec:
    scaleTargetRef:
      apiVersion: apps/v1
      kind: Deployment
      name: aegis-ingestion
    minReplicas: 10
    maxReplicas: 100
    metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: 70
    - type: Resource
      resource:
        name: memory
        target:
          type: Utilization
          averageUtilization: 80
  ```

- [ ] Create PostgreSQL StatefulSet:
  ```yaml
  # postgres-statefulset.yaml
  apiVersion: apps/v1
  kind: StatefulSet
  metadata:
    name: aegis-postgres
  spec:
    serviceName: aegis-postgres
    replicas: 3  # Primary + 2 replicas
    selector:
      matchLabels:
        app: aegis-postgres
    template:
      metadata:
        labels:
          app: aegis-postgres
      spec:
        containers:
        - name: postgres
          image: postgres:16
          ports:
          - containerPort: 5432
          env:
          - name: POSTGRES_DB
            value: aegis
          - name: POSTGRES_USER
            valueFrom:
              secretKeyRef:
                name: aegis-secrets
                key: postgres-user
          - name: POSTGRES_PASSWORD
            valueFrom:
              secretKeyRef:
                name: aegis-secrets
                key: postgres-password
          
          volumeMounts:
          - name: postgres-storage
            mountPath: /var/lib/postgresql/data
          
          resources:
            requests:
              cpu: "1000m"
              memory: "2Gi"
            limits:
              cpu: "2000m"
              memory: "4Gi"
  
  volumeClaimTemplates:
  - metadata:
      name: postgres-storage
    spec:
      accessModes: ["ReadWriteOnce"]
      storageClassName: fast-ssd
      resources:
        requests:
          storage: 1Ti
  ```

- [ ] Create observability stack (Prometheus + Grafana + Jaeger):
  ```yaml
  # prometheus-config.yaml
  global:
    scrape_interval: 15s
  
  scrape_configs:
  - job_name: 'aegis-ingestion'
    kubernetes_sd_configs:
    - role: pod
      namespaces:
        names:
        - aegis
    relabel_configs:
    - source_labels: [__meta_kubernetes_pod_label_app]
      action: keep
      regex: aegis-ingestion
  
  - job_name: 'postgres'
    static_configs:
    - targets: ['aegis-postgres-exporter:9187']
  
  - job_name: 'redis'
    static_configs:
    - targets: ['aegis-redis-exporter:9121']
  ```

- [ ] Create alert rules:
  ```yaml
  # alert-rules.yaml
  groups:
  - name: aegis
    rules:
    - alert: HighUploadLatency
      expr: histogram_quantile(0.95, aegis_upload_latency_ms) > 100
      for: 5m
      annotations:
        summary: "Upload latency exceeds 100ms (p95)"
    
    - alert: ConnectionPoolExhaustion
      expr: aegis_db_pool_size / aegis_db_pool_max > 0.9
      for: 5m
      annotations:
        summary: "Database connection pool > 90% utilized"
    
    - alert: GarbageCollectionFailure
      expr: aegis_gc_failures_total > 0
      for: 1m
      annotations:
        summary: "Garbage collection is failing"
    
    - alert: SLAViolation
      expr: aegis_sla_violations_total > 0
      for: 1m
      annotations:
        summary: "SLA violation detected"
  ```

- [ ] Create Grafana dashboards:
  - Upload throughput (RPS, MB/s)
  - Latency distribution (p50, p95, p99)
  - Error rates (4xx, 5xx)
  - Database connection pool utilization
  - Garbage collection metrics
  - Derivation worker lag

- [ ] Create structured logging:
  ```go
  // Log format: JSON for parsing
  log.Info("upload_initiated",
    "tenant_id", tenantID,
    "session_id", sessionID,
    "total_size", totalSize,
    "missing_hashes", len(missingHashes),
  )
  ```

- [ ] Create runbooks for common scenarios:
  - Database connection pool exhaustion
  - Garbage collection falling behind
  - Derivation worker lag
  - Multi-region failover
  - Certificate renewal

**Acceptance Criteria:**
- Deployment is automated (no manual steps)
- Auto-scaling responds within 2 minutes to load changes
- Metrics are collected and alertable
- Logs are structured and searchable
- Runbooks document recovery procedures

---

## PHASE 11: HARDENING & PRODUCTION READINESS

### PROMPT 11.1: Security, Resilience, & SLA Verification
**Context & Objective:**
Before production, every security threat must be mitigated. Every SLA guarantee must be verified with load tests. Every failure mode must have a documented recovery procedure.

**Task:**
- [ ] Security hardening:
  - [ ] Enable TLS 1.3 for all connections (client ↔ edge, edge ↔ ingestion, ingestion ↔ database)
  - [ ] Enable encryption at rest for all storage (KMS key rotation)
  - [ ] Implement rate limiting: max 1000 RPS per tenant
  - [ ] Implement DDoS protection at edge (Cloudflare)
  - [ ] Implement CORS restrictions
  - [ ] Audit logging for all API calls
  - [ ] Credential rotation: KMS keys, database passwords, API tokens (every 90 days)
  - [ ] Penetration testing by third party
  - [ ] OWASP Top 10 verification

- [ ] Resilience testing:
  - [ ] Chaos engineering: random pod kills, network delays
  - [ ] Database failover: simulate primary failure, verify RTO < 30s
  - [ ] Object storage failure: verify degraded mode still works
  - [ ] Network partition: client isolation from backend
  - [ ] Cascade failure: ingestion failure → derivation workers backlog → disk full

- [ ] SLA verification tests:
  ```go
  func TestSLACompliance(t *testing.T) {
    // Durability: 11 Nines (99.999999999%)
    // Achieved via 8+4 Reed-Solomon erasure coding
    // Verify: 1 block deleted → can recover from 7 lost replicas
    
    // Latency: p95 < 45ms for commit manifest
    // Run 1000 operations, verify histogram
    
    // RPO: 0 seconds (synchronous replication)
    // Write to primary, verify immediately replicated to secondaries
    
    // RTO: < 30 seconds
    // Kill primary pod, verify failover and restoration
  }
  ```

- [ ] Document disaster recovery:
  - [ ] Backup strategy (PostgreSQL PITR, object storage versioning)
  - [ ] Recovery procedures (restore from backup)
  - [ ] Testing (conduct full recovery drill quarterly)
  - [ ] RTO targets for each scenario

- [ ] Create production checklist:
  - [ ] All dependencies security-scanned
  - [ ] All secrets in secret management (Vault / AWS Secrets Manager)
  - [ ] All logs centralized (ELK / Datadog)
  - [ ] All metrics collected and alerted
  - [ ] All documentation complete
  - [ ] All runbooks written and tested
  - [ ] Incident response team trained
  - [ ] SLA signed with customers

**Acceptance Criteria:**
- Zero known security vulnerabilities (CVSS > 5.0)
- All SLA metrics verified with load tests
- Failover works < 30 seconds
- Backup/restore tested and documented
- Production ready ✓

---

## EXECUTION STRATEGY

### How to Use These Prompts:

1. **Sequential Execution**: Execute prompts in order. Each phase builds on previous.
2. **AI Agent Workflows**: Each prompt is self-contained and can be given to an AI agent with full context.
3. **Quality Gates**: Each phase has acceptance criteria. Don't proceed until criteria met.
4. **Iterative Refinement**: Prompts expect feedback loops (tests fail → fix code → re-run tests).
5. **Documentation**: Each prompt should produce runnable code + documentation.

### Success Metrics:

- [ ] System achieves 99.999999999% (11 Nines) durability
- [ ] Upload latency: p95 < 45ms, p99 < 85ms
- [ ] Throughput: > 1GB/s aggregate
- [ ] Deduplication ratio: > 50% better than fixed-size chunking
- [ ] RTO < 30 seconds on any failure
- [ ] Zero data loss events
- [ ] 100% test coverage on critical paths

---

**END OF PROMPT SUITE**

This document contains the complete blueprint for building Project Aegis from scratch. Each prompt is detailed enough to guide an AI to production-quality code with no ambiguity. Execute systematically, verify at each gate, and you'll have an exabyte-capable distributed storage system.

