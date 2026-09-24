# Subsystem Spec 05: Generational Garbage Collection Pipeline

## 1. Overview
The **Garbage Collection (GC) Pipeline** (`internal/gc`) implements a generational mark-and-sweep algorithm for unreferenced Content-Addressed Storage (CAS) blocks. It safely recycles storage space without introducing data loss or race conditions with in-flight upload sessions.

---

## 2. Three-Phase Execution Pipeline

```
Phase 1 (Hourly)  ──► Expire Stale Upload Sessions (is_completed = FALSE, expires_at < NOW())
Phase 2 (Daily)   ──► Mark-and-Sweep Orphan Blocks (ref_count = 0 AND updated_at < NOW() - 7d)
Phase 3 (Async)   ──► Emit Tombstones to Kafka -> Physical DeleteObjects from S3/MinIO
```

### Phase 1: Expire Stale Sessions (`SessionCleanupInterval = 1h`)
- Finds uncommitted `upload_sessions` past their 24-hour expiration TTL and marks them expired.

### Phase 2: Orphan Block Detection (`BlockSweepInterval = 24h`)
- Scans `cas_blocks` for rows where `ref_count = 0` and age exceeds `SafetyWindow = 7d`.
- Performs a double-check query before emitting tombstone deletion events.

### Phase 3: Tombstone Deletion (`cas-tombstones` topic)
- Async tombstone consumer receives tombstone events and issues batch `DeleteObjects` calls to AWS S3/MinIO.

---

## 3. Safety Rules & Controls
- **7-Day Tombstone Safety Window:** Blocks with `ref_count = 0` are quarantined for 7 full days before physical deletion to prevent race conditions with concurrent uploads.
- **Double-Check Ref-Count:** Re-queries `ref_count` immediately prior to issuing physical deletion commands (`DoubleCheckSaved` metric).
- **Max Blocks Per Hour Rate Limit:** Configurable max deletion rate (`MaxBlocksPerHour = 5000`) to prevent storage bill spikes or accidental mass deletions.
- **Dry-Run Mode (`DryRun = true`):** Logs candidate deletion blocks without making permanent storage calls.
