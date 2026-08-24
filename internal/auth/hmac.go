// ============================================================================
// Project Aegis — HMAC pre-signed upload tokens (the security perimeter).
// PROMPT 3.2 (Cryptographic Ingress & HMAC Token System).
//
// Token layout
//
//	message = "aegis1:" ver ":" tenant ":" block_hash ":" exp ":" endpoint ":" nonce
//	sig     = hex(HMAC_SHA256(key = KMS_key[tenant, ver], message))
//	URL     = {base}/chunks/{block_hash}
//	             ?tenant={tenant}&ts={exp}&kv={ver}&ep={endpoint}&nonce={nonce}&sig={sig}
//
// Security properties (each enforced in ValidateToken, in this order):
//
//  1. STRUCTURE — every field is strictly format-validated before use. All
//     fields are colon-free by construction (UUID / lowercase-hex /
//     decimal / [A-Za-z0-9_-]), which makes the colon-joined message
//     unambiguous: no field can masquerade as a separator or another field.
//     The "aegis1" domain-separation prefix stops signature reuse across
//     protocols.
//  2. FRESHNESS — ts is the expiry (issued_at + TTL). Tokens are rejected
//     when expired; absurdly-future expiries beyond TTL+skew are rejected
//     too so a leaked signing oracle could not mint long-lived tokens.
//  3. AUTHENTICITY — the key version is part of the signed message, so
//     version downgrade is signature-breaking, not just a lookup change.
//     Signature comparison is constant-time via crypto/hmac.Equal.
//  4. REPLAY — the random nonce is consumed exactly once (SETNX semantics)
//     AFTER the signature passes; failures here never burn valid nonces.
//  5. ISOLATION — tenant is inside the message AND the key is per-tenant:
//     cross-tenant replay fails twice over (wrong key → sig mismatch).
//     An optional OwnershipChecker additionally proves the tenant owns the
//     block where a backend-side validator is available.
//
// Any failure surfaces as (false, wrapped sentinel error). Callers at the
// edge MUST map every failure to 401 and never leak which check failed.
// ============================================================================
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Policy constants & sentinels
// ---------------------------------------------------------------------------

const (
	// DefaultTokenTTL is the mandated 15-minute validity window.
	DefaultTokenTTL = 15 * time.Minute

	// DefaultClockSkew tolerates edge/origin clock drift in both directions.
	DefaultClockSkew = time.Minute

	// NonceSize is the random byte count per token (hex-encoded on the wire).
	NonceSize = 16

	// msgDomain separates these signatures from any other HMAC use of the
	// same keys ("1" = message format revision).
	msgDomain = "aegis1"

	defaultBaseURL = "https://blob-storage"
)

var (
	ErrMalformed        = errors.New("auth: malformed token")
	ErrExpired          = errors.New("auth: token expired")
	ErrTooFarInFuture   = errors.New("auth: token expiry too far in future")
	ErrUnknownKeyVer    = errors.New("auth: unknown or retired key version")
	ErrBadSignature     = errors.New("auth: signature mismatch")
	ErrReplay           = errors.New("auth: nonce already used")
	ErrNotTenantBlock   = errors.New("auth: tenant does not own block")
	ErrNonceUnavailable = errors.New("auth: replay store unavailable")
)

// ---------------------------------------------------------------------------
// Claims
// ---------------------------------------------------------------------------

// Claims carries every signed field plus the key version used.
type Claims struct {
	TenantID   uuid.UUID
	BlockHash  string // lowercase hex SHA-256, 64 chars
	ExpiryUnix int64  // seconds; token invalid past this instant
	KeyVersion int
	EndpointID string // edge PoP the URL was minted for
	NonceHex   string // 32 lowercase hex chars (16 random bytes)
}

// message builds the canonical signed payload. Field validation happens in
// validate() — callers must only feed it validated claims.
func (c Claims) message() []byte {
	return []byte(msgDomain + ":" +
		strconv.Itoa(c.KeyVersion) + ":" +
		c.TenantID.String() + ":" +
		c.BlockHash + ":" +
		strconv.FormatInt(c.ExpiryUnix, 10) + ":" +
		c.EndpointID + ":" +
		c.NonceHex)
}

