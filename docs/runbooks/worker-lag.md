# Runbook: Derivation Worker Lag

**Alert:** None (no Prometheus metrics for workers yet — monitor via logs + queue depth)
**Severity:** warning → critical
**Impact:** Derivation jobs (ClamAV, OCR, FFmpeg, VectorEmbed) accumulate; downstream consumers see stale results.

## Symptoms

- `derivation_results` table: rows with `status='pending'` growing
- Worker pod CPU at 100% or idle (stuck/not processing)
- Logs show repeated timeout or error messages

## Diagnosis

```bash
# 1. Check worker pod status
kubectl get -n aegis pods -l app.kubernetes.io/name=aegis-derivation-workers

# 2. Check pending job count
kubectl exec -n aegis postgres-0 -- \
  psql -U aegis_admin -d aegis -c \
  "SELECT worker_type, status, count(*) FROM derivation_results
   WHERE status IN ('pending','running')
   GROUP BY worker_type, status;"

# 3. Check worker logs for errors
kubectl logs -n aegis -l app.kubernetes.io/name=aegis-derivation-workers --tail=200 | grep -i "error\|fail\|timeout"

# 4. Check if workers are consuming from the queue
kubectl logs -n aegis -l app.kubernetes.io/name=aegis-derivation-workers --tail=50 | grep -i "processing\|claimed\|completed"
```

## Remediation

### Immediate (0–5 min)

1. **Restart stuck workers:**
   ```bash
   kubectl rollout restart -n aegis deployment/aegis-derivation-workers
   ```

2. **Scale up workers** (if queue is deep):
   ```bash
   kubectl scale -n aegis deployment/aegis-derivation-workers --replicas=4
   ```

3. **Check for OOMKilled pods:**
   ```bash
   kubectl get -n aegis pods -l app.kubernetes.io/name=aegis-derivation-workers -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.conditions[?(@.type=="Ready")].status}{"\t"}{.status.containerStatuses[*].lastState.terminated.reason}{"\n"}{end}'
   ```

### Short-term (5–30 min)

1. **Increase worker resources** if OOMKilled:
   ```bash
   kubectl patch deployment -n aegis aegis-derivation-workers --type='json' \
     -p='[{"op":"replace","path":"/spec/template/spec/containers/0/resources/limits/memory","value":"2Gi"}]'
   ```

2. **Check for ClamAV DB updates** blocking worker startup:
   ```bash
   kubectl logs -n aegis -l app.kubernetes.io/name=aegis-derivation-workers --tail=50 | grep -i "clamav\|freshclam"
   ```

3. **If Redis is down** (worker claims use Redis for coordination):
   ```bash
   kubectl exec -n aegis redis-0 -- redis-cli -a $REDIS_PASSWORD ping
   ```

### Long-term (hours–days)

1. **Add Prometheus metrics to workers** — instrument `WorkerResult` counters
2. **Tune `AEGIS_WORKER_CONCURRENCY`** — increase from 4 to 8 per pod
3. **Add queue depth metric** and wire it to HPA
4. **Consider per-worker-type Deployments** — ClamAV, OCR, FFmpeg, VectorEmbed
   as separate deployments for independent scaling
5. **Review `AEGIS_MAX_QUEUE_DEPTH`** — increase if legitimate backlogs occur

## Escalation

If workers are failing due to external service errors (ClamAV, FFmpeg):
- Check if ClamAV signature DB is stale
- Check if FFmpeg is installed correctly in the container
- Check OCR model download succeeded

## References

- `internal/workers/engine.go` — Worker pool, WorkerResult
- `cmd/ingest/derivation_bridge.go` — derivationBridge
- `cmd/ingest/derivation_helpers.go` — tool implementations
- `cmd/ingest/main.go:156-188` — derivation worker wiring
