# Runbook: GC Falling Behind

**Alert:** `AegisGCBehind`
**Severity:** warning (no sweep for >2h)
**Impact:** Orphan blocks accumulate; storage costs rise; dedup ratio degrades.

## Symptoms

- `aegis_cas_orphan_blocks` gauge steadily increasing
- `aegis_cas_gc_sweeps_total` not incrementing
- `aegis_cas_gc_sweep_duration_seconds` flat (no recent data)

## Diagnosis

```bash
# 1. Check GC CronJob status
kubectl get -n aegis cronjob aegis-gc-sweep
kubectl get -n aegis jobs --sort-by=.metadata.creationTimestamp | tail -5

# 2. Check if GC is running in-process (AEGIS_GC_ENABLED=true)
kubectl logs -n aegis -l app.kubernetes.io/name=aegis-ingestion --tail=100 | grep -i "gc"

# 3. Check for GC sweep errors in logs
kubectl logs -n aegis -l app.kubernetes.io/name=aegis-gc --tail=100

# 4. Check orphan block count
curl -s http://aegis-ingestion:8080/metrics | grep aegis_cas_orphan
```

## Remediation

### Immediate (0–5 min)

1. **Trigger manual GC sweep** via CronJob:
   ```bash
   kubectl create job -n aegis --from=cronjob/aegis-gc-sweep manual-gc-$(date +%s)
   ```

2. **Check GC logs** for errors:
   ```bash
   kubectl logs -n aegis job/manual-gc-*
   ```

### Short-term (5–30 min)

1. **If blob deletion is failing** (`AegisGCDeletionFailed` alert):
   - Check S3/MinIO connectivity
   - Verify IAM credentials haven't expired
   - Check bucket permissions

2. **If GC is rate-limited** (`AegisGCRateLimited` alert):
   - Increase batch size in ConfigMap: `AEGIS_GC_BATCH_SIZE` → 5000
   - Or increase rate limit in GC config

3. **If sweep duration is too long:**
   - GC sweep duration >10min indicates too many orphan blocks
   - Consider running GC in the CronJob (not in-process) to avoid impacting ingestion

### Long-term (hours–days)

1. **Analyze orphan block causes:**
   ```sql
   SELECT
     CASE
       WHEN ref_count = 0 THEN 'orphan'
       WHEN ref_count > 100 THEN 'hot'
       WHEN ref_count > 10 THEN 'warm'
       ELSE 'cold'
     END AS tier,
     count(*),
     pg_size_pretty(sum(size_bytes)) AS total_size
   FROM cas_blocks
   GROUP BY 1;
   ```

2. **Review TombstoneEvent emission** — ensure GC is emitting events correctly
3. **Check GCConfig.Interval** — default is 1h; reduce to 15m for faster cleanup
4. **Consider offloading GC** to a dedicated CronJob (not in-process)

## Escalation

If GC is failing due to S3/MinIO errors, check:
- `aegis_cas_gc_deletion_failed_total` metric
- S3 bucket lifecycle policies
- IAM role/credential rotation

## References

- `internal/gc/engine.go` — GC engine loop
- `internal/cas/gc.go` — GCWorker
- `internal/cas/metrics.go` — GC metrics
- `cmd/ingest/gc_adapter.go` — GC adapter wiring
- `cmd/ingest/main.go:243-250` — GC metrics hooks
