# Disaster Recovery Plan

**Version:** 1.0
**Last Updated:** 2025-01-15
**Owner:** Platform Engineering
**Review Cadence:** Quarterly

## 1. Recovery Objectives

| Scenario | RPO | RTO | Strategy |
|----------|-----|-----|----------|
| Database corruption | 0s (WAL) | < 30s | Automated failover to read replica |
| Database loss | 5 min (backup) | < 15 min | PITR restore from S3 WAL archive |
| Object storage loss | 0s (replication) | < 5 min | Cross-region replication |
| Redis loss | 0s (AOF) | < 10s | Restart with AOF replay |
| Full region outage | 5 min | < 30 min | Multi-region failover |
| Namespace accidental deletion | N/A | < 30 min | GitOps re-apply from repo |

## 2. Backup Strategy

### 2.1 PostgreSQL

| Component | Method | Frequency | Retention | Storage |
|-----------|--------|-----------|-----------|---------|
| Full backup | pg_basebackup | Daily 02:00 UTC | 30 days | S3 (cross-region) |
| WAL archiving | archive_command | Continuous | 30 days | S3 (cross-region) |
| Logical backup | pg_dump | Weekly Sun 03:00 UTC | 90 days | S3 (cross-region) |

```bash
# Verify backup status
kubectl exec -n aegis postgres-0 -- \
  psql -U aegis_admin -d aegis -c \
  "SELECT * FROM pg_stat_archiver;"

# Test PITR restore (quarterly drill)
# 1. Create recovery target time
# 2. Restore from WAL archive
# 3. Verify data integrity
```

### 2.2 Object Storage (S3/MinIO)

| Feature | Configuration |
|---------|--------------|
| Versioning | Enabled on aegis-chunks bucket |
| Cross-Region Replication | Enabled (us-east-1 → us-west-2) |
| Lifecycle Rules | IA after 90 days, Glacier after 365 days |
| MFA Delete | Enabled (production) |

### 2.3 Redis

| Feature | Configuration |
|---------|--------------|
| AOF | Always fsync (appendonly yes) |
| RDB Snapshots | Every 600 seconds if ≥1000 keys changed |
| ElastiCache | Automatic backups, 7-day retention |

### 2.4 K8s Manifests

| Source | Backup Method |
|--------|--------------|
| All manifests | Git repository (GitOps) |
| Secrets | Sealed Secrets or External Secrets Operator |
| PersistentVolumes | Velero snapshots (daily) |

## 3. Recovery Procedures

### 3.1 Database Primary Failure (RTO < 30s)

```bash
# Automated: PostgreSQL failover via K8s readiness probe
# 1. postgres-0 becomes unhealthy
# 2. K8s marks pod as not ready
# 3. Application switches to postgres-replica service
# 4. Promote postgres-1 to primary

# Manual intervention (if automated failover fails):
kubectl exec -n aegis postgres-1 -- \
  psql -U aegis_admin -d aegis -c "SELECT pg_promote();"

# Verify promotion
kubectl exec -n aegis postgres-1 -- \
  psql -U aegis_admin -d aegis -c "SELECT pg_is_in_recovery();"
# Should return 'f'
```

### 3.2 Full Database Loss (RTO < 15 min)

```bash
# 1. Stop all ingestion pods
kubectl scale -n aegis deployment/aegis-ingestion --replicas=0
kubectl scale -n aegis deployment/aegis-derivation-workers --replicas=0

# 2. Restore from PITR backup
# (AWS RDS example)
aws rds restore-db-instance-to-point-in-time \
  --source-db-instance-identifier aegis-primary \
  --target-db-instance-identifier aegis-restored \
  --restore-time "2025-01-15T10:30:00Z"

# 3. Wait for restore to complete
aws rds wait db-instance-available --db-instance-identifier aegis-restored

# 4. Update DSN to point to restored instance
kubectl create secret generic aegis-secrets \
  --namespace aegis \
  --from-literal=database-url="postgres://aegis_admin:...@restored-db:5432/aegis" \
  --dry-run=client -o yaml | kubectl apply -f -

# 5. Restart ingestion
kubectl scale -n aegis deployment/aegis-ingestion --replicas=3
kubectl scale -n aegis deployment/aegis-derivation-workers --replicas=2

# 6. Verify
curl -s http://aegis-ingestion:8080/readyz
```

### 3.3 Object Storage Loss (RTO < 5 min)

```bash
# If primary S3 bucket is lost, switch to cross-region replica:
# 1. Update AEGIS_S3_ENDPOINT to replica region
# 2. Restart ingestion pods
# 3. Verify blob reads succeed

# For MinIO: restore from backup
minio server /data --console-address :9001
# Import data from backup bucket
```

### 3.4 Namespace Deletion (RTO < 30 min)

```bash
# Re-apply all manifests from Git
kubectl apply -k deploy/k8s/

# Verify all pods are running
kubectl get -n aegis pods

# Verify database connectivity
curl -s http://aegis-ingestion:8080/readyz
```

## 4. Quarterly Recovery Drill

| Step | Action | Owner | Time |
|------|--------|-------|------|
| 1 | Announce maintenance window | On-call | T-24h |
| 2 | Take snapshot of current state | SRE | T-0 |
| 3 | Simulate database failure | SRE | T+5min |
| 4 | Verify failover completes < 30s | SRE | T+10min |
| 5 | Verify data integrity | DBA | T+20min |
| 6 | Restore to original state | SRE | T+30min |
| 7 | Verify all services healthy | SRE | T+40min |
| 8 | Document drill results | On-call | T+1h |
| 9 | Update runbooks if needed | On-call | T+2h |

## 5. Monitoring & Alerting

| Metric | Alert Threshold | Action |
|--------|----------------|--------|
| Backup age > 25h | Warning | Check backup job |
| Backup age > 48h | Critical | Emergency backup |
| WAL archive lag > 100MB | Warning | Check archiver |
| WAL archive lag > 1GB | Critical | Check disk/network |
| Replica lag > 5s | Warning | Check replication |
| Replica lag > 30s | Critical | Check network |
| Cross-region lag > 5min | Warning | Check CRR |
| Cross-region lag > 30min | Critical | Check CRR |

## 6. Contact List

| Role | Name | Contact | Backup |
|------|------|---------|--------|
| On-call SRE | _TBD_ | _TBD_ | _TBD_ |
| DBA | _TBD_ | _TBD_ | _TBD_ |
| Security | _TBD_ | _TBD_ | _TBD_ |
| Management | _TBD_ | _TBD_ | _TBD_ |

## References

- `deploy/terraform/` — Infrastructure as Code
- `deploy/k8s/` — Kubernetes manifests
- `docs/runbooks/` — Operational runbooks
- `docs/runbooks/multi-region-failover.md` — Failover procedure
