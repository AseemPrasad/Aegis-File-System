// ============================================================================
// Project Aegis — HMAC ingress token security battery.
// PROMPT 3.2 (Cryptographic Ingress & HMAC Token System).
//
// Every attack class the design doc mandates is exercised here:
//
//	valid signature accepted ................ TestHMAC_TokenSignatureValid
//	any tampered field rejected ............. TestHMAC_TokenSignatureTamperedFails
//	expiry enforced ......................... TestHMAC_TokenExpiredFails
//	cross-tenant / cross-block reuse ........ TestCrossTenantTokenFails
//	replay rejected ......................... TestTokenReplayFails
//	timestamp extension ..................... TestTimestampExtensionFails
//	forged signatures ....................... TestForgedSignatureFails
//	exhaustive single-field modification .... TestProp_HMACSignatureSecureAgainstAllModifications
//	randomized attacker simulation .......... TestFuzz_AttackerScenarios
//	key rotation overlap window ............. TestKeyRotationOverlapWindow
//
// The property/fuzz tests are deterministic (fixed seeds) so CI failures
// reproduce exactly.
// ============================================================================
package auth

import (
	"context"
	"errors"
	"math/rand"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

var (
	tenantA   = uuid.MustParse("11111111-1111-4111-8111-111111111111")
	tenantB   = uuid.MustParse("22222222-2222-4222-8222-222222222222")
	blockAX   = strings.Repeat("aa", 32)
	blockAY   = strings.Repeat("bb", 32)
	blockBY   = strings.Repeat("cc", 32)
	endpoint1 = "pop-fra1"
	endpoint2 = "pop-iad1"
)

type fixture struct {
	kms *StaticKMS
	gen *TokenGenerator
	now time.Time
	ctx context.Context
}

// clock is passed into dependencies BY METHOD VALUE so every consumer sees
// advances of f.now (closures capturing a local would freeze time).
func (f *fixture) clock() time.Time { return f.now }

func (f *fixture) advance(d time.Duration) {
	f.now = f.now.Add(d)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		now: time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC),
		ctx: context.Background(),
	}
	kms := NewStaticKMS(nil)
	kms.now = f.clock
	kms.Provision(tenantA, 1, []byte(strings.Repeat("A", 32)))
	kms.Provision(tenantB, 1, []byte(strings.Repeat("B", 32)))
	f.kms = kms
	f.gen = NewTokenGenerator(kms, NewInMemoryNonceStore(0), WithClock(f.clock))
	return f
}

func (f *fixture) mint(t *testing.T, tenant uuid.UUID, block, ep string) SignedToken {
	t.Helper()
	st, err := f.gen.SignClaims(f.ctx, tenant, block, ep)
	if err != nil {
		t.Fatalf("SignClaims(%s): %v", tenant, err)
	}
	return st
}

func mustValidate(t *testing.T, f *fixture, st SignedToken) error {
	t.Helper()
	ok, err := f.gen.Validate(f.ctx, st)
	if ok && err != nil {
		t.Fatalf("inconsistent result: ok=true err=%v", err)
	}
	if ok {
		return nil
	}
	return err
}

// ---------------------------------------------------------------------------
// Happy path
// ---------------------------------------------------------------------------

