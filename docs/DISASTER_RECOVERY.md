# Project Aegis: Disaster Recovery & Business Continuity Plan

## 1. SLA Targets
- **Recovery Point Objective (RPO):** $0\text{ seconds}$ (Zero data loss guarantee for committed transactions).
- **Recovery Time Objective (RTO):** $< 15\text{ minutes}$ (Full service restoration target).

---

## 2. Multi-Region Replication Strategy

```
  PRIMARY REGION (us-east-1)                 SECONDARY REGION (us-west-2)
┌─────────────────────────────┐            ┌─────────────────────────────┐
│ EKS Ingress Cluster         │            │ EKS Ingress Standby Cluster │
│ Aurora PG (Primary Writer)  ├─Async Rep──► Aurora PG (Read Replica)    │
│ ElastiCache Redis Cluster   │            │ ElastiCache Redis Standby   │
│ S3 Bucket (Primary CAS)     ├─Cross Region S3 Bucket (Replica CAS)     │
└─────────────────────────────┘            └─────────────────────────────┘
```

1. **Database Replication:** AWS Aurora PostgreSQL Multi-AZ cluster with cross-region read replicas streaming WAL changes asynchronously to `us-west-2`.
2. **Object Storage Replication:** S3 Cross-Region Replication (CRR) automatically replicates `blocks/<sha256_hex>` objects to secondary region buckets with KMS encryption key transformation.

---

## 3. Disaster Recovery Failover Procedure

In the event of a catastrophic region outage in `us-east-1`:

### Step 1: Promote Secondary Aurora PostgreSQL Database
```bash
aws rds promote-read-replica \
  --db-instance-identifier aegis-postgres-us-west-2 \
  --region us-west-2
```

### Step 2: Reroute Anycast / Route 53 DNS Traffic
```bash
aws route53 change-resource-record-sets \
  --hosted-zone-id Z12345678 \
  --change-batch file://dns-failover-west.json
```

### Step 3: Validate Secondary Ingress Cluster Readiness
```bash
kubectl --context=us-west-2 get pods -n aegis-production
curl -i https://api-west.aegis.internal/readyz
```

---

## 4. Point-In-Time Recovery (PITR)
- Aurora PostgreSQL automated continuous backups retained for 35 days.
- To restore database state to a specific timestamp prior to a corrupt transaction:
  ```bash
  aws rds restore-db-instance-to-point-in-time \
    --target-db-instance-identifier aegis-restored \
    --source-db-instance-identifier aegis-primary \
    --restore-time 2026-09-25T03:00:00Z
  ```
