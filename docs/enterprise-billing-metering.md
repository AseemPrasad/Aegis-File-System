# Aegis B2B Enterprise Billing, Metering & API Key Architecture

## Overview
Aegis Enterprise Billing provides scalable, multi-tenant subscription management, usage metering, developer API key authentication, and offline cryptographic license key validation.

## Key Subsystems

### 1. Developer API Key Management (`internal/billing/apikey.go`)
- **Format:** `aegis_live_sk_<48_hex_chars>` or `aegis_test_sk_<48_hex_chars>`
- **Storage:** Standard SHA-256 hex checksum stored in database (`tenant_api_keys.key_hash`).
- **Scopes:** Granular capability authorization (`read`, `write`, `admin`, `ingest`, `query`).

### 2. Real-Time Usage Metering Engine (`internal/billing/metering.go`)
- Multi-dimensional meters:
  - `ingress_gb`: Total raw ingestion throughput.
  - `api_calls`: Total API invocations.
  - `cas_queries`: Deduplicated storage lookup queries.
  - `retention_days`: Extended log/file retention days.
- Quota enforcement with real-time transactional checking before processing high-volume jobs.

### 3. Cryptographic Enterprise License Keys (`internal/billing/license.go`)
- HMAC-SHA256 / RSA signed offline tokens for air-gapped or self-hosted enterprise deployments.
- Contains claims for storage limits, node caps, enabled features, and expiration dates.

### 4. Stripe Webhook Synchronization (`internal/billing/stripe.go`)
- Subscribes to subscription lifecycle events (`customer.subscription.created`, `updated`, `deleted`).
- Automated signature header verification (`t=...`, `v1=...`) with timestamp drift prevention.
