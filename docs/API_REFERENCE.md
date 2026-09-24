# Project Aegis: REST API & OpenAPI 3.0 Reference

## Overview
All Aegis control plane APIs communicate over HTTPS using UTF-8 JSON payloads. Direct file byte transfers bypass these endpoints via HMAC pre-signed S3/MinIO URLs.

---

## Base Endpoints
- **Production API:** `https://api.aegis.internal/v1`
- **Development API:** `http://localhost:8080/api/v1`

---

## Authentication
Requests require a Bearer token in the `Authorization` header:
```http
Authorization: Bearer aegis_live_sk_7f8c9b...
```

---

## Endpoint Specifications

### 1. Initiate Ingestion Session
- **Route:** `POST /api/v1/ingest/initiate`
- **Description:** Pre-checks chunk hashes against Redis Bloom filters and CAS registries, creates an upload session, and returns pre-signed S3 upload URLs for missing chunks.

#### Request Body Schema:
```json
{
  "tenant_id": "9f8b7c6d-5e4f-3a2b-1c0d-9e8f7a6b5c4d",
  "file_name": "production-archive.tar.gz",
  "parent_id": "1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d",
  "total_size": 104857600,
  "chunks": [
    {
      "block_hash": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
      "size_bytes": 4194304
    }
  ]
}
```

#### Response Body Schema (`200 OK`):
```json
{
  "session_id": "c7b6a5f4-e3d2-c1b0-a9f8-e7d6c5b4a3f2",
  "node_id": "8f7e6d5c-4b3a-2f1e-0d9c-8b7a6f5e4d3c",
  "expires_at": "2026-09-26T03:30:00Z",
  "upload_urls": [
    {
      "block_hash": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
      "url": "https://s3.us-east-1.amazonaws.com/aegis-cas/blocks/e3b0c442...?X-Amz-Signature=...",
      "size_bytes": 4194304
    }
  ]
}
```

---

### 2. Commit Ingestion Session
- **Route:** `POST /api/v1/ingest/commit`
- **Description:** Finalizes upload session, transactionally inserts a new `file_versions` record, updates `file_manifest_blocks`, bumps CAS reference counts, and emits CDC events.

#### Request Body Schema:
```json
{
  "session_id": "c7b6a5f4-e3d2-c1b0-a9f8-e7d6c5b4a3f2",
  "tenant_id": "9f8b7c6d-5e4f-3a2b-1c0d-9e8f7a6b5c4d",
  "content_sha256": "7f8c9b0a1e2f3d4c5b6a7f8e9d0c1b2a3f4e5d6c7b8a9f0e1d2c3b4a5f6e7d8c",
  "blocks": [
    {
      "chunk_index": 0,
      "block_hash": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
      "offset_bytes": 0,
      "size_bytes": 4194304
    }
  ]
}
```

#### Response Body Schema (`201 Created`):
```json
{
  "version_id": "3f4e5d6c-7b8a-9f0e-1d2c-3b4a5f6e7d8c",
  "version_number": 1
}
```

---

### 3. Stripe Webhook Processing
- **Route:** `POST /api/v1/stripe/webhook`
- **Headers:** Requires `Stripe-Signature: t=1727220000,v1=9f8c...`
- **Events Processed:** `customer.subscription.created`, `updated`, `deleted`.

---

### 4. Health & Metrics Probes
- **`GET /healthz`**: Returns `{"status":"ok"}` (`200 OK`).
- **`GET /readyz`**: Checks database connectivity; returns `{"status":"ready"}` (`200 OK`) or `503 Service Unavailable`.
- **`GET /metrics`**: Prometheus metrics endpoint exposing `aegis_ingest_initiate_total`, `aegis_cas_hits_total`, and `aegis_commit_latency_seconds`.

---

## Error Codes
| Status Code | Message | Description |
|---|---|---|
| `400` | `bad_request` | Invalid JSON syntax or missing mandatory fields. |
| `401` | `unauthorized` | Missing or invalid Bearer token / API key. |
| `402` | `quota_exceeded` | Tenant storage or bandwidth quota limit reached. |
| `409` | `session_completed` | Attempted to commit a session that was already finalized. |
| `410` | `session_expired` | Upload session exceeded 24-hour expiration TTL. |
| `429` | `too_many_requests` | Per-tenant rate limit exceeded (1000 RPS limit). |
