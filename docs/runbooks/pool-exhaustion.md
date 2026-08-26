# Runbook: Database Connection Pool Exhaustion

**Alert:** `AegisDBConnectionPoolExhausted`
**Severity:** warning (avg wait > 500ms for 5min) → critical (if sustained)
**Impact:** Ingestion requests slow down; eventual HTTP 5xx responses.

## Symptoms

- `aegis_db_acquire_wait_seconds` histogram shows increasing wait times
- Ingestion P99 latency climbing
- Grafana "Database Overview" dashboard shows flatline connections

## Diagnosis

```bash
# 1. Check current connection count per pool
kubectl exec -n aegis postgres-0 -- \
  psql -U aegis_admin -d aegis -c \
  "SELECT state, count(*) FROM pg_stat_activity WHERE datname='aegis' GROUP BY state;"

# 2. Check for idle-in-transaction (common cause)
kubectl exec -n aegis postgres-0 -- \
  psql -U aegis_admin -d aegis -c \
  "SELECT pid, state, query, state_change FROM pg_stat_activity
   WHERE datname='aegis' AND state != 'active' ORDER BY state_change;"

# 3. Check for lock contention
kubectl exec -n aegis postgres-0 -- \
  psql -U aegis_admin -d aegis -c \
  "SELECT blocked.pid AS blocked_pid,
          blocked.query AS blocked_query,
          blocking.pid AS blocking_pid,
          blocking.query AS blocking_query
   FROM pg_locks AS blocked
   JOIN pg_stat_activity AS blocked_act ON blocked.pid = blocked_act.pid
   JOIN pg_locks AS blocking ON blocked.locktype = blocking.locktype
     AND blocked.relation = blocking.relation
     AND blocked.pid != blocking.pid
   JOIN pg_stat_activity AS blocking_act ON blocking.pid = blocking_act.pid
   WHERE NOT blocked.granted;"
```

## Remediation

### Immediate (0–5 min)

1. **Kill idle-in-transaction sessions:**
   ```sql
   SELECT pg_terminate_backend(pid)
   FROM pg_stat_activity
   WHERE state = 'idle in transaction'
     AND state_change < now() - interval '5 minutes';
   ```

2. **Scale up ingestion pods** (if HPA hasn't caught up):
   ```bash
   kubectl scale -n aegis deployment/aegis-ingestion --replicas=5
   ```

3. **Restart ingestion pods** to clear stale connections:
   ```bash
   kubectl rollout restart -n aegis deployment/aegis-ingestion
   ```

### Short-term (5–30 min)

1. **Tune pool settings** in ConfigMap `aegis-config`:
   - `AEGIS_MAX_CONNS_PER_POOL`: reduce from 20 to 10 per pod
   - This distributes load more evenly across pods

2. **Check for slow queries** blocking connections:
   ```sql
   SELECT pid, now() - pg_stat_activity.query_start AS duration, query
   FROM pg_stat_activity
   WHERE state = 'active' AND now() - pg_stat_activity.query_start > interval '10 seconds';
   ```

### Long-term (hours–days)

1. **Increase `max_connections`** in postgres ConfigMap if DB is healthy
2. **Add read replicas** to offload analytical queries
3. **Review connection pool configuration** in `database/config.go`
   - Consider PgBouncer for connection multiplexing
4. **Investigate query regression** — check for missing indexes or seq scans

## Escalation

If connections remain exhausted after killing idle sessions, escalate to DBA
to check for `pg_stat_replication` lag or `pg_prepared_xacts` stuck transactions.

## References

- `internal/database/client.go` — pool taxonomy (Write/Read/Metadata/Analytical)
- `internal/database/config.go` — pool size configuration
- `internal/database/metrics.go` — `aegis_db_acquire_wait_seconds` definition
