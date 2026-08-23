# Project Aegis: CI/CD Pipeline Specification

| | |
|---|---|
| **Prompt** | 1.2 — Development Environment & Dependency Selection |
| **Workflow file** | `.github/workflows/ci.yml` |
| **Local mirror** | `Makefile` targets (identical commands) |

---

## 1. Pipeline Topology

```
 push / PR
    │
    ▼
 ┌───────────── STATIC FAST GATES ─────────────┐
 │ rust-lint   fmt --check · clippy -D warn    │
 │ go-lint     gofmt · vet · golangci-lint     │
 │ security    gitleaks · cargo-deny           │
 │             govulncheck · semgrep · trivy fs│
 └──────────────┬──────────────────────────────┘
                │ all green
                ▼
 ┌───────────── CORRECTNESS ───────────────────┐
 │ rust-test  unit+property, coverage ≥85%     │
 │ go-test    -race suite, coverage ≥85%       │
 └──────────────┬──────────────────────────────┘
                ▼
 ┌───────────── INTEGRATION (real services) ───┐
 │ compose up postgres/redis/redpanda/minio    │
 │ ltree extension probe (I-2 prerequisite)    │
 │ /ready contract probe (IC-2)                │
 │ go test -tags=integration (per-prompt suites)│
 └──────────────┬──────────────────────────────┘
                ▼
 ┌───────────── CONTAINERS ────────────────────┐
 │ build ingest + fastcdc images               │
 │ trivy image scan: CRITICAL/HIGH = block     │
 │ main → push GHCR (sha + latest tags)        │
 └──────────────┬──────────────────────────────┘
                ▼
         promote-gate: "main is releasable"

 nightly (schedule)
 ├─ rust-fuzz-soak      extended property budget (~1h, PROMPT 9.1 precursor)
 └─ rust-bench-gate     critcmp vs baseline; >10% regression = FAIL
```

---

## 2. Gate Register — What Blocks Merging, and Why

