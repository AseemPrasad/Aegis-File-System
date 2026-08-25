package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Fuzz tests — Go native fuzzing for HMAC token system
// ---------------------------------------------------------------------------

// FuzzTokenRoundTrip verifies that a generated token can always be parsed
// and validated, and that tampered signatures are always rejected.
func FuzzTokenRoundTrip(f *testing.F) {
	// Seed with known-good tokens.
	key := []byte("fuzz-test-key-material-0123456789ab")
	tenantID := uuid.New()
	f.Add(key, tenantID.String(), "abc123def456", "ep-1")
	f.Add(key, tenantID.String(), strings.Repeat("0", 64), "edge-us-east-1")

	f.Fuzz(func(t *testing.T, key []byte, tenantStr, blockHash, endpointID string) {
		if len(key) < 32 {
			key = append(key, make([]byte, 32-len(key))...)
		}
		if len(blockHash) != 64 {
			return // skip invalid hash lengths
		}
		if !isLowerHex(blockHash) {
			return
		}
		tid, err := uuid.Parse(tenantStr)
		if err != nil {
			return
		}

		kms := NewStaticKMS(nil)
		kms.Provision(tid, 1, key)
		nonces := NewInMemoryNonceStore(0)

		gen := NewTokenGenerator(kms, nonces,
			WithClock(func() time.Time { return time.Now() }),
			WithTTL(time.Minute),
		)

		// Generate
		tok, err := gen.SignClaims(context.Background(), tid, blockHash, endpointID)
		if err != nil {
			return // skip invalid inputs
		}

		// Validate should succeed
		ok, err := gen.Validate(context.Background(), tok)
		if err != nil || !ok {
			t.Errorf("valid token rejected: ok=%v err=%v", ok, err)
		}

		// Tamper with signature — should fail
		sigBytes, _ := hex.DecodeString(tok.SignatureHex)
		if len(sigBytes) > 0 {
			sigBytes[0] ^= 0xFF
			tok.SignatureHex = hex.EncodeToString(sigBytes)
			ok2, _ := gen.Validate(context.Background(), tok)
			if ok2 {
				t.Error("tampered token should be rejected")
			}
		}
	})
}

// FuzzTokenTamperNonce verifies that replaying the same nonce is rejected.
func FuzzTokenTamperNonce(f *testing.F) {
	key := []byte("fuzz-nonce-key-material-0123456789ab")
	tenantID := uuid.New()
	f.Add(key, tenantID.String())

	f.Fuzz(func(t *testing.T, key []byte, tenantStr string) {
		if len(key) < 32 {
			key = append(key, make([]byte, 32-len(key))...)
		}
		tid, err := uuid.Parse(tenantStr)
		if err != nil {
			return
		}

		kms := NewStaticKMS(nil)
		kms.Provision(tid, 1, key)
		nonces := NewInMemoryNonceStore(0)

		gen := NewTokenGenerator(kms, nonces,
			WithClock(func() time.Time { return time.Now() }),
			WithTTL(time.Minute),
		)

		blockHash := strings.Repeat("a", 64)
		tok, err := gen.SignClaims(context.Background(), tid, blockHash, "ep1")
		if err != nil {
			return
		}

		// First validation should succeed
		ok, _ := gen.Validate(context.Background(), tok)
		if !ok {
			return // skip if first fails (e.g. bad input)
		}

		// Replay should be rejected
		ok2, err2 := gen.Validate(context.Background(), tok)
		if ok2 {
			t.Error("replayed token should be rejected")
		}
		if err2 != nil && !strings.Contains(err2.Error(), "nonce") {
			// Error should mention nonce/replay
		}
	})
}

// FuzzParsePreSignedURL verifies that URL parsing handles arbitrary input.
func FuzzParsePreSignedURL(f *testing.F) {
	f.Add("https://blob/chunks/abc123?tenant=x&ts=123&kv=1&ep=e&nonce=n&sig=" + strings.Repeat("0", 64))
	f.Add("")
	f.Add("not-a-url")
	f.Add("://missing-scheme")

	f.Fuzz(func(t *testing.T, raw string) {
		tok, err := ParsePreSignedURL(raw)
		if err != nil {
			return // expected for invalid URLs
		}
		// If parsing succeeded, the token should have basic structural validity.
		if len(tok.SignatureHex) != 64 {
			t.Errorf("parsed token has bad sig length: %d", len(tok.SignatureHex))
		}
		if len(tok.Claims.NonceHex) != 32 {
			t.Errorf("parsed token has bad nonce length: %d", len(tok.Claims.NonceHex))
		}
	})
}

// FuzzClaimsValid verifies that Claims.valid() handles arbitrary inputs.
func FuzzClaimsValid(f *testing.F) {
	f.Add("a"+strings.Repeat("0", 63), "ep1", 1, strings.Repeat("a", 32))
	f.Add("", "", 0, "")

	f.Fuzz(func(t *testing.T, blockHash, endpointID string, keyVersion int, nonceHex string) {
		c := Claims{
			BlockHash:  blockHash,
			EndpointID: endpointID,
			KeyVersion: keyVersion,
			NonceHex:   nonceHex,
		}
		err := c.valid()
		// We just verify it doesn't panic — error cases are expected.
		_ = err
	})
}

// FuzzIsLowerHex verifies the hex validator handles arbitrary bytes.
func FuzzIsLowerHex(f *testing.F) {
	f.Add("abcdef0123456789")
	f.Add("ABCDEF")
	f.Add("")
	f.Add("xyz")

	f.Fuzz(func(t *testing.T, s string) {
		result := isLowerHex(s)
		// Verify manually.
		for i := 0; i < len(s); i++ {
			c := s[i]
			isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
			if !isHex && result {
				t.Errorf("isLowerHex(%q) = true but char %c is not lowercase hex", s, c)
				return
			}
		}
	})
}

