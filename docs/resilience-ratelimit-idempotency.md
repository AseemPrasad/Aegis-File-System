# Advanced Resilience: Sliding-Window Rate Limiting & Idempotency Keys

This document details the distributed rate limiting and state fencing mechanics for Project Aegis.

## Distributed Redis Sliding-Window Rate Limiter (`internal/ingress/redis_ratelimit.go`)

- **Atomic Sorted Set Counter:** Executes Lua script (`ZADD`/`ZREMRANGEBYSCORE`/`ZCARD`) calculating real-time request rates over sliding 60-second windows.
- **RFC 6585 Headers:** Emits `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset`, and `Retry-After` on HTTP 429 response limit breaches.

## Idempotency Key Engine (`internal/ingress/idempotency.go`)

- **Header Interception:** Intercepts `Idempotency-Key` headers on `/files/initiate` and `/files/commit`.
- **Atomic Fencing (`SET NX EX 86400`):** Prevents duplicate execution collisions and caches payload outputs in Redis for instant HITS without triggering database transactions or token generation.