func (c Claims) valid() error {
	switch {
	case c.BlockHash == "":
		return fmt.Errorf("%w: empty block hash", ErrMalformed)
	case len(c.BlockHash) != 64 || !isLowerHex(c.BlockHash):
		return fmt.Errorf("%w: block hash must be 64 lowercase hex chars", ErrMalformed)
	case c.EndpointID == "" || len(c.EndpointID) > 64 || !isEndpointSafe(c.EndpointID):
		return fmt.Errorf("%w: endpoint id charset", ErrMalformed)
	case c.KeyVersion < 1 || c.KeyVersion > 1_000_000:
		return fmt.Errorf("%w: key version out of range", ErrMalformed)
	case len(c.NonceHex) != NonceSize*2 || !isLowerHex(c.NonceHex):
		return fmt.Errorf("%w: nonce encoding", ErrMalformed)
	}
	return nil
}

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func isEndpointSafe(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		alnum := c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
		if !alnum && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Ownership verification hook (backend-side defense in depth)
// ---------------------------------------------------------------------------

// BlockOwnershipChecker proves the tenant↔block binding where a validator
// can reach the metadata store. Edge validators leave this nil: they enforce
// structure/freshness/authenticity/replay, and the first backend touch
// re-checks ownership (documented layering — see docs/ingress-security.md).
type BlockOwnershipChecker interface {
	OwnsBlock(ctx context.Context, tenantID uuid.UUID, blockHash string) (bool, error)
}

// ---------------------------------------------------------------------------
// TokenGenerator
// ---------------------------------------------------------------------------

// TokenGenerator mints and validates pre-signed upload URLs.
type TokenGenerator struct {
	kms       KMSClient
	nonces    NonceStore
	now       func() time.Time
	ttl       time.Duration
	skew      time.Duration
	baseURL   string
	ownership BlockOwnershipChecker
}

// Option configures a TokenGenerator.
type Option func(*TokenGenerator)

// WithClock overrides the time source (tests rotate time explicitly).
func WithClock(now func() time.Time) Option {
	return func(t *TokenGenerator) { t.now = now }
}

// WithTTL overrides the validity window.
func WithTTL(d time.Duration) Option {
	return func(t *TokenGenerator) { t.ttl = d }
}

// WithSkew overrides clock-drift tolerance.
func WithSkew(d time.Duration) Option {
	return func(t *TokenGenerator) { t.skew = d }
}

// WithBaseURL overrides the storage host baked into generated URLs.
func WithBaseURL(u string) Option {
	return func(t *TokenGenerator) { t.baseURL = u }
}

// WithOwnershipChecker installs backend-side tenant↔block verification.
func WithOwnershipChecker(o BlockOwnershipChecker) Option {
	return func(t *TokenGenerator) { t.ownership = o }
}

// NewTokenGenerator wires the perimeter together. Both dependencies are
// mandatory: an HMAC without KMS-backed keys or without anti-replay would
// be theater.
func NewTokenGenerator(kms KMSClient, nonces NonceStore, opts ...Option) *TokenGenerator {
	t := &TokenGenerator{
		kms:     kms,
		nonces:  nonces,
		now:     time.Now,
		ttl:     DefaultTokenTTL,
		skew:    DefaultClockSkew,
		baseURL: defaultBaseURL,
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// ---------------------------------------------------------------------------
// Signing
// ---------------------------------------------------------------------------

// GeneratePreSignedURL mints a single-use upload URL for one tenant/block
// pair, scoped to one endpoint, valid for the TTL window starting now.
func (t *TokenGenerator) GeneratePreSignedURL(
	ctx context.Context,
	tenantID uuid.UUID,
	blockHash string,
	endpointID string,
) (string, error) {
	claims, err := t.SignClaims(ctx, tenantID, blockHash, endpointID)
	if err != nil {
		return "", err
	}
	return claims.URL(t.baseURL), nil
}

// SignClaims computes the signed claim set (exposed separately for tests
// and for callers that want the raw query parameters rather than a URL).
func (t *TokenGenerator) SignClaims(
	ctx context.Context,
	tenantID uuid.UUID,
	blockHash string,
	endpointID string,
) (SignedToken, error) {
	version, key, err := t.kms.SigningKey(ctx, tenantID)
	if err != nil {
		return SignedToken{}, fmt.Errorf("auth: signing key: %w", err)
	}
	if len(key) < 32 {
		return SignedToken{}, fmt.Errorf("auth: tenant key too weak (%d bytes)", len(key))
	}

	nonce := make([]byte, NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return SignedToken{}, fmt.Errorf("auth: nonce entropy: %w", err)
	}

	st := SignedToken{
		Claims: Claims{
			TenantID:   tenantID,
			BlockHash:  blockHash,
			ExpiryUnix: t.now().Add(t.ttl).Unix(),
			KeyVersion: version,
			EndpointID: endpointID,
			NonceHex:   hex.EncodeToString(nonce),
		},
	}
	if err := st.Claims.valid(); err != nil {
		return SignedToken{}, err // e.g. caller-supplied hash with bad charset
	}
	st.SignatureHex = hex.EncodeToString(computeMAC(key, st.Claims.message()))
	return st, nil
}

// computeMAC is the single HMAC site for both directions.
func computeMAC(key, message []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(message)
	return mac.Sum(nil)
}

// ---------------------------------------------------------------------------
// Wire format helpers
// ---------------------------------------------------------------------------

// SignedToken is the full on-the-wire artifact.
type SignedToken struct {
	Claims       Claims
	SignatureHex string
}

// Query serializes to the canonical query parameter set.
func (s SignedToken) Query() url.Values {
	q := url.Values{}
	q.Set("tenant", s.Claims.TenantID.String())
	q.Set("ts", strconv.FormatInt(s.Claims.ExpiryUnix, 10))
	q.Set("kv", strconv.Itoa(s.Claims.KeyVersion))
	q.Set("ep", s.Claims.EndpointID)
	q.Set("nonce", s.Claims.NonceHex)
	q.Set("sig", s.SignatureHex)
	return q
}

// URL renders the pre-signed URL exactly as the ingress contract defines.
func (s SignedToken) URL(base string) string {
	return fmt.Sprintf("%s/chunks/%s?%s", base, s.Claims.BlockHash, s.Query().Encode())
}

// ParsePreSignedURL parses and structurally validates the wire artifact.
// It does NOT verify anything cryptographic — that is Validate's job.
func ParsePreSignedURL(raw string) (SignedToken, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return SignedToken{}, fmt.Errorf("%w: url parse", ErrMalformed)
	}
	q := u.Query()
	var st SignedToken
	if seg := u.Path; len(seg) > len("/chunks/") {
		st.Claims.BlockHash = seg[len("/chunks/"):]
	}
	st.Claims.TenantID, err = uuid.Parse(q.Get("tenant"))
	if err != nil {
		return SignedToken{}, fmt.Errorf("%w: tenant id", ErrMalformed)
	}
	if st.Claims.ExpiryUnix, err = strconv.ParseInt(q.Get("ts"), 10, 64); err != nil {
		return SignedToken{}, fmt.Errorf("%w: timestamp", ErrMalformed)
	}
	if st.Claims.KeyVersion, err = strconv.Atoi(q.Get("kv")); err != nil {
		return SignedToken{}, fmt.Errorf("%w: key version", ErrMalformed)
	}
	st.Claims.EndpointID = q.Get("ep")
	st.Claims.NonceHex = q.Get("nonce")
	st.SignatureHex = q.Get("sig")
	if len(st.SignatureHex) != sha256.Size*2 || !isLowerHex(st.SignatureHex) {
		return SignedToken{}, fmt.Errorf("%w: signature encoding", ErrMalformed)
	}
	if err := st.Claims.valid(); err != nil {
		return SignedToken{}, err
	}
	return st, nil
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------
//
// Convenience wrapper matching the interface contract in the design doc;
// prefer Validate for full control.
func (t *TokenGenerator) ValidateToken(
	ctx context.Context,
	tenantID uuid.UUID,
	blockHash string,
	signature string,
	timestamp int64,
	keyVersion int,
	endpointID string,
	nonceHex string,
) (bool, error) {
	return t.Validate(ctx, SignedToken{
		Claims: Claims{
			TenantID:   tenantID,
			BlockHash:  blockHash,
			ExpiryUnix: timestamp,
			KeyVersion: keyVersion,
			EndpointID: endpointID,
			NonceHex:   nonceHex,
		},
		SignatureHex: signature,
	})
}

// Validate runs the full gauntlet. Order matters: cheap structural checks
// first, then freshness, then key resolution, then the MAC, and ONLY THEN
// the nonce consumption (so failed guesses cannot burn valid nonces), and
// finally the optional ownership proof.
func (t *TokenGenerator) Validate(ctx context.Context, tok SignedToken) (bool, error) {
	// 1. Structure.
	if err := tok.Claims.valid(); err != nil {
		return false, err
	}
	if len(tok.SignatureHex) != sha256.Size*2 || !isLowerHex(tok.SignatureHex) {
		return false, fmt.Errorf("%w: signature encoding", ErrMalformed)
	}

	// 2. Freshness: expired, or expiry implausibly far out.
	now := t.now().Unix()
	if tok.Claims.ExpiryUnix+int64(t.skew.Seconds()) < now {
		return false, ErrExpired
	}
	maxExpiry := now + int64((t.ttl + t.skew).Seconds())
	if tok.Claims.ExpiryUnix > maxExpiry {
		return false, ErrTooFarInFuture
	}

	// 3. Resolve verification key: current version, else overlap-active.
	version := tok.Claims.KeyVersion
	key, active, err := t.kms.VerificationKey(ctx, tok.Claims.TenantID, version)
	if err != nil {
		return false, fmt.Errorf("auth: verification key: %w", err)
	}
	if !active {
		return false, fmt.Errorf("%w: v%d", ErrUnknownKeyVer, version)
	}

	// 4. Authenticity — constant-time comparison.
	want := computeMAC(key, tok.Claims.message())
	got, err := hex.DecodeString(tok.SignatureHex)
	if err != nil {
		return false, fmt.Errorf("%w: signature decode", ErrMalformed)
	}
	if !hmac.Equal(want, got) {
		return false, ErrBadSignature
	}

	// 5. Replay: consume the nonce exactly once.
	ok, err := t.nonces.Consume(ctx, tok.Claims.NonceHex, t.ttl+t.skew)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrNonceUnavailable, err)
	}
	if !ok {
		return false, ErrReplay
	}

	// 6. Ownership (backend-side only; nil at the edge).
	if t.ownership != nil {
		owns, err := t.ownership.OwnsBlock(ctx, tok.Claims.TenantID, tok.Claims.BlockHash)
		if err != nil {
			return false, fmt.Errorf("auth: ownership probe: %w", err)
		}
		if !owns {
			return false, ErrNotTenantBlock
		}
	}

	return true, nil
}
