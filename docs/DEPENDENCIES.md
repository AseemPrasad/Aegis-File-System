# Project Aegis: Dependency Selection & Justification Register

| | |
|---|---|
| **Prompt** | 1.2 — Development Environment & Dependency Selection |
| **Companion files** | `Cargo.toml` (+ `crates/fastcdc/Cargo.toml`), `go.mod`, `deploy/**` |
| **Acceptance link** | "All dependencies have documented justifications" · "No dependency contradicts the design invariants" |

---

## 1. Pinning Strategy

### 1.1 Rust

| Mechanism | Policy |
|---|---|
| Manifest ranges | Caret within minor (`"1.39"` ≡ `^1.39`) — permits patch/minor fixes during development |
| Determinism | **`Cargo.lock` is committed**; every build (local, CI, image) resolves from it |
| Upgrades | Renovate opens weekly grouped PRs; humans review changelogs, not diff noise |
| Supply chain | `cargo-deny` enforces: RustSEC advisories = deny, unknown licenses = deny, duplicate versions = warn |
| Toolchain | `rust-toolchain.toml` pins channel=stable with components; MSRV floor `rust-version = "1.79"` asserted by oldest-supported build |
| Security cadence | cargo-audit/cargo-deny run on every PR **and** nightly (advisory DB moves without code moving) |

### 1.2 Go

| Mechanism | Policy |
|---|---|
| Manifest | Exact patch versions pinned in `go.mod` (`v5.7.1`, never `latest`) |
| Verification | `go.sum` committed as soon as service code imports modules (PROMPT 2.2+); CI fails if `go mod tidy` produces a diff — drift cannot merge |
| Vulnerabilities | `govulncheck` (call-graph-aware, not just version-matching) on PRs + nightly |
| Toolchain | `go 1.23` directive; setup-go reads `go-version-file` so local == CI == image builder |
| MVS discipline | No `replace` directives outside a temporary security cherry-pick (must carry expiry comment) |

### 1.3 Containers & Infrastructure

| Artifact | Policy |
|---|---|
| Base images | Distroless/static only for runtime; digest-pinned in release pipelines, tag-pinned during development |
| Compose stack | Tag-pinned images (e.g. `postgres:16-alpine`, `redpandadata/redpanda:v24.2.7`) — bump via Renovate PR only |
| Terraform providers | Pessimistic constraint (`~> 5.70`) + `.terraform.lock.hcl` committed |
| Helm charts | Chart version pinned in the install command documented in `values-redpanda.yaml` |

---

## 2. Rust Dependencies — Justifications

### tokio `1.39` — async runtime
- **Why over alternatives:** async-std is in maintenance decline; actix-rt couples you to its actor framework; smol's ecosystem is thin for high-throughput socket IO. Tokio's work-stealing multi-thread scheduler is the industry default with the deepest ecosystem (hyper → tonic, tower middleware).
- **Performance impact:** zero-copy `AsyncReadExt`/`AsyncWriteExt` buffering; cooperative budgeting prevents a single streaming upload from starving others — directly protects SLA rows 3–4.
- **Security:** continuously fuzzed, RUSTSEC-monitored, enormous audit surface.
- **Invariant link:** I-1 (streaming chunk pipeline), I-3 (non-blocking token issuance).
- **Note:** features are enumerated explicitly, not `"full"` — leaner compile graph, smaller attack surface.

### sha2 `0.10` — SHA-256 (address identity)
- **Why over alternatives:** `ring` is excellent but carries C sources and heavier cross-compile story; `openssl-sys` drags a C toolchain and CVE surface into every build host. RustCrypto's `sha2` is pure Rust, `no_std`-capable, property-tested by the RustCrypto project.
- **Performance impact:** ~1–2 GB/s/core scalar baseline; assembly acceleration via CPU feature detection. The PROMPT 3.1 benchmark suite measures against exactly this floor.
- **Invariant link:** I-1a/I-1b — address = SHA-256(bytes) is the entire CAS identity model.

### serde + serde_json `1.0` — serialization
- **Why over alternatives:** no serious competitor for derive-based schema serialization in Rust; manual encoding was rejected as correctness risk with zero payoff at our scale.
- **Performance:** zero-copy deserialization available (`#[serde(borrow)]`) where hot paths demand it later.
- **Usage boundary:** client↔control-plane JSON only (IC-1). Inter-service uses protobuf (below); chunk bytes are never serialized by any library.

### tonic `0.12` + prost `0.13` — gRPC
- **Why over alternatives:** `grpc-rs` wraps C core (cmake, protobuf-sys) complicating hermetic builds; `tarpc` isn't wire-compatible gRPC. tonic/prost are pure-Rust, tower-integrated.
- **Performance impact:** binary framing ≫ JSON for worker control channel (PROMPT 7.1); generated types are plain structs with cheap clones.
- **Invariant link:** B-4 (one-way event flow needs efficient fan-out transport).

### uuid `1.10`
- **Why:** smallest audited implementation; v4 (random) standardizes IDs with the database's `gen_random_uuid()` defaults so application and schema agree on identity semantics. v7 enabled for future time-ordered identifiers (event IDs) without a second crate.

### tracing + tracing-subscriber `0.3`
- **Why over alternatives:** `log`+`env_logger` lacks span context (tenant/session correlation across await points is mandatory per IC-3 observability contract); `slog` is effectively superseded. JSON subscriber output matches the structured-logging contract consumed by Grafana/Loki.
- **Performance impact:** spans compile to no-ops when their level is disabled — hot path stays clean.

### thiserror `2.0` / anyhow `1.0`
- **Why:** thiserror for typed library errors (`ChunkError`) crossing crate boundaries; anyhow for binaries' context-wrapping. Both are zero-cost at runtime relative to hand-rolled error enums.

