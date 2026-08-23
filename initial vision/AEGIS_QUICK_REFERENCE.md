# PROJECT AEGIS: PROMPT-TO-OUTPUT QUICK REFERENCE
## Fast Navigation Guide for AI Agents & Development Teams

---

## PHASE OVERVIEW & PROMPT MAP

```
PHASE 1: FOUNDATION (Weeks 1-2)
├─ PROMPT 1.1: Architecture Validation Document
│  └─ Output: Architectural blueprint, invariant definitions, SLA matrix
│
└─ PROMPT 1.2: Development Environment Setup
   └─ Output: Cargo.toml, go.mod, CI/CD pipeline, Terraform manifests

PHASE 2: DATABASE (Weeks 1-2, parallel)
├─ PROMPT 2.1: PostgreSQL Schema Design
│  └─ Output: Production schema, indexes, stored procedures, tests
│
└─ PROMPT 2.2: Connection Pooling & Caching
   └─ Output: Database client (Go), connection pool, failover logic

PHASE 3: ALGORITHMS (Weeks 3-4)
├─ PROMPT 3.1: FastCDC Implementation
│  └─ Output: Rust implementation, SIMD optimization, benchmarks
│
└─ PROMPT 3.2: HMAC Token System
   └─ Output: Token generation, validation, KMS rotation, security tests

PHASE 4: INGESTION (Weeks 5-7)
├─ PROMPT 4.1: Stateless Ingestion Engine
│  └─ Output: HandleInitiate, HandleCommit, error handling, metrics
│
└─ PROMPT 4.2: Object Storage Abstraction
   └─ Output: S3/MinIO backends, pre-signed URLs, lifecycle policies

PHASE 5: DEDUPLICATION (Weeks 8-9)
├─ PROMPT 5.1: CAS Registry & Block Management
│  └─ Output: Block registry, ref-counting, storage tiering, Bloom filter
│
└─ PROMPT 6.1: Garbage Collection Pipeline
   └─ Output: Unreferenced block detection, Kafka tombstones, async deletion

PHASE 6: DERIVATION (Weeks 10-11)
└─ PROMPT 7.1: CDC Event Streaming
   └─ Output: Kafka topics, ClamAV/OCR/FFmpeg workers, result persistence

PHASE 7: TESTING (Weeks 12-13)
└─ PROMPT 9.1: Comprehensive Test Suite
   └─ Output: Unit/integration/system/fuzz/load tests, > 85% coverage

PHASE 8: OPERATIONS (Weeks 14-16)
├─ PROMPT 10.1: Kubernetes Deployment
│  └─ Output: K8s manifests, Prometheus/Grafana, alert rules
│
└─ PROMPT 11.1: Production Hardening
   └─ Output: Security hardening, runbooks, disaster recovery plan
```

---

## PROMPT QUICK REFERENCE TABLE

| Prompt | Title | Input | Output | Success Criteria | Hours |
|--------|-------|-------|--------|-----------------|-------|
| 1.1 | Architecture Validation | Spec document | Diagrams + invariants + SLA matrix | All components defined, no circular deps | 20-30 |
| 1.2 | Dev Environment | Requirements | Cargo.toml + go.mod + CI/CD | All tools installed, CI passes | 15-20 |
| 2.1 | PostgreSQL Schema | Architecture | Production schema + procedures | Query latency < 5ms (p95) | 30-40 |
| 2.2 | Connection Pooling | Schema | Database client + pool mgmt | Handles 1000+ concurrent | 20-30 |
| 3.1 | FastCDC | Algorithm spec | Rust code + SIMD + benchmarks | 2-4GB/s throughput | 40-50 |
| 3.2 | HMAC Tokens | Security spec | Token gen/validation + KMS | Zero successful attacks (fuzz) | 30-40 |
| 4.1 | Ingestion Engine | DB + crypto | Go handlers + error logic | p95 < 50ms for initiate | 40-50 |
| 4.2 | Object Storage | Ingestion spec | S3/MinIO backends + lifecycle | Pre-signed URLs work, tiering | 30-40 |
| 5.1 | CAS Registry | Ingestion code | Block registry + Bloom filter | > 50% dedup ratio | 25-35 |
| 6.1 | Garbage Collection | CAS code | GC pipeline + Kafka tombstones | No race conditions, safe deletes | 25-35 |
| 7.1 | CDC Derivation | GC code | Kafka topics + 3 workers | Workers complete within SLA | 35-45 |
| 9.1 | Test Suite | All code | Unit/integration/fuzz/load tests | > 85% coverage, SLA verified | 50-60 |
| 10.1 | Kubernetes | Test suite | K8s manifests + dashboards | All metrics collected | 30-40 |
| 11.1 | Prod Hardening | K8s code | TLS + encryption + runbooks | Zero critical vulnerabilities | 25-35 |

