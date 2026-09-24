# Subsystem Spec 06: Enterprise Billing, Usage Metering & API Keys

## 1. Overview
The **B2B Enterprise SaaS Subsystem** (`internal/billing`) manages multi-tenant plan tiers, real-time usage telemetry, developer API keys, offline cryptographic license key validation, and Stripe webhook synchronization.

---

## 2. Developer API Key Infrastructure (`apikey.go`)
- **Key Prefixing:** `aegis_live_sk_<48_hex_chars>` or `aegis_test_sk_<48_hex_chars>`.
- **SHA-256 Storage Checksum:** Secret API keys are unhashed only once upon creation. Database stores standard SHA-256 checksums (`key_hash`).
- **Scope Authorization:** Validates granular permissions (`read`, `write`, `admin`, `ingest`, `query`).

---

## 3. Real-Time Usage Metering Engine (`metering.go`)

Tracks telemetry across three core operational dimensions:
- `STORAGE_BYTES`: Total raw CAS storage occupied by tenant versions.
- `BANDWIDTH_BYTES`: Total egress/ingress data transfer.
- `WORKER_CREDITS`: Compute credits consumed by derivation workers.

Real-time transactional quota enforcement checks accumulated usage and returns `QuotaExceededError` (HTTP `402 Payment Required`) when allowance is exceeded.

---

## 4. Cryptographic Enterprise Offline Licensing (`license.go`)
For air-gapped or on-premise enterprise customer installations:
- Issues cryptographically signed HMAC-SHA256 license tokens (`<base64_payload>.<signature>`).
- Encodes metadata claims: `TenantID`, `PlanTier`, `MaxStorageTB`, `MaxNodes`, `Features`, and `ExpiresAt`.

---

## 5. Stripe Webhook Synchronization (`stripe.go`)
- Processes Stripe webhook events (`customer.subscription.created`, `updated`, `deleted`).
- Validates `Stripe-Signature` headers (`t=...`, `v1=...`) with timestamp drift prevention (<300s window) to reject replay attacks.