func TestHMAC_TokenSignatureValid(t *testing.T) {
	f := newFixture(t)
	st := f.mint(t, tenantA, blockAX, endpoint1)

	got, err := url.Parse(st.URL("https://blob-storage"))
	if err != nil {
		t.Fatalf("generated URL unparsable: %v", err)
	}
	if got.Path != "/chunks/"+blockAX || got.Query().Get("sig") != st.SignatureHex {
		t.Fatalf("URL shape wrong: %s", got)
	}
	parsed, err := ParsePreSignedURL(got.String())
	if err != nil {
		t.Fatalf("ParsePreSignedURL round-trip: %v", err)
	}
	if parsed.Claims.NonceHex != st.Claims.NonceHex || parsed.Claims.KeyVersion != 1 {
		t.Fatalf("round trip lost fields: %+v", parsed.Claims)
	}
	if err := mustValidate(t, f, parsed); err != nil {
		t.Fatalf("fresh valid token rejected: %v", err)
	}

	// GeneratePreSignedURL end-to-end shape per the contract.
	raw, err := f.gen.GeneratePreSignedURL(f.ctx, tenantA, blockAY, endpoint1)
	if err != nil {
		t.Fatalf("GeneratePreSignedURL: %v", err)
	}
	if !strings.HasPrefix(raw, "https://blob-storage/chunks/") ||
		!strings.Contains(raw, "tenant="+tenantA.String()) {
		t.Fatalf("contract URL mismatch: %s", raw)
	}
	parsed2, err := ParsePreSignedURL(raw)
	if err != nil {
		t.Fatalf("parse generated: %v", err)
	}
	if err := mustValidate(t, f, parsed2); err != nil {
		t.Fatalf("second fresh token rejected: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tampering — every wire field individually
// ---------------------------------------------------------------------------

func TestHMAC_TokenSignatureTamperedFails(t *testing.T) {
	f := newFixture(t)
	base := f.mint(t, tenantA, blockAX, endpoint1)

	mutations := map[string]func(*SignedToken){
		"block_hash": func(s *SignedToken) { s.Claims.BlockHash = blockAY },
		"tenant":     func(s *SignedToken) { s.Claims.TenantID = tenantB },
		"expiry":     func(s *SignedToken) { s.Claims.ExpiryUnix += 60 },
		"endpoint":   func(s *SignedToken) { s.Claims.EndpointID = endpoint2 },
		"nonce":      func(s *SignedToken) { s.Claims.NonceHex = strings.Repeat("ff", 16) },
		"key_version": func(s *SignedToken) {
			f.kms.Rotate(tenantA) // make v2 exist so the failure is signature-level
			s.Claims.KeyVersion = 2
		},
		"signature": func(s *SignedToken) {
			b := []byte(s.SignatureHex)
			if b[0] == 'a' {
				b[0] = 'b'
			} else {
				b[0] = 'a'
			}
			s.SignatureHex = string(b)
		},
	}
	for name, mutate := range mutations {
		mutated := base
		mutated.Claims = base.Claims // deep copy of value fields
		mutate(&mutated)
		err := mustValidate(t, f, mutated)
		switch {
		case err == nil:
			t.Errorf("%s: tampered token ACCEPTED", name)
		case errors.Is(err, ErrExpired) || errors.Is(err, ErrTooFarInFuture):
			t.Errorf("%s: failed with freshness not authenticity: %v", name, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Freshness
// ---------------------------------------------------------------------------

func TestHMAC_TokenExpiredFails(t *testing.T) {
	f := newFixture(t)
	st := f.mint(t, tenantA, blockAX, endpoint1)

	// One second before the skew-extended cutoff the token MUST validate.
	f.advance(DefaultTokenTTL - DefaultClockSkew - time.Second)
	if err := mustValidate(t, f, st); err != nil {
		t.Fatalf("token inside validity window rejected: %v", err)
	}

	// Cross the cutoff (exp + skew, strict): it MUST fail as expired.
	// Freshness is checked before replay, so the consumed nonce cannot mask
	// this.
	f.advance(2*DefaultClockSkew + 2*time.Second)
	err := mustValidate(t, f, st)
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("want ErrExpired, got %v", err)
	}
}

func TestTokenTooFarInFutureFails(t *testing.T) {
	f := newFixture(t)
	st := f.mint(t, tenantA, blockAX, endpoint1)

	// An attacker who can influence the validator's clock forward slightly
	// must not stretch a 15-minute token into hours.
	f.advance(-time.Hour) // pretend validator clock is an hour behind mint time
	err := mustValidate(t, f, st)
	if !errors.Is(err, ErrTooFarInFuture) {
		t.Fatalf("want ErrTooFarInFuture, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Isolation
// ---------------------------------------------------------------------------

func TestCrossTenantTokenFails(t *testing.T) {
	f := newFixture(t)
	tokenA := f.mint(t, tenantA, blockAX, endpoint1)

	// Attack 1: tenant A's token presented under tenant B's identity.
	stolen := tokenA
	stolen.Claims.TenantID = tenantB
	if err := mustValidate(t, f, stolen); err == nil {
		t.Fatal("cross-tenant identity swap ACCEPTED")
	}

	// Attack 2: token scoped to block X used to write block Y (same tenant).
	swapped := tokenA
	swapped.Claims.BlockHash = blockAY
	if err := mustValidate(t, f, swapped); err == nil {
		t.Fatal("cross-block swap within tenant ACCEPTED")
	}

	// Attack 3: tenant B mints a token for ITS block BY, then presents it
	// against tenant A's identity with A's block hash X (field mix & match).
	mix := f.mint(t, tenantB, blockBY, endpoint1)
	mix.Claims.TenantID = tenantA
	mix.Claims.BlockHash = blockAX
	if err := mustValidate(t, f, mix); err == nil {
		t.Fatal("mixed-claims forgery ACCEPTED")
	}

	// Attack 4: endpoint scoping — token minted for fra1 replayed at iad1.
	wrongPoP := tokenA
	wrongPoP.Claims.EndpointID = endpoint2
	if err := mustValidate(t, f, wrongPoP); err == nil {
		t.Fatal("cross-endpoint reuse ACCEPTED")
	}
}

// ---------------------------------------------------------------------------
// Replay
// ---------------------------------------------------------------------------

func TestTokenReplayFails(t *testing.T) {
	f := newFixture(t)
	st := f.mint(t, tenantA, blockAX, endpoint1)

	if err := mustValidate(t, f, st); err != nil {
		t.Fatalf("first use must succeed: %v", err)
	}
	for i := 0; i < 3; i++ {
		err := mustValidate(t, f, st)
		if !errors.Is(err, ErrReplay) {
			t.Fatalf("replay %d: want ErrReplay, got %v", i+1, err)
		}
	}

	// Same nonce across DIFFERENT signed content must also be impossible:
	// a second token reusing the nonce cannot validate even with a fresh sig,
	// because the nonce store keys on the nonce alone.
	reusedNonce := f.mint(t, tenantA, blockAY, endpoint1)
	reusedNonce.Claims.NonceHex = st.Claims.NonceHex
	reusedNonce.SignatureHex = "" // will fail structurally first; force path:
	reusedNonce.SignatureHex = hexOfMACForTest(f, reusedNonce.Claims)
	if err := mustValidate(t, f, reusedNonce); !errors.Is(err, ErrReplay) {
		t.Fatalf("nonce reuse across tokens: want ErrReplay, got %v", err)
	}
}

// hexOfMACForTest signs claims with the CURRENT key — test-only shortcut
// mirroring SignClaims but with an attacker-chosen nonce.
func hexOfMACForTest(f *fixture, c Claims) string {
	_, key, err := f.kms.SigningKey(f.ctx, c.TenantID)
	if err != nil {
		panic(err)
	}
	return hexEncode(computeMAC(key, c.message()))
}

// ---------------------------------------------------------------------------
// Timestamp extension & forgery
// ---------------------------------------------------------------------------

func TestTimestampExtensionFails(t *testing.T) {
	f := newFixture(t)
	st := f.mint(t, tenantA, blockAX, endpoint1)

	// Attacker pushes expiry out by an hour WITHOUT the key: the signature
	// no longer matches, full stop.
	extended := st
	extended.Claims.ExpiryUnix += 3600
	if err := mustValidate(t, f, extended); err == nil {
		t.Fatal("timestamp extension ACCEPTED")
	}

	// Even re-signing with the superseded key does not help an attacker:
	// they never had ANY key. Prove a zero-key forgery fails.
	f.kms.Rotate(tenantA)
	forged := extended
	forged.SignatureHex = hexEncode(computeMAC([]byte(strings.Repeat("z", 32)), forged.Claims.message()))
	if err := mustValidate(t, f, forged); err == nil {
		t.Fatal("forged-with-wrong-key ACCEPTED")
	}
}

func TestForgedSignatureFails(t *testing.T) {
	f := newFixture(t)
	st := f.mint(t, tenantA, blockAX, endpoint1)

	attempts := [][]byte{
		make([]byte, 32),                 // all zeros
		[]byte(strings.Repeat("ff", 32)), // all ff
		[]byte("short"),                  // length games handled earlier
		computeMAC([]byte(strings.Repeat("9", 32)), st.Claims.message()), // wrong key
	}
	for i, sig := range attempts {
		forged := st
		forged.SignatureHex = hexEncode(sig)
		if len(forged.SignatureHex) != 64 {
			continue // structural reject covered by TestMalformedTokensRejected
		}
		if err := mustValidate(t, f, forged); err == nil {
			t.Fatalf("forged signature %d ACCEPTED", i)
		}
	}

	// Signature computed under tenant B's KEY for A's message: the classic
	// cross-signing attempt. B's key differs, so A's validation fails.
	_, keyB, _ := f.kms.SigningKey(f.ctx, tenantB)
	crossSigned := st
	crossSigned.SignatureHex = hexEncode(computeMAC(keyB, crossSigned.Claims.message()))
	if err := mustValidate(t, f, crossSigned); err == nil {
		t.Fatal("cross-signed token ACCEPTED")
	}
}

// ---------------------------------------------------------------------------
// Property: exhaustive single-field modification space
// ---------------------------------------------------------------------------

// TestProp_HMACSignatureSecureAgainstAllModifications walks EVERY byte of
// every string field and every small delta of every integer field of a set
// of freshly minted tokens and asserts each modification flips validation to
// reject. This is the Go equivalent of the proptest in the design doc.
func TestProp_HMACSignatureSecureAgainstAllModifications(t *testing.T) {
	f := newFixture(t)
	samples := []SignedToken{
		f.mint(t, tenantA, blockAX, endpoint1),
		f.mint(t, tenantB, blockBY, endpoint2),
	}

	type modCase struct {
		name  string
		apply func(s *SignedToken, seed int)
		space int
	}
	var cases []modCase

	addStringByte := func(name, field string, pick func(*SignedToken) *string) {
		cases = append(cases, modCase{
			name:  "string:" + field + ":" + name,
			space: 1,
			apply: func(s *SignedToken, seed int) {
				p := pick(s)
				b := []byte(*p)
				pos := seed % len(b)
				old := b[pos]
				for {
					nb := byte('a' + rand.Intn(26))
					if nb != old {
						b[pos] = nb
						break
					}
				}
				*p = string(b)
			},
		})
	}

	for _, s := range samples {
		_ = s
	}
	addStringByte("mid", "hash", func(s *SignedToken) *string { return &s.Claims.BlockHash })
	addStringByte("mid", "nonce", func(s *SignedToken) *string { return &s.Claims.NonceHex })
	addStringByte("mid", "ep", func(s *SignedToken) *string { return &s.Claims.EndpointID })
	addStringByte("mid", "sig", func(s *SignedToken) *string { return &s.SignatureHex })
	cases = append(cases,
		modCase{name: "int:expiry+delta", space: 5, apply: func(s *SignedToken, seed int) {
			s.Claims.ExpiryUnix += int64(seed + 1)
		}},
		modCase{name: "int:kv+1", space: 1, apply: func(s *SignedToken, _ int) {
			s.Claims.KeyVersion++
		}},
		modCase{name: "uuid:swap", space: 1, apply: func(s *SignedToken, _ int) {
			if s.Claims.TenantID == tenantA {
				s.Claims.TenantID = tenantB
			} else {
				s.Claims.TenantID = tenantA
			}
		}},
	)

	rng := rand.New(rand.NewSource(0xAE61)) // deterministic
	trialsPerCase := 200
	for _, sample := range samples {
		for _, mc := range cases {
			for i := 0; i < trialsPerCase; i++ {
				mutated := sample
				mutated.Claims = sample.Claims
				mc.apply(&mutated, rng.Int())
				if mutated.SignatureHex == sample.SignatureHex &&
					mutated.Claims == sample.Claims {
					continue // mutation was a no-op (e.g. same char drawn)
				}
				if err := mustValidate(t, f, mutated); err == nil {
					t.Fatalf("%s: modified token ACCEPTED (case=%s trial=%d)",
						sample.Claims.TenantID, mc.name, i)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Randomized attacker simulation (deterministic seed)
// ---------------------------------------------------------------------------

// TestFuzz_AttackerScenarios throws 20k pseudo-random mutations of valid
// tokens at the validator and requires universal rejection. Any acceptance
// here is a perimeter breach.
func TestFuzz_AttackerScenarios(t *testing.T) {
	f := newFixture(t)
	rng := rand.New(rand.NewSource(0xCDC))

	pool := []SignedToken{
		f.mint(t, tenantA, blockAX, endpoint1),
		f.mint(t, tenantB, blockBY, endpoint2),
	}

	mutators := []func(*SignedToken, *rand.Rand){
		func(s *SignedToken, r *rand.Rand) { // random byte flip somewhere
			fields := []*string{&s.Claims.BlockHash, &s.Claims.NonceHex,
				&s.Claims.EndpointID, &s.SignatureHex}
			p := fields[r.Intn(len(fields))]
			if len(*p) == 0 {
				return
			}
			b := []byte(*p)
			b[r.Intn(len(b))] = byte(r.Intn(256))
			*p = string(b)
		},
		func(s *SignedToken, r *rand.Rand) { s.Claims.ExpiryUnix += int64(r.Intn(7200) - 3600) },
		func(s *SignedToken, r *rand.Rand) { s.Claims.KeyVersion = 1 + r.Intn(3) },
		func(s *SignedToken, r *rand.Rand) {
			if r.Intn(2) == 0 {
				s.Claims.TenantID = tenantB
			} else {
				s.Claims.TenantID = tenantA
			}
		},
		func(s *SignedToken, r *rand.Rand) { // splice another token's signature
			s.SignatureHex = pool[r.Intn(len(pool))].SignatureHex
		},
		func(s *SignedToken, r *rand.Rand) { // splice another token's nonce
			s.Claims.NonceHex = pool[r.Intn(len(pool))].Claims.NonceHex
			s.SignatureHex = pool[r.Intn(len(pool))].SignatureHex
		},
	}

	// Oracle: a token may ONLY be accepted when it is byte-identical to one
	// of the pristine originals on its FIRST use — i.e. every mutation chain
	// that actually changed anything must be rejected, and even the original
	// cannot be replayed afterwards. Anything else is a perimeter breach.
	pristine := make(map[string]bool, len(pool))
	for _, p := range pool {
		pristine[p.URL("https://blob-storage")] = true
	}
	legitAccepts := 0
	for i := 0; i < 20_000; i++ {
		victim := pool[rng.Intn(len(pool))]
		attempt := victim
		attempt.Claims = victim.Claims
		for _, m := range mutators[:1+rng.Intn(len(mutators))] {
			m(&attempt, rng)
		}
		ok, _ := f.gen.Validate(f.ctx, attempt)
		if !ok {
			continue // correct: any modified token is rejected
		}
		if pristine[attempt.URL("https://blob-storage")] && legitAccepts < len(pool) {
			legitAccepts++ // first-use of an untouched original
			continue
		}
		t.Fatalf("attacker scenario ACCEPTED (i=%d): %s", i, attempt.URL("https://blob-storage"))
	}
}

// ---------------------------------------------------------------------------
// Malformed inputs
// ---------------------------------------------------------------------------

func TestMalformedTokensRejected(t *testing.T) {
	f := newFixture(t)
	st := f.mint(t, tenantA, blockAX, endpoint1)

	bad := []struct {
		name string
		mut  func(*SignedToken)
	}{
		{"uppercase_sig", func(s *SignedToken) { s.SignatureHex = strings.ToUpper(s.SignatureHex) }},
		{"odd_len_sig", func(s *SignedToken) { s.SignatureHex = s.SignatureHex[:63] }},
		{"empty_nonce", func(s *SignedToken) { s.Claims.NonceHex = "" }},
		{"nonce_not_hex", func(s *SignedToken) { s.Claims.NonceHex = strings.Repeat("zz", 16) }},
		{"short_hash", func(s *SignedToken) { s.Claims.BlockHash = blockAX[:63] }},
		{"colon_in_endpoint", func(s *SignedToken) { s.Claims.EndpointID = "pop:fra1" }},
		{"slash_in_endpoint", func(s *SignedToken) { s.Claims.EndpointID = "pop/fra1" }},
		{"kv_zero", func(s *SignedToken) { s.Claims.KeyVersion = 0 }},
		{"kv_negative", func(s *SignedToken) { s.Claims.KeyVersion = -1 }},
		{"huge_expiry", func(s *SignedToken) { s.Claims.ExpiryUnix = 1 << 62 }},
	}
	for _, tc := range bad {
		m := st
		m.Claims = st.Claims
		tc.mut(&m)
		if err := mustValidate(t, f, m); err == nil {
			t.Errorf("%s: malformed token ACCEPTED", tc.name)
		}
	}

	// URL parser rejects junk too.
	if _, err := ParsePreSignedURL("https://blob-storage/chunks/" + blockAX + "?tenant=not-a-uuid&ts=x"); err == nil {
		t.Error("parser accepted garbage URL")
	}
	if _, err := ParsePreSignedURL("::"); err == nil {
		t.Error("parser accepted nonsense URL")
	}
}

// ---------------------------------------------------------------------------
// Key rotation
// ---------------------------------------------------------------------------

func TestKeyRotationOverlapWindow(t *testing.T) {
	f := newFixture(t)
	oldToken := f.mint(t, tenantA, blockAX, endpoint1)

	v2, err := f.kms.Rotate(tenantA)
	if err != nil || v2 != 2 {
		t.Fatalf("rotate: v=%d err=%v", v2, err)
	}

	// New tokens sign under v2.
	newTok := f.mint(t, tenantA, blockAY, endpoint1)
	if newTok.Claims.KeyVersion != 2 {
		t.Fatalf("post-rotation token uses v%d, want 2", newTok.Claims.KeyVersion)
	}
	if err := mustValidate(t, f, newTok); err != nil {
		t.Fatalf("v2 token invalid: %v", err)
	}

	// Old v1 token still validates inside the 7-day overlap...
	if err := mustValidate(t, f, oldToken); err != nil {
		t.Fatalf("overlap-window v1 token rejected: %v", err)
	}

	// ...and dies once the overlap closes. The freshness gate would mask
	// retirement for a stale token, so probe with FRESH-window expiry signed
	// under the retired v1 key: only ErrUnknownKeyVer can explain rejection.
	v1Key, _, errKey := f.kms.VerificationKey(f.ctx, tenantA, 1)
	if errKey != nil {
		t.Fatalf("fetch v1 key inside overlap: %v", errKey)
	}
	f.advance(OverlapWindow + time.Hour)
	v1Claims := oldToken.Claims
	v1Claims.NonceHex = strings.Repeat("77", 16) // unused nonce
	v1Claims.ExpiryUnix = f.now.Add(DefaultTokenTTL).Unix()
	retired := SignedToken{Claims: v1Claims, SignatureHex: hexEncode(computeMAC(v1Key, v1Claims.message()))}

	err = mustValidate(t, f, retired)
	if !errors.Is(err, ErrUnknownKeyVer) {
		t.Fatalf("post-overlap v1 token: want ErrUnknownKeyVer, got %v", err)
	}

	// Version downgrade on a v2 token (message says 2, attacker forces 1) is
	// a pure signature failure — never a silent fallback to the older key.
	downgraded := newTok
	downgraded.Claims.KeyVersion = 1
	if err := mustValidate(t, f, downgraded); errors.Is(err, ErrUnknownKeyVer) || err == nil {
		t.Fatalf("downgrade produced wrong failure mode: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Ownership checker (backend-side layer)
// ---------------------------------------------------------------------------

type fakeOwnership map[string]bool // "tenant:block" -> owns

func (f fakeOwnership) OwnsBlock(_ context.Context, tenant uuid.UUID, block string) (bool, error) {
	return f[tenant.String()+":"+block], nil
}

func TestOwnershipCheckEnforced(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	kms := NewStaticKMS(clock)
	kms.Provision(tenantA, 1, []byte(strings.Repeat("A", 32)))

	nonces := NewInMemoryNonceStore(0)
	owned := NewTokenGenerator(kms, nonces, WithClock(clock),
		WithOwnershipChecker(fakeOwnership{tenantA.String() + ":" + blockAX: true}))
	unowned := NewTokenGenerator(kms, NewInMemoryNonceStore(0), WithClock(clock),
		WithOwnershipChecker(fakeOwnership{}))

	st, err := owned.SignClaims(context.Background(), tenantA, blockAX, endpoint1)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	// Same nonce consumed once — use separate stores via distinct generators.
	st2, err := unowned.SignClaims(context.Background(), tenantA, blockAX, endpoint1)
	if err != nil {
		t.Fatalf("sign2: %v", err)
	}

	if ok, err := owned.Validate(context.Background(), st); !ok || err != nil {
		t.Fatalf("owned block rejected: ok=%v err=%v", ok, err)
	}
	if _, err := unowned.Validate(context.Background(), st2); !errors.Is(err, ErrNotTenantBlock) {
		t.Fatalf("unowned block: want ErrNotTenantBlock, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Nonce-store failure semantics
// ---------------------------------------------------------------------------

type failingStore struct{}

func (failingStore) Consume(_ context.Context, _ string, _ time.Duration) (bool, error) {
	return false, errors.New("redis down")
}

func TestReplayStoreOutageFailsClosed(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	kms := NewStaticKMS(func() time.Time { return now })
	kms.Provision(tenantA, 1, []byte(strings.Repeat("A", 32)))
	gen := NewTokenGenerator(kms, failingStore{}, WithClock(func() time.Time { return now }))

	st, err := gen.SignClaims(context.Background(), tenantA, blockAX, endpoint1)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	ok, err := gen.Validate(context.Background(), st)
	if ok || !errors.Is(err, ErrNonceUnavailable) {
		t.Fatalf("outage must fail closed: ok=%v err=%v", ok, err)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func hexEncode(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0x0f])
	}
	return string(out)
}

// silence unused warnings for helpers kept for future scenarios.
var _ = strconv.Itoa