---

## ACCEPTANCE CRITERIA CHECKLIST

### For Each Prompt

#### PROMPT 1.1: Architecture Validation
- [ ] System topology diagram shows all components
- [ ] Data plane physically separated from control plane
- [ ] All three invariants formally defined
- [ ] All integration contracts documented
- [ ] SLA matrix complete (Durability, Latency, RPO, RTO)
- [ ] No circular dependencies between components

**Verification**: Execute with architecture review board

---

#### PROMPT 1.2: Development Environment
- [ ] `cargo build` succeeds with no warnings
- [ ] `go build ./...` succeeds with no warnings
- [ ] CI/CD pipeline runs in < 10 minutes
- [ ] All linters pass (clippy, golangci-lint)
- [ ] Dependency security scan passes
- [ ] Container images build successfully

**Verification**: Run `make verify-env` (all checks pass)

---

#### PROMPT 2.1: PostgreSQL Schema
- [ ] All 7 tables created (tenants, namespace_nodes, file_versions, cas_blocks, file_manifest_blocks, upload_sessions, derivation_results)
- [ ] All foreign keys correctly defined (no orphaned records possible)
- [ ] All indexes exist (GIST for ltree, B-tree for lookups)
- [ ] Stored procedures: move_directory() works correctly
- [ ] Cycle detection works (test cases pass)
- [ ] Schema tests achieve 100% coverage
- [ ] EXPLAIN ANALYZE shows < 5ms query latency (p95)

**Verification**: Run `psql -f schema.sql && pytest tests/schema/`

---

#### PROMPT 2.2: Connection Pooling
- [ ] Database client connects with pgx pool
- [ ] Separate pools for read/write operations
- [ ] Max connections configurable (default: CPU_cores * 4)
- [ ] Connection wait time < 100ms (p95)
- [ ] Pool metrics tracked (usage %, wait time, errors)
- [ ] Failover works within 30 seconds
- [ ] Connection leaks detected and prevented
- [ ] Cache invalidation is immediate (< 1ms)

**Verification**: Run `go test -race ./internal/database/` (all pass)

---

#### PROMPT 3.1: FastCDC
- [ ] Rust compilation succeeds
- [ ] Throughput > 2GB/s (verified benchmark)
- [ ] Chunk boundaries are content-aware (test with modifications)
- [ ] Chunk size distribution: 64KB-4MB with 1MB average
- [ ] Deduplication > 50% better than fixed-size
- [ ] SIMD optimized (2-4GB/s on modern CPUs)
- [ ] All edge cases handled (< 1MB files, empty files, repetitive data)
- [ ] Fuzz tests run for 1+ hour without crashes

**Verification**: Run `cargo bench && cargo test --release`

---

#### PROMPT 3.2: HMAC Tokens
- [ ] Token generation works correctly
- [ ] Token validation rejects tampered signatures
- [ ] Tokens expire after 15 minutes
- [ ] Cross-tenant attacks fail (fuzz tested)
- [ ] Replay attacks fail (nonce validation)
- [ ] KMS key rotation seamless (overlap window)
- [ ] Constant-time comparison prevents timing attacks
- [ ] Edge validation (Cloudflare) correctly validates

