# Project Aegis: Operational Deployment & Incident Runbook

## 1. Cloud Infrastructure Provisioning (Terraform)

Infrastructure is provisioned on AWS using Terraform / OpenTofu modules in `infra/terraform/`:

```bash
cd infra/terraform

# Initialize OpenTofu / Terraform provider plugins
tofu init

# Inspect execution plan
tofu plan -out=tfplan

# Apply infrastructure changes (VPC, EKS, Aurora PG, ElastiCache, S3)
tofu apply tfplan
```

### Environment Variable Bindings for EKS Pods:
```yaml
env:
  - name: AEGIS_DATABASE_DSN
    valueFrom:
      secretKeyRef:
        name: aegis-db-credentials
        key: primary_dsn
  - name: AEGIS_REDIS_ADDR
    value: "aegis-redis.cache.amazonaws.com:6379"
  - name: AEGIS_STORAGE_BACKEND
    value: "s3"
  - name: AEGIS_S3_BUCKET
    value: "aegis-production-cas-us-east-1"
  - name: AEGIS_S3_REGION
    value: "us-east-1"
```

---

## 2. Kubernetes Helm 3 Deployment

Package and deploy Aegis microservice deployments using Helm 3 charts (`deploy/helm/aegis`):

```bash
# Lint Helm chart syntax
helm lint deploy/helm/aegis

# Upgrade/Install Aegis release in production namespace
helm upgrade --install aegis-prod deploy/helm/aegis \
  --namespace aegis-production \
  --create-namespace \
  --values deploy/helm/aegis/values-production.yaml
```

---

## 3. Monitoring & Grafana Dashboards

Prometheus metrics exposed at `/metrics`:
- `aegis_ingest_initiate_total{status="200"}`: Total initiate requests.
- `aegis_cas_hits_total`: Deduplicated chunk hit counter.
- `aegis_commit_latency_seconds_bucket`: Ingress commit latency distribution.

### Alerting Rules:
1. **High Ingest Latency Alert:** Triggered if P95 initiate latency exceeds 50ms for 5 consecutive minutes.
2. **Database Pool Saturation Alert:** Triggered if available connections in `pgxpool` drop below 10%.
3. **GC Deletion Failure Alert:** Triggered if physical S3 blob deletions fail consistently during mark-and-sweep passes.

---

## 4. Incident Response Runbooks

### Incident A: High Rate of HTTP 402 Quota Exceeded Errors
1. Inspect tenant resource usage in `usage_meters` table.
2. Verify if tenant subscription tier upgrade is pending in Stripe.
3. To temporarily grant an emergency quota burst:
   ```sql
   UPDATE tenants SET storage_quota_bytes = storage_quota_bytes + 107374182400 WHERE tenant_id = '<tenant_id>';
   ```

### Incident B: Compromised Developer API Key
1. Instantly revoke the API key:
   ```sql
   UPDATE tenant_api_keys SET is_active = FALSE WHERE key_id = '<key_id>';
   ```
2. Invalidate tenant session cache in Redis.
