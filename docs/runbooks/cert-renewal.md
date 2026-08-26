# Runbook: Certificate Renewal

**Alert:** N/A — proactive maintenance
**Severity:** critical (if certificate expires)
**Impact:** TLS termination fails; all HTTPS traffic returns 502/503.

## Certificates to Track

| Certificate | Location | Renewal Method |
|-------------|----------|----------------|
| Ingress TLS | Ingress controller | cert-manager auto-renewal |
| PostgreSQL TLS | StatefulSet pods | Manual / cert-manager |
| Redis TLS | StatefulSet pods | Manual / cert-manager |
| S3/MinIO TLS | External / in-cluster | Cloud provider |
| HMAC tokens | `aegis-secrets` | Manual rotation |

## Auto-Renewal (cert-manager)

If cert-manager is installed:

```bash
# Check certificate status
kubectl get -n aegis certificate
kubectl describe -n aegis certificate aegis-tls

# Check for expiring certificates
kubectl get -n aegis certificate -o json | \
  jq -r '.items[] | "\(.metadata.name)\t\(.status.notAfter)"'
```

## Manual Renewal

### Step 1: Generate New Certificate

```bash
# Using openssl
openssl req -x509 -nodes -days 365 -newkey rsa:2048 \
  -keyout tls.key -out tls.crt \
  -subj "/CN=aegis-ingestion.aegis.svc.cluster.local"

# Or using kubectl
kubectl create secret tls aegis-tls \
  --namespace aegis \
  --cert=tls.crt \
  --key=tls.key \
  --dry-run=client -o yaml | kubectl apply -f -
```

### Step 2: Update Ingress

```bash
kubectl patch -n aegis ingress aegis-ingress \
  --type='json' \
  -p='[{"op":"replace","path":"/spec/tls/0/secretName","value":"aegis-tls-new"}]'
```

### Step 3: Rolling Restart

```bash
# Restart pods to pick up new TLS secrets
kubectl rollout restart -n aegis deployment/aegis-ingestion
```

### Step 4: Verify

```bash
# Check TLS handshake
curl -kv https://aegis-ingestion.external/healthz 2>&1 | grep -A2 "SSL connection"

# Check certificate expiry
echo | openssl s_client -connect aegis-ingestion.external:443 2>/dev/null | \
  openssl x509 -noout -dates
```

## HMAC Token Rotation

HMAC tokens (`api-token`) have no expiry — rotate manually:

```bash
# Generate new token
NEW_TOKEN=$(openssl rand -hex 32)

# Update secret
kubectl create secret generic aegis-secrets \
  --namespace aegis \
  --from-literal=api-token="$NEW_TOKEN" \
  --dry-run=client -o yaml | kubectl apply -f -

# Rolling restart to pick up new token
kubectl rollout restart -n aegis deployment/aegis-ingestion
```

**Important:** Update all clients (SDK, CI/CD, scripts) with the new token
BEFORE restarting the ingestion pods. Otherwise requests will fail with 401.

## Automation

To automate certificate renewal:

1. Install cert-manager in the cluster
2. Create a `Certificate` resource for each TLS cert
3. Configure Let's Encrypt ClusterIssuer
4. Set `renewBefore: 720h` (30 days before expiry)

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: aegis-tls
  namespace: aegis
spec:
  secretName: aegis-tls
  dnsNames:
    - aegis-ingestion.aegis.svc.cluster.local
    - aegis.example.com
  issuerRef:
    name: letsencrypt-prod
    kind: ClusterIssuer
  renewBefore: 720h
```

## References

- `internal/auth/hmac.go` — HMAC token generation
- `internal/auth/keys.go` — KeyRecord, StaticKMS
- `deploy/k8s/01-secrets.yaml` — secret definitions
