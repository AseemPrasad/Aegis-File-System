# Aegis File System — Master Repository Analysis

> **Repository:** `github.com/aegis-dev/aegis`
> **Generated:** August 2026
> **Purpose:** Reverse-engineered learning curriculum for AI assistants and onboarding engineers.

---

## Table of Contents

1. [Executive Summary](#1-executive-summary)
2. [Repository Map](#2-repository-map)
3. [System Mental Model](#3-system-mental-model)
4. [Architecture Overview](#4-architecture-overview)
5. [Component Architecture](#5-component-architecture)
6. [Data Architecture](#6-data-architecture)
7. [Runtime Flows](#7-runtime-flows)
8. [Important Features](#8-important-features)
9. [Design Patterns](#9-design-patterns)
10. [Architecture Decisions](#10-architecture-decisions)
11. [Alternatives and Tradeoffs](#11-alternatives-and-tradeoffs)
12. [Security](#12-security)
13. [Reliability](#13-reliability)
14. [Performance](#14-performance)
15. [Testing](#15-testing)
16. [Infrastructure](#16-infrastructure)
17. [Weaknesses and Technical Debt](#17-weaknesses-and-technical-debt)
18. [Learning Curriculum](#18-learning-curriculum)
19. [Knowledge Gaps](#19-knowledge-gaps)
20. [Recommended Deep-Dive Order](#20-recommended-deep-dive-order)

---

## 1. Executive Summary

Aegis is a **multi-tenant, secure file system** built on a Content-Addressable Storage (CAS) core. It provides:

- **Secure file ingestion** via pre-signed URLs with HMAC authentication
- **Content-addressable storage** with automatic deduplication across tenants
- **Intelligent CDC-based chunking** (FastCDC) for efficient storage
- **Transparent pipeline derivation** (screenshot, thumbnail, OCR, etc.)
- **Strong security** with chaos engineering, rate limiting, and disaster recovery
- **Production-grade infrastructure** with Kubernetes manifests, observability, and SLA verification

**Scale Targets:**
- 1 PB data under management
- 10,000 concurrent file operations
- P95 latency < 45ms for initiate/commit
- > 99.9% availability

**Tech Stack:**
- **Backend:** Go 1.25.0 (stdlib `net/http` with Go 1.22+ routing)
- **Reference Implementation:** Rust (FastCDC algorithm, edition 2021)
- **Database:** PostgreSQL (primary), Redis (caching/coordination), MinIO (edge/local S3)
- **Message Queue:** Redpanda (Kafka-compatible)
- **Object Storage:** AWS S3 (production), MinIO (development/edge)
- **Orchestration:** Kubernetes with Kustomize, Terraform for cloud resources
- **Observability:** Prometheus, Grafana, Jaeger (distributed tracing)

---

## 2. Repository Map

```
aegis/
├── cmd/
│   └── ingest/
│       ├── main.go                    # Service entrypoint (349 lines)
│       ├── gc_adapter.go              # GC engine adapter (13 lines)
│       ├── derivation_bridge.go       # Event bus → worker pool bridge (47 lines)
│       └── derivation_helpers.go      # DB block reader + no-op tools (223 lines)
├── internal/
│   ├── ingress/
│   │   ├── server.go                  # IngressServer, Config, middleware chain (228 lines)
│   │   ├── store.go                   # Store interface, PgStore, FakeStore (504 lines)
│   │   ├── models.go                  # Request/response types, errors (215 lines)
│   │   ├── initiate.go               # handleInitiate: quota→dedup→presign (718 lines)
│   │   ├── commit.go                 # handleCommit: session→build→publish (973 lines)
│   │   ├── events.go                 # EventBus interface, InProcessBus (68 lines)
│   │   ├── metrics.go                # IngestMetrics (Prometheus) (227 lines)
│   │   ├── ratelimit.go              # Per-tenant token bucket (NEW in 9.0)
│   │   ├── cors.go                   # CORS middleware (NEW in 9.0)
│   │   └── audit.go                  # Audit logging middleware (NEW in 9.0)
│   ├── database/
│   │   ├── client.go                 # DatabaseClient (572 lines)
│   │   ├── cache.go                  # NamespaceCache (Redis) (196 lines)
│   │   ├── config.go                 # Config struct (62 lines)
│   │   ├── failover.go              # CircuitBreaker, atomicCounter (92 lines)
│   │   └── metrics.go               # Database metrics (169 lines)
│   ├── auth/
│   │   ├── hmac.go                   # HMAC tokens (394 lines)
│   │   ├── keys.go                   # StaticKMS, rotation (170 lines)
│   │   └── replay.go                # NonceStore (Redis/Memory) (196 lines)
│   ├── cas/
│   │   ├── bloom.go                  # Redis bloom filter (140 lines)
│   │   ├── registry.go              # CASRegistry, PgRegistry (166 lines)
│   │   ├── gc.go                    # GCWorker (503 lines)
│   │   └── metrics.go              # CAS metrics (169 lines)
│   ├── objectstorage/
│   │   ├── storage.go               # ObjectStorageClient interface (52 lines)
│   │   ├── s3.go                    # S3Client (738 lines)
│   │   └── minio.go                 # MinIOClient (166 lines)
│   ├── derivation/
│   │   ├── retry.go                 # Exponential backoff (86 lines)
│   │   └── dlq.go                  # Dead letter queue (80 lines)
│   ├── fastcdc/
│   │   └── fastcdc.go               # FastCDC algorithm (316 lines)
│   ├── gc/
│   │   └── gc.go                    # GC engine (503 lines)
│   └── workers/
│       └── engine.go                # Worker pool (missing/excluded)
├── aegis/                            # SDK (public API)
│   ├── client.go                     # Client (270 lines)
│   ├── protocol.go                   # Wire types (150 lines)
│   ├── upload.go                     # Parallel upload (242 lines)
│   └── session.go                    # Pause/resume persistence (94 lines)
├── crates/
│   └── fastcdc/
│       └── src/
│           └── lib.rs               # Reference Rust FastCDC (551 lines)
├── db/
│   └── schema.sql                    # PostgreSQL schema (268 lines)
├── deploy/
│   ├── compose/
│   │   └── docker-compose.yml       # Dev stack (165 lines)
│   ├── k8s/                         # Kubernetes manifests (30+ files)
│   └── terraform/
│       └── variables.tf             # Terraform config (81 lines)
├── docs/
│   ├── learning/
│   │   ├── 00-master-repository-analysis.md  # This file
│   │   └── ...
│   ├── runbooks/
│   │   ├── pool-exhaustion.md
│   │   ├── gc-behind.md
│   │   ├── worker-lag.md
│   │   ├── multi-region-failover.md
│   │   ├── cert-renewal.md
│   │   └── credential-rotation.md
│   ├── structured-logging.md
│   ├── production-readiness-checklist.md
│   ├── disaster-recovery.md
│   └── tls-configuration.md
├── go.mod                            # Go 1.25.0, 8 direct deps, 40+ indirect
├── Cargo.toml                        # Rust workspace (single member)
├── rust-toolchain.toml               # 1.79.0 (MSRV)
├── deny.toml                         # cargo-deny configuration
└── README.md
```

**Total estimated lines of code (Go):** ~8,000–9,000 across all packages.
**Total estimated lines (Rust):** ~550 (FastCDC reference only).

---

## 3. System Mental Model

Think of Aegis as a **content-addressable vault** with three layers:

### Layer 1: Identity Layer (CAS)
Every piece of content is identified by its SHA-256 hash. If two tenants upload the same file, only one copy is stored. The CAS layer manages deduplication, reference counting, and garbage collection.

### Layer 2: Namespace Layer (Tenancy)
Each tenant has a isolated namespace tree (ltree paths like `tenant1.projectX.folderY`). Files live in this tree. The namespace layer enforces ACLs and provides version history.

### Layer 3: Pipeline Layer (Derivation)
When a file lands, derivation workers can automatically process it: extract text (OCR), generate thumbnails, transcribe audio, etc. Results are stored as derived nodes in the namespace.

```
┌─────────────────────────────────────────────────────────────────┐
│                        CLIENT (aegis SDK)                        │
│  UploadFile → Initiate → PUT blocks → Commit → Derive          │
└──────────┬──────────────────────────────────────┬───────────────┘
           │                                      │
           ▼                                      ▼
┌──────────────────────┐           ┌──────────────────────────────┐
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

### The Deduplication Model

```
Tenant A uploads "report.pdf" (SHA-256: abc123)
  → CAS block created, ref_count = 1
  → File version node created in tenant A's namespace

Tenant B uploads identical "report.pdf" (SHA-256: abc123)
  → NO new CAS block (dedup by hash)
  → ref_count incremented to 2 (via database trigger)
  → File version node created in tenant B's namespace

Tenant A deletes "report.pdf"
  → ref_count decremented to 1 (via trigger)
  → CAS block NOT garbage collected (still referenced by tenant B)

GC sweep runs
  → Finds blocks with ref_count = 0
  → Marks them for deletion
  → S3 lifecycle policy cleans up after 7-day grace period
```

---

## 4. Architecture Overview

### High-Level Architecture

Aegis follows a **microservices-ready monolith** pattern. The main binary (`cmd/ingest/main.go`) wires together all components:

1. **IngressServer** — HTTP API for file operations
2. **DatabaseClient** — PostgreSQL connection pool with caching
3. **GC Engine** — Background garbage collection
4. **Derivation Workers** — Optional pipeline processing
5. **Session Reaper** — Cleans up stale upload sessions

### Component Dependency Graph

```
main.go
  ├── IngressServer
  │     ├── Store (PgStore → DatabaseClient)
  │     ├── ObjectStorageClient (S3Client/MinIOClient)
  │     ├── TokenSigner (HMAC)
  │     ├── CASRegistry (PgRegistry → DatabaseClient)
  │     ├── BloomFilter (RedisBloomFilter → Redis)
  │     ├── EventBus (InProcessBus)
  │     ├── Metrics (IngestMetrics)
  │     ├── RateLimiter (per-tenant)
  │     └── CORS + Audit middleware
  ├── DatabaseClient
  │     ├── Primary pool (read/write)
  │     ├── Replica pools (read-only, round-robin)
  │     ├── Statement pool (prepared statements)
  │     ├── Transaction pool (short-lived txns)
  │     ├── Cache (Redis-backed, generation invalidation)
  │     └── CircuitBreaker (failover to replicas)
  ├── GC Engine
  │     ├── PgGCRegistry → DatabaseClient
  │     ├── S3ObjectStore → S3Client
  │     └── Metrics (PubSub/NoopPubSub)
  ├── Derivation Workers (optional)
  │     ├── EventConsumer (Redpanda/Kafka)
  │     ├── PipelineRegistry (tool → pipeline mapping)
  │     ├── ToolExecutor (OCR, thumbnail, etc.)
  │     └── RetryWithBackoff + DeadLetterQueue
  └── Session Reaper
        └── DatabaseClient (delete stale sessions)
```

### Interface Boundaries

The system uses **explicit Go interfaces** at every boundary:

| Interface | File | Purpose |
|-----------|------|---------|
| `Store` | `ingress/store.go` | All database operations for ingress |
| `ObjectStorageClient` | `objectstorage/storage.go` | S3/MinIO abstraction |
| `TokenSigner` | `auth/hmac.go` | HMAC token creation/verification |
| `ReplayProtector` | `auth/replay.go` | Nonce-based replay prevention |
| `BloomFilter` | `cas/bloom.go` | Approximate dedup checking |
| `Registry` | `cas/registry.go` | CAS block lifecycle management |
| `GCRegistry` | `gc/gc.go` | GC-specific block queries |
| `ObjectStore` | `gc/gc.go` | GC-specific blob deletion |
| `EventBus` | `ingress/events.go` | Publish/subscribe for derivations |
| `PipelineRegistry` | (derivation) | Tool → pipeline mapping |
| `ToolExecutor` | (derivation) | Individual pipeline tool execution |

---

## 5. Component Architecture

### 5.1 Ingress Server (`internal/ingress/`)

**Purpose:** HTTP API for secure file ingestion with pre-signed URLs.

**Request Flow:**
```
POST /api/v1/files/initiate
  → authenticate() (HMAC token)
  → RateLimiter.Allow() (per-tenant)
  → handleInitiate()
    → quota check (file size, storage)
    → file node creation (namespace_nodes)
    → CAS dedup check (bloom filter → registry)
    → pre-signed upload URLs (one per non-deduped block)
    → upload session creation (upload_sessions)
  → AuditMiddleware.Log()

PUT /api/v1/files/upload/{session_id}/{block_idx}
  → (no auth — pre-signed URL validates)
  → S3 direct upload

POST /api/v1/files/commit
  → authenticate() (HMAC token)
  → handleCommit()
    → session lookup + validation
    → manifest build (file_manifest_blocks)
    → content SHA-256 computation
    → commit (namespace_nodes update)
    → EventBus.Publish()
  → AuditMiddleware.Log()
```

**Key Components:**
- **RateLimiter** (`ratelimit.go`): Per-tenant token bucket, 1000 RPS default, burst 2000
- **CORSMiddleware** (`cors.go`): Configurable origin whitelist
- **AuditMiddleware** (`audit.go`): Structured JSON audit log with timing
- **IngestMetrics** (`metrics.go`): Prometheus counters/histograms/gauges

### 5.2 Database Client (`internal/database/`)

**Purpose:** High-performance PostgreSQL access with caching and failover.

**Pool Architecture:**
```
DatabaseClient
  ├── PrimaryPool (read/write, max 25 connections)
  ├── ReplicaPool[0..N] (read-only, round-robin)
  ├── StatementPool (prepared statements, max 10)
  ├── TransactionPool (short-lived txns, max 20)
  ├── Cache (Redis, generation-based invalidation)
  └── CircuitBreaker (failover on primary failure)
```

**Circuit Breaker States:**
```
Closed (normal) → [3 consecutive failures] → Open (failover to replicas)
    ↑                                              │
    └──────── [success] ──────────── Half-Open ────┘
                                              [failure] → Open
```

**Cache Strategy:**
- **Write-through:** Updates invalidate cache entries
- **Generation-based:** `INCR` on namespace changes invalidates all children
- **TTL:** 5-minute expiry for cache entries
- **Eviction:** LRU with 10,000 entry limit

### 5.3 Auth System (`internal/auth/`)

**Purpose:** HMAC-based authentication with replay protection.

**Token Structure:**
```
HMAC-SHA256:
  payload = {tenant_id, node_id, expires_at, nonce}
  signature = HMAC-SHA256(secret_key, payload)
  token = base64(payload) + "." + hex(signature)
```

**Security Properties:**
1. **Structure:** Payload format validation
2. **Freshness:** Expiry check (default 5 minutes)
3. **Authenticity:** HMAC signature verification
4. **Replay:** Nonce store (Redis or in-memory)
5. **Isolation:** Tenant-scoped keys

**Key Rotation:**
- `KeyRecord` with `ValidFrom`/`ValidUntil` for overlapping windows
- `StaticKMS` for development; production uses cloud KMS
- 7-day overlap period for zero-downtime rotation

### 5.4 CAS Layer (`internal/cas/`)

**Purpose:** Content-addressable storage with deduplication.

**Dedup Pipeline:**
```
Block arrives → Bloom filter check (1% FP rate)
  → [miss] → Database lookup (CAS blocks table)
    → [not found] → Store block, ref_count = 1
    → [found] → Increment ref_count, return existing URL
  → [hit] → Assume dedup, skip database query
```

**GC Strategy:**
- **Reference counting:** Triggers maintain `ref_count` on `cas_blocks`
- **Sweep:** Hourly cron, processes 5,000 blocks/hour max
- **Safety:** 7-day grace period before S3 deletion
- **Metrics:** Blocks scanned, deleted, failed (Prometheus)

### 5.5 FastCDC (`internal/fastcdc/`)

**Purpose:** Content-defined chunking for efficient deduplication.

**Algorithm:**
1. **Gear Hash:** Rolling hash using gear table (256 entries)
2. **Normalized Two-Phase Masking:** Determines cut points
3. **SHA-256:** Content addressing for each chunk

**Parameters:**
- Min chunk: 64KB
- Average chunk: 1MB
- Max chunk: 4MB

**Key Fix:** `findCut` no longer increments `c.offset` (was double-counting with `readUntilCut`).

### 5.6 Object Storage (`internal/objectstorage/`)

**Purpose:** Abstraction over S3/MinIO with lifecycle management.

**S3Client Features:**
- Region-specific storage classes (STANDARD, STANDARD_IA, GLACIER)
- Lifecycle policies (transition to IA after 30d, Glacier after 90d)
- Integrity checks (ETag verification on upload/download)
- Pre-signed URL generation (15-minute expiry)

### 5.7 GC Engine (`internal/gc/`)

**Purpose:** Background garbage collection for unreferenced CAS blocks.

**GC Worker Flow:**
```
1. Query: SELECT block_id FROM cas_blocks WHERE ref_count = 0
   AND updated_at < NOW() - INTERVAL '7 days'
   LIMIT 5000
2. For each block:
   a. Delete from S3 (ObjectStore.Delete)
   b. Delete from database (DELETE FROM cas_blocks)
   c. Record metric (PubSub.Publish)
3. Sleep 1 minute between batches
```

**Adapters:**
- `gcStoreAdapter`: Wraps DatabaseClient for GC queries
- `gcPublisherAdapter`: Wraps PubSub for metrics
- `blobDeleterAdapter`: Wraps ObjectStorageClient for S3 deletion

---

## 6. Data Architecture

### 6.1 Schema Overview

```sql
-- Core tables (db/schema.sql, 268 lines)
tenants              -- Multi-tenancy root
namespace_nodes      -- File/folder tree (ltree)
acl_entries          -- Access control
file_versions        -- Immutable file snapshots
cas_blocks           -- Content-addressable blocks (ref_count maintained by triggers)
file_manifest_blocks -- Links versions to blocks
upload_sessions      -- Active upload tracking
audit_logs           -- Range-partitioned monthly
derivation_results   -- Pipeline output tracking
```

### 6.2 Key Relationships

```
tenants (1) ──── (*) namespace_nodes
namespace_nodes (1) ──── (*) file_versions
namespace_nodes (1) ──── (*) acl_entries
file_versions (1) ──── (*) file_manifest_blocks
cas_blocks (1) ──── (*) file_manifest_blocks
namespace_nodes (1) ──── (*) upload_sessions
```

### 6.3 Ltree Namespace Model

```sql
-- Path hierarchy using PostgreSQL ltree extension
tenant1.projectX.folderY.report.pdf  -- depth 4
tenant1.projectX.folderY.images/      -- folder node
tenant1.projectX.folderY.images/cat.jpg -- depth 5
```

**Advantages:**
- Native PostgreSQL support for hierarchy queries
- Efficient `@>` (ancestor) and `<@` (descendant) operators
- GIST index for fast path lookups

### 6.4 Trigger-Maintained Reference Counting

```sql
-- NEVER write ref_count from application code
-- Triggers handle it automatically:

-- On file_manifest_blocks INSERT:
CREATE TRIGGER manifest_block_added
  AFTER INSERT ON file_manifest_blocks
  FOR EACH ROW EXECUTE FUNCTION increment_ref_count();

-- On file_manifest_blocks DELETE:
CREATE TRIGGER manifest_block_removed
  AFTER DELETE ON file_manifest_blocks
  FOR EACH ROW EXECUTE FUNCTION decrement_ref_count();

-- Atomic increment with FOR UPDATE:
UPDATE cas_blocks
SET ref_count = ref_count + 1
WHERE block_hash = NEW.block_hash
RETURNING ref_count;
```

### 6.5 CAS Deduplication (Cross-Tenant)

```sql
-- CAS blocks are global, NOT tenant-scoped
-- Two tenants uploading identical content share the same block
-- ref_count tracks how many file_manifest_blocks reference each block

-- Dedup query:
SELECT block_hash FROM cas_blocks
WHERE block_hash = $1;

-- If exists: increment ref_count, skip upload
-- If not: insert with ref_count = 1, upload to S3
```

### 6.6 Version Density Guarantee

```sql
-- next_version_number uses FOR UPDATE for dense numbering
CREATE OR REPLACE FUNCTION next_version_number(p_node_id UUID)
RETURNS INTEGER AS $$
DECLARE
  next_ver INTEGER;
BEGIN
  SELECT COALESCE(MAX(version_number), 0) + 1
  INTO next_ver
  FROM file_versions
  WHERE node_id = p_node_id
  FOR UPDATE;  -- Locks row for dense numbering

  RETURN next_ver;
END;
$$ LANGUAGE plpgsql;
```

### 6.7 Audit Log Partitioning

```sql
-- audit_logs range-partitioned monthly for performance
CREATE TABLE audit_logs (
  id UUID DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL,
  action TEXT NOT NULL,
  details JSONB,
  created_at TIMESTAMPTZ DEFAULT NOW()
) PARTITION BY RANGE (created_at);

-- Auto-create partitions:
CREATE TABLE audit_logs_2026_09 PARTITION OF audit_logs
  FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');
```

---

## 7. Runtime Flows

### 7.1 File Upload Flow (End-to-End)

```
1. CLIENT: Initiate upload
   POST /api/v1/files/initiate
   Authorization: Bearer <hmac_token>
   Body: {node_path: "projectX/report.pdf", file_size: 1048576, block_size: 1048576}

2. INGRESS: Authenticate
   → Validate HMAC signature
   → Check freshness (5-min expiry)
   → Verify nonce (replay protection)
   → Extract tenant_id, node_id

3. INGRESS: Rate limit
   → Per-tenant token bucket check
   → 1000 RPS default, burst 2000

4. INGRESS: Quota check
   → File size < tenant limit
   → Storage quota available

5. INGRESS: CAS dedup
   → Bloom filter check (Redis, 1% FP rate)
   → [miss] → Database lookup
   → [found] → Return existing block URL, skip upload
   → [not found] → Generate pre-signed PUT URL

6. INGRESS: Create session
   → INSERT INTO upload_sessions (node_id, tenant_id, status='PENDING')
   → Return session_id + upload URLs

7. CLIENT: Upload blocks
   PUT /api/v1/files/upload/{session_id}/{0}
   Authorization: <pre_signed_url>
   Body: <raw bytes>

   → S3 direct upload (bypasses Go server)
   → S3 returns ETag

8. CLIENT: Commit
   POST /api/v1/files/commit
   Authorization: Bearer <hmac_token>
   Body: {
     session_id: "...",
     file_size: 1048576,
     block_hashes: ["abc123..."],
     block_sizes: [1048576],
     block_etags: ["etag123..."]
   }

9. INGRESS: Validate + Build manifest
   → Verify all blocks uploaded (ETag check)
   → Compute content SHA-256
   → INSERT INTO file_manifest_blocks
   → UPDATE file_versions SET status='COMPLETED'

10. INGRESS: Publish event
    → EventBus.Publish(CommittedEvent{
        tenant_id, node_id, version_id, block_hashes
    })

11. DERIVATION: Process pipelines
    → EventConsumer receives event
    → PipelineRegistry.GetPipelines("application/pdf")
    → Execute: OCR, thumbnail, metadata extraction
    → Store results as derived namespace_nodes
```

### 7.2 GC Sweep Flow

```
1. CRON: Hourly trigger
   → GCWorker.Run()

2. GC: Query unreferenced blocks
   → SELECT block_id FROM cas_blocks
     WHERE ref_count = 0
     AND updated_at < NOW() - INTERVAL '7 days'
     LIMIT 5000

3. GC: Process batch
   → For each block:
     a. ObjectStore.Delete(block_id)  -- S3 deletion
     b. DELETE FROM cas_blocks WHERE block_id = $1
     c. PubSub.Publish({block_id, deleted_at})

4. GC: Rate limiting
   → 5000 blocks/hour max
   → Sleep 1 minute between batches

5. GC: Metrics
   → Blocks scanned, deleted, failed
   → Duration, errors
```

### 7.3 Key Rotation Flow

```
1. ROTATION: Generate new key
   → KeyRecord{ValidFrom: now, ValidUntil: now + 7 days}
   → Store in KMS (or StaticKMS for dev)

2. OVERLAP: Both keys valid
   → Old key: ValidUntil = now + 7 days
   → New key: ValidFrom = now
   → Token verification tries both keys

3. CUTOVER: Old key expires
   → ValidUntil reached
   → Only new key accepted
   → Old tokens rejected

4. CLEANUP: Remove old key
   → Delete from KMS
   → Audit log rotation event
```

---

## 8. Important Features

### 8.1 Content-Addressable Storage (CAS)

- **Cross-tenant deduplication:** Identical content shared across tenants
- **Reference counting:** Triggers maintain `ref_count` automatically
- **Safety:** 7-day grace period before GC deletion
- **Bloom filter:** 1% false positive rate, Redis-backed, 1-hour TTL

### 8.2 Pre-Signed URLs

- **HMAC authentication:** SHA-256 signatures
- **Time-limited:** 15-minute expiry
- **Nonce-based replay protection:** Single-use tokens
- **Direct-to-S3:** Uploads bypass Go server entirely

### 8.3 Intelligent CDC Chunking

- **FastCDC algorithm:** Content-defined boundaries
- **Gear hash:** Rolling hash for cut point detection
- **Normalized two-phase masking:** Deterministic chunk sizes
- **Parameters:** Min=64KB, Avg=1MB, Max=4MB

### 8.4 Transparent Pipeline Derivation

- **Event-driven:** Kafka/Redpanda for async processing
- **Tool-based:** OCR, thumbnail, transcription, etc.
- **Retry with backoff:** Exponential backoff for transient failures
- **Dead letter queue:** Failed jobs preserved for debugging

### 8.5 Multi-Tenant Isolation

- **Namespace tree:** ltree paths per tenant
- **ACL entries:** Role-based access control
- **Rate limiting:** Per-tenant token buckets
- **Audit logging:** Structured JSON with timing

### 8.6 Disaster Recovery

- **RPO:** 5 minutes (continuous WAL archiving)
- **RTO:** 1 hour (point-in-time recovery)
- **Backup:** Daily full + hourly incremental + continuous WAL
- **Multi-AZ:** PostgreSQL, Redis, S3 cross-region replication

### 8.7 Chaos Engineering

- **DB down simulation:** Circuit breaker activation
- **Panic recovery:** Graceful degradation
- **Overload handling:** Rate limiting, backpressure
- **Invalid input:** Graceful error responses

---

## 9. Design Patterns

### 9.1 Repository Pattern

```go
// Store interface abstracts all database operations
type Store interface {
    BeginWriteTx(ctx context.Context) (WriteTx, error)
    GetTenantQuota(ctx context.Context, tenantID string) (*TenantQuota, error)
    CreateFileNode(ctx context.Context, req InitiateRequest) (*SessionRecord, error)
    // ... 12 methods total
}

// PgStore: Production implementation (PostgreSQL)
// FakeStore: In-memory test double
```

**Why:** Clean separation between business logic and data access. Enables testing without database.

### 9.2 Circuit Breaker Pattern

```go
type CircuitBreaker struct {
    failureCount atomic.Int64
    threshold    int64
    isOpen       atomic.Bool
    openedAt     atomic.Int64
    cooldown     time.Duration
}

// States: Closed → Open → Half-Open → Closed
// Triggers failover to replica databases
```

**Why:** Prevents cascade failures when primary database is unhealthy.

### 9.3 Strategy Pattern (Object Storage)

```go
type ObjectStorageClient interface {
    GeneratePresignedURL(ctx context.Context, key string, expiry time.Duration) (string, error)
    Upload(ctx context.Context, key string, reader io.Reader) error
    Delete(ctx context.Context, key string) error
    GetPresignedURL(ctx context.Context, key string) (string, error)
}

// S3Client: AWS S3 (production)
// MinIOClient: MinIO (development/edge)
```

**Why:** Swap storage backends without changing application code.

### 9.4 Observer Pattern (Event Bus)

```go
type EventBus interface {
    Publish(ctx context.Context, event CommittedEvent) error
    Subscribe(handler func(CommittedEvent)) error
}

// InProcessBus: Single-process pub/sub
// KafkaEventBus: Distributed pub/sub (future)
```

**Why:** Decouple ingestion from derivation processing.

### 9.5 Adapter Pattern (GC Engine)

```go
// gcStoreAdapter wraps DatabaseClient for GC queries
type gcStoreAdapter struct {
    db *database.DatabaseClient
}

// gcPublisherAdapter wraps PubSub for metrics
type gcPublisherAdapter struct {
    pubsub metrics.PubSub
}

// blobDeleterAdapter wraps ObjectStorageClient
type blobDeleterAdapter struct {
    store objectstorage.ObjectStorageClient
}
```

**Why:** GC engine depends on abstractions, not concrete implementations.

### 9.6 Chain of Responsibility (Middleware)

```go
func (s *IngressServer) wrap(next http.Handler) http.Handler {
    return s.metrics.Middleware(
        s.rateLimiter.Middleware(
            s.cors.Middleware(
                s.audit.Middleware(next),
            ),
        ),
    )
}
```

**Why:** Composable middleware stack for cross-cutting concerns.

### 9.7 Retry with Backoff

```go
func RetryWithBackoff(ctx context.Context, maxRetries int, operation func() error) error {
    for attempt := 0; attempt <= maxRetries; attempt++ {
        if err := operation(); err == nil {
            return nil
        } else if !IsTransient(err) || attempt == maxRetries {
            return err
        }
        backoff := time.Duration(1<<uint(attempt)) * baseDelay
        select {
        case <-time.After(backoff):
        case <-ctx.Done():
            return ctx.Err()
        }
    }
    return ErrMaxRetriesExceeded
}
```

**Why:** Handle transient failures (network, S3, database) gracefully.

---

## 10. Architecture Decisions

### ADR-001: Go over Rust for Main Service

**Decision:** Use Go for the main service, Rust only for FastCDC reference implementation.

**Rationale:**
- Go stdlib `net/http` with Go 1.22+ routing is sufficient
- No external web framework needed
- Faster development iteration
- Easier hiring/maintenance
- Rust only where performance-critical (FastCDC)

### ADR-002: Pre-Signed URLs for Uploads

**Decision:** Client uploads directly to S3 via pre-signed URLs.

**Rationale:**
- Server doesn't handle file bytes (reduced memory/CPU)
- S3 handles durability and availability
- HMAC tokens provide authentication without proxying
- Supports large files without server-side buffering

### ADR-003: Trigger-Maintained Reference Counting

**Decision:** PostgreSQL triggers maintain `ref_count` on `cas_blocks`, not application code.

**Rationale:**
- Atomic updates prevent race conditions
- Application code never writes `ref_count`
- Triggers run in the same transaction as inserts/deletes
- Guarantees consistency even with concurrent operations

### ADR-004: Ltree for Namespace Hierarchy

**Decision:** Use PostgreSQL `ltree` extension for file/folder hierarchy.

**Rationale:**
- Native PostgreSQL support
- Efficient hierarchy queries (`@>`, `<@` operators)
- GIST index for fast path lookups
- Simpler than recursive CTEs

### ADR-005: Bloom Filter for CAS Dedup

**Decision:** Redis-backed bloom filter for approximate dedup checking.

**Rationale:**
- 1% false positive rate is acceptable (skip unnecessary DB queries)
- Redis provides fast lookups (sub-millisecond)
- 1-hour TTL prevents stale data
- Reduces database load by ~90% for repeat uploads

### ADR-006: Event-Driven Derivation

**Decision:** Kafka/Redpanda for async pipeline processing.

**Rationale:**
- Decouples ingestion from derivation
- Supports replay for failed jobs
- Scales independently (more workers)
- DLQ for debugging failed pipelines

### ADR-007: Kustomize over Helm

**Decision:** Kustomize for Kubernetes manifests.

**Rationale:**
- No template language to learn
- Declarative overlays
- Built into `kubectl`
- Simpler than Helm for this use case

### ADR-008: Multi-Pool Database Architecture

**Decision:** Four separate connection pools (primary, replica, statement, transaction).

**Rationale:**
- Prevents connection exhaustion under load
- Statement pool for prepared statements (performance)
- Transaction pool for short-lived transactions
- Replica pool for read-heavy workloads

---

## 11. Alternatives and Tradeoffs

### 11.1 CAS vs Traditional File System

| Aspect | CAS (Aegis) | Traditional FS |
|--------|-------------|----------------|
| Deduplication | Automatic (content hash) | Manual (file-level) |
| Versioning | Immutable blocks | Copy-on-write |
| Immutability | By design | Mutable |
| Storage efficiency | High (dedup) | Low (duplicates) |
| Query complexity | Higher (block manifests) | Simpler (paths) |

### 11.2 Pre-Signed URLs vs Proxy Uploads

| Aspect | Pre-Signed URLs | Proxy Uploads |
|--------|-----------------|---------------|
| Server load | Low (no bytes) | High (buffers) |
| Memory usage | Minimal | Proportional to file size |
| Authentication | HMAC tokens | Session cookies |
| Large file support | Native | Requires streaming |
| Complexity | Higher (token mgmt) | Lower |

### 11.3 Ltree vs Materialized Path vs Closure Table

| Aspect | Ltree | Materialized Path | Closure Table |
|--------|-------|-------------------|---------------|
| Query speed | Fast (GIST index) | Medium (LIKE) | Fast (JOIN) |
| Update cost | Low | Medium (repath) | High (rebuild) |
| Storage | Low | Medium | High |
| PostgreSQL support | Native extension | Application-level | Application-level |

### 11.4 Kustomize vs Helm

| Aspect | Kustomize | Helm |
|--------|-----------|------|
| Learning curve | Low | Medium |
| Templating | No (overlays) | Yes (Go templates) |
| Flexibility | High | Medium |
| Ecosystem | Smaller | Larger |
| Complexity | Lower | Higher |

### 11.5 Go vs Rust for Main Service

| Aspect | Go | Rust |
|--------|----|------|
| Development speed | Faster | Slower |
| Runtime performance | Good | Excellent |
| Memory safety | GC-managed | Compile-time |
| Concurrency | Goroutines | Tokio/async |
| Learning curve | Lower | Higher |
| Ecosystem | Mature | Growing |

---

## 12. Security

### 12.1 Authentication

- **HMAC tokens:** SHA-256 signatures with tenant-scoped keys
- **Time-limited:** 5-minute expiry (configurable)
- **Nonce-based replay protection:** Single-use tokens
- **Key rotation:** 7-day overlap period

### 12.2 Authorization

- **ACL entries:** Role-based access control per namespace node
- **Tenant isolation:** Namespace tree partitioned by tenant
- **Pre-signed URLs:** Time-limited, single-use upload tokens

### 12.3 Rate Limiting

- **Per-tenant token bucket:** 1000 RPS default, burst 2000
- **Sliding window:** Redis-backed distributed rate limiting
- **Graceful degradation:** 429 responses with Retry-After header

### 12.4 Transport Security

- **TLS 1.3 enforced:** Strong cipher suites only
- **HSTS:** Strict-Transport-Security header
- **Certificate management:** Let's Encrypt with auto-renewal

### 12.5 Data Security

- **Encryption at rest:** S3 SSE-S3 / KMS
- **Encryption in transit:** TLS 1.3
- **Integrity checks:** SHA-256 content hashing, ETag verification
- **Audit logging:** Structured JSON with timing

### 12.6 Infrastructure Security

- **Pod Security Standards:** Restricted profile
- **Network Policies:** Default-deny + per-component whitelists
- **Secrets management:** Kubernetes Secrets (production: cloud KMS)
- **Resource limits:** CPU/memory limits on all containers

### 12.7 Chaos Engineering (Security Tests)

- **DB down simulation:** Circuit breaker activation
- **Panic recovery:** Graceful degradation
- **Overload handling:** Rate limiting, backpressure
- **Invalid input:** Graceful error responses

---

## 13. Reliability

### 13.1 Availability Targets

- **99.9% uptime** (8.76 hours downtime/year)
- **P95 latency:** < 45ms for initiate/commit
- **Throughput:** > 45,000 ops/sec

### 13.2 Failure Modes

| Failure | Detection | Recovery |
|---------|-----------|----------|
| Primary DB down | Circuit breaker | Failover to replicas |
| Redis down | Connection timeout | Degrade to no-cache |
| S3 down | HTTP 5xx | Retry with backoff |
| Worker crash | Kafka consumer lag | Auto-restart |
| Network partition | Health checks | Graceful degradation |

### 13.3 Data Durability

- **PostgreSQL:** WAL archiving, daily backups, point-in-time recovery
- **S3:** 11 9's durability, cross-region replication
- **Redis:** Persistence (RDB + AOF), replication

### 13.4 Disaster Recovery

- **RPO:** 5 minutes (continuous WAL archiving)
- **RTO:** 1 hour (point-in-time recovery)
- **Backup:** Daily full + hourly incremental + continuous WAL
- **Multi-AZ:** PostgreSQL, Redis, S3

### 13.5 Health Checks

- **Liveness:** `/healthz` (is the process alive?)
- **Readiness:** `/readyz` (can it accept traffic?)
- **Startup:** `/startupz` (has initialization completed?)

---

## 14. Performance

### 14.1 SLA Verification Results

- **Initiate P95:** 7.7ms (target < 45ms) ✅
- **Concurrent P95:** 13ms ✅
- **Throughput:** 45,000+ ops/sec ✅
- **Error rate:** 0.00% ✅

### 14.2 Optimization Strategies

- **Bloom filter:** 1% FP rate reduces DB queries by ~90%
- **Cache-aside:** Redis caching for hot paths
- **Connection pooling:** 4 pool tiers prevent exhaustion
- **Pre-signed URLs:** Bypass Go server for uploads
- **FastCDC:** Content-defined chunking for efficient dedup

### 14.3 Bottlenecks

- **Database connections:** 25 max primary, 10 per replica
- **Redis memory:** 1GB default, eviction policy
- **S3 throughput:** 5,500 GET/s, 3,500 PUT/s per prefix
- **Worker concurrency:** Configurable pool size

### 14.4 Monitoring

- **Prometheus:** 16 alert rules, 4 groups
- **Grafana:** 3 dashboards (ingestion, storage, derivation)
- **Jaeger:** Distributed tracing for request flows

---

## 15. Testing

### 15.1 Test Coverage

- **Unit tests:** 27 new tests (PROMPT 9.0)
- **Package tests:** 11/11 packages pass `go test` and `go vet`
- **Integration tests:** Docker Compose stack
- **Chaos tests:** DB down, panic recovery, overload

### 15.2 Test Patterns

- **FakeStore:** In-memory test double for database
- **InProcessBus:** Single-process event bus for testing
- **StaticKMS:** Deterministic key management for tests
- **InMemoryNonceStore:** No Redis dependency for tests

### 15.3 Test Organization

```
internal/ingress/
  ├── server_test.go          # Integration tests
  ├── initiate_test.go        # Initiate handler tests
  ├── commit_test.go          # Commit handler tests
  ├── ratelimit_test.go       # Rate limiter tests
  ├── cors_test.go            # CORS tests
  ├── audit_test.go           # Audit middleware tests
  ├── sla_test.go             # SLA verification tests
  └── chaos_test.go           # Chaos engineering tests
```

### 15.4 Testing Strategies

- **Table-driven tests:** Standard Go pattern
- **Testify assertions:** `require` and `assert`
- **Context timeouts:** Prevent hanging tests
- **Parallel execution:** `t.Parallel()` where safe

---

## 16. Infrastructure

### 16.1 Kubernetes Manifests (deploy/k8s/)

- **Namespace:** `aegis`
- **Deployments:** aegis-ingestion (3 replicas, HPA 3→20)
- **StatefulSets:** postgres (3 replicas), redis (3 replicas)
- **CronJobs:** GC sweep (hourly)
- **HPAs:** Ingestion (3→20), derivation workers (2→10)
- **PDBs:** MinAvailable 2 for stateful, 1 for deployments
- **NetworkPolicies:** Default-deny + per-component whitelists

### 16.2 Observability Stack

- **Prometheus:** K8s service discovery, 16 alert rules
- **Grafana:** 3 dashboards (ingestion, storage, derivation)
- **Jaeger:** All-in-one for development, collector for production

### 16.3 Terraform (deploy/terraform/)

- **VPC:** Multi-AZ, public/private subnets
- **EKS:** Managed Kubernetes cluster
- **RDS:** PostgreSQL Multi-AZ
- **ElastiCache:** Redis cluster
- **S3:** CAS blocks bucket with lifecycle
- **KMS:** Encryption key management

### 16.4 Docker Compose (deploy/compose/)

- **postgres** (16-alpine) on port 15432
- **redis** (7-alpine) on port 6379
- **redpanda** (v24.2.7) on ports 9092/9644
- **minio** on ports 9000/9001
- **prometheus** on port 9090
- **grafana** on port 3000
- **ingest** service (Go binary)

---

## 17. Weaknesses and Technical Debt

### 17.1 Current Weaknesses

1. **No worker engine file:** `internal/workers/engine.go` missing/excluded
2. **Limited error handling:** Some paths lack structured error responses
3. **No retry in GC:** GC engine doesn't retry failed S3 deletions
4. **Bloom filter TTL:** 1-hour TTL may cause false negatives for rapid uploads
5. **No circuit breaker for S3:** Only database has circuit breaker
6. **Hardcoded constants:** Some thresholds are hardcoded (e.g., 5000 blocks/hour)
7. **No distributed tracing in workers:** Jaeger integration incomplete
8. **Missing load tests:** No sustained load testing results
9. **No chaos testing in CI:** Chaos tests run manually
10. **Documentation gaps:** Some internal APIs undocumented

### 17.2 Technical Debt

1. **Legacy Rust code:** `crates/fastcdc/` only used for reference, not production
2. **Missing Go modules:** Some internal packages not fully modularized
3. **Test coverage gaps:** Some edge cases untested
4. **Configuration sprawl:** 20+ environment variables in main.go
5. **No feature flags:** All features always enabled
6. **Missing monitoring:** Some components lack metrics
7. **No canary deployments:** K8s manifests don't support canary
8. **Limited benchmarking:** No performance regression tests

### 17.3 Recommended Improvements

1. **Add circuit breaker for S3:** Prevent cascade failures
2. **Implement retry in GC:** Handle transient S3 errors
3. **Add distributed tracing in workers:** Complete Jaeger integration
4. **Create load test suite:** Sustained performance validation
5. **Add chaos tests to CI:** Automated resilience testing
6. **Document internal APIs:** Improve code readability
7. **Implement feature flags:** Gradual rollout capability
8. **Add performance benchmarks:** Regression detection

---

## 18. Learning Curriculum

### Phase 1: Foundation (Start Here)

1. **Executive Summary** (Section 1)
   - Understand what Aegis is and its goals
   - Review scale targets and tech stack

2. **Repository Map** (Section 2)
   - Navigate the codebase structure
   - Identify key files and their purposes

3. **System Mental Model** (Section 3)
   - Grasp the three-layer architecture
   - Understand the deduplication model

### Phase 2: Core Concepts

4. **Architecture Overview** (Section 4)
   - Study the component dependency graph
   - Understand interface boundaries

5. **Data Architecture** (Section 6)
   - Review schema design
   - Understand ltree namespace model
   - Study trigger-maintained reference counting

6. **CAS Layer** (Section 5.4)
   - Learn dedup pipeline
   - Understand bloom filter usage
   - Study GC strategy

### Phase 3: Implementation Details

7. **Ingress Server** (Section 5.1)
   - Trace request flow (initiate → commit)
   - Study middleware chain
   - Understand pre-signed URL generation

8. **Database Client** (Section 5.2)
   - Study pool architecture
   - Understand circuit breaker
   - Review caching strategy

9. **Auth System** (Section 5.3)
   - Learn HMAC token structure
   - Understand replay protection
   - Study key rotation

### Phase 4: Advanced Topics

10. **FastCDC** (Section 5.5)
    - Study the algorithm (Gear hash, two-phase masking)
    - Understand chunk size parameters
    - Review the Rust reference implementation

11. **Event-Driven Architecture** (Section 7)
    - Trace the derivation flow
    - Understand event bus patterns
    - Study retry/DLQ mechanisms

12. **Security** (Section 12)
    - Review authentication/authorization
    - Study rate limiting
    - Understand chaos engineering tests

### Phase 5: Operations

13. **Reliability** (Section 13)
    - Study failure modes
    - Understand DR procedures
    - Review health checks

14. **Performance** (Section 14)
    - Review SLA verification results
    - Study optimization strategies
    - Understand bottlenecks

15. **Infrastructure** (Section 16)
    - Study K8s manifests
    - Review Terraform configuration
    - Understand observability stack

### Phase 6: Mastery

16. **Design Patterns** (Section 9)
    - Study all patterns used
    - Understand why each was chosen

17. **Architecture Decisions** (Section 10)
    - Review all ADRs
    - Understand tradeoffs made

18. **Weaknesses** (Section 17)
    - Identify current limitations
    - Plan improvements

---

## 19. Knowledge Gaps

### 19.1 Missing Documentation

1. **Internal API docs:** Some package interfaces undocumented
2. **Deployment guide:** Step-by-step production deployment
3. **Troubleshooting guide:** Common issues and solutions
4. **Performance tuning guide:** Optimization recommendations
5. **Security hardening guide:** Production security checklist

### 19.2 Missing Tests

1. **Load tests:** Sustained performance validation
2. **Chaos tests in CI:** Automated resilience testing
3. **Integration tests:** End-to-end workflow tests
4. **Security tests:** Penetration testing results
5. **Disaster recovery tests:** DR procedure validation

### 19.3 Missing Features

1. **Distributed tracing in workers:** Jaeger integration
2. **Circuit breaker for S3:** Prevent cascade failures
3. **Feature flags:** Gradual rollout capability
4. **Canary deployments:** K8s support
5. **Performance benchmarks:** Regression detection

### 19.4 Missing Operational Knowledge

1. **Runbook for common issues:** Troubleshooting guide
2. **Capacity planning:** Resource sizing guidelines
3. **Cost optimization:** S3 lifecycle, right-sizing
4. **Incident response:** Step-by-step procedures
5. **Post-incident review:** Template and process

---

## 20. Recommended Deep-Dive Order

### For New Developers (1-2 weeks)

1. **Day 1-2:** Sections 1-3 (Executive Summary, Repository Map, Mental Model)
2. **Day 3-4:** Section 4 (Architecture Overview)
3. **Day 5-6:** Section 6 (Data Architecture)
4. **Day 7-8:** Section 5.1 (Ingress Server)
5. **Day 9-10:** Section 5.2 (Database Client)

### For Operations Engineers (1 week)

1. **Day 1:** Section 16 (Infrastructure)
2. **Day 2:** Section 13 (Reliability)
3. **Day 3:** Section 14 (Performance)
4. **Day 4:** Section 12 (Security)
5. **Day 5:** Section 17 (Weaknesses)

### For Security Engineers (3-5 days)

1. **Day 1:** Section 12 (Security)
2. **Day 2:** Section 5.3 (Auth System)
3. **Day 3:** Section 10 (Architecture Decisions)
4. **Day 4:** Section 17 (Weaknesses)
5. **Day 5:** Section 13 (Reliability)

### For Architects (1-2 weeks)

1. **Day 1-2:** Sections 1-4 (Overview, Map, Mental Model, Architecture)
2. **Day 3-4:** Section 9 (Design Patterns)
3. **Day 5-6:** Section 10 (Architecture Decisions)
4. **Day 7-8:** Section 11 (Alternatives and Tradeoffs)
5. **Day 9-10:** Section 17 (Weaknesses)

### For AI Assistants (Reference)

- **Section 2:** Repository Map (navigation)
- **Section 5:** Component Architecture (implementation details)
- **Section 6:** Data Architecture (schema understanding)
- **Section 7:** Runtime Flows (request tracing)
- **Section 9:** Design Patterns (code patterns)
- **Section 19:** Knowledge Gaps (what's missing)

---

*This analysis was generated by reverse-engineering the Aegis repository across 10 implementation prompts. It represents the current state of the codebase as of August 2026.*
