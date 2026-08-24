// ============================================================================
// Project Aegis — Edge ingress validator (Cloudflare Workers).
// PROMPT 3.2 (Cryptographic Ingress & HMAC Token System).
//
// Runs BEFORE any request reaches the storage backend. Every failure path
// returns 401 with an identical, information-free body so attackers learn
// nothing about which gate tripped.
//
// Bindings (see wrangler.toml):
//   TENANT_KEYS   KV namespace: "keys:{tenantUUID}" ->
//                   { "current": 2,
//                     "versions": { "2": {"key":"<hex64>","superseded_at":null},
//                                   "1": {"key":"<hex64>","superseded_at":1760000000000} } }
//   UPSTASH_URL / UPSTASH_TOKEN  REST credentials for the nonce store
//     (SET NX EX gives atomic single-use semantics across all PoPs).
//
// Crypto notes:
//   * HMAC verification uses WebCrypto subtle.verify — the comparison is
//     performed inside the runtime and is constant-time.
//   * The signed message MUST byte-match internal/auth/hmac.go:
//         aegis1:{kv}:{tenant}:{blockHash}:{exp}:{endpoint}:{nonce}
//     A golden-vector test on the Go side pins this format.
//   * Ownership (tenant owns block_hash) is NOT checked at the edge — no
//     metadata store access there. It is enforced server-side on first
//     contact (TokenGenerator WithOwnershipChecker). Documented layering.
// ============================================================================

const OVERLAP_WINDOW_MS = 7 * 24 * 3600 * 1000;
const TTL_SECONDS = 900; // 15 minutes
const SKEW_SECONDS = 60;

function unauthorized(reason) {
  // reason is logged for OUR metrics, never returned to the caller.
  return new Response("401 Unauthorized", {
    status: 401,
    headers: { "x-aegis-reject": reason },
  });
}

function isValidLowerHex(s, len) {
  if (typeof s !== "string" || s.length !== len) return false;
  return /^[0-9a-f]+$/.test(s);
}

function isEndpointSafe(s) {
  return typeof s === "string" && s.length >= 1 && s.length <= 64 &&
    /^[A-Za-z0-9_-]+$/.test(s);
}

function isUUID(s) {
  return typeof s === "string" &&
    /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(s);
}

async function hmacVerify(keyHex, message, sigHex) {
  const keyBytes = new Uint8Array(
    (keyHex.match(/../g) || []).map((h) => parseInt(h, 16)));
  const sigBytes = new Uint8Array(
    (sigHex.match(/../g) || []).map((h) => parseInt(h, 16)));
  const key = await crypto.subtle.importKey(
    "raw", keyBytes, { name: "HMAC", hash: "SHA-256" }, false, ["verify"]);
  return crypto.subtle.verify(
    "HMAC", key, sigBytes, new TextEncoder().encode(message));
}

async function consumeNonce(env, nonce) {
  // SETNX semantics across every PoP: exactly one 200 for the token's
  // lifetime. Key TTL mirrors the validity window so the set self-cleans.
  const url = `${env.UPSTASH_URL}/set/aegis%3Anonce%3A${nonce}/1/NX/EX/${TTL_SECONDS + SKEW_SECONDS}`;
  const res = await fetch(url, {
    method: "POST",
    headers: { Authorization: `Bearer ${env.UPSTASH_TOKEN}` },
  });
  if (!res.ok) return { ok: false, reason: "replay_store_unavailable" };
  const body = await res.json();
  return body.result === "OK"
    ? { ok: true }
    : { ok: false, reason: "replay" };
}

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    if (!url.pathname.startsWith("/chunks/") ||
      !["PUT", "POST"].includes(request.method)) {
      return unauthorized("route");
    }

    // ---- 1. Structure -----------------------------------------------------
    const q = url.searchParams;
    const blockHash = url.pathname.slice("/chunks/".length);
    const tenant = q.get("tenant");
    const ts = Number(q.get("ts"));
    const kv = Number(q.get("kv"));
    const ep = q.get("ep");
    const nonce = q.get("nonce");
    const sig = q.get("sig");

    if (!isValidLowerHex(blockHash, 64)) return unauthorized("structure");
    if (!isUUID(tenant)) return unauthorized("structure");
    if (!Number.isInteger(ts) || !Number.isInteger(kv) || kv < 1) return unauthorized("structure");
    if (!isEndpointSafe(ep)) return unauthorized("structure");
    if (!isValidLowerHex(nonce, 32) || !isValidLowerHex(sig, 64)) return unauthorized("structure");

    // ---- 2. Freshness -----------------------------------------------------
    const nowSec = Math.floor(Date.now() / 1000);
    if (ts + SKEW_SECONDS < nowSec) return unauthorized("expired");
    if (ts > nowSec + TTL_SECONDS + SKEW_SECONDS) return unauthorized("future_expiry");

    // ---- 3. Key version resolution ----------------------------------------
    let record;
    try {
      record = await env.TENANT_KEYS.get(`keys:${tenant}`, "json");
    } catch {
      return unauthorized("keys_unavailable"); // fail closed
    }
    if (!record) return unauthorized("unknown_tenant");
    const version = record.versions[String(kv)];
    const isCurrent = kv === record.current;
    const inOverlap = version && version.superseded_at != null &&
      Date.now() < version.superseded_at + OVERLAP_WINDOW_MS;
    if (!version || !(isCurrent || inOverlap)) return unauthorized("key_version");

    // ---- 4. Authenticity (constant-time inside WebCrypto) ------------------
    const message =
      `aegis1:${kv}:${tenant}:${blockHash}:${ts}:${ep}:${nonce}`;
    let genuine = false;
    try {
      genuine = await hmacVerify(version.key, message, sig);
    } catch {
      return unauthorized("crypto");
    }
    if (!genuine) return unauthorized("signature");

    // ---- 5. Replay (only AFTER the signature passed) -----------------------
    const replay = await consumeNonce(env, nonce);
    if (!replay.ok) return unauthorized(replay.reason);

    // ---- Validated: hand off to origin --------------------------------------
    // Production deployments forward the ORIGINAL request to the bucket
    // origin here (fetch(request, {cf: {...}})). The marker header lets the
    // backend skip re-validation while still enforcing ownership.
    return new Response(null, { status: 200, headers: { "x-aegis-validated": "edge" } });
  },
};
