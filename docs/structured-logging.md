# Project Aegis — Structured Logging Conventions

## Overview

All Aegis services use Go's `log/slog` package with JSON output format for
production. This enables machine-parseable logs for aggregation in
Loki, ELK, CloudWatch, or similar systems.

## Configuration

Set via environment variables (in `deploy/k8s/02-configmap-aegis.yaml`):

| Variable | Values | Default | Description |
|----------|--------|---------|-------------|
| `AEGIS_LOG_LEVEL` | `debug`, `info`, `warn`, `error` | `info` | Minimum log level |
| `AEGIS_LOG_FORMAT` | `json`, `text` | `json` | Output format (use `text` for local dev) |

## JSON Log Schema

Every log line is a single JSON object with these standard fields:

```json
{
  "time": "2025-01-15T10:30:00.123456Z",
  "level": "INFO",
  "msg": "upload session initiated",
  "session_id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
  "node_id": "ns:files:documents",
  "block_count": 42,
  "total_bytes": 1048576,
  "duration_ms": 150
}
```

### Standard Fields

| Field | Type | Source | Description |
|-------|------|--------|-------------|
| `time` | string | slog | ISO 8601 timestamp |
| `level` | string | slog | Log level (DEBUG, INFO, WARN, ERROR) |
| `msg` | string | slog | Human-readable message |
| `session_id` | string | handler | Upload session UUID |
| `node_id` | string | handler | Namespace node path |
| `error` | string | handler | Error message (when present) |
| `duration_ms` | int | handler | Operation duration in milliseconds |

## Usage in Code

```go
// Basic structured log
slog.Info("upload session initiated",
    "session_id", sessionID,
    "node_id", nodeID,
    "block_count", len(blocks),
)

// With error context
slog.Error("blob upload failed",
    "block_hash", blockHash,
    "error", err,
    "attempt", attempt,
    "duration_ms", time.Since(start).Milliseconds(),
)

// With nested attributes
slog.Info("GC sweep completed",
    "sweep_id", sweepID,
    "blocks_deleted", deleted,
    "blocks_tombstoned", tombstoned,
    "orphan_bytes", orphanBytes,
    "duration_ms", duration.Milliseconds(),
)
```

## Log Levels

| Level | When to Use |
|-------|-------------|
| `DEBUG` | Detailed internal state (per-chunk processing, bloom filter lookups) |
| `INFO` | Business events (session initiated, commit completed, GC sweep done) |
| `WARN` | Degraded but recoverable (retry attempt, rate limit hit, slow query) |
| `ERROR` | Unrecoverable failures (DB connection lost, blob delete failed) |

## K8s Log Collection

In Kubernetes, logs are collected from stdout/stderr by the container runtime.
Use a log shipper (Fluent Bit, Promtail) to forward to Loki or ELK:

```yaml
# Promtail pipeline for aegis pods
- match:
    selector: '{namespace="aegis"}'
    pipeline_stages:
      - json:
          expressions:
            level: level
            msg: msg
      - labels:
          level:
```

## Request Logging

The `LoggingMiddleware` in `internal/ingress/middleware.go` logs every HTTP
request with:

```json
{
  "method": "POST",
  "path": "/api/v1/initiate",
  "status": 200,
  "duration_ms": 145,
  "remote_addr": "10.0.1.50",
  "request_id": "req-abc123"
}
```

Request IDs are generated per-request and included in response headers
(`X-Request-ID`) for end-to-end tracing.

## References

- `internal/ingress/middleware.go` — LoggingMiddleware
- `cmd/ingest/main.go:70-100` — slog initialization
- `deploy/k8s/02-configmap-aegis.yaml` — log level/format config