**Verification**: Run `go test -fuzz=FuzzHMAC ./internal/auth/`

---

#### PROMPT 4.1: Ingestion Engine
- [ ] HandleInitiate returns 200 OK with session + missing hashes
- [ ] HandleCommit returns 201 Created with version ID
- [ ] p95 latency < 50ms for initiate (1000 concurrent clients)
- [ ] p95 latency < 100ms for commit
- [ ] All error codes correct (400, 402, 403, 404, 410, etc.)
- [ ] Connection pool never exhausts
- [ ] Rate limiting works (max 1000 RPS/tenant)
- [ ] Metrics collected (latency, throughput, errors)
- [ ] Load tests pass (> 1000 RPS aggregate)

**Verification**: Run `go test -bench=BenchmarkUpload ./internal/ingress/`

---

#### PROMPT 4.2: Object Storage
- [ ] S3 backend generates pre-signed URLs
- [ ] MinIO backend works (on-premise)
- [ ] URLs expire after 15 minutes (verified)
- [ ] Block verification prevents corrupted uploads
- [ ] Tiered storage transitions work (HOT → WARM → COLD)
- [ ] Lifecycle policies delete incomplete uploads after 24 hours
- [ ] Integration with S3 (LocalStack) passes
- [ ] Integration with MinIO passes

**Verification**: Run `go test -tags=integration ./internal/storage/`

---

#### PROMPT 5.1: CAS Registry
- [ ] Blocks inserted with correct hash and ref_count
- [ ] Ref_count increments on duplicate uploads
- [ ] Bloom filter correctly predicts existence (no false negatives)
- [ ] Storage tier automation: ref_count > 100 → HOT, etc.
- [ ] Deduplication measurable (actual vs. logical size)
- [ ] Concurrent operations don't break ref_count
- [ ] Metrics: total blocks, total bytes, dedup ratio

**Verification**: Run `go test ./internal/cas/`

---

#### PROMPT 6.1: Garbage Collection
- [ ] Incomplete sessions cleaned up after 24 hours
- [ ] Orphaned blocks detected correctly
- [ ] Blocks deleted only after 7-day window (safety)
- [ ] Tombstone events emitted to Kafka
- [ ] Storage engines process tombstones
- [ ] No race conditions (concurrent GC + uploads)
- [ ] Audit trail logs every deletion
- [ ] Rate limiting prevents capacity spikes

**Verification**: Run `go test -timeout=10h ./internal/gc/`

---

#### PROMPT 7.1: CDC Derivation
- [ ] CDC events emitted for every version commit
- [ ] Kafka topics created (file_versions, cas-tombstones)
- [ ] ClamAV worker processes events within 60s
- [ ] OCR worker processes images within 120s
- [ ] FFmpeg worker generates thumbnails within 300s
- [ ] Results persisted to derivation_results table
- [ ] Failed events retried with exponential backoff
- [ ] Dead letter queue captures permanent failures

**Verification**: Run `go test ./internal/derivation/` with Kafka container

---

#### PROMPT 9.1: Test Suite
- [ ] Unit test coverage > 85% (measured by coverage tools)
- [ ] Integration tests cover happy path + 10 failure scenarios
- [ ] System tests verify end-to-end workflows
- [ ] Fuzz tests run for 1+ hour without crashes
- [ ] Load tests verify SLA compliance
- [ ] Security tests: all OWASP Top 10 addressed
- [ ] All tests pass in CI/CD pipeline
- [ ] Performance baselines established

**Verification**: Run `coverage check && load-test-suite`

---

#### PROMPT 10.1: Kubernetes Deployment
- [ ] Ingestion deployment: 10 replicas, auto-scaling enabled
- [ ] PostgreSQL StatefulSet: 3 replicas (primary + 2 standby)
- [ ] Redis StatefulSet: 3 replicas
- [ ] All services created (ClusterIP, Ingress)
- [ ] Prometheus scrapes all targets
- [ ] Grafana dashboards display all metrics
- [ ] Alert rules fire on threshold violations
- [ ] Logs aggregated and searchable

