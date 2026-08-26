# Runbook: Multi-Region Failover

**Alert:** N/A — manual failover procedure
**Severity:** critical (region outage)
**Impact:** Ingestion unavailable in primary region; reads may still work via replicas.

## Prerequisites

- Terraform-managed infrastructure (`deploy/terraform/`)
- Cross-region RDS read replica or PgBouncer cross-region routing
- S3 Cross-Region Replication (CRR) enabled on the chunk bucket
- Redis Global Datastore or cross-region replication

## Pre-Failover Checklist

- [ ] Confirm primary region is truly down (not just monitoring)
- [ ] Verify secondary region infrastructure is healthy
- [ ] Verify S3 CRR is caught up (check `aegis_cas_total_bytes` in both regions)
- [ ] Verify DB replication lag is < 5s
- [ ] Notify team on-call

## Failover Procedure

### Step 1: Promote RDS Read Replica (if using AWS)

```bash
# Promote the read replica in the secondary region
aws rds promote-read-replica \
  --db-instance-identifier aegis-secondary-replica \
  --region us-west-2
```

Wait for promotion to complete (~5-10 minutes).

### Step 2: Update DNS / Load Balancer

```bash
# Route traffic to secondary region's ingestion endpoint
# Option A: Route53 failover
aws route53 change-resource-record-sets \
  --hosted-zone-id Z1234567890 \
  --change-batch file://failover-dns.json

# Option B: Update ingress controller
kubectl patch svc -n aegis aegis-ingestion-external \
  --type='json' \
  -p='[{"op":"replace","path":"/spec/type","value":"LoadBalancer"}]'
```

### Step 3: Update Secrets

```bash
# Point to secondary region's DB
kubectl create secret generic aegis-secrets \
  --namespace aegis \
  --from-literal=database-url='postgres://aegis_admin:...@secondary-db:5432/aegis?sslmode=require' \
  --dry-run=client -o yaml | kubectl apply -f -

# Restart pods to pick up new secrets
kubectl rollout restart -n aegis deployment/aegis-ingestion
kubectl rollout restart -n aegis deployment/aegis-derivation-workers
```

### Step 4: Verify

```bash
# Check readiness
kubectl get -n aegis pods -l app.kubernetes.io/name=aegis-ingestion

# Test endpoint
curl -k https://aegis-ingestion.external/api/v1/healthz

# Check metrics flowing
curl -s http://aegis-ingestion:8080/metrics | grep aegis_ingest_initiate_total
```

### Step 5: Post-Failover

- [ ] Verify ingestion is processing requests
- [ ] Verify GC is running in secondary region
- [ ] Monitor for 30 minutes
- [ ] Update status page
- [ ] Begin primary region recovery

## Failback Procedure

1. Fix primary region infrastructure
2. Promote secondary's replica back to primary (reverse replication)
3. Update DNS to point back to primary
4. Monitor for 1 hour
5. Re-enable cross-region replication

## References

- `deploy/terraform/` — infrastructure-as-code
- `deploy/terraform/variables.tf` — region configuration
- `internal/database/config.go` — DSN configuration
- `internal/database/failover.go` — CircuitBreaker, failover logic