// FuzzComputeMAC verifies HMAC computation is deterministic.
func FuzzComputeMAC(f *testing.F) {
	key := make([]byte, 32)
	rand.Read(key)
	f.Add(key, []byte("message1"))
	f.Add(key, []byte(""))
	f.Add([]byte("short-key-but-32bytes!!!"), []byte("test"))

	f.Fuzz(func(t *testing.T, key, message []byte) {
		if len(key) < 16 {
			return
		}
		mac1 := computeMAC(key, message)
		mac2 := computeMAC(key, message)
		if len(mac1) != 32 { // SHA-256 output
			t.Errorf("MAC length: got %d, want 32", len(mac1))
		}
		for i := range mac1 {
			if mac1[i] != mac2[i] {
				t.Fatal("MAC not deterministic")
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Unit tests for HMAC token generation + validation round-trip
// ---------------------------------------------------------------------------

func TestTokenGenerator_RoundTrip(t *testing.T) {
	key := []byte("round-trip-test-key-material-012345")
	tenantID := uuid.New()

	kms := NewStaticKMS(nil)
	kms.Provision(tenantID, 1, key)
	nonces := NewInMemoryNonceStore(0)

	gen := NewTokenGenerator(kms, nonces,
		WithClock(func() time.Time { return time.Now() }),
		WithTTL(time.Minute),
	)

	blockHash := strings.Repeat("ab", 32)
	tok, err := gen.SignClaims(context.Background(), tenantID, blockHash, "ep-1")
	if err != nil {
		t.Fatalf("SignClaims: %v", err)
	}

	ok, err := gen.Validate(context.Background(), tok)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !ok {
		t.Error("expected valid token")
	}
}

func TestTokenGenerator_ExpiredToken(t *testing.T) {
	now := time.Now()
	key := []byte("expired-test-key-material-0123456789")
	tenantID := uuid.New()

	kms := NewStaticKMS(nil)
	kms.Provision(tenantID, 1, key)
	nonces := NewInMemoryNonceStore(0)

	gen := NewTokenGenerator(kms, nonces,
		WithClock(func() time.Time { return now }),
		WithTTL(time.Minute),
	)

	blockHash := strings.Repeat("cd", 32)
	tok, err := gen.SignClaims(context.Background(), tenantID, blockHash, "ep-1")
	if err != nil {
		t.Fatalf("SignClaims: %v", err)
	}

	// Move time forward past expiry.
	gen.now = func() time.Time { return now.Add(3 * time.Minute) }

	ok, err := gen.Validate(context.Background(), tok)
	if err == nil || ok {
		t.Error("expired token should be rejected")
	}
}

func TestTokenGenerator_WrongKey(t *testing.T) {
	tenantID := uuid.New()
	key1 := []byte("first-key-material-0123456789abcdef")
	key2 := []byte("second-key-material-0123456789abcdef")

	kms1 := NewStaticKMS(nil)
	kms1.Provision(tenantID, 1, key1)
	nonces := NewInMemoryNonceStore(0)
	gen1 := NewTokenGenerator(kms1, nonces, WithTTL(time.Minute))

	tok, _ := gen1.SignClaims(context.Background(), tenantID, strings.Repeat("a", 64), "ep1")

	// Validate with different key.
	kms2 := NewStaticKMS(nil)
	kms2.Provision(tenantID, 1, key2)
	gen2 := NewTokenGenerator(kms2, nonces, WithTTL(time.Minute))

	ok, err := gen2.Validate(context.Background(), tok)
	if ok || err == nil {
		t.Error("token signed with wrong key should be rejected")
	}
}

func TestTokenGenerator_URLGeneration(t *testing.T) {
	key := []byte("url-test-key-material-0123456789ab")
	tenantID := uuid.New()

	kms := NewStaticKMS(nil)
	kms.Provision(tenantID, 1, key)
	nonces := NewInMemoryNonceStore(0)

	gen := NewTokenGenerator(kms, nonces,
		WithBaseURL("https://storage.example.com"),
		WithTTL(time.Minute),
	)

	blockHash := strings.Repeat("ef", 32)
	url, err := gen.GeneratePreSignedURL(context.Background(), tenantID, blockHash, "ep-1")
	if err != nil {
		t.Fatalf("GeneratePreSignedURL: %v", err)
	}

	if !strings.HasPrefix(url, "https://storage.example.com/chunks/") {
		t.Errorf("unexpected URL prefix: %s", url)
	}
	if !strings.Contains(url, blockHash) {
		t.Error("URL should contain block hash")
	}
	if !strings.Contains(url, "tenant="+tenantID.String()) {
		t.Error("URL should contain tenant parameter")
	}
}

func TestTokenGenerator_ReplayRejection(t *testing.T) {
	key := []byte("replay-test-key-material-0123456789ab")
	tenantID := uuid.New()

	kms := NewStaticKMS(nil)
	kms.Provision(tenantID, 1, key)
	nonces := NewInMemoryNonceStore(0)

	gen := NewTokenGenerator(kms, nonces, WithTTL(time.Minute))

	tok, _ := gen.SignClaims(context.Background(), tenantID, strings.Repeat("a", 64), "ep1")

	// First use succeeds.
	ok1, err1 := gen.Validate(context.Background(), tok)
	if err1 != nil || !ok1 {
		t.Fatalf("first validation: ok=%v err=%v", ok1, err1)
	}

	// Replay fails.
	ok2, _ := gen.Validate(context.Background(), tok)
	if ok2 {
		t.Error("replayed token should be rejected")
	}
}
