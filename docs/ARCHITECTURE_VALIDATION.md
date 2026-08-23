# Project Aegis: Architectural Validation Document

| | |
|---|---|
| **Document Track** | Core Systems Architecture |
| **Prompt** | 1.1 — Core Architecture & Design Document |
| **Status** | Draft for Architecture Review Board |
| **Source Specs** | `initial vision/pdfcrowd.pdf` (Engineering Design Document), `initial vision/pdfcrowd (1).pdf` (Stack Addendum) |
| **Classification** | Technical Blueprint |

---

## Table of Contents

1. [Design Goals & Invariant Summary](#1-design-goals--invariant-summary)
2. [System Topology](#2-system-topology)
3. [Plane Separation & Communication Boundaries](#3-plane-separation--communication-boundaries)
4. [Component Catalog](#4-component-catalog)
5. [Immutable Invariants](#5-immutable-invariants)
6. [Data Flow Paths](#6-data-flow-paths)
7. [Integration Contracts](#7-integration-contracts)
8. [Dependency Direction Rules](#8-dependency-direction-rules)
9. [Failure Mode Register](#9-failure-mode-register)
10. [SLA Matrix](#10-sla-matrix)
11. [Acceptance Criteria Self-Validation](#11-acceptance-criteria-self-validation)

---

## 1. Design Goals & Invariant Summary

Project Aegis is a production-grade, exabyte-capable, multi-tenant distributed storage and synchronization engine modeled on the operational mechanics of Google Drive / Dropbox Magic Pocket.

The foundational architectural decision is **physical decoupling of the binary data plane from the relational control/metadata plane**, so that high-throughput binary I/O can never exhaust database connection pools or starve metadata transaction threads.

Three invariants are absolute and non-negotiable; every design decision downstream must preserve them:

| # | Invariant | One-Line Statement |
|---|-----------|--------------------|
| I-1 | **Bit-Perfect CAS** | Data payloads are addressed exclusively by cryptographic hash; mutations are strictly append-only. |
| I-2 | **Acyclic Namespace** | The filesystem graph is an immutable DAG; directory cycles and broken lineage paths are rejected at schema level. |
| I-3 | **Cryptographic Ingress** | Direct-to-blob access requires short-lived, tenant-scoped HMAC tokens evaluated at edge storage endpoints. |

---

## 2. System Topology

```
                                ┌─────────────────────────────────────────────────┐
                                │                   CLIENTS                       │
                                │   Aegis SDK: FastCDC chunker · SHA-256 hasher   │
                                │   dedup pre-filter · retry · pause/resume       │
                                └──────┬───────────────────────────────┬──────────┘
                                       │ (1) CONTROL: HTTPS/QUIC       │ (2) DATA: direct
                                       │     TLS 1.3 · JSON APIs       │     pre-signed PUT/GET
                                       │     JWT bearer auth           │     HMAC-signed URLs
                                       ▼                               ▼
        ╔════════════════════════════════════════════╗   ╔═══════════════════════════════════════╗
        ║          CONTROL / METADATA PLANE          ║   ║             DATA PLANE                ║
        ║                                            ║   ║                                       ║
        ║  ┌──────────────────────────────────────┐  ║   ║  ┌─────────────────────────────────┐  ║
        ║  │ EDGE LAYER (Anycast PoPs)            │  ║   ║  │ DIRECT INGRESS PoP              │  ║
        ║  │ Cloudflare Workers / Lambda@Edge     │  ║   ║  │ S3-compatible front door        │  ║
        ║  │ · Anycast routing                    │  ║   ║  │ · Validates tenant-scoped HMAC  │  ║
        ║  │ · JWT session termination            │  ║   ║  │   BEFORE proxying bytes         │  ║
        ║  │ · Rate limit 1000 RPS/tenant         │◄─┼───┼──┤ · Rejects 401 at edge           │  ║
        ║  │ · Circuit breaker → ingress          │  ║   ║  └───────────────┬─────────────────┘  ║
        ║  └──────────────────┬───────────────────┘  ║   ║                  │                      ║
        ║                     ▼                      ║   ║                  ▼                      ║
        ║  ┌──────────────────────────────────────┐  ║   ║  ┌─────────────────────────────────┐  ║
        ║  │ INGRESS ENGINE (stateless Go)        │  ║   ║  │ CAS BLOB STORE                  │  ║
        ║  │ HandleInitiate()                     │  ║   ║  │ S3 / MinIO / R2 / Ceph          ║
        ║  │ HandleCommit()                       │  ║   ║  │ · Content-addressed objects     ║
        ║  │ · Dedup hash filtering               │  ║   ║  │ · Key = SHA-256(block bytes)    ║
        ║  │ · Pre-signed URL issuance            │  ║   ║  │ · 8+4 Reed-Solomon erasure code ║
        ║  │ · Transactional version commits      │  ║   ║  │ · HOT/WARM/COLD tiering         ║
        ║  └───┬──────────────┬───────────────┬───┘  ║   ║  │ · Lifecycle: abort incomplete   ║
        ║      │              │               │      ║   ║  │   uploads @ 24h                 ║
        ║      ▼              ▼               │      ║   ║  └──┬───────────────▲──────────────┘  ║
        ║  ┌────────────┐ ┌──────────────┐   │      ║   ║     │(3) tombstone  │(4) chunk GET    ║
        ║  │ METADATA   │ │ REDIS CACHE  │   │ emit │   ║     │    deletes    │    by workers   ║
        ║  │ STORE      │ │ POOL         │   │ CDC  │   ╚═════╪═══════════════╪═════════════════╝
        ║  │ PostgreSQL │ │ · Bloom      │   │ evt. │           │               │
        ║  │ 16+ ltree  │ │   filter     │   │      │           │               │
        ║  │ · tenants  │ │ · nonces     │   │      │           │               │
        ║  │ · nodes    │ │   (replay)   │   │      │           │               │
        ║  │   (ltree)  │ │ · sessions   │   │      │           │               │
        ║  │ · versions │ │ · leases     │   │      │           │               │
        ║  │ · cas reg. │ └──────────────┘   ▼      ║   ╔═════▼═══════════════════════════════╗
        ║  │ · manifests│ ┌──────────────────────┐ ║   ║        ASYNC DERIVATION PLANE       ║
        ║  │ · sessions │ │ CDC EVENT BUS        │ ║   ║                                     ║
        ║  └────────────┘ │ Redpanda / Kafka     │ ║   ║  ┌───────────────────────────────┐  ║
        ║        ▲        │ topics:              │ ║   ║  │ DERIVATION WORKER FLEET       │  ║
        ║        │ sweep  │ · file_versions      │╏│   ║  │ · ClamAV scanner (<60s SLA)   │  ║
        ║        │ batches│ · cas-tombstones     │ ║   ║  │ · OCR (<120s SLA)             │  ║
        ║        │        └──────────┬───────────┘ ║   ║  │ · FFmpeg transcode (<300s)    │  ║
        ║  ┌─────┴──────────┐        │ consume     ║   ║  │ · Vector embeddings           │  ║
        ║  │ GC / COMPACTION│────────┼─────────────┼───╫──► results → derivation_     │  ║
        ║  │ · ref_count=0  │        └─────────────┼───╫──► results table (PG)        │  ║
        ║  │   detection    │ emit tombstones      │ ║   ║  · offset commit post-success │  ║
        ║  │ · 7-day safety │                      │ ║   ║  · DLQ for permanent failures │  ║
        ║  │ · dry-run mode │                      │ ║   ║  └───────────────────────────────┘  ║
        ║  └────────────────┘                      │ ║   ╚═════════════════════════════════════╝
        ╚══════════════════════════════════════════╝
                                   ▲
                        ┌──────────┴──────────┐
                        │  KMS / SECRETS      │
                        │  Tenant-scoped keys │
                        │  90-day rotation    │
                        │  7-day overlap      │
                        └─────────────────────┘
```

### Topology Legend

| Arrow | Meaning |
|-------|---------|
| (1) Control path | JSON API calls over HTTPS/QUIC; JWT auth; never carries file bytes |
| (2) Data path | Raw chunk bytes; client → blob store directly via pre-signed URL; validated at edge |
| (3) Tombstones | Async delete events on `cas-tombstones` topic |
| (4) Chunk reads | Workers pull chunks straight from CAS with scoped read-only credentials |

---

## 3. Plane Separation & Communication Boundaries

### 3.1 The Three Planes

| Plane | Components | State Owned | Latency Class |
|-------|-----------|-------------|---------------|
| **Control / Metadata** | Edge API, Ingress Engine, PostgreSQL, Redis, KMS | All relational state: tenants, namespace DAG, versions, CAS registry, manifests, sessions, derivation results | Interactive; p95 < 5ms per query |
| **Data** | Direct Ingress PoPs, CAS Blob Store | Immutable chunk bytes keyed by content hash | Streaming; throughput-bound (>1 GB/s aggregate) |
| **Async Derivation** | Event Bus, Worker Fleet, GC/Compaction | None (reads facts, writes results/tombstones) | Batch/eventual; seconds-to-minutes SLAs |

### 3.2 Cross-Plane Boundary Rules

These rules are **hard architectural constraints**. Violations require architecture review board sign-off.

| # | Rule | Rationale |
|---|------|-----------|
| B-1 | **No file bytes ever traverse the control plane.** Chunks flow only Client → Edge PoP → CAS. | Prevents binary I/O from exhausting DB pools and app-server memory. |
| B-2 | **No synchronous calls from Data plane back into Control plane.** Blob store never queries PostgreSQL during upload PUTs. | Eliminates circular dependency; keeps data path failure-isolated from metadata outages. |
| B-3 | **All data-plane authorization is evaluated at the edge** via self-contained HMAC tokens — no database lookup required to validate a chunk PUT. | Token validation must cost O(1) CPU, not a network round trip. |
| B-4 | **Control→Async communication is one-way via CDC events.** Ingress publishes commit facts; workers consume them. Workers never call Ingress synchronously. | Decouples hot path from slow consumers entirely. |
| B-5 | **Workers get read-only credentials to CAS** and write only to `derivation_results` (or quarantine flags). They cannot mutate namespace or registry tables. | Least privilege; a compromised worker cannot corrupt canonical state. |
| B-6 | **GC deletes are two-phase:** metadata registry row removal first, physical object deletion later, driven by tombstone consumption — never in the same transaction as user operations. | A GC bug must never be able to corrupt live state atomically with user writes. |

### 3.3 What Crosses Each Boundary

```
CONTROL ⇄ CLIENT :  InitiateRequest/Response · CommitRequest/Response · JWT · signed URL sets
CLIENT ⇄ DATA    :  raw chunk bytes (PUT) · assembled byte ranges (GET)
DATA ⇄ ASYNC     :  chunk GETs by workers · tombstone-driven DELETEs
CONTROL → ASYNC  :  VERSION_COMMITTED facts · unreferenced-block tombstone batches
ASYNC → CONTROL  :  derivation_results inserts ONLY
ALL → KMS        :  key fetch / versioned rotation reads
```

---

## 4. Component Catalog

Every component has exactly one reason to exist ("single responsibility"). "Does NOT" clauses are binding.

### 4.1 Client SDK (`aegis-sdk`)

| Attribute | Specification |
|---|---|
| **Responsibility** | Deterministic local chunking (FastCDC), hashing, dedup-aware upload orchestration, retry/resume. |
| **Owns** | Local session state file (`~/.aegis/sessions/*.json`); nothing server-side. |
| **Does NOT** | Trust server responses as authoritative for chunk identity; store secrets beyond the user's JWT. |
| **Failure mode** | Process crash mid-upload → resume replays only unconfirmed chunks; server session expiry (24h) forces fresh initiate. |

### 4.2 Edge Layer (Anycast PoPs)

| Attribute | Specification |
|---|---|
| **Responsibility** | Terminate TLS 1.3/QUIC; validate JWT (control) and HMAC tokens (data) *before* backend dispatch; rate-limit 1000 RPS/tenant; DDoS absorption; circuit-break toward Ingress. |
| **Owns** | Nothing durable. Ephemeral validation state only (nonce checks delegated to Redis). |
| **Does NOT** | Touch the database; inspect chunk payloads; make authorization policy decisions beyond token math. |
| **Failure mode** | PoP loss → Anycast re-route to nearest healthy PoP (BGP-level failover, no client action). |

### 4.3 Ingress Engine (Stateless Go)

| Attribute | Specification |
|---|---|
| **Responsibility** | `HandleInitiate` (quota check, batch dedup filtering, session creation, signed URL issuance) and `HandleCommit` (single ACID transaction: version insert + block upserts + manifest rows + session close + CDC emission). |
| **Owns** | Nothing. Zero durable state — restart at any time without data loss. |
| **Does NOT** | Stream file bytes; cache canonical truth locally; perform derivation work inline. |
| **Failure mode** | Pod kill mid-commit → transaction rolls back; client retries idempotently (session FOR UPDATE guard prevents double-version). |

### 4.4 Metadata Store (PostgreSQL 16+ / CockroachDB)

| Attribute | Specification |
|---|---|
| **Responsibility** | Sole source of truth: `tenants`, `namespace_nodes` (ltree DAG), `file_versions`, `cas_blocks`, `file_manifest_blocks`, `upload_sessions`, `derivation_results`. Enforces I-2 at schema level. |
| **Owns** | All relational state; stored procedures incl. `move_directory()`; ACL epochs. |
| **Does NOT** | Store bytes; serve read traffic for data-plane decisions synchronously; accept schema-bypassing parent mutations. |
| **Failure mode** | Primary loss → synchronous standby promotion <30s (RTO). Zero committed-transaction loss (RPO=0) via sync replication. PITR available for logical corruption. |

### 4.5 Redis Cache Pool (Redis Cluster / DragonflyDB)

| Attribute | Specification |
|---|---|
| **Responsibility** | Fast-path existence pre-filtering (Bloom filter of block hashes), nonce registry for anti-replay, upload-session fast index, distributed locks/fencing tokens, namespace metadata cache (TTL 5 min). |
| **Owns** | Only *derivable* or *ephemeral* state. Loss degrades latency, never correctness. |
| **Does NOT** | Act as source of truth; persist anything whose loss causes data loss. |
| **Failure mode** | Cache flush → cold-start rebuild from PostgreSQL; Bloom false-positive rate bounded by periodic rebuild (1h TTL). |

### 4.6 CAS Blob Store (S3 / MinIO / R2 / Ceph)

| Attribute | Specification |
|---|---|
| **Responsibility** | Durable, erasure-coded (8+4 RS) storage of immutable chunks; object key = SHA-256; lifecycle policies (abort incomplete multipart @24h; HOT→WARM@7d→COLD@30d); executes tombstone deletions. |
| **Owns** | Physical chunk bytes. Nothing else — no indexes, no manifests. |
| **Does NOT** | Interpret content; enforce tenancy (edge tokens already did); call back into control plane. |
| **Failure mode** | Node/disk loss → RS parity reconstruction (survives any 4 simultaneous shard losses per stripe); bitrot → scrubber quarantine + repair. |

### 4.7 CDC Event Bus (Redpanda / Kafka)

| Attribute | Specification |
|---|---|
| **Responsibility** | Durable ordered log of facts: `file_versions` (commit events) and `cas-tombstones` (GC deletions). Fan-out to consumer groups with independent offsets. |
| **Owns** | Retention-windowed event log (tiered storage for replay). |
| **Does NOT** | Transform events; guarantee global ordering across partitions (ordering is per-key: `version_id`/`block_hash`). |
| **Failure mode** | Broker loss → replicated ISR survives; producer retries with idempotence; consumer lag monitored + alerted. |

### 4.8 Derivation Worker Fleet

| Attribute | Specification |
|---|---|
| **Responsibility** | Consume `VERSION_COMMITTED`; run ClamAV scan (<60s), OCR (<120s), FFmpeg thumbnail/transcode (<300s), vector embedding; persist to `derivation_results`; route permanent failures to DLQ. |
| **Owns** | Transient scratch space only. |
| **Does NOT** | Block or delay the upload hot path (contractually impossible — they run purely out-of-band); mutate canonical metadata. |
| **Failure mode** | Worker crash → Kafka rebalances partition to peer; uncommitted offsets reprocess (idempotent result upserts make this safe). |

### 4.9 GC / Compaction Pipeline

| Attribute | Specification |
|---|---|
| **Responsibility** | Hourly expired-session cleanup; daily batched detection of `ref_count = 0 ∧ age > 7 days`; double-check before delete; tombstone emission; audit trail; rate-limited physical deletion via tombstone consumers; dry-run mode. |
| **Owns** | Deletion decision workflow + audit log. |
| **Does NOT** | Delete anything younger than the 7-day safety window; run inside user-facing transactions; exceed its deletion-rate budget. |
| **Failure mode** | GC halted → storage grows (safe direction); GC buggy → audit trail + object versioning allow forensic restore; alert fires on abnormal deletion rates. |

### 4.10 KMS / Secrets

| Attribute | Specification |
|---|---|
| **Responsibility** | Per-tenant key material (`kms_key_arn`); versioned keys rotated every 90 days with 7-day dual-validation overlap; encryption-at-rest key management. |
| **Owns** | Key material and version history. |
| **Does NOT** | Participate in the request hot path (keys cached at edge/ingress within TTL). |
| **Failure mode** | KMS unreachable → cached keys continue serving within TTL; new token issuance degrades with circuit breaker. |

---

## 5. Immutable Invariants

Each invariant below is specified as: formal definition → enforcement points → violation scenarios → testing strategy.

---

### 5.1 Invariant I-1: Bit-Perfect CAS

#### Formal Definition (First-Order Logic)

Let `B` = set of stored blocks, `bytes(b)` = payload of block b, `h(b)` = declared address.

```
(I-1a) Address identity:
   ∀b ∈ B : h(b) = SHA-256(bytes(b))

(I-1b) Read-back identity (no silent mutation):
   ∀h ∈ addr(B) : SHA-256(Read(h)) = h

(I-1c) Append-only (immutability):
   ∀b ∈ B : ¬∃t₂ > t₁ : bytes_t₁(b) ≠ bytes_t₂(b)
   (blocks are created once; "updates" create new blocks with new hashes)

(I-1d) Acknowledgment integrity (an upload is confirmed iff verified):
   Ack(commit(M)) ⇒ ∀c ∈ M.chunks : ∃b ∈ B : h(b)=c.hash ∧ |bytes(b)|=c.size ∧ c.offset consistent

(I-1e) Manifest closure:
   ∀v ∈ Versions : Assemble(v.manifest) has length v.total_size_bytes
                   ∧ v.content_sha256 = SHA-256(Assemble(v.manifest))
```

#### Enforcement Points

| EP | Location | Mechanism |
|----|----------|-----------|
| EP-1 | Client SDK (Rust `fastcdc` crate) | Every chunk hashed with SHA-256 (`sha2`) *before* transit; manifest carries `{hash, offset, size}` triples. |
| EP-2 | Ingress `HandleInitiate` | Declared hashes checked against `cas_blocks` — existing hashes never re-transferred (dedup gate is also an integrity gate: only verified bytes exist under that hash). |
| EP-3 | Object store PUT | Pre-signed PUT requires Content-MD5/ETag match; mismatched bytes physically cannot land under a hash key. |
| EP-4 | Ingress `HandleCommit` | Runs inside a single ACID tx: `INSERT cas_blocks ... ON CONFLICT DO UPDATE ref_count+1` + manifest FK constraints. Commit acknowledged (201) only when all checksums reconcile with the storage manifest. |
| EP-5 | Schema | `cas_blocks.block_hash BYTEA PRIMARY KEY` (address = identity); `file_manifest_blocks PK(version_id, chunk_index)` with FK → ordered Merkle manifest cannot dangle. |
| EP-6 | Retrieval path | Assembled file optionally re-hashed end-to-end against `file_versions.content_sha256` before delivery completes. |
| EP-7 | At-rest scrubber (background) | Periodically re-hashes sampled stored objects vs their keys; mismatches quarantined and repaired from RS parity. |

#### Violation Scenarios & Consequences

| Scenario | If Unenforced | With Enforcement |
|----------|---------------|------------------|
| Corrupted in-transit chunk | Permanent silent corruption addressed as valid data | ETag/hash mismatch at EP-3/EP-4 → 400 rejection; client retries chunk |
| Hash collision (SHA-256) | Two distinct contents alias one key | Accepted risk (~2⁻¹²⁸ collision probability); documented policy: identical address ≡ identical content. Detection practically impossible; mitigation is algorithm strength itself |
| Bitrot at rest | Read-back violates I-1b undetected | Scrubber (EP-7) detects, quarantines, repairs from parity |
| Partial commit visibility | Readers see version referencing missing blocks | Single-transaction commit (EP-4): version row invisible until manifest complete |
| Double-counted ref_count → premature GC | Live block deleted while referenced | Upsert is atomic; concurrent-commit race tests (§5.1 testing) prove monotone correctness |

#### Testing Strategy

| Test Type | Scenario |
|-----------|----------|
| Property (proptest) | `chunk(data)` deterministic: same input → identical chunk stream, any run/platform |
| Round-trip fuzz | For random inputs ≤10MB: `assemble(manifest(chunk(f))) == f` bit-exact |
| Corruption injection | Flip bit in uploaded chunk → expect reject; flip byte in stored object → expect scrubber quarantine event |
| Race test (`go test -race`) | N concurrent commits sharing k overlapping hashes → final ref_counts exact; no lost increments |
| End-to-end | Upload → verify 201 → download → compare SHA-256 vs original; repeat with injected network corruption |
| Soak | 1-hour fuzz: no crash, no invariant breach logged |

---

### 5.2 Invariant I-2: Acyclic Namespace

#### Formal Definition (First-Order Logic)

Model namespace per tenant as directed graph `N_t = (V_t, E_t)`, edge `(p,c)` means p is parent of c.

```
(I-2a) Acyclicity:
   ∀v ∈ V_t : ¬(v →⁺ v)          where →⁺ is transitive closure

(I-2b) Rootedness & single parenthood:
   ∃!r ∈ V_t : lineage(r) = ⟨r⟩  ∧
   ∀v ≠ r : ∃!p : (p,v) ∈ E_t  ∧  lineage(v) = lineage(p) ++ ⟨v⟩

(I-2c) Sibling-name uniqueness (live nodes):
   ∀p, name : |{c : parent(c)=p ∧ name(c)=name ∧ ¬is_deleted(c)}| ≤ 1

(I-2d) Move legality predicate:
   LegalMove(v, p') ⇔ p' type=DIRECTORY
                     ∧ sameTenant(v, p')
                     ∧ ¬(p' →⁺ v)          ← cycle rejection
                     ∧ name(v) free under p'

(I-2e) Lineage consistency (materialized path truth):
   ∀v : lineage_path(v) = lineage_path(parent(v)) ++ munge(id(v))
   (maintained for entire subtree after every move; acl_epoch bumped to invalidate caches)
```

#### Enforcement Points

| EP | Location | Mechanism |
|----|----------|-----------|
| EP-1 | Schema | `parent_id UUID REFERENCES namespace_nodes(node_id)` self-FK; `CONSTRAINT uq_parent_name UNIQUE (tenant_id, parent_id, name, is_deleted)` enforces I-2c at storage layer |
| EP-2 | Schema | `lineage_path LTREE NOT NULL` + `GIST` index — ancestor/descendant tests are index-supported, O(1)-per-node structural checks, no recursive traversal |
| EP-3 | `move_directory()` stored procedure | Pessimistic locking: `SELECT … FOR UPDATE` on source node + `FOR SHARE` on destination; explicit guard `IF v_dst_path <@ v_src_path THEN RAISE EXCEPTION 'Cyclic hierarchy violation'` enforces I-2d |
| EP-4 | `move_directory()` single UPDATE | Atomic subtree remap: `lineage_path = v_dst_path ‖ src_seg ‖ subpath(...)` for all `WHERE lineage_path <@ v_src_path`; simultaneously bumps `acl_epoch += 1` (cache invalidation) |
| EP-5 | Application layer | All parent mutations routed through the procedure; ad-hoc `UPDATE … SET parent_id` forbidden by review policy + least-privilege DB roles |

#### Violation Scenarios & Consequences

| Scenario | If Unenforced | With Enforcement |
|----------|---------------|------------------|
| Move `/a/b` into `/a/b/c` | Cycle → infinite traversals, unreachable subtree, broken recursion everywhere | EP-3 raises exception; tx rollback; client receives error; zero side effects |
| Concurrent moves into same dest w/ same name | Duplicate children, ambiguous resolution | EP-1 unique constraint: second tx aborts → 409 Conflict; client retries |
| Concurrent move + rename of ancestor | Lost update, inconsistent lineage | Row locks serialize ops; worst case deadlock-free ordering (source→destination lock order), retry succeeds |
| Stale cache after move | Permission check against old path | acl_epoch bump (EP-4) invalidates epoch-stamped caches; stale reads rejected |
| Broken lineage (path ≠ parent chain) | Subtree queries silently wrong | Consistency auditor job asserts I-2e continuously; drift alerts + auto-quarantine |

#### Testing Strategy

| Test Type | Scenario |
|-----------|----------|
| Unit SQL | Self-move, descendant-move, cross-tenant move, rename-collision — each must raise expected exception |
| Concurrency | Two sessions racing moves/rename on shared subtree: serialized outcome, no deadlock, both clients get deterministic result |
| Property fuzz | Random legal/illegal move sequences; after each op assert: acyclicity, lineage consistency (I-2e holds for every node), subtree size preserved |
| Thundering herd sim | Move dir containing 250k entries → assert single-row-scope update plan (EXPLAIN), single `NODE_MOVED` event emitted, no child re-download required by clients |
| Performance | EXPLAIN ANALYZE: subtree select + move < 5ms p95 using GIST index |

---

### 5.3 Invariant I-3: Cryptographic Ingress

#### Formal Definition (First-Order Logic + Pseudocode)

Let `K_t` = tenant t's current KMS key (versioned), `Token(t,h,ts,ep,n) := HMAC-SHA256(K_t, t‖h‖ts‖ep‖n)`.

```
(I-3a) Validity predicate:
   Valid(R) ⇔ R.sig = Token(R.t, R.h, R.ts, R.ep, R.n)     [constant-time compare]
             ∧ |now − R.ts| ≤ T_TTL                         [T_TTL ≤ 900s]
             ∧ R.n ∉ UsedNonces                             [single-use]
             ∧ scope(R.t, R.h) matches issuing session      [tenancy binding]

(I-3b) Edge admission guarantee:
   ∀byte-stream X written to CAS : ∃R preceding X : Valid(R) ∧ addr(X)=R.h

(I-3c) Unforgeability (computational):
   Without K_t, P(forge Valid sig) ≤ negl(λ=SHA-256/HMAC security)

(I-3d) Non-replay:
   ∀R : |{executions}| ≤ 1        enforced by nonce registration with TTL = T_TTL
```

Token construction pseudocode:

```go
sig  := hex(HMAC_SHA256(kmsKey(tenantID),
          fmt.Sprintf("%s:%s:%d:%s:%s", tenantID, blockHash, unixTS, endpointID, nonce)))
url  := "https://blob-storage.internal/chunks/" + blockHash +
        "?tenant=" + tenantID + "&ts=" + ts + "&nonce=" + n + "&kv=" + keyVersion + "&sig=" + sig
```

#### Enforcement Points

| EP | Location | Mechanism |
|----|----------|-----------|
| EP-1 | Ingress issuance | Tokens minted only after tenant authn (JWT) + quota pass; message binds tenant+hash+time+endpoint+nonce — nothing else validates |
| EP-2 | Edge PoP validation (data plane) | Recomputes HMAC from cached tenant key, `hmac.Equal` constant-time compare, TTL window check, Redis nonce SETNX — all *before* any byte is proxied. Failure = 401, zero backend cost |
| EP-3 | TTL bound | Max 15 minutes validity; stolen URL usefulness bounded |
| EP-4 | Anti-replay | Nonce recorded in Redis with TTL=T_TTL; second use rejected |
| EP-5 | Key lifecycle | Versioned keys (`kv` param), 90-day rotation, 7-day dual-accept overlap; compromised version revocable independently |
| EP-6 | Defense in depth | Bucket lifecycle aborts incomplete uploads @24h; GC 7-day orphan window bounds damage even if a forged write somehow landed |
| EP-7 | Constant-time discipline | No variable-time string comparison anywhere in signature verification paths |

#### Violation Scenarios & Consequences

| Scenario | If Unenforced | With Enforcement |
|----------|---------------|------------------|
| Tampered signature | Unauthorized write | 401 at edge; request never reaches backend |
| Replay within TTL | Same bytes written repeatedly (DoS/cost) | Nonce collision → 401 |
| Clock-skew forgery extension | Expired tokens revived | Strict |now − ts| ≤ T_TTL; NTP-monitored PoPs; skew tolerance documented and tested |
| Cross-tenant reuse (A's token → B's target) | Tenancy breach | Signature binds tenant context; recomputation under B's scope fails → 401 |
| Stolen KMS material | Mass forge capability | Rotation revokes old version; overlap window caps blast radius ≤7 days; audit log identifies abuse window |
| Timing side-channel | Progressive signature recovery | Constant-time compare (EP-7) makes statistical recovery infeasible |

#### Testing Strategy

| Test Type | Scenario |
|-----------|----------|
| Fuzz (1h+) | Mutate each field independently (sig, ts, nonce, tenant, hash, kv): every permutation must yield 401 |
| Matrix | Full cross-product: {valid tokens × all tenants} × {all endpoints} — only diagonal accepted |
| Replay | Capture valid request, replay verbatim → 401; replay with fresh nonce but same sig → 401 |
| Rotation drill | Rotate key mid-flight: old-version tokens accepted during overlap window then hard-fail; no acceptance gap |
| Timing | Statistical distribution test over comparison durations (constant-time assertion harness) |
| Expiry probe | Synthetic requests at T_TTL−ε / T_TTL+ε boundaries — clean cut, no grace ambiguity |

---

## 6. Data Flow Paths

Every flow is numbered for traceability; each step names its plane.

### Flow U — Upload Initiation (hot path, budget: ≤50ms p95)

```
U1  Client        [SDK]     FastCDC-chunks file locally; SHA-256 each chunk        [I-1 EP-1]
U2  Client→Edge   [C]       POST /initiate {tenant_id, total_size, hashes[]}       (JWT)
U3  Edge→Ingress  [C]       Validate JWT; forward w/ identity claims
U4  Ingress→PG    [C]       Quota check: used_bytes + total_size ≤ quota           → 402 if exceeded
U5  Ingress→PG    [C]       Batch: SELECT existing of {hashes} FROM cas_blocks     (≤1000/batch)
                            [optional Redis Bloom pre-filter first]
U6  Ingress       [C]       missing := hashes − existing                           ← dedup savings
U7  Ingress→KMS   [X]       Mint HMAC token per missing hash                       [I-3 EP-1]
U8  Ingress→PG    [C]       INSERT upload_session (expires +24h)
U9  Ingress→Edge→Client [C] 200 {session_id, missing_hashes, upload_urls{}}
```

### Flow T — Chunk Transfer (data plane, throughput-bound)

```
T1  Client→EdgePoP [D]      PUT chunk bytes → /chunks/{hash}?tenant&ts&nonce&sig
T2  EdgePoP        [D]      Validate: constant-time HMAC + TTL + nonce SETNX       [I-3 EP-2..4] → 401 on any failure
T3  EdgePoP→CAS    [D]      Proxy bytes; CAS enforces ETag == hash-key match       [I-1 EP-3]
T4  CAS            [D]      201 stored (erasure-coded across 12 shards)
T5  Parallelize             Steps T1–T4 ×N chunks, ≤10 concurrent per client
```

### Flow C — Commit (hot path, budget: ≤45ms p95 / ≤85ms p99)

```
C1  Client→Ingress [C]      POST /commit {session_id, node_id, content_hash, chunks[]}
C2  Ingress        [C]      BEGIN TX (READ COMMITTED)
C3  Ingress→PG     [C]      SELECT session FOR UPDATE → 410 Gone if expired
C4  Ingress→PG     [C]      Ownership check: session.tenant == req.tenant → 403 otherwise
C5  Ingress→PG     [C]      next_version := MAX(version_number)+1 WHERE node_id    [fenced]
C6  Ingress→PG     [C]      INSERT file_versions(version_id, …, content_sha256)    [I-1 EP-4]
C7  Ingress→PG     [C]      ∀chunk: UPSERT cas_blocks(ref_count+1)                 atomic dedup accounting
C8  Ingress→PG     [C]      INSERT file_manifest_blocks(version_id, idx, hash, off, size)
C9  Ingress→PG     [C]      UPDATE upload_session SET is_completed = TRUE
C10 Ingress→Bus    [A]      Publish VERSION_COMMITTED fact (after-commit hook)     [B-4]
C11 Ingress        [C]      COMMIT → 201 {"status":"COMMITTED","version_id"}       ack ⟺ I-1d satisfied
```

### Flow R — Retrieval (read path)

```
R1  Client→Edge→Ingress [C] GET /files/{node_id}/versions/{n} (JWT)
R2  Ingress→PG     [C]      Resolve manifest: ordered (block_hash, offset, size) rows
                            [+ optional derivation_results join for thumbnails/text]
R3  Ingress        [C]      Mint short-lived HMAC GET tokens per block
R4  Client→CAS     [D]      Parallel ranged GETs via signed URLs (edge-validated)
R5  Client         [SDK]    Reassemble by offset; verify SHA-256 == content_sha256  [I-1 EP-6]
```

### Flow M — Directory Move (metadata-only; no bytes touched)

```
M1  Client→Ingress [C]      MOVE /nodes/{id} → {new_parent_id}
M2  Ingress→PG     [C]      CALL move_directory(tenant, node, new_parent)
M3  PG             [C]      Lock src FOR UPDATE + dst FOR SHARE                    [I-2 EP-3]
M4  PG             [C]      Guard: dst.path <@ src.path → RAISE (cycle) → 409
M5  PG             [C]      Single UPDATE remaps subtree lineage_paths; acl_epoch++ [I-2 EP-4]
M6  Bus→Clients    [A]      NODE_MOVED event → clients patch local path mapping,
                            no child re-fetch (thundering-herd mitigation)
```

### Flow G — Garbage Collection (async, conservative)

```
G1  GC→PG hourly   [A]      Expire upload_sessions past expires_at, incomplete     (soft mark)
G2  GC→PG daily    [A]      Detect: ref_count=0 ∧ age>7d, batched (≤1000/tx)       7-day safety window
G3  GC             [A]      DOUBLE-CHECK each candidate still ref_count=0          race protection
G4  GC→Bus         [A]      Emit tombstones → topic cas-tombstones                 + audit trail row
G5  GC→PG          [A]      Remove registry rows (post-publish; idempotent)
G6  Consumer→CAS   [D]      Consume tombstones → DELETE objects (rate-limited)     failures retry non-fatally
```

### Flow V — Derivation (async, fully out-of-band per B-4/B-5)

```
V1  Bus→Worker     [A]      Consume VERSION_COMMITTED (offset committed post-success)
V2  Worker→CAS     [D]      GET chunks (scoped READ-ONLY creds)                    [I-3-style scoping]
V3  Worker         [A]      Process: ClamAV <60s · OCR <120s · FFmpeg <300s · embed
V4  Worker→PG      [A]      UPSERT derivation_results (idempotent)                 [only allowed write]
V5  Worker→DLQ     [A]      Permanent failures after backoff ladder (1→16s, ×N)
```

---

## 7. Integration Contracts

Each contract states: protocol, payload shape, guarantees, error semantics, and SLO. Contracts are binding between component owners.

### IC-1: Client ↔ Edge Layer

| Field | Contract |
|---|---|
| Protocol | HTTPS / HTTP-3 (QUIC), TLS 1.3 only |
| Authn | Control calls: `Authorization: Bearer <JWT>` (tenant-scoped). Data calls: query-string HMAC token (IC-4 format) |
| Payloads | `InitiateRequest{tenant_id, total_size, hashes[string]}` → `InitiateResponse{session_id, missing_hashes[], upload_urls{hash→url}}`; `CommitRequest{session_id, node_id, tenant_id, total_size, content_hash, chunks[{index, block_hash, offset, size}]}` → `201 {status:"COMMITTED", version_id}` |
| Guarantees | Invalid data-plane tokens rejected at edge (zero backend reach); rate limit 1000 RPS/tenant → `429`; request bodies >100MB rejected pre-parse |
| Error semantics | 400 malformed · 401 bad/expired/replayed token · 402 quota exceeded · 403 tenancy violation · 404 unknown node · 409 conflict (move/name/version race) · 410 expired session · 429 throttled · 503 circuit-open |
| SLO | Edge overhead ≤10ms p95 added to any proxied call |
| Failure handling | PoP unreachable → client fails over to next Anycast PoP transparently; edge emits structured access logs for audit |

### IC-2: Edge ↔ Ingress Engine

| Field | Contract |
|---|---|
| Protocol | Internal mTLS HTTP/2; service mesh optional |
| Identity | Forwarded claims header set by edge after JWT validation (ingress trusts edge network position + mTLS, does not re-parse JWT) |
| Guarantees | Ingress remains stateless & horizontally scalable; edge applies circuit breaker: ingress p95 >100ms → open → `503` fast-fail at edge |
| Health | `GET /healthz` (liveness), `GET /ready` (readiness gates pool connectivity) |
| Error semantics | Ingress errors mapped to IC-1 codes; internal errors never leak stack traces cross-boundary |
| SLO | Edge→ingress hop adds ≤5ms p95 intra-region |

### IC-3: Ingress ↔ Metadata Store

| Field | Contract |
|---|---|
| Driver/Protocol | pgx/v5 connection pools: separate WRITE (small, ordered) and READ (larger, replica-balanced) pools; ANALYTICAL isolated pool |
| Isolation | Commits: READ COMMITTED + `FOR UPDATE` row locks on session rows; moves: procedure-managed pessimistic locks |
| Operations (stable surface) | `GetExistingBlocks(tenant, hashes[≤1000])` · `CreateUploadSession(...)` · `NextVersionNumber(node)` · `CommitVersionTx(...)` single-tx composite (steps C6–C9 indivisible) · `MoveDirectory(...)` via stored proc |
| Guarantees | Parameterized prepared statements only (no string-built SQL); idempotent-safe upserts permit at-least-once retry; pool wait <100ms p95; query <5ms p95; failover RTO<30s, RPO=0 |
| Error semantics | Transient (connection, serialization) → exponential backoff retry; constraint violation → mapped 409; exhaustion → 503 + circuit-break signal to edge |
| Observability contract | Every op emits: duration histogram (p50/p95/p99), pool saturation gauge, slow-query log >50ms, per-op error counter — labels include `op`, never tenant data values |

### IC-4: Control ↔ CAS (signed-channel linkage)

| Field | Contract |
|---|---|
| Relationship | Registry row `cas_blocks.block_hash` ⇔ object key `chunks/{hash}` in bucket. Logical FK via `file_manifest_blocks`; **no runtime coupling** (rule B-2) |
| Signed channel format | `https://{blob-endpoint}/chunks/{block_hash}?tenant={tid}&ts={unix}&nonce={n}&kv={keyver}&sig={hex-hmac-sha256}` — message = `tid:block_hash:ts:endpoint:nonce` |
| Guarantees | URL TTL ≤15 min; PUT verified via ETag==hash before block counts as present; GET tokens equally scoped & expiring |
| Tiering | Registry `storage_tier` field drives lifecycle: HOT↔WARM↔COLD transitions automated by policy (ref_count popularity + age) |
| Error semantics | Edge rejects: 401 (token) — CAS itself rejects: 400 (ETag mismatch), 404 (unknown key on GET) |
| Note | Stack addendum adjustment adopted here: CAS registry primary key is globally unique by hash (tenancy isolation enforced at manifest/token layers) to maximize cross-tenant dedup savings |

### IC-5: CAS ↔ Derivation Workers

| Field | Contract |
|---|---|
| Access model | Workers pull chunks directly from CAS using scoped READ-ONLY service credentials (never through ingress, never via user tokens) |
| Guarantees | Read-only: workers hold no write permission on buckets; immutability of addressed blocks means no TOCTOU on content |
| Write-back surface | Exclusively `derivation_results(result_id, version_id, worker_name, status, result_data JSONB, created_at)` + quarantine flag on INFECTED verdicts |
| Idempotency | Result upsert keyed `(version_id, worker_name)` — redelivery safe |
| Error semantics | Missing chunk (GC'd mid-flight — prevented by 7-day window, but handled) → transient retry; malformed content → permanent → DLQ |

### IC-6: Derivation Workers ↔ Event Bus

| Field | Contract |
|---|---|
| Topics | `file_versions` — fact: `VERSION_COMMITTED {event_id, version_id, node_id, tenant_id, file_size_bytes, mime_type, created_at, chunks[]}`. `cas-tombstones` — fact: `CASBlockDeletion {block_hash, tenant_id, size_bytes, timestamp}` |
| Consumption | Consumer groups per worker class; partition key = `version_id` (ordering per file); manual offset commit **only after successful processing** |
| Retry policy | Transient errors: exponential backoff 1→2→4→8→16s, N attempts → DLQ; poison messages never block partitions |
| Guarantees | At-least-once delivery + idempotent consumers = effectively-once effects; broker replication factor ≥3; tiered retention enables temporal replay for worker rebuilds |
| Lag SLO | Consumer lag alerted at threshold; sustained lag breaches worker-scaling playbook |

### Secondary contracts (registered, abbreviated)

| Pair | Contract gist |
|------|--------------|
| Ingress ↔ Redis | Nonce SETNX (TTL=T_TTL) · Bloom existence pre-filter (rebuild 1h) · session fast-index · fencing tokens. Loss = degradation only (B: derivable state) |
| GC ↔ Metadata | Batched sweeps (≤1000/tx), off-peak scheduling, dry-run flag, audit-trail append per candidate |
| All ↔ KMS | Versioned key fetch, cached ≤TTL at consumers; rotation = publish new version + overlap window; revocation propagates within overlap bound |

---

## 8. Dependency Direction Rules

```
                    ┌──────────┐
                    │  CLIENT  │
                    └────┬─────┘
                         ▼
                    ┌──────────┐
                    │   EDGE   │
                    └────┬─────┘
                         ▼
                  ┌─────────────┐
                  │   INGRESS   │
                  └─┬────┬────┬─┘
        produce     │    │    │
        ┌───────────┘    │    └────────────┐
        ▼                ▼                 ▼
  ┌──────────┐    ┌──────────┐      ┌───────────┐
  │ EVENT BUS│    │ METADATA │      │  REDIS    │
  └────┬─────┘    │  STORE   │      └───────────┘
       │ consume  └─┬──────┬─┘            ▲
       ▼            │      │              │ cache-fill
 ┌───────────┐      │      └──────┐       │
 │  WORKERS  ├──────┘        (results)    │
 └────┬──────┘ reads                       │
      │ (READ-ONLY)                 ┌──────┴─────┐
      ▼                             │    KMS     │
 ┌──────────┐  tombstone-deletes    │ (consumers)│
 │   CAS    │◄──────────────────────┴────────────┘
 └──────────┘   GC pipeline drives deletes
```

**Binding direction rules (acyclicity proof by construction):**

| Allowed | Forbidden |
|---|---|
| Client → Edge → Ingress | Ingress → Edge (unsolicited) |
| Ingress → {Metadata, Redis, Bus, KMS} | Metadata → Ingress |
| Bus → Workers (consume) | Workers → Ingress / Edge |
| Workers → {Metadata(results), CAS(read)} | CAS → any component (pure sink/source) |
| GC → {Metadata(sweep), Bus(tombstones)}; Consumers → CAS(delete) | Bus → Ingress |
| Any → KMS (pull, cached) | KMS push into hot path |

No cycle exists: every arrow terminates in either a leaf store (CAS), a pure consumer (workers/GC-delete), or pull-only infrastructure (KMS). The bus is the sole async intermediary — nothing consumes-and-calls-back-upstream.

---

## 9. Failure Mode Register

| Component | Failure | Detection | Automated Mitigation | Residual Risk |
|---|---|---|---|---|
| Client SDK | Crash mid-upload | Session file absence | Resume replays unconfirmed chunks only; 24h server expiry bounds orphans | User confusion → documented UX |
| Edge PoP | PoP outage / DDoS | Anycast health probes | BGP re-route; Cloudflare absorb; rate limiting per tenant | Regional ISP path issues → multi-PoP diversity |
| Ingress pod | Kill mid-request | K8s liveness | Tx rollback (atomic); HPA replaces; client retry idempotent | None beyond latency blip |
| Metadata primary | Hardware/process loss | Health-check miss | Sync standby promotion <30s (RTO); RPO=0 | Split-brain → fencing via epoch/quorum |
| Metadata logic corruption | Bad deploy writes bad rows | Consistency auditor (I-2e checker), CI gates | PITR restore; deploy rollback | Window between corruption & detection → continuous audits shrink it |
| Redis cluster | Flush / node loss | Saturation + hit-ratio alarms | Rebuild from PG; nonce loss worst-case = replay window until TTL | Brief replay exposure ≤15min, bounded by I-3 TTL anyway |
| CAS node/disk | Shard loss | Scrubber + array telemetry | RS 8+4 reconstructs (any 4-of-12 lost OK) | >4 concurrent shard losses/stripe → durability event; multi-region replication covers |
| Bitrot | Silent media decay | Hash-vs-key scrub sampling | Quarantine + parity repair | Sample gap → configurable scrub frequency |
| Event bus | Broker loss / lag | ISR + consumer-lag metrics | Replicated ISR; scale consumers; tiered replay | Sustained overload → derivation SLA breach alert (§10) triggers scaling |
| Derivation worker | Crash loop / poison msg | Restart count + DLQ depth | Rebalance; DLQ isolation; idempotent upserts | DLQ backlog needs ops runbook |
| GC | Bug deletes live block | Deletion-rate anomaly alarm + audit reconciliation | Halt switch (dry-run default on new rules); object versioning restore | Worst case bounded by audit trail + versioned bucket |
| KMS | Unavailability | Circuit metrics | Cached keys serve within TTL; issuance degraded gracefully | Cold region cold-cache → provisioning runbook |
| Cascade | Ingest flood → worker backlog → disk pressure | Composite dashboards | Load shedding at edge; independent plane scaling (the architecture's core purpose) | Correlated cloud-zone failure → multi-AZ/multi-region posture |

---

## 10. SLA Matrix

Verification cadence: **continuous** (Prometheus scrape, 15s interval) · **weekly** SLA compliance report · **quarterly** DR drill. Breach definition: SLO burn-rate alert fires per multiwindow rules.

| # | Guarantee | Target | Responsible Component(s) | Measurement / Verification | Breach Fallback Behavior |
|---|-----------|--------|--------------------------|----------------------------|--------------------------|
| 1 | **Durability** | 99.999999999% (11 nines) | CAS Blob Store (8+4 RS, multi-region) | Annualized FR modeling + scrub-audit logs + quarterly fault-injection (shard-loss drills) | Parity reconstruction; regional failover; incident postmortem gate |
| 2 | **Commit-manifest latency** | p95 <45ms, p99 <85ms | Ingress Engine + Metadata Store | Prometheus histograms on C-flow; weekly report | Auto-scale ingress HPA; pool tuning playbook; shed load at edge |
| 3 | **Upload-initiate latency** | p95 <50ms @1000 concurrent | Ingress + Redis (Bloom path) | Load-test suite (k6/vegeta) weekly + prod histograms | Degrade to DB-only existence checks; scale replicas |
| 4 | **Aggregate throughput** | >1GB/s sustained | Data plane (PoPs + CAS) | Synthetic bulk benchmarks + prod byte counters | Add PoP capacity; parallel-chunk fan-out increase |
| 5 | **Token TTL** | ≤15 minutes, hard cap | Edge + KMS | Synthetic boundary probes (TTL±ε) every minute | Immediate key-version revocation; overlap window closes |
| 6 | **Dedup effectiveness** | >50% better than fixed-size | CDC params + CAS registry | Dedup-ratio gauge: logical ÷ physical bytes; benchmark suite | Retune Gear masks/chunk targets; report to review board |
| 7 | **RPO** | 0 seconds (zero loss) | Metadata sync consensus + CAS replication | Failover drills counting committed-but-unreplicated txns (=0) | PITR restore + freeze writes; RCA mandatory |
| 8 | **RTO** | <30 seconds | LB failover + PG standby promotion | Chaos drills: kill primary, time-to-served | Multi-AZ rebalance; escalate to multi-region active-active |
| 9 | **Control-plane availability** | 99.99% monthly | Edge + Ingress fleet | SLO burn-rate alerting on 5xx/latency composite | Load shedding; capacity surge; regional evacuation |
| 10 | **Derivation freshness** | ClamAV <60s · OCR <120s · FFmpeg <300s | Worker Fleet + Bus | Event→result timestamp deltas; consumer-lag alarms | Scale worker pools; partition re-sharding; DLQ triage |
| 11 | **Rate-limit fairness** | 1000 RPS/tenant enforced | Edge | Per-tenant counters; synthetic over-limit probes | Escalating throttle; abusive-tenant quarantine |
| 12 | **GC safety** | Zero live-reference deletions | GC pipeline + audit trail | Daily reconciliation: manifest-referenced hashes ⊆ live objects; halt-switch readiness | Freeze GC; restore via versioned bucket + audit forensics |
| 13 | **Query latency (DB)** | <5ms p95 localized | Metadata Store (+indexes) | EXPLAIN ANALYZE regression suite in CI + pg_stat_statements | Index redesign; replica routing; cache warming |

**SLA ownership:** each row has a named component owner in the ops matrix; weekly review inspects burn rates; two consecutive weekly breaches trigger the phase-gate escalation defined in the Implementation Roadmap.

---

## 11. Acceptance Criteria Self-Validation

| Acceptance Criterion (from Prompt 1.1) | Where Satisfied |
|---|---|
| Detailed architecture diagram showing dual-plane separation | §2 topology + legend |
| Every component + responsibilities | §4 Component Catalog (responsibility / owns / does-NOT / failure mode per component) |
| Three invariants + enforcement shown | §5 (each invariant: FOL definition, enforcement-point tables mapping to schema/code) |
| Data flows: upload, commit, retrieval, GC | §6 Flows U, C, R, G (+ M move, V derivation, T transfer for completeness) |
| Cross-plane communication boundaries | §3.2 hard rules B-1…B-6 + §3.3 crossing manifest |
| Invariants: formal definition (FOL/pseudocode) | §5.1–5.3 formal blocks (I-1a…I-1e, I-2a…I-2e, I-3a…I-3d + token pseudocode) |
| Invariants: enforcement in code/schema | Enforcement-point tables cite exact schema constraints, procedures, and code paths |
| Invariants: failure scenarios + consequences | Violation-scenario tables (unenforced vs enforced consequence columns) |
| Invariants: testing strategies | Testing-strategy tables (property/fuzz/race/injection/rotation drills) |
| All six integration points documented | §7 IC-1…IC-6 (+ secondary contracts) |
| Explicit integration contract | Each IC fixes protocol, payload schema, guarantees, error semantics, SLO, failure handling |
| SLA matrix: guarantees, responsible component, measurement, fallback | §10 rows 1–13 |
| **Single responsibility per component** | §4 "Does NOT" clauses are binding scope fences |
| **No circular dependencies between planes** | §8 direction table + leaf/pull-only termination argument (acyclic by construction) |
| **All data flows acyclic and traceable** | §6 numbered steps; dependency arrows terminate in sinks (§8) |
| **Failure modes documented for every component** | §4 per-component failure modes + §9 consolidated register |
| **Integration contract explicit** | §7 contract tables |

---

*End of Architectural Validation Document — ready for Architecture Review Board execution gate.*
