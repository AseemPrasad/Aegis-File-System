# Ingress Security: HMAC Pre-Signed Tokens (PROMPT 3.2)

The pre-signed URL system is the **security perimeter** of Project Aegis:
every byte that enters storage must present a token minted by the origin
and validated before touching the backend. This document is the normative
reference for the format, the validation order, key rotation, replay
protection, and the threat model.

Implementation map:

| component                | location                                   |
|--------------------------|--------------------------------------------|
| token mint + validate    | `internal/auth/hmac.go`                    |
| KMS keys + rotation      | `internal/auth/keys.go`                    |
| anti-replay stores       | `internal/auth/replay.go`                  |
| edge validator           | `deploy/cloudflare-worker/worker.js`       |
| security test battery    | `internal/auth/hmac_test.go`               |
| real-Redis integration   | `internal/auth/integration_test.go`        |

---

## 1. Token format

```
message = "aegis1:{kv}:{tenant_id}:{block_hash}:{exp}:{endpoint_id}:{nonce}"
sig     = hex( HMAC_SHA256( key = KMS_key[tenant_id, kv], message ) )

URL     = {base}/chunks/{block_hash}
            ?tenant={tenant_id}&ts={exp}&kv={kv}&ep={endpoint_id}&nonce={nonce}&sig={sig}
```

Design decisions and their reasons:

* **Domain-separation prefix `aegis1`.** The same tenant keys must never
  verify blobs from another protocol; the version digit (`1`) lets us bump
  the wire format without key rotation.
* **Colon-joined fields are unambiguous.** Field grammars are enforced
  *before* signing and again before verification: UUID (tenant), exactly 64
  lowercase hex (block hash — a SHA-256), digits only (exp, kv),
  `[A-Za-z0-9_-]{1,64}` (endpoint), 32 lowercase hex (nonce). No field can
  contain a colon, so no field can impersonate a separator or shift the
  parse of another. `TestMalformedTokensRejected` pins injection attempts
  (`pop:fra1`, `pop/fra1`, uppercase sigs, …).
* **`ts` is the expiry** (`issued_at + TTL`), matching the contract's
  generation rule ("now + 15 minutes").
* **The key version is inside the message.** An attacker flipping `kv=2 →
  kv=1` changes the signed bytes, so downgrade attacks fail on signature,
  not policy (`TestKeyRotationOverlapWindow` asserts the exact failure
  mode).
* **16 random bytes of nonce per token**, generated with `crypto/rand`.

## 2. Validation gauntlet (order matters)

`TokenGenerator.Validate` executes, in order:

1. **Structure** — every field grammar; reject before touching KMS/Redis.
2. **Freshness** — reject if `now > exp + skew` (expired) or
   `exp > now + TTL + skew` (implausibly long-lived; caps damage of any
   future signing-oracle leak). Skew defaults to ±60 s.
3. **Key resolution** — current version always; superseded versions while
   within the overlap window (§3); anything else fails closed.
4. **Authenticity** — recompute HMAC over the canonical message and compare
   with `crypto/hmac.Equal` (**constant-time**). On the edge, WebCrypto
   `subtle.verify` performs the comparison internally, also constant-time.
5. **Replay** — consume the nonce via SETNX (`SET key 1 NX EX ttl+skew`).
   Only reached after step 4 passed, so garbage requests can never burn a
   victim's unused nonce (that would be a DoS on valid tokens).
6. **Ownership** *(backend-side only)* — optional `BlockOwnershipChecker`
   proves the tenant↔block binding against metadata.

Every failure maps to one wrapped sentinel (`ErrMalformed`, `ErrExpired`,
`ErrTooFarInFuture`, `ErrUnknownKeyVer`, `ErrBadSignature`, `ErrReplay`,
`ErrNonceUnavailable`, `ErrNotTenantBlock`). Edge deployments translate ALL
of them to an identical **401** — no differential responses, nothing
reaches the backend.

### Layering note (edge vs origin)

The edge cannot query the metadata store, so ownership is enforced where
the data lives: the first backend touch re-checks it. Crypto + freshness +
replay at the edge keep unauthorized traffic off the backend entirely;
ownership at the origin keeps *authorized tenants honest about blocks they
don't hold*. Both layers have tests.

## 3. Key rotation

* Policy: rotate every **90 days** (`KeyRotationPeriod`; scheduler-owned).
* Versions are small integers per tenant, monotonically increasing.
* Rotation demotes the old version with `superseded_at = now`; new tokens
  sign under the next version immediately.
