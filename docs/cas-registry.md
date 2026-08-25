# CAS Registry & Block Management

> PROMPT 4.3 — Content-Addressable Storage lifecycle

## Overview

The `internal/cas` package manages the full lifecycle of deduplicated binary
blocks in the Aegis CAS registry. Every unique chunk has exactly one row in
`cas_blocks`. Multiple file versions reference the same block via
`file_manifest_blocks`. The `ref_count` tracks how many versions reference each
block; when `ref_count = 0`, the block is orphaned and eligible for GC after a
7-day safety window.

### Architecture

```
┌────────────────┐  batch query   ┌────────────────┐
│ HandleInitiate │ ─────────────▸ │ Bloom Filter   │ (Redis, 1% FP)
│                │ ◂─ might exist │ (skip DB hit)  │
│                │  ────────────▸ │ cas_blocks DB  │ (definitive check)
└────────────────┘                └────────────────┘

┌────────────────┐  ON CONFLICT   ┌────────────────┐
│ HandleCommit   │ ── DO NOTHING▸ │ cas_blocks     │ (ensure row exists)
│                │    + manifest  │ (trigger bumps │
│                │                │  ref_count)    │
└────────────────┘                └────────────────┘

┌────────────────┐  ref_count=0   ┌────────────────┐
│ GC Worker      │ ──── 7 days ▸  │ DELETE         │ (orphan sweep)
│ (background)   │                │ cas_blocks     │
└────────────────┘                └────────────────┘
```

## Tier Auto-Promotion

Blocks are automatically assigned a storage tier based on their `ref_count`:

| Tier  | Threshold | Description                           | Cost/Performance              |
|-------|-----------|---------------------------------------|-------------------------------|
| COLD  | ref < 10  | Infrequent access, single region       | Lowest cost, highest latency  |
| WARM  | 10 ≤ ref ≤ 100 | Standard replication, 2 regions  | Balanced cost/performance     |
| HOT   | ref > 100 | High availability, 3+ regions          | Highest cost, lowest latency  |

### How Tier Changes Happen

1. **DB Trigger (automatic):** `manifest_block_added()` promotes to HOT when
   `ref_count + 1 > 100`. This is the fast path for the common case.
2. **Application Layer (this package):** `Registry.UpdateTier()` applies full
   three-tier logic including WARM/COLD demotion. Called after ref_count changes
   that might warrant demotion (e.g., file deletes).

### Cost Implications

- **HOT blocks** cost ~$0.023/GB/month (S3 Standard) but serve at <50ms p99.
- **WARM blocks** cost ~$0.0125/GB/month (S3 Standard-IA) with <200ms p99.
- **COLD blocks** cost ~$0.004/GB/month (S3 Glacier) but require 3-5h retrieval.

A block referenced by 100+ file versions is almost certainly "hot" — demoting
it would cause latency spikes for many users.

## Bloom Filter

The bloom filter provides O(1) "definitely not exists" checks to skip DB
round-trips during `HandleInitiate`:

- **Redis-backed** using the RedisBloom module (`BF.ADD`, `BF.EXISTS`)
- **False positive rate:** 1% — a positive result falls through to the DB
- **TTL:** 1 hour — the filter is rebuilt periodically
- **Key format:** `aegis:cas:bloom:{endpoint_id}`

### Usage Flow

```
1. Client sends block hashes for dedup check
2. For each hash:
   a. bloom.MightContain(hash) → false → skip DB (guaranteed miss)
   b. bloom.MightContain(hash) → true  → query DB (possible false positive)
3. Bloom is updated on every EnsureBlock (add new hashes)
```

## GC Sweep

The `GCWorker` runs as a background goroutine and periodically:

1. Queries `cas_blocks` for rows with `ref_count = 0` and `created_at < NOW() - 7 days`
2. Best-effort deletes corresponding blobs from object storage
3. Deletes the DB rows (only `ref_count = 0` double-check)
4. Emits metrics (blocks deleted, sweep duration)

### Configuration

| Env var / field     | Default | Description                    |
|--------------------|---------|--------------------------------|
| GC interval        | 5 min   | Time between sweep cycles      |
| GC batch size      | 1000    | Max blocks per sweep           |
| Safety window      | 7 days  | Minimum age before GC eligible |

## Metrics

All metrics are prefixed `aegis_cas_`:

| Metric                          | Type      | Description                          |
|--------------------------------|-----------|--------------------------------------|
| `total_blocks`                 | Gauge     | Total CAS block rows                 |
| `total_bytes`                  | Gauge     | Total stored bytes                   |
| `orphan_blocks`                | Gauge     | Blocks with ref_count = 0            |
| `orphan_bytes`                 | Gauge     | Bytes in orphaned blocks             |
| `hot_blocks`                   | Gauge     | Blocks in HOT tier                   |
| `warm_blocks`                  | Gauge     | Blocks in WARM tier                  |
| `cold_blocks`                  | Gauge     | Blocks in COLD tier                  |
| `unverified_blocks`            | Gauge     | Awaiting ETag verification           |
| `avg_ref_count`                | Gauge     | Average ref_count                    |
| `max_ref_count`                | Gauge     | Maximum ref_count                    |
| `dedup_ratio`                  | Gauge     | Content bytes / stored bytes         |
| `ref_count_distribution`       | Histogram | Distribution of ref_count values     |
| `gc_sweeps_total`              | Counter   | Total GC sweep cycles                |
| `gc_blocks_deleted_total`      | Counter   | Total blocks deleted by GC           |
| `gc_sweep_duration_seconds`    | Histogram | Duration of GC sweeps                |

## Test Doubles

- `FakeRegistry` — in-memory Registry for unit tests
- `FakeBloomFilter` — in-memory BloomFilterer for unit tests

Both support error injection (`SetErr`, `SetAddErr`, `SetExistErr`) for
negative-path testing.

## Database Schema

```sql
CREATE TABLE cas_blocks (
    block_hash    BYTEA PRIMARY KEY CHECK (octet_length(block_hash) = 32),
    tenant_id     UUID        NOT NULL REFERENCES tenants(tenant_id),
    size_bytes    INTEGER     NOT NULL CHECK (size_bytes > 0),
    storage_tier  VARCHAR(32) NOT NULL DEFAULT 'HOT'
                  CHECK (storage_tier IN ('HOT', 'WARM', 'COLD')),
    ref_count     BIGINT      NOT NULL DEFAULT 0 CHECK (ref_count >= 0),
    verified      BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

### Key Invariant

`ref_count` is **exclusively** maintained by the `manifest_block_added` /
`manifest_block_removed` triggers. Application code NEVER writes `ref_count`
directly. This is defense-in-depth — the triggers are the single source of
truth.
