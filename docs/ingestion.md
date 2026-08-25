# Ingestion Engine (PROMPT 4.1)

Stateless HTTP API for file ingestion with CAS dedup, pre-signed upload URLs,
atomic version commits, CDC event publishing, and quota enforcement.

## Architecture

```
Client → POST /api/v1/ingest/initiate → IngressServer
                                         ├─ validate tenant + quota
                                         ├─ create/validate file node
                                         ├─ batch CAS dedup query (global)
                                         ├─ mint pre-signed URLs (missing blocks)
                                         └─ create upload session

Client → PUT {pre_signed_url} → Object Storage (out of scope)

Client → POST /api/v1/ingest/commit → IngressServer
                                        ├─ begin READ COMMITTED tx
                                        ├─ lock session FOR UPDATE
                                        ├─ lock namespace node FOR UPDATE
                                        ├─ SELECT next_version_number (fenced)
                                        ├─ INSERT file_versions
                                        ├─ UPSERT cas_blocks (ON CONFLICT DO NOTHING)
                                        ├─ INSERT file_manifest_blocks (trigger bumps ref_count)
                                        ├─ UPDATE session → COMPLETED
                                        ├─ COMMIT
                                        ├─ bump cache generation (best-effort)
                                        └─ publish CDC event
```

## API Reference

### `POST /api/v1/ingest/initiate`

**Request:**
```json
{
  "tenant_id": "uuid",
  "file_name": "photo.jpg",
  "parent_id": "uuid | null",
  "node_id": "uuid | null",
  "total_size_bytes": 2048,
  "chunks": [
    {"block_hash": "64hex", "size_bytes": 1024}
  ]
}
```

| Field | Required | Notes |
|-------|----------|-------|
| `tenant_id` | yes | Must exist in `tenants` table |
| `file_name` | yes | Leaf name for the file node |
| `parent_id` | no | Parent directory; null = root |
| `node_id` | no | Existing file for re-upload; null = create new |
| `total_size_bytes` | yes | Must be > 0 |
| `chunks` | yes | Non-empty; each block_hash is 64 lowercase hex |

**Response (200):**
```json
{
  "session_id": "uuid",
  "node_id": "uuid",
  "expires_at": "2026-08-25T12:00:00Z",
  "upload_urls": [
    {"block_hash": "64hex", "url": "https://...", "size_bytes": 1024}
  ]
}
```

Only blocks **not** already in CAS receive upload URLs. If all blocks exist,
`upload_urls` is empty — the client can proceed directly to commit.

### `POST /api/v1/ingest/commit`

**Request:**
```json
{
  "session_id": "uuid",
  "content_sha256": "64hex",
  "blocks": [
    {"block_hash": "64hex", "chunk_index": 0, "offset_bytes": 0, "size_bytes": 1024}
  ]
}
```

**Response (201):**
```json
{
  "version_id": "uuid",
  "version_number": 3
}
```

## HTTP Status Codes

| Code | Error | When |
|------|-------|------|
| 200 | — | Initiate success |
| 201 | — | Commit success |
| 400 | `bad request` | Malformed JSON, missing fields, bad hash format |
| 401 | `unauthorized` | Missing or invalid bearer token |
| 402 | `storage quota exceeded` | Tenant quota exceeded |
| 404 | `not found` | Tenant, session, or node not found |
| 409 | `already completed` | Duplicate commit on same session |
| 410 | `session expired` | Session past its TTL |
| 503 | `database unavailable` | Circuit breaker open, pool timeout, serialization failure |

## Auth

API endpoints use static bearer tokens configured via `AEGIS_API_TOKEN`.
Pre-signed upload URLs use the HMAC token system from PROMPT 3.2.

## Upload Session Lifecycle

```
INITIATED ──(commit success)──→ COMPLETED
    │
    └──(expires_at reached)──→ EXPIRED (reaper)
```

Sessions expire after 15 minutes. A background reaper calls
`expire_stale_upload_sessions()` every 60 seconds.

## Ref-Count Protocol

`cas_blocks.ref_count` is maintained **exclusively** by database triggers:

- `manifest_block_added` (AFTER INSERT on `file_manifest_blocks`) increments
- `manifest_block_removed` (AFTER DELETE on `file_manifest_blocks`) decrements

The ingress engine **never** writes `ref_count` directly. The commit handler
ensures CAS rows exist via `INSERT ... ON CONFLICT DO NOTHING`, then inserts
manifest rows — the trigger handles the rest.

## CDC Events

After a successful commit, a `FileCommitted` event is published to the
`EventBus`. When `KAFKA_BROKERS` is unset, a `NoopBus` discards events
with a log line.

## Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `aegis_ingest_initiate_total` | counter | status | Total initiate calls |
| `aegis_ingest_commit_total` | counter | status | Total commit calls |
| `aegis_ingest_initiate_duration_seconds` | histogram | status | Initiate latency |
| `aegis_ingest_commit_duration_seconds` | histogram | status | Commit latency |
| `aegis_ingest_cas_hits_total` | counter | — | Blocks already in CAS |
| `aegis_ingest_uploads_total` | counter | — | Blocks requiring upload |
| `aegis_ingest_quota_exceeded_total` | counter | — | Quota rejections |

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `AEGIS_HTTP_PORT` | `8080` | Listen address |
| `AEGIS_API_TOKEN` | `""` | Bearer token (empty = no auth) |
| `AEGIS_ENDPOINT_ID` | `default` | Edge PoP ID for pre-signed URLs |
| `AEGIS_DATABASE_DSN` | — | PostgreSQL DSN (required) |
| `AEGIS_REDIS_ADDR` | `localhost:6379` | Redis address |
| `AEGIS_REDIS_PASSWORD` | `""` | Redis password |
| `AEGIS_SESSION_REAPER` | `60s` | Session expiry check interval |
| `AEGIS_LOG_LEVEL` | `info` | slog level |
| `AEGIS_BLOB_BASE_URL` | `https://blob-storage` | Pre-signed URL base |

## Concurrency & Safety

- **Session locking:** `FOR UPDATE` in READ COMMITTED serializes concurrent
  commits on the same session — exactly one succeeds, others get 409.
- **Version fencing:** `next_version_number()` acquires `FOR UPDATE` on the
  namespace node row, yielding dense, gapless version numbers under load.
- **CAS dedup:** Cross-tenant block sharing is intentional. The batch query
  is tenant-agnostic; `ON CONFLICT DO NOTHING` preserves the original
  uploader's `tenant_id`.
- **Cache invalidation:** `BumpGeneration` runs after commit, best-effort.
  Redis outage does not fail the commit (TTL is the backstop).