| # | Gate | Threshold / Command | Enforces | SLA / Invariant link |
|---|------|--------------------|----------|----------------------|
| 1 | Rust formatting | `cargo fmt --all -- --check` zero diff | Reviewable diffs only | — |
| 2 | Rust lints | `cargo clippy --workspace --all-targets -- -D warnings` | Zero-warning quality bar (acceptance criterion) | Code-quality floor for I-1 EP-1 code paths |
| 3 | Go formatting/vet | `gofmt -l` empty · `go vet ./...` | Same bar, Go side | IC-3 client correctness |
| 4 | Go lints | golangci-lint (errcheck/gosec/staticcheck…) | Unchecked errors & insecure patterns cannot merge | Plane-boundary error contracts (IC-*) |
| 5 | Rust tests + coverage | `cargo llvm-cov --fail-under-lines 85` | >85% unit coverage target | PROMPT 9.1 precursor gate |
| 6 | Go tests + coverage | `-race` + `coverage_gate.py ≥85%` | Data-race freedom (concurrency safety) + coverage | GC race prevention (SLA row 12), pool safety (IC-3) |
| 7 | Module drift | `go mod tidy && git diff --exit-code go.mod go.sum` | Supply-chain determinism | DEPENDENCIES.md §1.2 |
| 8 | Secrets scan | gitleaks on full history | No credentials ever enter git | Security baseline |
| 9 | Rust supply chain | cargo-deny: advisories=deny, licenses=deny | Known-vulnerable/unlicensed crates blocked | I-3 crypto hygiene |
| 10 | Go vulnerabilities | govulncheck (call-graph aware) | Only *reachable* vulns flagged; still blocks | Security baseline |
| 11 | SAST | semgrep `--config auto --error` | Injection/insecure-pattern classes | OWASP posture (PROMPT 11.1 precursor) |
| 12 | FS misconfig | trivy scanners=vuln,misconfig,secret CRITICAL,HIGH | Terraform/compose hardening drift | Encryption-at-rest/TLS rules in deploy/** |
| 13 | Integration stack | compose `--wait` healthy + ltree probe + `/ready` probe + tagged suites | Real-service behavior, not mocks | IC-1..IC-6 contracts verified continuously |
| 14 | Image scan | trivy image CRITICAL/HIGH exit 1 | Vulnerable images never reach GHCR | PROMPT 11.1 zero-critical criterion |
| 15 | Perf gate (nightly) | critcmp threshold 10% regression | Throughput/latency floors don't erode silently | SLA rows 3–4 (initiate <50ms, >1GB/s path) |
| 16 | Fuzz soak (nightly) | extended proptest budget ~1h | Edge-case crashes surface pre-merge of algorithms | I-1/I-2/I-3 testing strategies from ARCHITECTURE doc §5 |

**Promotion rule:** a PR merges only when jobs 1–14 are green. Nightly failures (15–16) open an automatic issue — they cannot block a merge retroactively but must be triaged within one business day per the roadmap's weekly-SLA-verification discipline.

---

## 3. Branching & Promotion Flow

```
feature branch ──PR──► main          gates 1–14 enforced
                     main            images pushed to GHCR (sha tag)
                       │
                       ▼ make tf-plan/apply TF_ENV=staging
                     STAGING          chaos drills · RTO/RPO drills · perf soak
                       │  manual release review (roadmap phase-gate discipline)
                       ▼ git tag vX.Y.Z → same images re-scanned & promoted
                     PRODUCTION       tf apply with two-person review; deletion protection on
```

- **Images are built once** at merge to main and promoted by tag — production never rebuilds from source.
- **Rollback** = redeploy previous sha-tagged image + previous tfvars plan artifact.

---

## 4. Environment Matrix (provisioning templates)

| Environment | Template | Provisioned by | Stores | In-memory allowed | Purpose |
|---|---|---|---|---|---|
| local | `deploy/environments/local.env` | developer (`make compose-up`, optional fakes via `IN_MEMORY_STORES=true`) | single-node compose or in-process fakes | **yes (only here)** | inner loop; algorithm work with zero infra |
| integration | `deploy/environments/integration.env` | CI job / `make integration-test` | compose: pg16+ltree, redis7, redpanda, minio, prometheus/grafana/exporters | no | IC-contract suites; every PR |
| staging | `deploy/environments/staging.env` + `staging.tfvars` | Terraform (multi-AZ RDS/ElastiCache/S3/KMS) + Redpanda Helm on EKS | managed, replicated | no | prod parity at reduced scale; chaos/RTO/RPO drills; optional second region stack |
| production | `deploy/environments/production.env` + `production.tfvars` | Terraform with change review | managed multi-AZ, deletion protection, PITR 14d | startup asserts false | live traffic |

Secret resolution: templates use `${SM:path:key}` placeholders resolved at deploy time from AWS Secrets Manager/Vault — template files are secret-free by construction and safe to commit (gitleaks still scans them).

---

## 5. Nightly Job Details

| Job | Mechanics | Failure action |
|---|---|---|
| fuzz-soak | `PROPTEST_CASES=100000 cargo test --release`; upgraded to libfuzzer targets when PROMPT 3.1 lands | auto-issue with minimized failing input artifact |
| bench-gate | criterion baselines cached from last main run; `critcmp --threshold 10` | auto-issue tagged `perf-regression`; blocks next release until triaged |

Baseline cache key rotates per commit on main so improvements ratchet forward permanently.

---

## 6. Acceptance Criteria Traceability (Prompt 1.2)

| Criterion | Where satisfied |
|---|---|
| Lint checks (clippy, golangci-lint) | Jobs `rust-lint`, `go-lint` (+ Makefile mirrors) |
| Unit tests, coverage >85% | `rust-test`, `go-test` + `scripts/coverage_gate.py` threshold arg |
| Integration vs real PostgreSQL + MinIO | `integration` job: compose stack incl. minio-init bucket bootstrap |
| Performance benchmark gates | nightly `rust-bench-gate` (critcmp 10%) wired toward p95 SLA verification |
| SAST + dependency scanning | semgrep, cargo-deny, govulncheck, trivy fs, gitleaks |
| Container build + scanning | `images` matrix build → trivy → conditional GHCR push |
| Every environment provisionable from templates | §4 matrix: env files + tfvars + compose + helm values all present |
| CI catches regressions before production | §3 promotion flow: nothing reaches staging/prod without gates 1–14 green |