**Verification**: Run `kubectl apply -f manifests/ && verify-prometheus`

---

#### PROMPT 11.1: Production Hardening
- [ ] All connections use TLS 1.3 (verified via testssl.sh)
- [ ] Encryption at rest with KMS (random key verification)
- [ ] Rate limiting: max 1000 RPS/tenant (verified via load test)
- [ ] DDoS protection via Cloudflare enabled
- [ ] Secrets not hardcoded (audit code + secrets scanning)
- [ ] Audit logging for all API calls
- [ ] Credential rotation: keys, passwords, tokens (every 90 days)
- [ ] Security audit: zero critical vulnerabilities

**Verification**: Run `security-audit.sh && penetration-tests`

---

## OUTPUT FILE MANIFEST

### What Each Prompt Produces

**PROMPT 1.1**: 
```
outputs/
├─ architecture-diagram.md (ASCII diagram)
├─ invariants-formal.md (definitions in FOL)
├─ integration-contracts.md (component APIs)
└─ sla-matrix.md (all SLAs with verification)
```

**PROMPT 1.2**:
```
outputs/
├─ Cargo.toml (Rust dependencies)
├─ go.mod (Go dependencies)
├─ Makefile (build targets)
├─ .github/workflows/ci.yml (CI/CD)
└─ terraform/ (infrastructure-as-code)
```

**PROMPT 2.1**:
```
outputs/
├─ schema.sql (PostgreSQL DDL)
├─ schema-tests.sql (schema validation)
├─ indexes.sql (index definitions)
├─ stored-procedures.sql (functions)
└─ performance-report.md
```

**PROMPT 2.2**:
```
outputs/
├─ database_client.go (pooling + caching)
├─ database_client_test.go (comprehensive tests)
├─ metrics.go (Prometheus metrics)
└─ failover_test.go (recovery procedures)
```

**PROMPT 3.1**:
```
outputs/crates/fastcdc/
├─ src/lib.rs (implementation)
├─ benches/fastcdc_benchmark.rs (benchmarks)
├─ tests/ (unit + fuzz tests)
└─ PERFORMANCE.md (2-4GB/s verified)
```

**PROMPT 3.2**:
```
outputs/
├─ internal/auth/hmac.go (token generation/validation)
├─ internal/auth/hmac_test.go (security tests)
├─ internal/auth/kms_rotation.go (key rotation)
└─ internal/auth/fuzz_test.go (property-based tests)
```

**PROMPT 4.1**:
```
outputs/
├─ internal/ingress/server.go (HandleInitiate + HandleCommit)
├─ internal/ingress/server_test.go (integration tests)
├─ internal/ingress/handlers.go (error handling)
├─ internal/metrics/ingress_metrics.go (Prometheus)
└─ load-test-results.md (p95 < 50ms verified)
```

**PROMPT 4.2**:
```
outputs/
├─ internal/storage/client.go (abstraction)
├─ internal/storage/s3_client.go (AWS S3)
├─ internal/storage/minio_client.go (MinIO)
├─ internal/storage/lifecycle_policies.json
└─ integration-test-results.md
```

**PROMPT 5.1**:
```
outputs/
├─ internal/cas/registry.go (CAS operations)
├─ internal/cas/bloom_filter.go (existence checks)
├─ internal/cas/storage_tiers.go (tier automation)
├─ internal/cas/cas_test.go (unit tests)
└─ deduplication-report.md (> 50% ratio)
```

**PROMPT 6.1**:
```
outputs/
├─ internal/gc/collector.go (GC pipeline)
├─ internal/gc/tombstone_consumer.go (Kafka consumer)
├─ internal/gc/gc_test.go (safety verification)
├─ internal/gc/runbook.md (operational procedures)
└─ gc-metrics-report.md
```

