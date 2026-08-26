# Runbook: Credential Rotation

**Frequency:** Every 90 days (KMS keys, DB passwords, API tokens)
**Trigger:** Calendar-based or on compromise
**Impact:** Zero-downtime rotation with overlap windows.

## Rotation Schedule

| Credential | Rotation Period | Overlap Window | Method |
|------------|----------------|----------------|--------|
| KMS signing keys | 90 days | 7 days | `StaticKMS.Rotate()` |
| PostgreSQL password | 90 days | 24 hours | ALTER ROLE + rolling restart |
| Redis password | 90 days | 24 hours | ConfigMap update + rolling restart |
| API tokens (HMAC) | 90 days | 7 days | Secret update + rolling restart |
| S3/MinIO access keys | 90 days | 24 hours | Cloud console + Secret update |
| TLS certificates | Auto (cert-manager) | 30 days | cert-manager auto-renewal |

## KMS Key Rotation

```bash
# 1. Trigger rotation via API (or directly via StaticKMS.Rotate)
kubectl exec -n aegis postgres-0 -- \
  psql -U aegis_admin -d aegis -c \
  "SELECT rotate_key('tenant-uuid-here');"

# 2. Verify old key is still active (overlap window)
curl -s http://aegis-ingestion:8080/metrics | grep aegis_auth_key_version

# 3. After 7 days, old key auto-expires (KeyRecord.Active checks SupersededAt + 7d)
```

**Code path:** `internal/auth/keys.go:StaticKMS.Rotate()`
- Demotes current key (sets `SupersededAt = now`)
- Generates new 32-byte random key at `current + 1`
- Old key remains valid for 7 days (overlap window)

## PostgreSQL Password Rotation

```bash
# 1. Create new password
NEW_PW=$(openssl rand -base64 32)

# 2. Update password in Postgres
kubectl exec -n aegis postgres-0 -- \
  psql -U aegis_admin -d aegis -c \
  "ALTER ROLE aegis_app WITH PASSWORD '${NEW_PW}';"

# 3. Update K8s secret (new pods get new password)
kubectl create secret generic aegis-secrets \
  --namespace aegis \
  --from-literal=postgres-password="$NEW_PW" \
  --from-literal=database-url="postgres://aegis_app:${NEW_PW}@postgres:5432/aegis?sslmode=disable" \
  --dry-run=client -o yaml | kubectl apply -f -

# 4. Rolling restart (pods pick up new env vars)
kubectl rollout restart -n aegis deployment/aegis-ingestion
kubectl rollout restart -n aegis deployment/aegis-derivation-workers

# 5. Verify
kubectl rollout status -n aegis deployment/aegis-ingestion
curl -s http://aegis-ingestion:8080/readyz
```

## API Token Rotation

```bash
# 1. Generate new token
NEW_TOKEN=$(openssl rand -hex 32)

# 2. Update K8s secret
kubectl create secret generic aegis-secrets \
  --namespace aegis \
  --from-literal=api-token="$NEW_TOKEN" \
  --dry-run=client -o yaml | kubectl apply -f -

# 3. Update ALL clients BEFORE restarting ingestion pods
#    (SDK users, CI/CD pipelines, monitoring scripts)

# 4. Rolling restart
kubectl rollout restart -n aegis deployment/aegis-ingestion

# 5. Verify old token is rejected
curl -H "Authorization: Bearer OLD_TOKEN" http://aegis-ingestion:8080/api/v1/ingest/initiate
# Should return 401

# 6. Verify new token works
curl -H "Authorization: Bearer $NEW_TOKEN" http://aegis-ingestion:8080/api/v1/ingest/initiate
# Should return 200 (or proper error, not 401)
```

## Redis Password Rotation

```bash
# 1. Generate new password
NEW_REDIS_PW=$(openssl rand -base64 32)

# 2. Update Redis config (requires restart)
kubectl exec -n aegis redis-0 -- \
  redis-cli -a old_password CONFIG SET requirepass "$NEW_REDIS_PW"

# 3. Update K8s secret
kubectl create secret generic aegis-secrets \
  --namespace aegis \
  --from-literal=redis-password="$NEW_REDIS_PW" \
  --dry-run=client -o yaml | kubectl apply -f -

# 4. Rolling restart all Redis pods
kubectl rollout restart -n aegis statefulset/redis

# 5. Restart ingestion pods to pick up new Redis password
kubectl rollout restart -n aegis deployment/aegis-ingestion
```

## Emergency Rotation (Compromise)

If a credential is suspected compromised:

1. **Immediate:** Rotate the credential NOW (skip overlap window)
2. **Immediate:** Revoke all active sessions/tokens
3. **Immediate:** Check audit logs for unauthorized access:
   ```bash
   kubectl logs -n aegis -l app.kubernetes.io/name=aegis-ingestion --tail=10000 | \
     grep '"status":401'
   ```
4. **Within 1h:** Notify security team
5. **Within 24h:** Root cause analysis
6. **Within 7 days:** Implement additional controls to prevent recurrence

## Automation

Consider using:
- **Vault** for dynamic secrets (auto-rotated per-lease)
- **External Secrets Operator** for K8s secret sync
- **cert-manager** for TLS auto-renewal
- **CronJob** to trigger rotation alerts at 80-day mark

## References

- `internal/auth/keys.go:79-158` — StaticKMS, KeyRecord, Rotate()
- `internal/auth/hmac.go:379-438` — TokenGenerator.Validate()
- `deploy/k8s/01-secrets.yaml` — secret definitions
- `docs/runbooks/cert-renewal.md` — TLS renewal
