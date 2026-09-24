# Automated CI/CD Pipeline, Security Scans & Chaos Engineering

This document outlines the GitHub Actions enterprise pipeline, security scanning gates, and automated Chaos Engineering suites for Project Aegis.

## GitHub Actions Enterprise Workflow (`.github/workflows/ci.yml`)

1. **`rust-lint` & `rust-test`:** Cargo fmt zero-diff checks, Clippy zero-warning enforcement, and property testing.
2. **`go-lint` & `go-test`:** Golangci-lint static analysis and race detector test suites.
3. **`web-ci`:** Next.js ESLint code quality checks, TypeScript compilation, and production bundle generation (`npm run build`).
4. **`security-scans`:** `govulncheck` for Go dependencies and `aquasecurity/trivy-action` vulnerability scanning.
5. **`chaos-testing`:** Executes automated fault injection integration suites.

## Chaos Engineering Suite (`tests/chaos/`)

- **`TestS3RateLimitRetryRecovery`:** Simulates S3 503 Service Unavailable rate limits during chunk upload streams, verifying client exponential backoff retries.
- **`TestDatabasePoolDropRecovery`:** Simulates primary database connection drops, verifying automated health probes and reconnects.
