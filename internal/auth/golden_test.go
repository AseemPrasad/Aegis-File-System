// ============================================================================
// Project Aegis — wire-format golden vectors.
// PROMPT 3.2 (Cryptographic Ingress & HMAC Token System).
//
// The edge validator (deploy/cloudflare-worker/worker.js) re-implements the
// message layout in TypeScript. These tests pin the EXACT byte format so
// any accidental change here breaks CI before it breaks the edge:
//
//	message = "aegis1:{kv}:{tenant}:{hash}:{exp}:{ep}:{nonce}"
//	sig     = hex(HMAC_SHA256(key, message))
// ============================================================================

package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestGoldenVectorMessageLayout(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	kms := NewStaticKMS(func() time.Time { return now })
	key := []byte("0123456789abcdef0123456789abcdef") // 32 bytes, fixed
	kms.Provision(tenantA, 1, key)
	gen := NewTokenGenerator(kms, NewInMemoryNonceStore(0), WithClock(func() time.Time { return now }))

	st, err := gen.SignClaims(context.Background(), tenantA, blockAX, endpoint1)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// Independently rebuild the message from the SPEC STRING above.
	wantMsg := "aegis1:1:" +
		tenantA.String() + ":" +
		blockAX + ":" +
		strconv.FormatInt(now.Add(DefaultTokenTTL).Unix(), 10) + ":" +
		endpoint1 + ":" +
		st.Claims.NonceHex

	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(wantMsg))
	wantSig := hex.EncodeToString(mac.Sum(nil))

	if st.SignatureHex != wantSig {
		t.Fatalf("wire format drifted:\n got sig %s\nwant sig %s\nmessage spec %s",
			st.SignatureHex, wantSig, wantMsg)
	}
	if got := st.URL(defaultBaseURL); !strings.HasPrefix(got,
		defaultBaseURL+"/chunks/"+blockAX+"?") {
		t.Fatalf("URL contract drifted: %s", got)
	}
	// Parameter set is exactly the six documented ones.
	q := st.Query()
	if len(q) != 6 {
		t.Fatalf("query parameter count drifted: %v", q)
	}
	for _, p := range []string{"tenant", "ts", "kv", "ep", "nonce", "sig"} {
		if q.Get(p) == "" {
			t.Fatalf("missing query parameter %q in %v", p, q)
		}
	}
}

var _ = uuid.Nil // keep uuid import for tenant fixtures parity
