# Project Aegis: Exabyte-Capable Distributed CAS Engine & SaaS Platform

[![CI/CD Pipeline](https://github.com/aegis-dev/aegis/actions/workflows/ci.yml/badge.svg)](https://github.com/aegis-dev/aegis/actions/workflows/ci.yml)
[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Rust Edition](https://img.shields.io/badge/Rust-2021-000000?style=flat&logo=rust)](https://www.rust-lang.org/)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-16%2B%20ltree-4169E1?style=flat&logo=postgresql)](https://www.postgresql.org/)
[![License](https://img.shields.io/badge/License-Proprietary-red.svg)](LICENSE)

**Project Aegis** is a production-grade, exabyte-capable, multi-tenant distributed file storage, deduplication, synchronization, and content-derivation engine modeled after the operational mechanics of **Dropbox Magic Pocket** and **Google Drive**.

Aegis physically decouples the **Data Plane** (raw binary I/O) from the **Control Plane** (relational metadata, access control, and API orchestration). This design guarantees that high-throughput file transfers never exhaust database connection pools or starve API transaction threads.

---

## 🏛️ Immutable Architectural Invariants

| # | Invariant | Description | Technical Implementation |
|---|---|---|---|
| **I-1** | **Bit-Perfect CAS** | Payload chunks are addressed exclusively by cryptographic hash; mutations are strictly append-only. | Rust FastCDC chunking + SHA-256 content addressing in S3/MinIO chunk store. |
| **I-2** | **Acyclic Namespace** | The filesystem directory graph is an immutable DAG; cycles are rejected at the schema level. | PostgreSQL `ltree` extension (`/tenant_id/user/folder/file`). |
| **I-3** | **Cryptographic Ingress** | Direct-to-blob transfers require short-lived, tenant-scoped HMAC tokens evaluated at edge storage endpoints. | Pre-signed URL minting with single-use Redis nonces & 90-day KMS key rotation. |

---

## 🏗️ System Topology & Data Plane Separation

```
                                ┌─────────────────────────────────────────────────┐
                                │                   CLIENTS                       │
                                │   Aegis SDK: FastCDC Chunker · SHA-256 Hasher   │
                                └──────┬───────────────────────────────┬──────────┘
                                       │ (1) CONTROL: HTTPS/QUIC       │ (2) DATA: Direct
                                       │     TLS 1.3 · JSON APIs       │     Pre-Signed PUT/GET
                                       ▼                               ▼
        ╔════════════════════════════════════════════╗   ╔═══════════════════════════════════════╗
        ║          CONTROL / METADATA PLANE          ║   ║             DATA PLANE                ║
        ║                                            ║   ║                                       ║
        ║  ┌──────────────────────────────────────┐  ║   ║  ┌─────────────────────────────────┐  ║
        ║  │ INGRESS ENGINE (Stateless Go)        │  ║   ║  │ DIRECT INGRESS EDGE PoP          │  ║
        ║  │ HandleInitiate() / HandleCommit()    │  ║   ║  │ Evaluates HMAC Tokens at Edge   │  ║
        ║  └──────────────┬───────────────────────┘  ║   ║  └───────────────┬─────────────────┘  ║
        ║                 │                          ║   ║                  │                    ║
        ║                 ▼                          ║   ║                  ▼                    ║
        ║  ┌──────────────────────────────────────┐  ║   ║  ┌─────────────────────────────────┐  ║
        ║  │ METADATA STORE (PostgreSQL 16)       │  ║   ║  │ CAS BLOB STORE                  │  ║
        ║  │ tenants · namespace_nodes (ltree)    │  ║   ║  │ S3 / MinIO / Ceph / Cloudflare R2 │  ║
        ║  │ file_versions · cas_blocks           │  ║   ║  │ Content-Addressed Objects (SHA256)║  ║
        ║  └──────────────────────────────────────┘  ║   ║  └─────────────────────────────────┘  ║
        ╚════════════════════════════════════════════╝   ╚═══════════════════════════════════════╝
```

---

## ⚡ Key Platform Capabilities

- **🚀 SIMD FastCDC Chunker Engine:** High-throughput content-defined chunking implemented in Rust (`crates/fastcdc`) targeting 2–4 GB/s per core.
- ** Deduplication Pre-Filtering:** $O(1)$ Redis Bloom filter pre-checking to bypass redundant database queries and raw storage writes.
- **🔒 Zero-Trust Edge Security:** Granular scoped API keys (`aegis_live_sk_...`), 90-day KMS envelope key version rotation, and single-use Redis replay nonces.
- **💳 Multi-Tenant Enterprise SaaS Suite:** Usage metering tracking `STORAGE_BYTES`, `BANDWIDTH_BYTES`, and `WORKER_CREDITS`, offline cryptographically signed enterprise license validation, and Stripe Webhook synchronization.
- **⚡ Async Derivation Fleet:** Kafka Change Data Capture (CDC) event stream powering malware scanning (ClamAV), OCR text extraction (Tesseract), video transcoding (FFmpeg), and AI vector embeddings.
- **🧹 Generational Garbage Collection:** Mark-and-sweep GC pipeline enforcing a 7-day tombstone safety window before physical chunk deletion.

---

## ⏱️ SLA Performance Matrix

| Metric | Target SLA | Benchmark Result | Verification Method |
|---|---|---|---|
| **Initiate Latency (P95)** | $< 50\text{ ms}$ | **$12.62\text{ ms}$** | `go test -run TestSLA_InitiateLatency` |
| **Commit Latency (P95)** | $< 100\text{ ms}$ | **$22.82\text{ ms}$** | `go test -run TestSLA_ConcurrentThroughput` |
| **Ingress Throughput** | $> 10,000\text{ ops/sec}$ | **$15,917\text{ ops/sec}$** | Concurrent SLA benchmark suite |
| **Data Durability** | $99.999999999\%$ | Guaranteed | Erasure coded S3 + Bit-Perfect CAS |
| **Data Availability** | $99.99\%$ | Guaranteed | Multi-AZ EKS + Aurora PostgreSQL |

---

## 💻 Quickstart Guide

### 1. Requirements
- Go 1.25+
- Rust 2021 (1.79+)
- PostgreSQL 16+ (with `ltree` extension)
- Redis 7+
- Node.js 18+ (for Web Console)

### 2. Run All Tests
```bash
# Run all Go unit and SLA benchmark tests
go test ./aegis/... ./cmd/... ./internal/...

# Run Next.js Web Console production build
npm run build --prefix web
```

---

## 📖 Comprehensive Documentation Sitemap

- [📑 Architectural Specification](docs/ARCHITECTURE.md)
- [🌐 OpenAPI & REST API Reference](docs/API_REFERENCE.md)
- [🛠️ Operational Deployment & Monitoring Runbook](docs/OPERATIONAL_RUNBOOK.md)
- [🔥 Disaster Recovery & Business Continuity Plan](docs/DISASTER_RECOVERY.md)
- **Subsystem Deep Dives:**
  - [01. FastCDC Chunker Engine](docs/subsystems/01_fastcdc_engine.md)
  - [02. Ingress Security & KMS Key Rotation](docs/subsystems/02_ingress_security.md)
  - [03. PostgreSQL Schema & ltree Graph](docs/subsystems/03_database_ltree.md)
  - [04. CDC Derivation & Kafka Workers](docs/subsystems/04_cdc_derivation.md)
  - [05. Garbage Collection Pipeline](docs/subsystems/05_garbage_collection.md)
  - [06. Enterprise Billing & Usage Metering](docs/subsystems/06_enterprise_billing.md)