**PROMPT 7.1**:
```
outputs/
├─ internal/derivation/clamav_worker.go (malware scanning)
├─ internal/derivation/ocr_worker.go (text extraction)
├─ internal/derivation/ffmpeg_worker.go (transcoding)
├─ internal/derivation/worker_pool.go (orchestration)
└─ internal/derivation/derivation_test.go (integration)
```

**PROMPT 9.1**:
```
outputs/
├─ tests/unit/ (all unit tests)
├─ tests/integration/ (database + external services)
├─ tests/system/ (end-to-end workflows)
├─ tests/fuzz/ (property-based tests)
├─ tests/load/ (benchmark suite)
└─ coverage-report.html (> 85%)
```

**PROMPT 10.1**:
```
outputs/
├─ kubernetes/ (all manifests)
│  ├─ ingestion-deployment.yaml
│  ├─ postgres-statefulset.yaml
│  ├─ redis-statefulset.yaml
│  └─ monitoring/ (Prometheus + Grafana)
└─ deployment-guide.md
```

**PROMPT 11.1**:
```
outputs/
├─ internal/security/ (TLS, encryption)
├─ operations/runbooks/ (10+ runbooks)
├─ operations/disaster-recovery/ (backup/restore)
├─ security-audit-report.md (zero critical vulns)
└─ production-checklist.md
```

---

## DEPENDENCY GRAPH

```
1.1 (Architecture) ──┐
                     │
1.2 (Environment) ──┤
                     │
        ┌────────────┤
        │            │
2.1 (Schema) ────────┼─────────────────────────────┐
        │            │                             │
2.2 (Pooling)        │                             │
        │            │                             │
        └────────────┼─────────────┐               │
                     │             │               │
3.1 (FastCDC) ──────┐├─────────────┼───────────────┼─────┐
                     ││             │               │     │
3.2 (HMAC) ────────┐ ││             │               │     │
                     ││             │               │     │
4.1 (Ingestion) ─────┴┴─────────────┼───────────────┼─────┼─┐
                                    │               │     │ │
4.2 (Storage) ──────────────────────┴───────────────┼─────┼─┼─┐
                                                    │     │ │ │
5.1 (CAS) ──────────────────────────────────────────┴─────┼─┼─┼─┐
                                                          │ │ │ │
6.1 (GC) ───────────────────────────────────────────────┐ │ │ │ │
                                                        │ │ │ │ │
7.1 (Derivation) ──────────────────────────────────────┴─┴─┴─┴─┘
                                                        │
9.1 (Tests) ───────────────────────────────────────────┘
                                                        │
10.1 (Kubernetes) ─────────────────────────────────────┘
                                                        │
11.1 (Hardening) ──────────────────────────────────────┘

Critical Path: 1.1 → 1.2 → 2.1 → 4.1 → 5.1 → 6.1 → 9.1
Parallel Paths: 2.2, 3.1, 3.2, 4.2, 7.1 can start after 4.1
```

---

## EXECUTION COMMAND REFERENCE

### Run All Tests for a Prompt

```bash
# PROMPT 1.1: Verify architecture
./scripts/verify-architecture.sh

# PROMPT 1.2: Verify environment
make verify-env

# PROMPT 2.1: Verify schema
psql -f schema.sql && pytest tests/schema/

# PROMPT 2.2: Verify connection pooling
go test -race ./internal/database/

# PROMPT 3.1: Verify FastCDC
cargo bench && cargo test --release

# PROMPT 3.2: Verify HMAC
go test -fuzz=FuzzHMAC -fuzztime=1h ./internal/auth/

# PROMPT 4.1: Verify ingestion engine
go test -bench=BenchmarkUpload ./internal/ingress/

# PROMPT 4.2: Verify object storage
go test -tags=integration ./internal/storage/

# PROMPT 5.1: Verify CAS registry
go test ./internal/cas/

# PROMPT 6.1: Verify garbage collection
go test -timeout=10h ./internal/gc/

# PROMPT 7.1: Verify derivation workers
go test ./internal/derivation/

# PROMPT 9.1: Verify test suite
coverage check && go test -bench=. ./...

# PROMPT 10.1: Verify Kubernetes
kubectl apply -f kubernetes/ --dry-run=client && verify-prometheus

# PROMPT 11.1: Verify production hardening
./security-audit.sh && penetration-tests
```