### Dev/test-only
| Crate | Purpose | Note |
|---|---|---|
| criterion `0.5` | Benchmark harness feeding the CI perf-gate | HTML reports; baselines stored via critcmp workflow |
| proptest `1.5` | Property tests mandated by I-1/I-2 testing strategies | determinism, cycle soundness |
| rand `0.8` | Synthetic corpus generation in benches/tests | dev-dependency only; prod code must be deterministic |

---

## 3. Go Dependencies — Justifications (memory/GC focus)

### pgx/v5 `5.7.1` — PostgreSQL driver + pooling
- **Why over alternatives:** `lib/pq` is maintenance-mode and database/sql's interface forces value-copy overhead pgx avoids with its native binary protocol (~fewer allocations per query, prepared-statement caching).
- **Memory/GC impact:** binary protocol means no string-escaping churn per query — under the 1000-concurrent-client target (SLA row 2), allocation rate is the GC-pressure budget. `pgxpool` provides the separate read/write pools demanded by contract IC-3.
- **Invariant link:** I-2 enforcement flows through parameterized statements only (EP-5).

### google/uuid `1.6.0`
Smallest audited RFC-4122 implementation; crypto/rand-backed; matches schema defaults.

### labstack/echo/v4 `4.12.0` — HTTP routing
- **Why over gin:** echo's radix router allocates ~zero per match; gin leans harder on reflection-based binding (more garbage per request → more GC cycles on the hot path). Echo bundles rate-limit/timeout/recover/request-id middleware natively — precisely IC-1/IC-2 edge-boundary needs.
- **Hot-path note:** handlers will use pre-bound request structs with manual field validation (PROMPT 4.1) rather than reflective binding, keeping allocation count flat.

### redis/go-redis/v9 `9.6.1`
- **Why over alternatives:** redigo effectively unmaintained; rueidis (RESP3, client-side caching) is technically ahead but younger — **recorded revisit trigger: PROMPT 5.1** when Bloom-filter throughput data exists.
- **Memory/GC:** pooled connections reuse buffers; the nonce SETNX replay-check path (I-3 EP-4) is O(1) allocations.

### twmb/franz-go `1.18.0` — Kafka/Redpanda client
- **Why over alternatives:** sarama — historically heavy allocator, slower maintenance; confluent-go requires librdkafka cgo (kills CGO_ENABLED=0 distroless builds); segmentio/kafka-go weaker producer throughput. franz-go: pure Go, idempotent producers, transactions, KIP parity, group balancing.
- **Memory/GC:** batched zstd-compressed records keep CDC emission (commit step C10) off the p99 tail.
- **Invariant link:** B-4 one-way facts; tombstone reliability (SLA row 12).

### prometheus/client_golang `1.20.0`
Canonical metrics surface for the SLA verification loops (rows 2, 3, 10); histogram collectors allocate once at startup.

### stdlib `log/slog` — deliberate NON-dependency
Structured JSON logging without zap/zerolog: zero module-graph cost, adequate performance at our log volumes, and one fewer supply-chain node. Revisit only if logging becomes measurable hot-path cost.

### Test-only (isolated from production imports)
| Module | Purpose |
|---|---|
| stretchr/testify `1.9.0` | assertion ergonomics in integration suites (PROMPT 9.1) |
| testcontainers-go `0.33.0` | real PostgreSQL/MinIO/Redis containers for IC-contract tests |
| golang.org/x/sync `0.8.0` | errgroup-bounded concurrency in utilities/tests |

---

## 4. SIMD Decision Record

The design spec suggests `packed_simd`. That crate is **archived/unmaintained** — adopting it would violate the security baseline (no unmaintained dependencies).

**Decision:** PROMPT 3.1 implements Gear-hash cut-point search scalar-first, then adds `core::arch` intrinsics (`AVX2`/`NEON`; AVX-512 guarded by runtime detection) behind `#[cfg]` with a scalar fallback. The workspace currently sets `unsafe_code = "forbid"`; the SIMD module will opt out locally via reviewed `#[allow(unsafe_code)]` blocks with miri/asan coverage in its test plan.

---

## 5. Invariant Conflict Audit

| Invariant | Dependencies that ENFORCE it | Audited conflicts |
|---|---|---|
| I-1 Bit-Perfect CAS | sha2 (address identity), criterion/proptest (determinism proof), pgx (atomic ref_count upserts) | None. No dep performs lossy transformation of payload bytes (serde/json confined to control messages) |
| I-2 Acyclic Namespace | PostgreSQL ltree extension (compose bootstrap + RDS migrator role), pgx parameterized statements | None. No ORM present that could bypass `move_directory()` guards |
| I-3 Cryptographic Ingress | RustCrypto pure-Rust stack (no C crypto), go-redis SETNX nonces, echo middleware (constant-time comparisons implemented in PROMPT 3.2 using `crypto/hmac` — stdlib constant-time) | None. No dependency performs string-typed signature comparison on our behalf |

Cross-cutting: CGO_ENABLED=0 build (franz-go choice) guarantees static distroless images; `unsafe_code = "forbid"` today; deny.toml license allowlist lands with first real lockfile.

---

## 6. Supply-Chain Security Baseline

1. Lockfiles (`Cargo.lock`, `go.sum`, `.terraform.lock.hcl`) committed; drift = failed CI.
2. Advisory scanning both ecosystems on PR **and** nightly schedules.
3. SAST (semgrep) + secret scanning (gitleaks) + misconfig scanning (trivy fs) gate merges.
4. Images scanned pre-push; CRITICAL/HIGH findings block promotion (CI `images` job).
5. New dependency procedure: justification entry in this document is part of the PR template checklist — an unregistered dependency fails review even if all scanners pass.
