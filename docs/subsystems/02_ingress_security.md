# Subsystem Spec 02: Ingress Security, HMAC Tokens & KMS Rotation

## 1. Zero-Trust Direct-to-Blob Security Model
Aegis enforces complete physical separation between control metadata and binary payload data. Binary file data streams directly to S3/MinIO using short-lived, tenant-scoped pre-signed URLs.

---

## 2. HMAC Token Structure (`aegis_hmac_...`)

Pre-signed upload URLs embed cryptographically signed token parameters:
```http
https://s3.us-east-1.amazonaws.com/aegis-cas/blocks/<block_hash>?aegis_tenant=<tenant_id>&aegis_version=<key_ver>&aegis_expires=<exp>&aegis_nonce=<nonce>&aegis_sig=<hmac_sha256>
```

### Signature Message Envelope:
$$\text{MAC} = \text{HMAC-SHA256}(K_{\text{tenant, ver}}, \text{tenant\_id} \mathbin{\Vert} \text{block\_hash} \mathbin{\Vert} \text{expires\_at} \mathbin{\Vert} \text{nonce})$$

---

## 3. KMS Envelope Key Version Rotation Model

Each tenant maintains a monotonically increasing key version integer:

```
Version 1 (Current Writer)  ───────────────► Active Signature Minting
Version 0 (Superseded Key)  ─── 7-Day Overlap Window ───► Verification Only (Retired After 7 Days)
```

- **Rotation Period:** Key versions SHOULD rotate every 90 days (`KeyRotationPeriod = 90d`).
- **Overlap Window:** Superseded keys are retained in `StaticKMS` / AWS KMS for 7 days (`OverlapWindow = 7d`), ensuring in-flight uploads minted prior to rotation succeed without error.

---

## 4. Replay Nonces (`SETNX`)
To prevent token reuse, edge PoPs validate nonces against Redis using `SETNX` with a matching 15-minute TTL. Replayed requests return `401 Unauthorized`.
