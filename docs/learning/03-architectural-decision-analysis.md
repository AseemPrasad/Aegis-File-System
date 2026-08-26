# Aegis File System — Architectural Decision Analysis

> **Perspective:** Senior Software Architect
> **Method:** Evidence-based analysis of every significant design decision
> **Generated:** August 2026

---

## Table of Contents

1. [Go over Rust for the Main Service](#1-go-over-rust-for-the-main-service)
2. [Pre-Signed URLs Instead of Proxy Uploads](#2-pre-signed-urls-instead-of-proxy-uploads)
3. [Content-Addressable Storage with Cross-Tenant Dedup](#3-content-addressable-storage-with-cross-tenant-dedup)
4. [Trigger-Maintained Reference Counting](#4-trigger-maintained-reference-counting)
5. [Ltree for Namespace Hierarchy](#5-ltree-for-namespace-hierarchy)
6. [Bloom Filter Before Database for CAS Dedup](#6-bloom-filter-before-database-for-cas-dedup)
7. [Four-Tier Connection Pool Architecture](#7-four-tier-connection-pool-architecture)
8. [Circuit Breaker for Primary Failover](#8-circuit-breaker-for-primary-failover)
9. [Generation-Based Cache Invalidation](#9-generation-based-cache-invalidation)
10. [HMAC Tokens for Pre-Signed URLs](#10-hmac-tokens-for-pre-signed-urls)
11. [Nonce-Based Replay Protection](#11-nonce-based-replay-protection)
12. [Event-Driven Derivation via Kafka](#12-event-driven-derivation-via-kafka)
13. [FastCDC Content-Defined Chunking](#13-fastcdc-content-defined-chunking)
14. [stdlib net/http Instead of Echo/Gin](#14-stdlib-nethttp-instead-of-echogin)
15. [Single Binary Monolith](#15-single-binary-monolith)
16. [Kustomize Over Helm](#16-kustomize-over-helm)
17. [Environment Variable Configuration](#17-environment-variable-configuration)
18. [Adapter Pattern for Cross-Package Boundaries](#18-adapter-pattern-for-cross-package-boundaries)
19. [Interface-Based Test Doubles](#19-interface-based-test-doubles)
20. [GC Safety Window and Rate Limiting](#20-gc-safety-window-and-rate-limiting)
21. [Token Bucket Rate Limiting (In-Memory)](#21-token-bucket-rate-limiting-in-memory)
22. [Structured JSON Logging with stdlib slog](#22-structured-json-logging-with-stdlib-slog)
23. [PostgreSQL Over NoSQL](#23-postgresql-over-nosql)
24. [Redis for Cache, Nonces, and Bloom Filter](#24-redis-for-cache-nonces-and-bloom-filter)

---

## 1. Go over Rust for the Main Service

### WHY THIS?

**What the repository does:** The main service (`cmd/ingest/main.go`, 349 lines) is written in Go 1.25.0. Rust is used only for the FastCDC reference implementation (`crates/fastcdc/`, 551 lines).

**Where it's implemented:**
- `go.mod` (line 3): `module github.com/aegis-dev/aegis`
- `Cargo.toml` (line 8): `members = ["crates/fastcdc"]`
- `cmd/ingest/main.go`: All service code is Go

**What problem it solves:** The service needs rapid development iteration, easy hiring/maintenance, and a mature ecosystem for HTTP servers, database drivers, and cloud SDKs.

**What engineering principle it represents:** *Choose the language that maximizes team velocity for the problem domain.* The main service is I/O-bound (database queries, HTTP handlers, S3 calls), not CPU-bound. Go's goroutine model handles this naturally.

**Evidence:**
- `go.mod` lists 8 direct dependencies, all mature Go libraries (pgx, go-redis, prometheus, aws-sdk)
- `server.go` uses stdlib `net/http` with Go 1.22+ routing — no external web framework needed
- All internal packages are Go with consistent style
- The Rust code is isolated to `crates/fastcdc/` — a compute-bound algorithm

### WHY NOT THAT?

**Alternative 1: Rust for the entire service**
- How it works: Rewrite all Go code in Rust using actix-web or axum
- Advantages: Memory safety without GC, better raw performance, smaller binary
- Disadvantages: Slower development (borrow checker friction), smaller hiring pool, async runtime complexity (tokio)
- Complexity: 3-5x higher development time
- Performance: 2-3x faster for CPU-bound work, marginal for I/O-bound
- Scalability: Similar (both handle concurrency well)
- Maintainability: Worse (Rust is harder to onboard new developers)
- Testability: Similar
- Operational consequences: Smaller binaries, lower memory usage

**Alternative 2: Go with Rust FFI for FastCDC only**
- How it works: Call Rust FastCDC from Go via CGO/FFI
- Advantages: Best performance for chunking, Go for everything else
- Disadvantages: CGO complexity, cross-compilation issues, ABI instability
- Complexity: Medium (CGO toolchain requirements)
- Performance: Marginal improvement for FastCDC only
- Operational consequences: Requires Rust toolchain in build pipeline

**Evidence for inference:** The `crates/fastcdc/` is explicitly a *reference implementation* (documented in README and comments). The Go implementation in `internal/fastcdc/fastcdc.go` (316 lines) is the production code. This suggests Rust was used for algorithm validation, not production deployment.

### TRADEOFF

**Gains:** Faster development, easier hiring, mature ecosystem, simpler build pipeline, stdlib HTTP server.
**Sacrifices:** GC pauses (negligible for I/O-bound), higher memory usage (~2-3x vs Rust), no compile-time memory safety.

### FAILURE POINT

The decision becomes problematic if:
- CPU-bound work becomes dominant (e.g., real-time video transcoding in the main process)
- Memory pressure becomes critical (e.g., 100K concurrent connections each buffering 1MB)
- The team acquires strong Rust expertise and the Go codebase becomes a maintenance burden

### CHANGE CONDITION

Replace if: The service needs to handle CPU-bound derivation work inline (not via separate workers), or if memory usage becomes the primary scaling bottleneck.

### SCALE CONDITION

Stops being appropriate at: ~100K concurrent connections where GC pause times become measurable, or if the service needs to embed a Lua/WASM runtime for user-defined pipelines.

### LEARNING QUESTION

The FastCDC algorithm is implemented in both Go and Rust. Why would the team maintain two implementations instead of just one? What does the Go implementation sacrifice compared to the Rust reference, and what does it gain?

---

## 2. Pre-Signed URLs Instead of Proxy Uploads

### WHY THIS?

**What the repository does:** Clients upload file blocks directly to S3 via pre-signed URLs. The server never handles file bytes.

**Where it's implemented:**
- `internal/ingress/initiate.go:87-95`: Generates presigned URLs for missing blocks
- `internal/ingress/server.go:62-65`: `BlobStore` interface for URL generation
- `internal/objectstorage/s3.go`: S3Client.GenerateUploadURL

**What problem it solves:** Eliminates server-side file buffering. A 100MB file uploaded through the server would require 100MB of server memory per concurrent upload. With pre-signed URLs, the server uses ~0 bytes per upload.

**What engineering principle it represents:** *Push data-plane work to the edge.* The server handles control-plane (metadata, auth, dedup); S3 handles data-plane (storage, durability).

**Evidence:**
- `initiate.go:87`: `url, uerr = s.blob.GenerateUploadURL(ctx, tenantID.String(), c.BlockHash, c.SizeBytes)`
- `initiate.go:93`: `url, uerr = s.tokens.GeneratePreSignedURL(ctx, tenantID.String(), c.BlockHash, s.cfg.EndpointID)`
- No `io.Copy` or file buffering anywhere in the ingress code
- `Config.MaxBodySize` is set to 100MB (`server.go:45`) — this is for request JSON, not file data

### WHY NOT THAT?

**Alternative 1: Proxy uploads (server buffers bytes)**
- How it works: Client sends file to server, server forwards to S3
- Advantages: Simpler client code, server can inspect/transform content, single authentication point
- Disadvantages: Server memory scales with file size × concurrency (10K × 100MB = 1TB), single point of failure for data path
- Complexity: Lower (no HMAC tokens, no presigned URL logic)
- Performance: Worse (extra network hop, server memory pressure)
- Scalability: Worse (memory-bound)
- Maintainability: Better (simpler auth model)
- Testability: Better (no S3 dependency for unit tests)
- Operational consequences: Larger server instances, higher cost

**Alternative 2: Chunked streaming proxy**
- How it works: Server streams chunks to S3 without buffering entire file
- Advantages: Lower memory than full proxy, server can inspect content
- Disadvantages: Complex streaming logic, still adds latency, server remains in data path
- Complexity: High (chunked transfer encoding, S3 multipart upload)
- Performance: Moderate (streaming adds ~10-20% latency)
- Scalability: Better than full proxy but worse than presigned URLs
- Operational consequences: Requires tuning chunk sizes and buffer pools

**Evidence for inference:** The `BlobStore` interface (`server.go:62-65`) and `TokenSigner` interface (`server.go:58-60`) are separate abstractions. When `blob != nil`, direct-to-S3 URLs are used. When `blob == nil`, HMAC tokens are used (edge PoP mode). This dual path suggests the architecture evolved to support both direct S3 and edge-proxy deployments.

### TRADEOFF

**Gains:** Zero server memory per upload, uploads don't pass through server, S3 handles durability/availability, supports arbitrarily large files.
**Sacrifices:** Complex auth (HMAC tokens), client must handle S3 directly, server cannot inspect file content during upload, two-step auth (server + S3).

### FAILURE POINT

The decision becomes problematic if:
- Content inspection during upload is required (virus scanning before storage)
- S3 presigned URL expiry (15 minutes) is too short for very large files
- Clients cannot reach S3 directly (corporate firewalls, air-gapped networks)
- S3 pricing for PUT requests becomes prohibitive at scale

### CHANGE CONDITION

Replace if: Regulatory requirements mandate real-time content inspection before storage, or if the system needs to support air-gapped deployments where clients cannot reach S3.

### SCALE CONDITION

Stops being appropriate at: When S3 PUT request pricing exceeds server proxying cost (approximately 10M+ PUT requests/month at current S3 pricing), or when client networks cannot reliably reach S3.

### LEARNING QUESTION

The `BlobStore` interface has two implementations: direct S3 and HMAC token (edge PoP). In what deployment scenario would you use the HMAC token path instead of direct S3? What does this tell you about the intended deployment architecture?

---

## 3. Content-Addressable Storage with Cross-Tenant Dedup

### WHY THIS?

**What the repository does:** CAS blocks are identified by SHA-256 hash. The `block_hash` is the primary key. Identical content from any tenant is stored once. `ref_count` tracks how many file versions reference each block.

**Where it's implemented:**
- `db/schema.sql:131-142`: `cas_blocks` table with `block_hash BYTEA PRIMARY KEY`
- `internal/cas/registry.go`: PgRegistry with EnsureBlock, BatchQueryExisting
- `internal/ingress/store.go:170-186`: BatchQueryExistingCAS

**What problem it solves:** Eliminates duplicate storage. If 100 tenants upload the same 1GB file, only 1GB is stored instead of 100GB.

**What engineering principle it represents:** *Content-based addressing enables automatic deduplication.* The address IS the content hash, so identical content always maps to the same address.

**Evidence:**
- `schema.sql:131`: `block_hash BYTEA PRIMARY KEY CHECK (octet_length(block_hash) = 32)`
- `schema.sql:133`: Comment: "One row per unique chunk; ref_count maintained exclusively by manifest triggers"
- `cas/registry.go:308`: `UPDATE cas_blocks SET storage_tier = CASE WHEN ref_count > $1 THEN 'HOT'...`
- `ingress/store.go:176`: `SELECT block_hash FROM cas_blocks WHERE block_hash = ANY($1)`

### WHY NOT THAT?

**Alternative 1: Tenant-scoped CAS (block_hash includes tenant_id)**
- How it works: PK is (tenant_id, block_hash) instead of just block_hash
- Advantages: Natural tenant isolation, no cross-tenant data proximity concerns
- Disadvantages: No cross-tenant dedup (100 tenants × 1GB = 100GB storage)
- Complexity: Similar
- Performance: Similar
- Scalability: Worse (storage scales linearly with tenants)
- Maintainability: Better (simpler mental model)
- Testability: Similar
- Operational consequences: Higher storage costs

**Alternative 2: Application-maintained ref_count**
- How it works: Application code increments/decrements ref_count on manifest insert/delete
- Advantages: Simpler code (no triggers needed)
- Disadvantages: Race conditions possible (concurrent operations can corrupt count), requires distributed locking
- Complexity: Lower initially, higher in production (race condition handling)
- Performance: Similar
- Scalability: Worse (contention on ref_count updates)
- Maintainability: Worse (subtle bugs)
- Testability: Worse (hard to reproduce races)
- Operational consequences: Data corruption risk

**Alternative 3: Eventual consistency CAS (no ref_count)**
- How it works: Mark blocks for deletion, GC periodically scans for unreferenced blocks
- Advantages: No trigger complexity, simpler writes
- Disadvantages: GC must scan all blocks to find orphans, slower reclamation, more complex GC logic
- Complexity: Higher (GC logic)
- Performance: Worse for writes (no immediate feedback)
- Scalability: Similar
- Maintainability: Worse (GC is complex)
- Operational consequences: Slower storage reclamation

**Evidence for inference:** The schema comment on `cas_blocks.ref_count` (line 142) explicitly states: "ref_count maintained exclusively by manifest triggers." The application code in `store.go:CommitFile` never writes to `ref_count`. This is a deliberate design choice, not an accident.

### TRADEOFF

**Gains:** Automatic cross-tenant dedup (massive storage savings), atomic ref_count via triggers (no race conditions), trigger-based invariants (application cannot corrupt state).
**Sacrifices:** Trigger complexity (harder to debug), cross-tenant data proximity (theoretical concern), tighter coupling to PostgreSQL trigger system.

### FAILURE POINT

The decision becomes problematic if:
- PostgreSQL triggers become a performance bottleneck under extreme write load
- Cross-tenant dedup creates compliance issues (data residency, GDPR right to deletion)
- The trigger system has bugs that corrupt ref_count silently

### CHANGE CONDITION

Replace if: Regulatory requirements mandate tenant data isolation at the storage block level, or if trigger performance becomes the write-path bottleneck.

### SCALE CONDITION

Stops being appropriate at: When the number of tenants × blocks creates trigger contention on the `cas_blocks` row during concurrent commits, or when cross-tenant dedup violates data residency requirements.

### LEARNING QUESTION

If a tenant requests deletion of all their data under GDPR Article 17, how does the system handle blocks that are shared with other tenants via cross-tenant dedup? What would need to change?

---

## 4. Trigger-Maintained Reference Counting

### WHY THIS?

**What the repository does:** `cas_blocks.ref_count` is never written by application code. Database triggers on `file_manifest_blocks` automatically increment/decrement it.

**Where it's implemented:**
- `db/schema.sql:142`: `ref_count BIGINT NOT NULL DEFAULT 0 CHECK (ref_count >= 0)`
- `db/schema.sql:142`: Comment: "ref_count ≡ COUNT(file_manifest_blocks WHERE block_hash=…); GC deletes only after 7-day zero window"
- Referenced triggers: `manifest_block_added`, `manifest_block_removed` (in stored-procedures.sql)
- `internal/ingress/store.go:268-275`: INSERT INTO file_manifest_blocks (triggers handle ref_count)

**What problem it solves:** Guarantees ref_count consistency even with concurrent operations. Application code cannot accidentally corrupt the count.

**What engineering principle it represents:** *Invariants should be enforced at the lowest possible layer.* Database triggers run in the same transaction as the INSERT/DELETE, making race conditions impossible.

**Evidence:**
- `store.go:268`: `INSERT INTO file_manifest_blocks (version_id, block_hash, chunk_index, offset_bytes, size_bytes) VALUES ...`
- `store.go:270`: Comment: "triggers bump ref_count"
- No `UPDATE cas_blocks SET ref_count` anywhere in application code
- `schema.sql:142`: CHECK constraint: `ref_count >= 0` (prevents negative counts)

### WHY NOT THAT?

**Alternative 1: Application-maintained ref_count**
- How it works: `UPDATE cas_blocks SET ref_count = ref_count + 1 WHERE block_hash = $1` after each manifest INSERT
- Advantages: Simpler code, no trigger debugging needed
- Disadvantages: Race conditions (two concurrent commits can lose an increment), requires FOR UPDATE locks, more application logic
- Complexity: Lower initially, higher in production
- Performance: Similar (FOR UPDATE adds lock contention)
- Scalability: Worse (lock contention on hot blocks)
- Maintainability: Worse (subtle concurrency bugs)
- Testability: Worse (hard to reproduce races)
- Operational consequences: Data corruption risk

**Alternative 2: Periodic reconciliation (no real-time ref_count)**
- How it works: Don't maintain ref_count in real-time. GC periodically counts references.
- Advantages: No trigger complexity, no lock contention
- Disadvantages: GC is slow (must count all references), blocks cannot be tiered accurately, delayed feedback
- Complexity: Higher (GC logic)
- Performance: Worse (GC must scan manifest)
- Scalability: Worse (GC scan time grows with data)
- Maintainability: Worse (complex GC)
- Operational consequences: Delayed storage reclamation

**Alternative 3: Distributed counter (Redis)**
- How it works: Store ref_count in Redis with atomic INCR/DECR
- Advantages: Fast reads/writes, no trigger dependency
- Disadvantages: Not durable (Redis data loss = ref_count corruption), requires reconciliation with DB
- Complexity: Higher (dual-write consistency)
- Performance: Better for reads
- Scalability: Better for writes
- Maintainability: Worse (consistency between Redis and DB)
- Operational consequences: Data loss risk on Redis failure

**Evidence for inference:** The CHECK constraint `ref_count >= 0` on `cas_blocks` (schema.sql:142) is a defense-in-depth measure. Even if a trigger bug somehow produces a negative count, the constraint prevents it. This suggests the architects anticipated trigger failures as a real risk.

### TRADEOFF

**Gains:** Atomic consistency (triggers run in same transaction as the manifest operation), no race conditions, application cannot corrupt state, CHECK constraint as defense-in-depth.
**Sacrifices:** Harder to debug (trigger logic is in stored procedures, not Go code), tighter PostgreSQL coupling, harder to test (requires actual PostgreSQL for trigger tests), harder to reason about (implicit behavior).

### FAILURE POINT

The decision becomes problematic if:
- A PostgreSQL upgrade changes trigger behavior
- The trigger has a bug that silently corrupts ref_count
- The team needs to support a non-PostgreSQL database
- Trigger performance degrades under extreme concurrent writes

### CHANGE CONDITION

Replace if: The system needs to support database-agnostic deployments (e.g., CockroachDB, TiDB) that don't support the same trigger semantics, or if trigger debugging becomes a significant development bottleneck.

### SCALE CONDITION

Stops being appropriate at: When concurrent manifest inserts for the same block cause row-level lock contention on `cas_blocks`, limiting write throughput to ~10K inserts/second on a single block.

### LEARNING QUESTION

The `CommitFile` transaction in `store.go` uses `INSERT INTO cas_blocks ... ON CONFLICT DO NOTHING` followed by `INSERT INTO file_manifest_blocks`. Why is the order important? What would happen if the manifest INSERT happened before the CAS ensure?

---

## 5. Ltree for Namespace Hierarchy

### WHY THIS?

**What the repository does:** Uses PostgreSQL's `ltree` extension for the `lineage_path` column on `namespace_nodes`. Paths look like `9f8b....c2d1....`.

**Where it's implemented:**
- `db/schema.sql:13`: `CREATE EXTENSION IF NOT EXISTS ltree`
- `db/schema.sql:82`: `lineage_path LTREE NOT NULL`
- `db/schema.sql:84`: Comment: "Materialized path; computed by trigger, never caller-supplied (I-2e)"

**What problem it solves:** Efficient hierarchy queries without recursive CTEs. `@>` (ancestor) and `<@` (descendant) operators are indexed via GIST.

**What engineering principle it represents:** *Use database-native features for domain-specific operations.* PostgreSQL's ltree is purpose-built for hierarchy, with optimized index support.

**Evidence:**
- `schema.sql:13`: `CREATE EXTENSION IF NOT EXISTS ltree`
- `schema.sql:82`: `lineage_path LTREE NOT NULL`
- `schema.sql:84`: Comment: "Materialized path; computed by trigger, never caller-supplied"
- The `uq_parent_name` unique constraint uses ltree semantics

### WHY NOT THAT?

**Alternative 1: Adjacency list (parent_id only)**
- How it works: Just `parent_id UUID REFERENCES namespace_nodes(node_id)`, query with recursive CTEs
- Advantages: Simpler schema, no extension dependency, easier to understand
- Disadvantages: Recursive CTEs are slow for deep hierarchies, no native index support for ancestor queries
- Complexity: Lower
- Performance: Worse (recursive CTEs don't use indexes efficiently)
- Scalability: Worse (query time grows with depth)
- Maintainability: Better (simpler mental model)
- Testability: Better (no extension dependency)
- Operational consequences: Requires recursive CTE tuning

**Alternative 2: Materialized path (string column)**
- How it works: Store path as string like "/root/parent/child", query with LIKE
- Advantages: No extension dependency, simple to understand
- Disadvantages: LIKE queries are slow (full scan), no index support, path updates require repathing entire subtree
- Complexity: Lower
- Performance: Worse (LIKE is O(N))
- Scalability: Worse
- Maintainability: Better
- Operational consequences: Slow hierarchy queries

**Alternative 3: Closure table**
- How it works: Separate table storing all ancestor-descendant pairs
- Advantages: O(1) ancestor/descendant queries, no extension dependency
- Disadvantages: Space overhead (O(N²) pairs), complex INSERT/DELETE for subtree moves
- Complexity: Higher
- Performance: Better for reads, worse for writes
- Scalability: Worse for writes (closure table grows quadratically)
- Maintainability: Worse (complex maintenance)
- Operational consequences: Storage overhead

**Evidence for inference:** The schema comment (line 84) says lineage_path is "computed by trigger, never caller-supplied (I-2e)." The invariant label "I-2e" suggests this is part of a formal invariant system (I-2 = Acyclic Namespace). The trigger ensures the lineage is always correct, making broken states unrepresentable.

### TRADEOFF

**Gains:** O(log N) hierarchy queries via GIST index, native PostgreSQL support, efficient ancestor/descendant operations, trigger-computed paths prevent broken states.
**Sacrifices:** PostgreSQL extension dependency (not portable), requires stored procedures for path computation, harder to understand than simple parent_id.

### FAILURE POINT

The decision becomes problematic if:
- The system needs to support non-PostgreSQL databases
- The ltree extension has bugs or performance issues
- Subtree moves require repathing (ltree handles this, but it's implicit)
- The team needs to migrate to a different database

### CHANGE CONDITION

Replace if: The system needs to support CockroachDB/TiDB (which don't have ltree), or if hierarchy depth exceeds ~20 levels where ltree performance degrades.

### SCALE CONDITION

Stops being appropriate at: When namespace trees exceed ~1M nodes per tenant, where ltree GIST index build time and query performance degrade.

### LEARNING QUESTION

The `lineage_path` is computed by a trigger, never supplied by the application. Why is this invariant important? What would happen if the application could supply arbitrary lineage paths?

---

## 6. Bloom Filter Before Database for CAS Dedup

### WHY THIS?

**What the repository does:** A Redis-backed bloom filter provides O(1) "definitely not exists" checks before hitting the database for CAS dedup.

**Where it's implemented:**
- `internal/cas/bloom.go:1-167`: RedisBloomFilter with BF.RESERVE/BF.ADD/BF.EXISTS
- `internal/cas/bloom.go:12`: `bloomFalsePosRate = 0.01` (1% false positive rate)
- `internal/cas/bloom.go:13`: `bloomTTL = 1 * time.Hour`

**What problem it solves:** Reduces database load for repeat uploads. Without bloom filter, every block hash requires a DB query. With bloom filter, ~90% of repeat hashes are caught in O(1) Redis calls.

**What engineering principle it represents:** *Probabilistic data structures can dramatically reduce expensive operations.* A 1% false positive rate means 99% of negative checks skip the DB entirely.

**Evidence:**
- `bloom.go:12`: `bloomFalsePosRate = 0.01`
- `bloom.go:13`: `bloomTTL = 1 * time.Hour`
- `bloom.go:63`: `MightContain` returns `true` on Redis error (fail-open)
- Redis key: `aegis:cas:bloom:{endpoint_id}` — namespaced per endpoint

### WHY NOT THAT?

**Alternative 1: Direct database query only**
- How it works: Every block hash goes to `SELECT block_hash FROM cas_blocks WHERE block_hash = ANY($1)`
- Advantages: Simpler code, no Redis dependency for dedup, always accurate
- Disadvantages: Higher DB load (every block hash = 1 query), DB becomes bottleneck for dedup
- Complexity: Lower
- Performance: Worse (DB round-trip per hash)
- Scalability: Worse (DB connection pressure)
- Maintainability: Better (no bloom filter logic)
- Testability: Better (no Redis dependency)
- Operational consequences: Higher DB CPU usage

**Alternative 2: In-memory bloom filter**
- How it works: Go-native bloom filter (e.g., `github.com/bits-and-blooms/bloom`)
- Advantages: No Redis dependency, O(1) local lookups
- Disadvantages: Not shared across replicas (each pod has its own filter), memory grows unbounded
- Complexity: Lower
- Performance: Better (no network hop)
- Scalability: Worse (not shared, memory grows)
- Maintainability: Better (simpler)
- Testability: Better
- Operational consequences: Memory pressure, inconsistent dedup across pods

**Alternative 3: Redis set (exact, not probabilistic)**
- How it works: Store all block hashes in a Redis SET, use SISMEMBER for exact checks
- Advantages: No false positives, exact dedup
- Disadvantages: Memory grows linearly with blocks (1M blocks × 32 bytes = 32MB), SET operations slower than bloom
- Complexity: Similar
- Performance: Similar for small sets, worse for large sets
- Scalability: Worse (memory grows linearly)
- Maintainability: Better (simpler mental model)
- Operational consequences: Higher Redis memory usage

**Evidence for inference:** The bloom filter has a 1-hour TTL (`bloomTTL = 1 * time.Hour`). This means the filter is reset every hour, causing a brief window of false negatives (all hashes fall through to DB). This tradeoff was accepted to prevent unbounded memory growth. The fail-open behavior on Redis error (`bloom.go:63`) means a Redis outage degrades to direct DB queries, not total failure.

### TRADEOFF

**Gains:** ~90% reduction in DB queries for repeat uploads, O(1) Redis lookup vs O(N) DB query, bounded memory (bloom filter is fixed-size), fail-open on Redis error.
**Sacrifices:** 1% false positive rate (some unnecessary DB queries), 1-hour TTL causes brief false negative windows, Redis dependency for dedup, harder to debug (probabilistic data structure).

### FAILURE POINT

The decision becomes problematic if:
- Redis is unavailable (degrades to DB-only, but doesn't fail)
- The bloom filter capacity (1M entries) is exceeded
- The 1-hour TTL causes too many false negatives during peak upload
- The 1% false positive rate causes excessive DB load

### CHANGE CONDITION

Replace if: The system needs exact dedup guarantees (no false positives), or if Redis memory becomes a constraint.

### SCALE CONDITION

Stops being appropriate at: When the number of unique blocks per hour exceeds the bloom filter capacity (1M entries), causing the filter to be reset more frequently and reducing effectiveness.

### LEARNING QUESTION

The bloom filter has a 1-hour TTL. What happens during the minute after the TTL expires and the filter is recreated? How does this affect dedup accuracy and DB load?

---

## 7. Four-Tier Connection Pool Architecture

### WHY THIS?

**What the repository does:** The DatabaseClient maintains four separate connection pool tiers: Write, Metadata, Read (replicas), and Analytical.

**Where it's implemented:**
- `internal/database/client.go:38-47`: Four pool fields (writePool, metadata, readPools, analyticalPool)
- `internal/database/config.go:25-30`: OpClass enum (OpWrite, OpRead, OpMetadata, OpAnalytical)
- `internal/database/client.go:107-140`: Pool construction with budget allocation

**What problem it solves:** Prevents connection exhaustion. Different traffic patterns (writes, metadata queries, read replicas, heavy aggregations) compete for connections. Isolating them prevents one pattern from starving others.

**What engineering principle it represents:** *Resource isolation through pool partitioning.* Each pool has its own connection budget, preventing thundering herds on any single tier.

**Evidence:**
- `client.go:118-120`: Pool budget: `budget := c.cfg.MaxTotalConns / 4`
- `config.go:25-30`: Four distinct OpClass values
- `client.go:318-340`: `pickPool` routes based on OpClass
- `config.go:76-78`: `MaxTotalConns` defaults to 96 (4 pools × 24 each)

### WHY NOT THAT?

**Alternative 1: Single shared pool**
- How it works: One pool shared by all operations
- Advantages: Simpler code, better connection utilization (no idle pools)
- Disadvantages: Write-heavy load starves reads, heavy aggregations starve transactions
- Complexity: Lower
- Performance: Worse under mixed load
- Scalability: Worse (contention)
- Maintainability: Better (simpler)
- Testability: Better
- Operational consequences: Harder to tune

**Alternative 2: PgBouncer external proxy**
- How it works: External connection pooler handles pool management
- Advantages: Language-agnostic, battle-tested, connection multiplexing
- Disadvantages: Additional infrastructure, loses prepared statement caching, additional latency
- Complexity: Higher (infrastructure)
- Performance: Similar (PgBouncer adds ~0.5ms)
- Scalability: Better (PgBouncer handles more connections)
- Maintainability: Better (separate concern)
- Operational consequences: Additional component to operate

**Alternative 3: Per-request pool (no pooling)**
- How it works: Create a new connection per query, close after
- Advantages: No pool contention, simple mental model
- Disadvantages: TCP + auth overhead per query (~5-10ms), server max_connections exhausted quickly
- Complexity: Lower
- Performance: Much worse
- Scalability: Much worse
- Maintainability: Better
- Operational consequences: Connection exhaustion

**Evidence for inference:** The `MaxTotalConns` default of 96 (`config.go:78`) with four pools means each pool gets ~24 connections. The comment at `client.go:118` explains: "respect the global budget so four pools never overrun server max_connections." This suggests the architects encountered connection exhaustion in production and designed the four-tier system as a response.

### TRADEOFF

**Gains:** Prevents connection exhaustion under mixed load, isolates traffic patterns, predictable behavior under stress, per-pool metrics for observability.
**Sacrifices:** More complex configuration, potential idle connections in underused pools, higher total connection count, harder to reason about pool sizing.

### FAILURE POINT

The decision becomes problematic if:
- Pool sizing is wrong (too few connections in a tier causes timeouts)
- A tier is unused but still consumes connections
- The global budget (96) is too low for production load
- Connection leaks in one tier affect others

### CHANGE CONDITION

Replace if: The system moves to a serverless architecture where connections are ephemeral, or if PgBouncer is adopted as an external pooler.

### SCALE CONDITION

Stops being appropriate at: When the number of concurrent connections exceeds PostgreSQL's `max_connections` (typically 100-200), requiring PgBouncer or connection multiplexing.

### LEARNING QUESTION

The `Metadata` pool exists separately from the `Read` pool. Why? What would happen if metadata queries (ACL resolution, namespace listing) ran on the same pool as regular read queries?

---

## 8. Circuit Breaker for Primary Failover

### WHY THIS?

**What the repository does:** A circuit breaker gates write traffic to the PostgreSQL primary. After 3 consecutive failures, the breaker opens and writes fail fast (no retry to replicas).

**Where it's implemented:**
- `internal/database/failover.go:1-158`: CircuitBreaker with Closed/Open/Half-Open states
- `internal/database/failover.go:47`: `breakerFailureThreshold = 3`
- `internal/database/failover.go:48`: `breakerCooldown = 2 * time.Second`

**What problem it solves:** Prevents cascade failures when the primary is unhealthy. Without a circuit breaker, every request would time out waiting for a dead primary, consuming connection pool resources.

**What engineering principle it represents:** *Fail fast when failure is detected.* The circuit breaker stops sending traffic immediately, freeing resources for recovery.

**Evidence:**
- `failover.go:58-72`: `Allow()` checks state: Closed→proceed, Open→reject (unless cooldown elapsed), Half-Open→one probe
- `failover.go:74-83`: `RecordSuccess()` resets failures and closes breaker
- `failover.go:85-93`: `RecordFailure()` increments failures, opens at threshold
- `client.go:320-322`: Write path: `if !c.breaker.Allow() { return nil, nil, ErrPrimaryUnavailable }`

### WHY NOT THAT?

**Alternative 1: Retry with backoff (no circuit breaker)**
- How it works: Retry failed writes with exponential backoff
- Advantages: Simpler code, transient failures are handled
- Disadvantages: Retries pile onto a failing primary, consume connections, delay failure detection
- Complexity: Lower
- Performance: Worse under failure (retries add load)
- Scalability: Worse (cascade failure)
- Maintainability: Better (simpler)
- Testability: Better
- Operational consequences: Slower failure detection

**Alternative 2: Transparent failover to replicas for writes**
- How it writes: Route writes to replicas when primary is down
- Advantages: Writes continue during primary failure
- Disadvantages: Stale reads from replicas cause write conflicts, forked history, data inconsistency
- Complexity: Higher (conflict resolution)
- Performance: Similar
- Scalability: Worse (data inconsistency)
- Maintainability: Worse (conflict resolution logic)
- Operational consequences: Data corruption risk

**Alternative 3: External health check (Kubernetes liveness)**
- How it works: K8s restarts the pod when primary is unreachable
- Advantages: No application-level circuit breaker needed
- Disadvantages: Slow detection (K8s probe interval), restart doesn't fix primary, loses in-flight state
- Complexity: Lower (application code)
- Performance: Worse (slow detection)
- Scalability: Similar
- Maintainability: Better (no circuit breaker code)
- Operational consequences: Pod restarts, lost connections

**Evidence for inference:** The `healthLoop` (`failover.go:134-147`) probes the primary every 500ms. Detection budget = interval × threshold = 500ms × 3 = 1.5 seconds. The comment at `failover.go:134` says: "Detection latency budget: interval × threshold ≈ 1.5s at defaults, which keeps RTO inside the SLA." This confirms the circuit breaker is designed for fast failure detection.

### TRADEOFF

**Gains:** Fast failure detection (1.5s), prevents cascade failures, writes fail fast (no wasted retries), half-open state enables automatic recovery.
**Sacrifices:** Writes fail during primary outage (no automatic failover for writes), requires careful threshold tuning, adds complexity to write path.

### FAILURE POINT

The decision becomes problematic if:
- The threshold (3 failures) is too low (false positives during transient network issues)
- The cooldown (2s) is too short (breaker flaps between open/closed)
- The health check itself fails (false positive: primary is healthy but probe fails)

### CHANGE CONDITION

Replace if: The system needs automatic write failover (e.g., synchronous replication to a standby primary), or if the primary failover time exceeds the SLA RTO.

### SCALE CONDITION

Stops being appropriate at: When write throughput exceeds what a single primary can handle, requiring write sharding or multi-primary replication.

### LEARNING QUESTION

The circuit breaker has three states: Closed, Open, Half-Open. In Half-Open state, exactly one caller is "elected" to probe the primary. Why is only one probe allowed instead of all callers? What would happen if all callers probed simultaneously?

---

## 9. Generation-Based Cache Invalidation

### WHY THIS?

**What the repository does:** The NamespaceCache uses a per-tenant generation counter in Redis. Mutations bump the generation after commit. Cache entries under old generations become unreachable.

**Where it's implemented:**
- `internal/database/cache.go:55-59`: `generationKey` and `nodeKey` builders
- `internal/database/cache.go:79-86`: `BumpGeneration` — Redis INCR
- `internal/database/cache.go:95-134`: `GetNodeMetadata` — cache-aside with singleflight

**What problem it solves:** O(1) invalidation of an entire tenant's cached metadata. Without generation-based invalidation, you'd need to SCAN/DEL individual keys, which is O(N) for N cached nodes.

**What engineering principle it represents:** *Indirection through versioning.* By embedding a generation counter in every cache key, mutations invalidate all entries atomically by incrementing one counter.

**Evidence:**
- `cache.go:55`: `generationKey(tenantID) = "aegis:ns:gen:" + tenantID`
- `cache.go:59`: `nodeKey(tenantID, gen, nodeID) = "aegis:ns:{tenant}:g{N}:{node}"`
- `cache.go:79-86`: `BumpGeneration` does `INCR aegis:ns:gen:{tenantID}`
- `client.go:538-549`: `bumpGenerationOnSuccess` called after every mutating wrapper

### WHY NOT THAT?

**Alternative 1: Key-based invalidation (DEL specific keys)**
- How it works: After mutation, delete the specific cache key for the changed node
- Advantages: Precise invalidation (only changed key is evicted)
- Disadvantages: Requires knowing all affected keys (hard for subtree moves), O(N) for N affected keys
- Complexity: Lower
- Performance: Worse for subtree operations
- Scalability: Worse (must track all keys)
- Maintainability: Better (simpler mental model)
- Testability: Better
- Operational consequences: Stale cache during subtree operations

**Alternative 2: TTL-only invalidation**
- How it works: Don't invalidate explicitly; let TTL expire entries
- Advantages: Simplest code, no invalidation logic
- Disadvantages: Stale reads for up to TTL duration (5 minutes), no immediate consistency
- Complexity: Lowest
- Performance: Best (no invalidation overhead)
- Scalability: Best
- Maintainability: Best
- Operational consequences: Stale data for up to 5 minutes

**Alternative 3: Redis pub/sub invalidation**
- How it works: Publish invalidation events on Redis pub/sub, all pods subscribe and evict
- Advantages: Real-time invalidation across pods, no generation counter needed
- Disadvantages: Redis pub/sub is fire-and-forget (missed messages = stale cache), requires subscription management
- Complexity: Higher
- Performance: Similar
- Scalability: Similar
- Maintainability: Worse (pub/sub reliability)
- Operational consequences: Stale cache if pub/sub message is missed

**Evidence for inference:** The comment at `cache.go:28-37` explains the coherency model in detail: "Any entry written under generation N becomes unreachable the instant N+1 exists — O(1) invalidation of an entire subtree without SCAN/DEL." The TTL (5 minutes) is explicitly documented as "pure garbage collection of orphaned generations, not the coherency mechanism." This confirms the generation counter is the coherency mechanism, not TTL.

### TRADEOFF

**Gains:** O(1) subtree invalidation, immediate consistency after mutation, no SCAN/DEL operations, singleflight prevents thundering herds on cache miss.
**Sacrifices:** Requires Redis for generation counter, cache entries are orphaned (not deleted) until TTL, slightly larger cache keys (include generation number), Redis INCR latency on every mutation.

### FAILURE POINT

The decision becomes problematic if:
- Redis is unavailable (cache bypasses to DB, no invalidation)
- The generation counter overflows (theoretical: uint64, not practical)
- Multiple mutations happen faster than INCR can propagate
- The TTL (5 minutes) is too short (excessive orphaned keys) or too long (memory pressure)

### CHANGE CONDITION

Replace if: The system needs cross-region cache invalidation (generation counter is per-Redis-instance), or if the cache hit rate drops below a threshold where the generation overhead isn't justified.

### SCALE CONDITION

Stops being appropriate at: When the number of tenants × cached nodes exceeds Redis memory capacity, or when generation INCR latency becomes a measurable fraction of request latency.

### LEARNING QUESTION

The cache uses singleflight to prevent thundering herds on cache miss. What happens if two goroutines request the same uncached node simultaneously? How does singleflight ensure only one database query is made?

---

## 10. HMAC Tokens for Pre-Signed URLs

### WHY THIS?

**What the repository does:** Pre-signed URLs use HMAC-SHA256 tokens with tenant-scoped keys, nonces, and expiry timestamps.

**Where it's implemented:**
- `internal/auth/hmac.go:1-439`: TokenGenerator, Claims, Validate
- `internal/auth/hmac.go:8-10`: Message format: `"aegis1:" ver ":" tenant ":" block_hash ":" exp ":" endpoint ":" nonce`
- `internal/auth/hmac.go:12-15`: 5 security properties enforced in order

**What problem it solves:** Secure, stateless upload authorization. The server mints a URL that allows direct S3 upload without proxying. The URL proves: (1) the tenant is authenticated, (2) the block hash is correct, (3) the URL hasn't expired, (4) the URL hasn't been replayed.

**What engineering principle it represents:** *Stateless authentication via cryptographic tokens.* No server-side session state is needed for URL validation.

**Evidence:**
- `hmac.go:8-10`: Message format with domain separation ("aegis1:")
- `hmac.go:12-15`: Security properties: Structure, Freshness, Authenticity, Replay, Isolation
- `hmac.go:282-283`: `computeMAC(key, message)` — single HMAC site
- `hmac.go:354-355`: `hmac.Equal(want, got)` — constant-time comparison

### WHY NOT THAT?

**Alternative 1: JWT tokens**
- How it works: Standard JWT with RSA/ECDSA signing
- Advantages: Standard format, wide library support, stateless validation
- Disadvantages: Heavier (RSA is ~10x slower than HMAC), JWK/JWKS infrastructure needed, header overhead
- Complexity: Higher (JWK infrastructure)
- Performance: Worse (RSA/ECDSA is slower)
- Scalability: Similar
- Maintainability: Better (standard format)
- Testability: Better (standard libraries)
- Operational consequences: Key management complexity

**Alternative 2: Session-based auth (server stores session)**
- How it works: Server stores session state, validates via session ID
- Advantages: Simpler tokens, server can revoke sessions instantly
- Disadvantages: Server-side state for every upload, session storage scales with concurrent uploads
- Complexity: Lower (no crypto)
- Performance: Worse (session lookup per request)
- Scalability: Worse (session state grows)
- Maintainability: Better (simpler)
- Testability: Better
- Operational consequences: Session store scaling

**Alternative 3: IP-based restrictions**
- How it works: Restrict uploads to specific IP ranges
- Advantages: Simple, no token management
- Disadvantages: Breaks behind NAT/load balancers, not identity-based
- Complexity: Lowest
- Performance: Best (no crypto)
- Scalability: Similar
- Maintainability: Best
- Operational consequences: IP management complexity

**Evidence for inference:** The `computeMAC` function (`hmac.go:282-283`) is the single site for HMAC computation. The comment says: "computeMAC is the single HMAC site for both directions." This suggests deliberate centralization to prevent inconsistent HMAC implementations. The `isLowerHex` helper (`hmac.go:156-163`) validates hex encoding at the byte level, not via `hex.DecodeString`, suggesting a performance optimization for the validation hot path.

### TRADEOFF

**Gains:** Stateless (no server-side session), fast (HMAC-SHA256 ~1μs), tenant isolation (per-tenant keys), replay protection (nonces), constant-time comparison.
**Sacrifices:** Custom token format (not standard JWT), requires KMS for key management, requires Redis for nonces, 15-minute expiry requires re-minting for long uploads.

### FAILURE POINT

The decision becomes problematic if:
- KMS is unavailable (can't sign/verify tokens)
- Redis is unavailable (can't check nonces for replay protection)
- The 15-minute TTL is too short for large files
- Clock skew between servers causes false rejections

### CHANGE CONDITION

Replace if: The system needs standard token format for third-party integration, or if KMS latency becomes a bottleneck.

### SCALE CONDITION

Stops being appropriate at: When the number of token validations per second exceeds HMAC-SHA256 throughput (~1M/sec on modern hardware), or when Redis nonce latency becomes the validation bottleneck.

### LEARNING QUESTION

The token validation enforces 5 security properties in a specific order. Why is the nonce consumed LAST (after signature verification)? What attack does this ordering prevent?

---

## 11. Nonce-Based Replay Protection

### WHY THIS?

**What the repository does:** Each pre-signed URL contains a random 16-byte nonce. The nonce is consumed exactly once via Redis SETNX.第二次使用同一URL会被拒绝。

**Where it's implemented:**
- `internal/auth/replay.go:1-102`: NonceStore interface, RedisNonceStore, InMemoryNonceStore
- `internal/auth/replay.go:38-40`: `nonceKey` domain-separates and hashes nonces
- `internal/auth/hmac.go:314-325`: Nonce consumption in Validate (after signature check)

**What problem it solves:** Prevents replay attacks. Without nonces, a captured pre-signed URL could be used multiple times.

**What engineering principle it represents:** *Single-use tokens via atomic consume.* SETNX semantics ensure exactly one consumer wins.

**Evidence:**
- `replay.go:38-40`: `nonceKey(nonce) = sha256("aegis:nonce:" + nonce)` — domain separation + hashing
- `replay.go:53-55`: `RedisNonceStore.Consume` uses `SETNX` (set if not exists)
- `hmac.go:314-325`: Nonce consumption happens AFTER signature verification
- `replay.go:82-84`: Comment: "Consume MUST only be reached after the signature check passes"

### WHY NOT THAT?

**Alternative 1: No replay protection**
- How it works: Accept any valid token, regardless of previous use
- Advantages: Simplest, no Redis dependency, no nonce storage
- Disadvantages: Captured URLs can be replayed无限次
- Complexity: Lowest
- Performance: Best
- Scalability: Best
- Maintainability: Best
- Operational consequences: Security vulnerability

**Alternative 2: Server-side session tracking**
- How it works: Server stores used tokens in a database
- Advantages: Persistent across restarts, queryable
- Disadvantages: Database write per validation, scales poorly
- Complexity: Higher
- Performance: Worse (DB write per validation)
- Scalability: Worse (DB contention)
- Maintainability: Worse
- Operational consequences: DB load

**Alternative 3: Short-lived tokens (no nonce)**
- How it works: Use very short TTL (e.g., 30 seconds) instead of nonces
- Advantages: Simpler, no nonce storage needed
- Disadvantages: Clock skew issues, very short window for legitimate use
- Complexity: Lower
- Performance: Better
- Scalability: Better
- Maintainability: Better
- Operational consequences: Usability issues

**Evidence for inference:** The `nonceKey` function (`replay.go:38-40`) hashes the nonce with SHA-256 before storing in Redis. This prevents raw nonce values from being visible in Redis key space (defense in depth). The InMemoryNonceStore (`replay.go:72-100`) has a lazy sweeper that evicts expired entries, bounded by `maxSize` (default 1M entries). This suggests the architects anticipated both Redis-backed and in-memory deployments.

### TRADEOFF

**Gains:** Prevents replay attacks, atomic consume (SETNX), self-cleaning (Redis TTL), domain-separated keys.
**Sacrifices:** Requires Redis for distributed deployments, adds ~1ms latency per validation, nonce storage grows with validation volume, 1-hour TTL means nonces live in Redis for an hour.

### FAILURE POINT

The decision becomes problematic if:
- Redis is unavailable (can't check nonces → all tokens rejected)
- Redis SETNX has race conditions (two validators for same nonce)
- The nonce TTL is too short (nonces expire before validation)
- The nonce storage grows too large (memory pressure)

### CHANGE CONDITION

Replace if: The system needs to validate tokens without Redis (edge PoPs), or if Redis latency becomes the validation bottleneck.

### SCALE CONDITION

Stops being appropriate at: When the number of token validations per second exceeds Redis SETNX throughput (~100K/sec), or when Redis memory for nonces exceeds capacity.

### LEARNING QUESTION

The `InMemoryNonceStore` has a lazy sweeper that runs on every `Consume` call when the map is full. What happens under high concurrency? How does the mutex affect performance?

---

## 12. Event-Driven Derivation via Kafka

### WHY THIS?

**What the repository does:** After a successful commit, a `FileCommittedEvent` is published to Kafka/Redpanda. Derivation workers consume these events asynchronously.

**Where it's implemented:**
- `internal/ingress/commit.go:127-145`: Event publication after commit
- `cmd/ingest/derivation_bridge.go:1-105`: derivationBridge forwards events to WorkerPool
- `internal/ingress/events.go:54-67`: EventBus interface, NoopBus

**What problem it solves:** Decouples ingestion from derivation. Without event-driven processing, derivation would block the ingestion pipeline.

**What engineering principle it represents:** *Asynchronous processing via message queues.* The ingestion pipeline is synchronous (fast); derivation is asynchronous (slow).

**Evidence:**
- `commit.go:127`: `_ = s.events.PublishFileCommitted(ctx, event)` — best-effort, underscore return
- `derivation_bridge.go:56`: `select { case b.events <- devt: default: b.logger.Warn("CDC channel full") }` — non-blocking send
- `events.go:54-67`: EventBus interface with NoopBus (dev) and derivationBridge (prod)

### WHY NOT THAT?

**Alternative 1: Synchronous derivation**
- How it works: Process derivation inline during commit
- Advantages: Simpler, results available immediately, no event queue needed
- Disadvantages: Blocks commit latency, derivation failures fail commits, can't scale independently
- Complexity: Lower
- Performance: Worse (commit latency = ingestion + derivation)
- Scalability: Worse (derivation limits commit throughput)
- Maintainability: Better (simpler)
- Testability: Better
- Operational consequences: Commit latency increases

**Alternative 2: Database polling (no Kafka)**
- How it works: Workers poll a `pending_derivations` table
- Advantages: No Kafka dependency, simpler infrastructure
- Disadvantages: Polling latency, DB load, harder to scale workers
- Complexity: Lower (no Kafka)
- Performance: Worse (polling interval)
- Scalability: Worse (DB contention)
- Maintainability: Better
- Operational consequences: DB load from polling

**Alternative 3: In-process channel (no Kafka)**
- How it works: DerivationBridge sends events via Go channel (current dev mode)
- Advantages: No Kafka dependency, low latency
- Disadvantages: Not distributed (single process), events lost on restart
- Complexity: Lowest
- Performance: Best (no network hop)
- Scalability: Worse (single process)
- Maintainability: Better
- Operational consequences: Events lost on restart

**Evidence for inference:** The `derivationBridge` (`derivation_bridge.go:56`) uses a buffered channel (256 capacity) with non-blocking send. When the channel is full, events are dropped with a warning. This is a deliberate backpressure mechanism — the ingestion pipeline is not blocked by slow derivation workers. The comment at `derivation_bridge.go:58` says: "CDC channel full, dropping event."

### TRADEOFF

**Gains:** Decoupled ingestion from derivation, independent scaling, failure isolation (derivation failures don't fail commits), backpressure via channel buffering.
**Sacrifices:** Eventual consistency (results appear later), requires Kafka infrastructure, events can be dropped under backpressure, harder to debug distributed flows.

### FAILURE POINT

The decision becomes problematic if:
- Kafka is unavailable (events are lost or queued)
- The channel buffer is full (events are dropped)
- Workers are too slow (events accumulate, lag grows)
- Events need to be processed exactly once (at-least-once semantics)

### CHANGE CONDITION

Replace if: Derivation results must be available synchronously, or if the system needs exactly-once event processing.

### SCALE CONDITION

Stops being appropriate at: When the event volume exceeds Kafka throughput (~1M events/sec), or when worker lag exceeds acceptable latency (minutes → hours).

### LEARNING QUESTION

The `derivationBridge` drops events when the channel buffer (256) is full. What happens to the files whose derivation events were dropped? How would you implement recovery for dropped events?

---

## 13. FastCDC Content-Defined Chunking

### WHY THIS?

**What the repository does:** FastCDC splits files into content-defined chunks using a Gear hash and normalized two-phase masking. Chunks are SHA-256 hashed for content addressing.

**Where it's implemented:**
- `internal/fastcdc/fastcdc.go:1-316`: Go implementation
- `crates/fastcdc/src/lib.rs:1-551`: Rust reference implementation
- Constants: MinChunk=64KB, AvgChunk=1MB, MaxChunk=4MB

**What problem it solves:** Maximizes deduplication by anchoring chunk boundaries to content. Small insertions/deletions only affect nearby chunks.

**What engineering principle it represents:** *Content-defined boundaries maximize dedup ratio.* Fixed-size chunking produces poor dedup when content shifts; content-defined chunking anchors boundaries to content.

**Evidence:**
- `fastcdc.go:15-16`: `MinChunk = 64 * 1024`, `AvgChunk = 1024 * 1024`, `MaxChunk = 4 * 1024 * 1024`
- `fastcdc.go:45-60`: Gear hash uses 256-entry lookup table
- `fastcdc.go:62-80`: Normalized two-phase masking for cut points
- `crates/fastcdc/src/lib.rs`: Reference implementation for validation

### WHY NOT THAT?

**Alternative 1: Fixed-size chunking**
- How it works: Split files into fixed-size chunks (e.g., 1MB each)
- Advantages: Simplest implementation, deterministic
- Disadvantages: Poor dedup when content shifts (inserting 1 byte shifts all boundaries)
- Complexity: Lowest
- Performance: Best (no hash computation)
- Scalability: Similar
- Maintainability: Best
- Operational consequences: Lower dedup ratio

**Alternative 2: Rabin fingerprint**
- How it works: Use Rabin fingerprint for rolling hash
- Advantages: Better statistical distribution than Gear hash
- Disadvantages: ~10x slower than Gear hash, more complex implementation
- Complexity: Higher
- Performance: Worse (10x slower hash)
- Scalability: Similar
- Maintainability: Worse (more complex)
- Operational consequences: Higher CPU usage

**Alternative 3: Fixed-content chunking (e.g., by file type)**
- How it works: Different chunking strategies for different file types
- Advantages: Optimized per file type
- Disadvantages: Requires file type detection, complex routing
- Complexity: Much higher
- Performance: Varies
- Scalability: Similar
- Maintainability: Much worse
- Operational consequences: Complex maintenance

**Evidence for inference:** The repository maintains both Go and Rust implementations of FastCDC. The Go implementation (`fastcdc.go`) is 316 lines; the Rust reference (`lib.rs`) is 551 lines. The Go version is simpler (no async, no error handling for streaming). The Rust version is more complete (handles edge cases, has benchmarks). This suggests the Rust version was used for algorithm validation and the Go version is the production implementation.

### TRADEOFF

**Gains:** Content-defined boundaries maximize dedup, O(1) per byte (Gear hash), bounded chunk sizes (64KB-4MB), SHA-256 content addressing.
**Sacrifices:** More complex than fixed-size chunking, requires hash computation per byte, chunk boundaries are content-dependent (harder to predict).

### FAILURE POINT

The decision becomes problematic if:
- The Gear hash has poor distribution for specific content types
- The min/max constraints produce too many small chunks
- The SHA-256 computation becomes a bottleneck for very large files

### CHANGE CONDITION

Replace if: A faster rolling hash with better distribution is available, or if the system needs predictable chunk boundaries (e.g., for partial file access).

### SCALE CONDITION

Stops being appropriate at: When files exceed 100GB and the chunking overhead (hash computation per byte) becomes a measurable fraction of upload time.

### LEARNING QUESTION

The FastCDC algorithm uses a Gear hash for cut point detection and SHA-256 for content addressing. Why are two different hashes used? Could you use SHA-256 for both?

---

## 14. stdlib net/http Instead of Echo/Gin

### WHY THIS?

**What the repository does:** The HTTP server uses Go's stdlib `net/http` with Go 1.22+ routing patterns.

**Where it's implemented:**
- `internal/ingress/server.go:108-112`: `mux.HandleFunc("POST /api/v1/ingest/initiate", ...)`
- No external web framework in `go.mod`
- Go 1.22+ routing patterns (method + path in handler registration)

**What problem it solve:** Zero external dependencies for HTTP routing. Go 1.22+ added method-based routing to stdlib, eliminating the primary reason for frameworks.

**What engineering principle it represents:** *Use stdlib when it's sufficient.* Frameworks add dependencies, learning curves, and lock-in. Go's stdlib is production-grade.

**Evidence:**
- `server.go:108`: `mux.HandleFunc("POST /api/v1/ingest/initiate", s.wrap(s.handleInitiate))`
- `go.mod`: No Echo, Gin, Chi, or any web framework dependency
- `server.go:120`: `http.NewServeMux()` — stdlib mux
- `server.go:200-207`: `http.Server` with stdlib config

### WHY NOT THAT?

**Alternative 1: Echo framework**
- How it works: Use echo.New() for routing, middleware, validation
- Advantages: Rich middleware ecosystem, built-in validation, faster routing
- Disadvantages: External dependency, learning curve, lock-in, larger binary
- Complexity: Lower (framework handles boilerplate)
- Performance: Similar (Echo is fast, but stdlib is too)
- Scalability: Similar
- Maintainability: Worse (framework-specific patterns)
- Testability: Worse (framework-specific testing)
- Operational consequences: Framework version management

**Alternative 2: Gin framework**
- How it works: Use gin.Default() for routing
- Advantages: Fastest routing, rich middleware, large community
- Disadvantages: External dependency, different API patterns, lock-in
- Complexity: Lower
- Performance: Similar
- Scalability: Similar
- Maintainability: Worse
- Testability: Worse
- Operational consequences: Framework version management

**Alternative 3: Chi router**
- How it works: Use chi.NewRouter() for routing
- Advantages: Lightweight, stdlib-compatible, composable middleware
- Advantages: Lightweight, stdlib-compatible, composable middleware
- Disadvantages: External dependency (minimal), learning curve
- Complexity: Similar
- Performance: Similar
- Scalability: Similar
- Maintainability: Similar (Chi is stdlib-compatible)
- Testability: Similar
- Operational consequences: Minimal

**Evidence for inference:** The `wrap` function (`server.go:135-157`) implements panic recovery, auth, and max body size as a manual middleware chain. In Echo/Gin, this would be built-in middleware. The manual implementation is ~20 lines — not enough complexity to justify a framework. The `handlerFunc` type (`server.go:133`) returns `error`, which the `wrap` function maps to HTTP status codes via `HTTPStatus()`. This is a custom pattern that would conflict with framework conventions.

### TRADEOFF

**Gains:** Zero dependencies, full control over behavior, stdlib is stable and well-tested, no learning curve for Go developers, smaller binary.
**Sacrifices:** No built-in middleware (must implement manually), no built-in validation, no framework ecosystem, more boilerplate for common patterns.

### FAILURE POINT

The decision becomes problematic if:
- The team needs rich middleware (rate limiting, CORS, etc.) — must implement manually
- The team needs request validation — must implement manually
- The team needs WebSocket support — must implement manually

### CHANGE CONDITION

Replace if: The team needs a rich middleware ecosystem that stdlib doesn't provide, or if the team is hired primarily from frameworks and the learning curve becomes a bottleneck.

### SCALE CONDITION

Stops being appropriate at: When the HTTP layer needs features that stdlib doesn't provide (e.g., gRPC-Web, HTTP/3, automatic OpenAPI generation).

### LEARNING QUESTION

Go 1.22 added method-based routing to `net/http`. How does this change the calculus of using a framework? What features would still require a framework in 2026?

---

## 15. Single Binary Monolith

### WHY THIS?

**What the repository does:** All components (ingestion, GC, derivation, auth, CAS) are compiled into a single binary (`cmd/ingest/main.go`).

**Where it's implemented:**
- `cmd/ingest/main.go:1-349`: Single main() wiring all components
- Optional components enabled via environment variables (derivation, S3)
- GC runs as a background goroutine within the same binary

**What problem it solves:** Simplifies deployment (one binary, one container), eliminates inter-service communication overhead, simplifies development.

**What engineering principle it represents:** *Start with a monolith, split when necessary.* The single binary is the simplest deployment unit.

**Evidence:**
- `main.go:1`: `package main` — single entry point
- `main.go:169-200`: GC pipeline started as background goroutine
- `main.go:202-230`: Derivation workers started as background goroutine
- No inter-service HTTP/gRPC calls between components

### WHY NOT THAT?

**Alternative 1: Microservices**
- How it works: Separate services for ingestion, GC, derivation, auth
- Advantages: Independent scaling, independent deployment, fault isolation
- Disadvantages: Inter-service communication, distributed tracing, more complex deployment
- Complexity: Much higher
- Performance: Worse (network hops)
- Scalability: Better (independent scaling)
- Maintainability: Worse (distributed system)
- Testability: Worse (integration testing)
- Operational consequences: More containers, more monitoring

**Alternative 2: Sidecar pattern**
- How it works: Main service + sidecar containers for GC, derivation
- Advantages: Separation of concerns, independent lifecycle
- Disadvantages: Kubernetes-specific, more complex networking
- Complexity: Higher
- Performance: Similar (shared network)
- Scalability: Better
- Maintainability: Better
- Operational consequences: More containers

**Alternative 3: Serverless (Lambda/Cloud Run)**
- How it works: Each function is a separate serverless deployment
- Advantages: Zero operational overhead, automatic scaling
- Disadvantages: Cold starts, state management complexity, vendor lock-in
- Complexity: Higher (state management)
- Performance: Worse (cold starts)
- Scalability: Best (automatic)
- Maintainability: Worse (distributed)
- Operational consequences: Vendor lock-in

**Evidence for inference:** The `main.go` uses `signal.NotifyContext` for graceful shutdown, which signals all components (GC, derivation, HTTP server) simultaneously. This is much simpler than coordinating shutdown across microservices. The optional components (derivation, S3) are toggled via environment variables, suggesting the monolith is designed for both minimal (dev) and full (prod) profiles.

### TRADEOFF

**Gains:** Simple deployment, no inter-service communication, shared memory (no serialization), simpler development, graceful shutdown across all components.
**Sacrifices:** Can't scale components independently, larger blast radius (one bug can affect all components), harder to partition by team.

### FAILURE POINT

The decision becomes problematic if:
- The derivation pipeline needs different resources than ingestion (CPU-heavy vs I/O-heavy)
- One component needs to be updated independently (e.g., GC logic change)
- The team grows and needs independent deployment pipelines

### CHANGE CONDITION

Replace if: The team exceeds ~10 engineers and needs independent deployment, or if one component becomes the scaling bottleneck.

### SCALE CONDITION

Stops being appropriate at: When the ingestion service needs to scale to 50+ replicas but the GC pipeline only needs 1-2, making the single binary wasteful.

### LEARNING QUESTION

The GC pipeline runs as a background goroutine within the ingestion server. What happens if the GC sweep takes longer than the tick interval? How does the system prevent overlapping sweeps?

---

## 16. Kustomize Over Helm

### WHY THIS?

**What the repository does:** Kubernetes manifests use Kustomize overlays, not Helm templates.

**Where it's implemented:**
- `deploy/k8s/`: Directory with Kustomize manifests
- No `Chart.yaml` or `templates/` directory
- Kustomize patches and overlays for environment-specific configuration

**What problem it solves:** Declarative configuration without template languages. Kustomize uses strategic merge patches instead of Go templates.

**What engineering principle it represents:** *Configuration as data, not code.* Kustomize patches are YAML, not templates.

**Evidence:**
- `deploy/k8s/`: All manifests are plain YAML with Kustomize overlays
- No Helm chart files anywhere in the repository
- `kubectl apply -k deploy/k8s/` is the deployment command

### WHY NOT THAT?

**Alternative 1: Helm**
- How it works: Template-based packaging with Chart.yaml, values.yaml, templates/
- Advantages: Rich ecosystem, templating, release management, rollback
- Disadvantages: Go templates are hard to read/debug, learning curve, template complexity
- Complexity: Higher (template language)
- Performance: Similar
- Scalability: Similar
- Maintainability: Worse (templates are hard to read)
- Testability: Worse (template testing)
- Operational consequences: Helm version management

**Alternative 2: Plain kubectl apply**
- How it works: Apply raw YAML manifests
- Advantages: Simplest, no tooling needed
- Disadvantages: No environment-specific overrides, no templating
- Complexity: Lowest
- Performance: Best
- Scalability: Similar
- Maintainability: Worse (manual diffing)
- Testability: Worse
- Operational consequences: Manual environment management

**Alternative 3: Terraform for K8s resources**
- How it works: Use Terraform kubernetes provider
- Advantages: Unified IaC (cloud + K8s), state management
- Disadvantages: Terraform is slower, more complex for K8s-native resources
- Complexity: Higher
- Performance: Worse (Terraform plan/apply)
- Scalability: Similar
- Maintainability: Worse (Terraform-specific)
- Testability: Better (Terraform test)
- Operational consequences: Terraform state management

**Evidence for inference:** The repository already uses Terraform for cloud resources (`deploy/terraform/`). Kustomize is used only for K8s manifests. This separation suggests the architects wanted K8s-native tooling for application deployment while using Terraform for infrastructure provisioning.

### TRADEOFF

**Gains:** No template language, YAML-native, built into kubectl, simpler than Helm for this use case.
**Sacrifices:** No release management, no rollback built-in, smaller ecosystem than Helm, no Helm repository integration.

### FAILURE POINT

The decision becomes problematic if:
- The team needs Helm's release management features
- The manifests become complex enough to need templating
- The team wants to publish to a Helm repository

### CHANGE CONDITION

Replace if: The team needs Helm's release management, rollback, or repository features, or if the number of environment-specific overrides exceeds what Kustomize patches can handle.

### SCALE CONDITION

Stops being appropriate at: When the number of K8s resources exceeds ~100 and manual Kustomize management becomes unwieldy.

### LEARNING QUESTION

The repository uses Terraform for cloud resources and Kustomize for K8s manifests. Why not use Terraform for everything? What are the tradeoffs of this split?

---

## 17. Environment Variable Configuration

### WHY THIS?

**What the repository does:** All configuration is via environment variables (20+ in main.go).

**Where it's implemented:**
- `cmd/ingest/main.go:14-41`: Comment listing all env vars
- `cmd/ingest/main.go:44-48`: `envOr()` helper function
- No configuration files, no YAML, no TOML

**What problem it solves:** 12-factor app compliance, Kubernetes-friendly (ConfigMaps, Secrets), no file management in containers.

**What engineering principle it represents:** *Configuration as environment, not files.* Containers are stateless; configuration comes from the environment.

**Evidence:**
- `main.go:14-41`: 20+ environment variables documented
- `main.go:44-48`: `envOr(key, fallback)` — simple env var reading
- No `config.yaml`, `config.toml`, or any configuration file in the repository

### WHY NOT THAT?

**Alternative 1: YAML configuration file**
- How it works: Read config from a YAML file
- Advantages: Structured, hierarchical, easy to version control
- Disadvantages: File management in containers, mount required, harder to override per-environment
- Complexity: Similar
- Performance: Similar
- Scalability: Similar
- Maintainability: Better (structured config)
- Testability: Better (can load test configs)
- Operational consequences: ConfigMap mounting

**Alternative 2: CLI flags**
- How it works: Pass configuration via command-line flags
- Advantages: Explicit, documented in --help
- Disadvantages: Long command lines, no secrets support, hard to override per-environment
- Complexity: Similar
- Performance: Similar
- Scalability: Similar
- Maintainability: Worse (long command lines)
- Testability: Better (flag testing)
- Operational consequences: Complex pod specs

**Alternative 3: Hybrid (env + config file)**
- How it works: Config file for defaults, env vars for overrides
- Advantages: Structured defaults + flexible overrides
- Disadvantages: Two configuration sources, precedence rules
- Complexity: Higher
- Performance: Similar
- Scalability: Similar
- Maintainability: Worse (two sources)
- Testability: Better
- Operational consequences: Config file management

**Evidence for inference:** The `envOr` helper (`main.go:44-48`) is a simple fallback pattern. No config file parsing code exists anywhere. This suggests the architects deliberately chose pure environment variables for simplicity and Kubernetes compatibility.

### TRADEOFF

**Gains:** 12-factor compliance, Kubernetes-native, no file management, simple override per-environment, secrets via K8s Secrets.
**Sacrifices:** No hierarchical config, no validation schema, 20+ env vars are hard to document, no config file versioning.

### FAILURE POINT

The decision becomes problematic if:
- The number of env vars exceeds ~30 (hard to document/manage)
- Config validation is needed (env vars are untyped strings)
- Config needs to be shared across services

### CHANGE CONDITION

Replace if: The number of configuration options exceeds what env vars can manage, or if the team needs config validation/schema.

### SCALE CONDITION

Stops being appropriate at: When the configuration surface exceeds ~50 options and env var management becomes unwieldy.

### LEARNING QUESTION

The `envOr` function provides a default value for each env var. What happens if a required env var (like `AEGIS_DATABASE_DSN`) is missing? How does the system fail?

---

## 18. Adapter Pattern for Cross-Package Boundaries

### WHY THIS?

**What the repository does:** Adapter structs bridge interfaces between packages (e.g., `gcStoreAdapter` bridges `database.DatabaseClient` to `gc.Store`).

**Where it's implemented:**
- `cmd/ingest/gc_adapter.go:1-127`: gcStoreAdapter, gcPublisherAdapter, blobDeleterAdapter
- `cmd/ingest/derivation_bridge.go:1-105`: derivationBridge
- `cmd/ingest/derivation_helpers.go:1-72`: dbBlockReader, noop tools

**What problem it solves:** Decouples packages that have different interfaces. The GC package defines its own `Store` interface; the database package has `DatabaseClient`. The adapter bridges them.

**What engineering principle it represents:** *Depend on abstractions, not concretions.* Each package defines its own interface; adapters bridge the gaps.

**Evidence:**
- `gc_adapter.go:10-12`: `gcStoreAdapter` wraps `*database.DatabaseClient`
- `gc_adapter.go:85-87`: `gcPublisherAdapter` wraps `ingress.EventBus`
- `derivation_bridge.go:20-22`: `derivationBridge` implements `ingress.EventBus`
- `derivation_helpers.go:10-12`: `dbBlockReader` wraps `*database.DatabaseClient`

### WHY NOT THAT?

**Alternative 1: Direct dependency (no adapter)**
- How it works: GC package imports database package directly
- Advantages: Simpler code, no adapter structs
- Disadvantages: Tight coupling, GC depends on database internals
- Complexity: Lower
- Performance: Better (no indirection)
- Scalability: Similar
- Maintainability: Worse (tight coupling)
- Testability: Worse (can't mock database)
- Operational consequences: None

**Alternative 2: Shared interface package**
- How it works: Define all interfaces in a shared package
- Advantages: Single source of truth for interfaces
- Disadvantages: Shared package becomes a dependency magnet, changes affect all consumers
- Complexity: Similar
- Performance: Similar
- Scalability: Similar
- Maintainability: Worse (shared package is a bottleneck)
- Testability: Similar
- Operational consequences: None

**Alternative 3: Code generation**
- How it works: Generate adapters from interface definitions
- Advantages: No manual adapter code, type-safe
- Disadvantages: Build complexity, generated code is hard to read
- Complexity: Higher (build tooling)
- Performance: Similar
- Scalability: Similar
- Maintainability: Worse (generated code)
- Testability: Similar
- Operational consequences: Build pipeline complexity

**Evidence for inference:** The `gcStoreAdapter` (`gc_adapter.go:10-12`) has method signatures that differ from `DatabaseClient`. For example, `GetExpiredSessions` takes a `limit int` parameter, while `DatabaseClient.QueryWithMetrics` takes a generic SQL string. The adapter translates between these interfaces, allowing the GC package to define its own domain-specific operations without depending on database internals.

### TRADEOFF

**Gains:** Package decoupling, testable (mock adapters), each package defines its own interface, clean separation of concerns.
**Sacrifices:** More boilerplate (adapter structs), indirection (harder to trace), more types to maintain.

### FAILURE POINT

The decision becomes problematic if:
- The number of adapters grows large (maintenance burden)
- Adapter logic becomes complex (translation logic)
- The team finds adapters confusing (learning curve)

### CHANGE CONDITION

Replace if: The team adopts a dependency injection framework that handles adapter generation, or if the interface differences become so small that adapters are trivial.

### SCALE CONDITION

Stops being appropriate at: When the number of adapter structs exceeds ~20 and the boilerplate becomes a significant fraction of the codebase.

### LEARNING QUESTION

The `derivationBridge` implements `ingress.EventBus` but forwards to `derivation.WorkerPool`. Why not have the worker pool implement `EventBus` directly? What does the bridge pattern provide that direct implementation doesn't?

---

## 19. Interface-Based Test Doubles

### WHY THIS?

**What the repository does:** Every major interface has a fake implementation for testing (FakeStore, FakeBloomFilter, StaticKMS, InMemoryNonceStore, NoopBus).

**Where it's implemented:**
- `internal/ingress/store.go:355-504`: FakeStore
- `internal/cas/bloom.go:134-167`: FakeBloomFilter
- `internal/auth/keys.go:60-130`: StaticKMS
- `internal/auth/replay.go:72-100`: InMemoryNonceStore
- `internal/ingress/events.go:32-42`: NoopBus

**What problem it solves:** Enables unit testing without external dependencies (PostgreSQL, Redis, S3, Kafka).

**What engineering principle it represents:** *Program to interfaces, not implementations.* This enables swapping real implementations for test doubles.

**Evidence:**
- `store.go:355`: `type FakeStore struct` — implements Store interface
- `bloom.go:134`: `type FakeBloomFilter struct` — implements BloomFilterer interface
- `keys.go:60`: `type StaticKMS struct` — implements KMSClient interface
- `replay.go:72`: `type InMemoryNonceStore struct` — implements NonceStore interface
- `events.go:32`: `type NoopBus struct` — implements EventBus interface

### WHY NOT THAT?

**Alternative 1: Integration tests only**
- How it works: Test against real PostgreSQL, Redis, S3
- Advantages: Tests real behavior, no fake maintenance
- Disadvantages: Slow, requires infrastructure, flaky (external dependencies)
- Complexity: Lower (no fakes)
- Performance: Worse (test execution time)
- Scalability: Similar
- Maintainability: Worse (test infrastructure)
- Testability: Worse (slow, flaky)
- Operational consequences: Test infrastructure management

**Alternative 2: Mock libraries (gomock, testify/mock)**
- How it works: Generate mocks from interfaces
- Advantages: Auto-generated, type-safe, flexible expectations
- Disadvantages: Generated code is hard to read, brittle (implementation-coupled), learning curve
- Complexity: Similar
- Performance: Similar
- Scalability: Similar
- Maintainability: Worse (generated code)
- Testability: Better (flexible expectations)
- Operational consequences: Mock generation in build

**Alternative 3: SQLite for testing**
- How it works: Use SQLite instead of PostgreSQL for tests
- Advantages: No infrastructure needed, fast
- Disadvantages: Not PostgreSQL-compatible (triggers, ltree, etc.), false positives
- Complexity: Lower
- Performance: Better (in-memory)
- Scalability: Similar
- Maintainability: Worse (incompatible SQL)
- Testability: Worse (false positives)
- Operational consequences: None

**Evidence for inference:** The `FakeStore` (`store.go:355-504`) is ~150 lines of hand-written code. It uses `sync.Mutex` for concurrent access, stores data in maps, and implements all 12 `Store` methods. The effort to write this fake suggests the team values testability over development speed. The `StaticKMS` (`keys.go:60-130`) supports key rotation via the `Rotate` method, enabling tests that verify rotation behavior.

### TRADEOFF

**Gains:** Fast unit tests, no external dependencies, deterministic behavior, easy to set up test scenarios.
**Sacrifices:** Fakes must be maintained (keep in sync with real implementations), may not capture all real-world behavior, more code to maintain.

### FAILURE POINT

The decision becomes problematic if:
- Fakes diverge from real implementations (tests pass but production fails)
- The number of interfaces grows large (many fakes to maintain)
- Fakes become complex (hard to understand test behavior)

### CHANGE CONDITION

Replace if: The team adopts a contract testing approach (e.g., Pact) that validates fake behavior against real implementations.

### SCALE CONDITION

Stops being appropriate at: When the number of interfaces exceeds ~30 and maintaining fakes becomes a significant fraction of development time.

### LEARNING QUESTION

The `FakeStore` uses `sync.Mutex` for concurrent access. The real `PgStore` uses PostgreSQL transactions. How do these concurrency models differ, and what bugs might the fake miss?

---

## 20. GC Safety Window and Rate Limiting

### WHY THIS?

**What the repository does:** GC only deletes blocks with ref_count = 0 for > 7 days. Rate limiting caps deletion at 5,000 blocks/hour.

**Where it's implemented:**
- `internal/cas/registry.go:23`: `GCSafetyWindow = 7 * 24 * time.Hour`
- `internal/gc/gc.go:35`: `MaxBlocksPerHour = 5000`
- `internal/gc/gc.go:120-130`: Rate limit check in blockSweepLoop

**What problem it solves:** Prevents premature deletion of blocks that might still be referenced. The safety window allows for concurrent operations; rate limiting prevents GC from overwhelming the system.

**What engineering principle it represents:** *Safety margins in distributed systems.* The 7-day window accounts for clock skew, concurrent operations, and delayed cleanup.

**Evidence:**
- `registry.go:23`: `GCSafetyWindow = 7 * 24 * time.Hour`
- `gc.go:35`: `MaxBlocksPerHour = 5000`
- `gc.go:120-130`: Rate limit check before each deletion
- `schema.sql:142`: Comment: "GC deletes only after 7-day zero window"

### WHY NOT THAT?

**Alternative 1: Immediate deletion (no safety window)**
- How it works: Delete blocks immediately when ref_count reaches 0
- Advantages: Faster storage reclamation, less orphaned storage
- Disadvantages: Race conditions (concurrent commit might reference the block), no recovery window
- Complexity: Lower
- Performance: Better (faster reclamation)
- Scalability: Similar
- Maintainability: Better (simpler)
- Testability: Better
- Operational consequences: Data loss risk

**Alternative 2: No rate limiting**
- How it works: Delete all orphans in one sweep
- Advantages: Faster reclamation
- Disadvantages: Can overwhelm DB with DELETE queries, spike in I/O
- Complexity: Lower
- Performance: Worse (DB spike)
- Scalability: Worse (DB pressure)
- Maintainability: Better
- Operational consequences: DB performance degradation

**Alternative 3: Tombstone-only (no double-check)**
- How it works: Delete based on tombstone events only
- Advantages: Simpler
- Disadvantages: Can delete blocks that were re-referenced between tombstone and delete
- Complexity: Lower
- Performance: Better
- Scalability: Similar
- Maintainability: Better
- Operational consequences: Data loss risk

**Evidence for inference:** The `DoubleCheckBlock` method (`gc_adapter.go:83-100`) re-queries ref_count before each deletion. The comment at `gc.go:120` says: "Double-check ref_count = 0 before every delete." This suggests the architects encountered a race condition where a block was re-referenced between `FindOrphans` and `DeleteBlocks`.

### TRADEOFF

**Gains:** Prevents premature deletion, allows concurrent operations, rate limiting prevents DB overload, double-check prevents race conditions.
**Sacrifices:** Slower storage reclamation (7-day delay), more orphaned storage, rate limiting delays reclamation further.

### FAILURE POINT

The decision becomes problematic if:
- The 7-day window is too long (excessive orphaned storage costs)
- The rate limit is too low (orphans accumulate faster than deletion)
- The double-check adds too much DB load

### CHANGE CONDITION

Replace if: Storage costs from orphaned blocks exceed the safety margin value, or if the system needs immediate reclamation for compliance.

### SCALE CONDITION

Stops being appropriate at: When the rate of new orphans exceeds 5,000 blocks/hour and the backlog grows unbounded.

### LEARNING QUESTION

The GC has a rate limit of 5,000 blocks/hour. If the system creates 10,000 orphaned blocks per hour, what happens? How does the backlog grow, and when does it become a problem?

---

## 21. Token Bucket Rate Limiting (In-Memory)

### WHY THIS?

**What the repository does:** Per-tenant token bucket rate limiting with 1000 RPS default and burst of 2000. In-memory, not Redis-backed.

**Where it's implemented:**
- `internal/ingress/ratelimit.go:1-132`: RateLimiter with per-tenant token buckets
- `internal/ingress/ratelimit.go:8`: Default: 1000 RPS, burst 2000

**What problem it solves:** Prevents abuse (DoS, brute force) per tenant. In-memory avoids Redis dependency for rate limiting.

**What engineering principle it represents:** *Simple, local rate limiting for single-process deployments.* Token bucket allows bursts while enforcing average rate.

**Evidence:**
- `ratelimit.go:8`: `rps: rps, burst: burst` — configurable per-deployment
- `ratelimit.go:66-75`: `tokenBucket.allow(now)` — refills tokens based on elapsed time
- `ratelimit.go:100-112`: `AllowHTTP` middleware — returns 429 with Retry-After

### WHY NOT THAT?

**Alternative 1: Redis-backed distributed rate limiting**
- How it works: Store rate limit state in Redis, shared across pods
- Advantages: Consistent across replicas, distributed coordination
- Disadvantages: Redis dependency, network latency, Redis failure = no rate limiting
- Complexity: Higher
- Performance: Worse (Redis round-trip)
- Scalability: Better (distributed)
- Maintainability: Worse (Redis dependency)
- Testability: Worse (Redis dependency)
- Operational consequences: Redis availability

**Alternative 2: Sliding window counter**
- How it works: Count requests in a sliding time window
- Advantages: More accurate than token bucket for fixed windows
- Disadvantages: More complex, memory grows with window size
- Complexity: Higher
- Performance: Similar
- Scalability: Similar
- Maintainability: Worse
- Testability: Similar
- Operational consequences: None

**Alternative 3: No rate limiting**
- How it works: Accept all requests
- Advantages: Simplest, no overhead
- Disadvantages: Vulnerable to abuse, no protection
- Complexity: Lowest
- Performance: Best
- Scalability: Worse (abuse)
- Maintainability: Best
- Operational consequences: Security vulnerability

**Evidence for inference:** The `RateLimiter` uses a per-tenant map (`buckets map[string]*tokenBucket`). Under high concurrency, the `sync.Mutex` on the map could become a bottleneck. The `NewRateLimiterWithClock` constructor (`ratelimit.go:30-36`) accepts a custom clock function, enabling deterministic testing without real time. This suggests the team prioritized testability over raw performance.

### TRADEOFF

**Gains:** No Redis dependency, O(1) per request, burst support, per-tenant isolation, deterministic testing.
**Sacrifices:** Not distributed (each pod has its own buckets), memory grows with active tenants, mutex contention under high concurrency.

### FAILURE POINT

The decision becomes problematic if:
- Multiple pods need consistent rate limiting (each pod has independent buckets)
- The number of active tenants exceeds memory capacity
- The mutex becomes a bottleneck under high concurrency

### CHANGE CONDITION

Replace if: The system needs distributed rate limiting across pods, or if per-tenant rate limiting needs to be consistent across deployments.

### SCALE CONDITION

Stops being appropriate at: When the number of concurrent tenants exceeds ~10K and the in-memory map becomes a memory pressure point, or when multi-pod consistency is required.

### LEARNING QUESTION

Each ingestion pod has its own rate limiter with independent buckets. If a client sends 500 RPS to pod A and 500 RPS to pod B, is the tenant's 1000 RPS limit enforced? What does this mean for production deployments?

---

## 22. Structured JSON Logging with stdlib slog

### WHY THIS?

**What the repository does:** Uses Go's stdlib `log/slog` with JSON handler for structured logging.

**Where it's implemented:**
- `cmd/ingest/main.go:44-48`: `slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: ...}))`
- `internal/database/metrics.go:150-165`: `c.logger.Log(ctx, level, "db.operation", ...)`
- `internal/ingress/server.go:227-240`: Request logging middleware

**What problem it solves:** Machine-parseable, structured logs that can be aggregated and queried. Zero external dependencies.

**What engineering principle it represents:** *Use stdlib when it's sufficient.* slog provides structured logging without external dependencies.

**Evidence:**
- `main.go:44-48`: slog JSON handler setup
- No zap, zerolog, or other logging library in `go.mod`
- `server.go:227-240`: Request logging with method, path, status, duration_ms

### WHY NOT THAT?

**Alternative 1: Zap (uber-go/zap)**
- How it works: High-performance structured logging
- Advantages: Fastest, rich features, field-based API
- Disadvantages: External dependency, larger binary, learning curve
- Complexity: Similar
- Performance: Better (faster)
- Scalability: Similar
- Maintainability: Worse (external dependency)
- Testability: Similar
- Operational consequences: Dependency management

**Alternative 2: Zerolog**
- How it works: Zero-allocation structured logging
- Advantages: Zero allocations, fast, small
- Disadvantages: External dependency, different API
- Complexity: Similar
- Performance: Better (zero alloc)
- Scalability: Similar
- Maintainability: Worse
- Testability: Similar
- Operational consequences: Dependency management

**Alternative 3: Unstructured logging (fmt.Println)**
- How it works: Print raw text
- Advantages: Simplest
- Disadvantages: Not machine-parseable, no structure, hard to query
- Complexity: Lowest
- Performance: Best
- Scalability: Similar
- Maintainability: Worst
- Operational consequences: Impossible to query

**Evidence for inference:** The `slowGate` function (`metrics.go:150-165`) uses `c.logger.Warn("slow query", "pool", ..., "duration_ms", ...)` — structured key-value pairs. This is only possible with slog's structured API. The choice of slog over zap/zerolog suggests the team valued stdlib simplicity over maximum performance.

### TRADEOFF

**Gains:** Zero dependencies, stdlib (stable, well-tested), structured JSON output, sufficient performance for I/O-bound services.
**Sacrifices:** Slower than zap/zerolog, fewer features (no sampling, no hooks), less configurable.

### FAILURE POINT

The decision becomes problematic if:
- Logging becomes a performance bottleneck (slog is slower than zap)
- The team needs advanced features (sampling, hooks, custom encoders)
- Log volume becomes very high and allocation pressure matters

### CHANGE CONDITION

Replace if: Logging performance becomes measurable in benchmarks, or if the team needs features that slog doesn't provide.

### SCALE CONDITION

Stops being appropriate at: When log volume exceeds ~100K lines/second and slog's allocation overhead becomes measurable.

### LEARNING QUESTION

The `logOperation` function in `metrics.go` emits a structured log for every database operation. How does this affect performance? What would be the impact of adding sampling (logging 10% of operations)?

---

## 23. PostgreSQL Over NoSQL

### WHY THIS?

**What the repository does:** Uses PostgreSQL as the primary metadata store with complex schema (ltree, triggers, CHECK constraints, partitioning).

**Where it's implemented:**
- `db/schema.sql:1-268`: Full schema with 9 tables, triggers, constraints
- `internal/database/client.go:1-613`: DatabaseClient with 4 pool tiers
- `go.mod`: `github.com/jackc/pgx/v5` — PostgreSQL driver

**What problem it solves:** ACID transactions, complex queries, referential integrity, triggers for invariants, ltree for hierarchy.

**What engineering principle it represents:** *Use the right tool for the job.* PostgreSQL provides features that NoSQL databases don't (transactions, triggers, ltree, CHECK constraints).

**Evidence:**
- `schema.sql:131-142`: `cas_blocks` with trigger-maintained ref_count
- `schema.sql:82`: `lineage_path LTREE NOT NULL` — ltree extension
- `schema.sql:158-160`: `CHECK (NOT is_completed OR node_id IS NOT NULL)` — complex constraint
- `client.go:350-390`: `BeginWriteTx` — transaction support

### WHY NOT THAT?

**Alternative 1: MongoDB**
- How it works: Document store for metadata
- Advantages: Flexible schema, horizontal scaling, JSON-native
- Disadvantages: No ACID transactions (multi-document), no triggers, no ltree, eventual consistency
- Complexity: Similar
- Performance: Similar
- Scalability: Better (horizontal)
- Maintainability: Worse (no schema enforcement)
- Testability: Similar
- Operational consequences: Sharding complexity

**Alternative 2: DynamoDB**
- How it works: Key-value store for metadata
- Advantages: Fully managed, auto-scaling, serverless
- Disadvantages: No complex queries, no joins, no triggers, limited indexing
- Complexity: Lower (managed)
- Performance: Better (single-digit ms)
- Scalability: Best (auto-scaling)
- Maintainability: Worse (query limitations)
- Testability: Worse (local emulator)
- Operational consequences: Vendor lock-in

**Alternative 3: CockroachDB**
- How it works: Distributed SQL database
- Advantages: PostgreSQL-compatible, distributed, horizontal scaling
- Disadvantages: No ltree extension, newer (less mature), higher latency
- Complexity: Higher (distributed)
- Performance: Similar
- Scalability: Better (distributed)
- Maintainability: Similar (PostgreSQL-compatible)
- Testability: Similar
- Operational consequences: Cluster management

**Evidence for inference:** The schema uses PostgreSQL-specific features that NoSQL databases don't support: ltree (line 13), triggers (referenced in stored-procedures.sql), CHECK constraints (line 158), partitioning (line 190). The `ref_count` trigger mechanism is fundamental to CAS correctness. Migrating away from PostgreSQL would require reimplementing these invariants in application code.

### TRADEOFF

**Gains:** ACID transactions, triggers for invariants, ltree for hierarchy, CHECK constraints, mature ecosystem, strong consistency.
**Sacrifices:** Vertical scaling (harder to scale horizontally), single-region by default, heavier than NoSQL for simple operations.

### FAILURE POINT

The decision becomes problematic if:
- Write throughput exceeds single-primary capacity (~10K writes/sec)
- The schema becomes too complex to maintain
- Horizontal scaling becomes required

### CHANGE CONDITION

Replace if: Write throughput exceeds primary capacity, or if the system needs multi-region active-active deployment.

### SCALE CONDITION

Stops being appropriate at: When write throughput exceeds ~10K writes/second (single primary limit), or when the dataset exceeds ~1TB (vertical scaling limit).

### LEARNING QUESTION

The schema uses CHECK constraints (e.g., `CHECK (NOT is_completed OR node_id IS NOT NULL)`). How do these constraints protect against application bugs? Can you find a scenario where the CHECK constraint catches an error that the application code misses?

---

## 24. Redis for Cache, Nonces, and Bloom Filter

### WHY THIS?

**What the repository does:** Redis serves three purposes: namespace cache (generation-based), nonce store (replay protection), and bloom filter (CAS dedup acceleration).

**Where it's implemented:**
- `internal/database/cache.go:1-184`: NamespaceCache (Redis-backed)
- `internal/auth/replay.go:45-60`: RedisNonceStore (SETNX)
- `internal/cas/bloom.go:55-90`: RedisBloomFilter (BF.EXISTS)

**What problem it solves:** Fast, in-memory operations for three different use cases. Redis is the common denominator.

**What engineering principle it represents:** *Use a versatile tool for multiple purposes.* Redis handles caching, locking (SETNX), and probabilistic data structures (bloom filter) efficiently.

**Evidence:**
- `cache.go:55`: `generationKey = "aegis:ns:gen:" + tenantID` — Redis key for generation counter
- `replay.go:53`: `client.SetNX(ctx, nonceKey(nonce), 1, ttl)` — Redis SETNX for nonce
- `bloom.go:55`: `cmd.BFReserve(ctx, key, errorRate, capacity)` — Redis Bloom module

### WHY NOT THAT?

**Alternative 1: Separate systems for each purpose**
- How it works: Memcached for cache, PostgreSQL for nonces, in-memory bloom for dedup
- Advantages: Specialized systems, no single point of failure
- Disadvantages: Three systems to operate, more complex
- Complexity: Higher (three systems)
- Performance: Similar
- Scalability: Similar
- Maintainability: Worse (three systems)
- Testability: Worse (three dependencies)
- Operational consequences: Three systems to monitor

**Alternative 2: PostgreSQL for everything**
- How it works: Use PostgreSQL for cache, nonces, and bloom filter
- Advantages: No Redis dependency, single system
- Disadvantages: Slower than Redis for in-memory operations, higher latency
- Complexity: Lower (one system)
- Performance: Worse (disk-based)
- Scalability: Worse (PostgreSQL is slower)
- Maintainability: Better (one system)
- Testability: Better (one dependency)
- Operational consequences: Higher DB load

**Alternative 3: In-memory only (no Redis)**
- How it works: All cache/nonces/bloom in process memory
- Advantages: No Redis dependency, lowest latency
- Disadvantages: Not shared across pods, lost on restart, memory grows unbounded
- Complexity: Lower
- Performance: Best (no network)
- Scalability: Worse (not shared)
- Maintainability: Better
- Testability: Better
- Operational consequences: State lost on restart

**Evidence for inference:** Redis serves three completely different purposes. The bloom filter requires the RedisBloom module (`BF.RESERVE`, `BF.ADD`, `BF.EXISTS`). The nonce store uses `SETNX`. The cache uses `GET/SET/INCR`. These are different Redis features, suggesting Redis was chosen for its versatility rather than being the optimal choice for any single use case.

### TRADEOFF

**Gains:** Single system for multiple purposes, in-memory performance, versatile (caching, locking, probabilistic data structures), mature ecosystem.
**Sacrifices:** Single point of failure for three features, Redis dependency for all three, memory management for three use cases.

### FAILURE POINT

The decision becomes problematic if:
- Redis is unavailable (all three features degrade simultaneously)
- Redis memory is exhausted (which feature gets evicted?)
- Redis restart causes state loss (nonces, bloom filter, cache all reset)

### CHANGE CONDITION

Replace if: One of the three use cases needs a specialized system (e.g., dedicated bloom filter service), or if Redis failure impact is unacceptable.

### SCALE CONDITION

Stops being appropriate at: When Redis memory requirements for all three use cases exceed a single instance's capacity (~100GB), requiring Redis Cluster or separate instances.

### LEARNING QUESTION

Redis serves three purposes: cache, nonces, and bloom filter. If Redis runs out of memory, which feature is most critical? How would you prioritize eviction?