---

## TROUBLESHOOTING QUICK REFERENCE

### Common Issues & Solutions

**FastCDC Performance (PROMPT 3.1)**
- Issue: < 2GB/s throughput
- Solution: Enable SIMD (check CPU flags), profile with `perf`, optimize hot loops
- Check: Run `cargo bench --release` with `RUSTFLAGS="-C target-cpu=native"`

**Database Latency (PROMPT 2.1)**
- Issue: Query latency > 5ms
- Solution: Add index, increase work_mem, optimize query (EXPLAIN ANALYZE)
- Check: `EXPLAIN ANALYZE SELECT ...` for all queries

**Connection Pool Exhaustion (PROMPT 2.2)**
- Issue: "connection pool exhausted" errors
- Solution: Increase max connections, tune query latency, add caching
- Check: Monitor `dbClient.metrics.pool_saturation` (should be < 0.9)

**HMAC Signature Failures (PROMPT 3.2)**
- Issue: Pre-signed URLs rejected at edge
- Solution: Verify time sync (NTP), check KMS key accessible, verify constants
- Check: `go test -fuzz=FuzzHMAC` with adversarial inputs

**Upload Latency Exceeds SLA (PROMPT 4.1)**
- Issue: p95 latency > 50ms
- Solution: Profile Go code, check database latency, check object storage latency
- Check: Flamegraph with `go-torch`, trace with distributed tracing

**Garbage Collection Race Conditions (PROMPT 6.1)**
- Issue: Deleted blocks appearing in live files
- Solution: Add pessimistic locking, increase GC window, add fuzz tests
- Check: Run `go test -race -fuzz=FuzzGC -fuzztime=1h`

**Derivation Worker Backlog (PROMPT 7.1)**
- Issue: Workers can't keep up with version commits
- Solution: Increase worker pool size, optimize worker code, add sharding
- Check: Monitor Kafka consumer lag, worker queue depth

---

## SUCCESS MILESTONE CHECKLIST

- [ ] **Week 2**: Architecture + Schema + CI/CD pipeline ready
- [ ] **Week 4**: FastCDC and HMAC implementations complete and verified
- [ ] **Week 7**: Ingestion engine SLA verified (p95 < 50ms)
- [ ] **Week 9**: Deduplication working end-to-end (> 50% ratio)
- [ ] **Week 11**: Derivation workers processing within SLA
- [ ] **Week 13**: Test suite > 85% coverage, all SLAs verified
- [ ] **Week 16**: Production deployment ready, disaster recovery tested

---

## TEAM COMMUNICATION TEMPLATE

### Weekly Status Report

```markdown
# Project Aegis Weekly Status - Week X

## Completed This Week
- [x] PROMPT Y.Z: [Title] - [Brief result]
- [ ] Acceptance Criteria: [Pass/Fail]
- [ ] Performance: [Latency/Throughput/Coverage]

## In Progress
- [ ] PROMPT A.B: [Title] - [% Complete]
- [ ] Expected completion: [Date]

## Blockers
- [ ] [Issue] - [Owner] - [Target Resolution Date]

## Metrics
- Upload latency (p95): [X] ms
- Throughput: [Y] RPS
- Test coverage: [Z] %

## Next Week
- [ ] PROMPT C.D: [Title]
- [ ] PROMPT E.F: [Title]

## Risks
- [ ] [Risk] - [Severity] - [Mitigation]
```

---

## CONCLUSION

This quick reference guide maps every prompt to its expected outputs and acceptance criteria. Use this as your daily navigation tool for the 16-week development cycle.

**Start with PROMPT 1.1, execute sequentially, verify at each gate, and you'll have a production-grade system.**

Good luck! 🚀