* A superseded key stays verification-valid for **7 days**
  (`OverlapWindow`), so URLs minted minutes before a rotation survive it.
* After the window the key is dead: verification returns
  `ErrUnknownKeyVer` even for perfectly-signed tokens.
* The production `KMSClient` fronts AWS KMS (per-tenant CMK; data-key
  caching at the ingress pods). `StaticKMS` provides deterministic
  in-process behavior for tests and `IN_MEMORY_STORES=true` profiles.

Versioning scheme example:

```
kv=1 (superseded_at=T)   valid until T+7d    old tokens still work
kv=2 (current)           all new mints
```

## 4. Anti-replay

* Nonce: 16 random bytes, hex on the wire, **inside the signed message**
  (an attacker cannot strip or swap it without breaking the signature).
* Store: Redis `SETNX` + `EX ttl+skew` — atomic across all PoPs; races
  resolve to exactly one winner (`TestIntegrationReplayRaceExactlyOneWinner`:
  64 concurrent validators → precisely 1 success, verified against real
  compose Redis).
* Keys self-expire with the validity window, so the replay set is bounded
  (`TestIntegrationNonceTTLEviction` observes native EX eviction).
* Failure semantics are **fail-closed**: if the nonce store is unreachable,
  validation fails (`TestReplayStoreOutageFailsClosed`). A hole in replay
  protection during an outage is worse than unavailability.
* Dev profile: `InMemoryNonceStore` (process-local, bounded, swept lazily)
  matches the `IN_MEMORY_STORES=true` posture used elsewhere.

## 5. Threat model & security properties

| property        | mechanism                                            | test |
|-----------------|------------------------------------------------------|------|
| Authentication  | only the origin holds per-tenant KMS keys            | `TestForgedSignatureFails`, `TestHMAC_TokenSignatureTamperedFails` |
| Freshness       | exp ≤ now+TTL bound both directions                  | `TestHMAC_TokenExpiredFails`, `TestTokenTooFarInFutureFails`, `TestTimestampExtensionFails` |
| Replay immunity | single-use SETNX nonce                               | `TestTokenReplayFails`, integration race test |
| Multi-tenancy isolation | tenant in message **and** per-tenant key      | `TestCrossTenantTokenFails` (identity swap, block swap, claim mixing, wrong-PoP reuse) |
| Defense in depth| edge 401s before backend; origin re-checks ownership | `TestOwnershipCheckEnforced` |
| Timing safety   | `hmac.Equal` / WebCrypto `subtle.verify`             | construction-level (stdlib/runtime guarantee) |
| Wire stability  | golden vector pins message layout                    | `TestGoldenVectorMessageLayout` |
| Exhaustive modification resistance | every byte/delta of every field   | `TestProp_HMACSignatureSecureAgainstAllModifications` |
| Randomized attack simulation | 20k seeded mutation chains              | `TestFuzz_AttackerScenarios` |

Residual risks, stated honestly:

* **Clock compromise at the validator** extends nothing beyond
  `TTL + skew`; beyond that the freshness ceiling rejects.
* **Key compromise** is bounded by the 15-minute expiry *only for tokens
  already issued*; a leaked signing key can mint fresh tokens until
  detected → rotate immediately (rotation is O(1) here) and the overlap
  window does NOT extend attacker power since old-version tokens expire on
  schedule.
* **Nonce-store outage** degrades to fail-closed (availability hit, never a
  security hit).
* **Ownership at the edge** is impossible by topology; documented layering
  above. If a deployment needs edge-side ownership, replicate the
  tenant→block bitmap into KV and extend the worker — out of scope here.

## 6. Running the gates

```powershell
gofmt -l internal/auth                     # formatting
go vet ./internal/auth/                    # static analysis
go test ./internal/auth/                   # unit + fuzz battery (14 tests)
go test -race ./internal/auth/             # race detector
go test -tags=integration -count=1 ./internal/auth/   # real Redis (compose up redis)
```

Acceptance mapping:

| acceptance criterion                          | evidence |
|-----------------------------------------------|----------|
| invalid signatures never reach backend         | worker returns 401 on every failure path; `Validate` sentinels |
| tokens expire after 15 minutes                 | `DefaultTokenTTL` + expiry tests |
| replay impossible                              | SETNX nonce + race test (64 → 1) |
| cross-tenant attacks impossible                | isolation tests + exhaustive/fuzz batteries |
| seamless key rotation                          | overlap-window test incl. post-window retirement & downgrade failure mode |
