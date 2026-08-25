package auth

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// KeyRecord.Active
// ---------------------------------------------------------------------------

func TestKeyRecordActive_CurrentKey(t *testing.T) {
	rec := KeyRecord{
		Key:         []byte("test-key-data-for-hmac-signing-1234"),
		ActivatedAt: time.Now(),
	}
	if !rec.Active(time.Now()) {
		t.Error("current key (zero SupersededAt) should always be active")
	}
}

func TestKeyRecordActive_WithinOverlapWindow(t *testing.T) {
	now := time.Now()
	rec := KeyRecord{
		Key:         []byte("test-key-data-for-hmac-signing-1234"),
		ActivatedAt: now.Add(-24 * time.Hour),
		SupersededAt: now.Add(-3 * 24 * time.Hour), // superseded 3 days ago
	}
	if !rec.Active(now) {
		t.Error("key superseded 3 days ago should still be active within 7-day window")
	}
}

func TestKeyRecordActive_OutsideOverlapWindow(t *testing.T) {
	now := time.Now()
	rec := KeyRecord{
		Key:         []byte("test-key-data-for-hmac-signing-1234"),
		ActivatedAt: now.Add(-30 * 24 * time.Hour),
		SupersededAt: now.Add(-8 * 24 * time.Hour), // superseded 8 days ago
	}
	if rec.Active(now) {
		t.Error("key superseded 8 days ago should be inactive (past 7-day window)")
	}
}

func TestKeyRecordActive_ExactlyAtWindowEdge(t *testing.T) {
	now := time.Now()
	rec := KeyRecord{
		Key:         []byte("test-key-data-for-hmac-signing-1234"),
		SupersededAt: now.Add(-OverlapWindow),
	}
	// At exactly the edge: SupersededAt + OverlapWindow = now, Before returns false.
	if rec.Active(now) {
		t.Error("key at exact overlap boundary should be inactive")
	}
}

func TestKeyRecordActive_JustInsideWindow(t *testing.T) {
	now := time.Now()
	rec := KeyRecord{
		Key:         []byte("test-key-data-for-hmac-signing-1234"),
		SupersededAt: now.Add(-OverlapWindow + time.Second),
	}
	if !rec.Active(now) {
		t.Error("key one second inside window should be active")
	}
}

// ---------------------------------------------------------------------------
// StaticKMS
// ---------------------------------------------------------------------------

func TestStaticKMS_ProvisionAndSigningKey(t *testing.T) {
	kms := NewStaticKMS(nil)
	tenantID := uuid.New()
	key := []byte("provisioned-key-0123456789abcdef")

	kms.Provision(tenantID, 1, key)

	version, gotKey, err := kms.SigningKey(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("SigningKey: %v", err)
	}
	if version != 1 {
		t.Errorf("version: got %d, want 1", version)
	}
	if string(gotKey) != string(key) {
		t.Errorf("key mismatch")
	}
}

func TestStaticKMS_SigningKeyUnknownTenant(t *testing.T) {
	kms := NewStaticKMS(nil)
	_, _, err := kms.SigningKey(context.Background(), uuid.New())
	if err == nil {
		t.Error("expected error for unknown tenant")
	}
}

func TestStaticKMS_VerificationKey(t *testing.T) {
	kms := NewStaticKMS(nil)
	tenantID := uuid.New()
	key := []byte("verification-key-0123456789abcdef")
	kms.Provision(tenantID, 1, key)

	gotKey, active, err := kms.VerificationKey(context.Background(), tenantID, 1)
	if err != nil {
		t.Fatalf("VerificationKey: %v", err)
	}
	if !active {
		t.Error("expected active=true for current version")
	}
	if string(gotKey) != string(key) {
		t.Error("key mismatch")
	}
}

func TestStaticKMS_VerificationKeyUnknownVersion(t *testing.T) {
	kms := NewStaticKMS(nil)
	tenantID := uuid.New()
	kms.Provision(tenantID, 1, []byte("key-data-here-0123456789abcdef"))

	_, active, err := kms.VerificationKey(context.Background(), tenantID, 999)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if active {
		t.Error("expected active=false for unknown version")
	}
}

func TestStaticKMS_Rotate(t *testing.T) {
	now := time.Now()
	kms := NewStaticKMS(func() time.Time { return now })
	tenantID := uuid.New()
	kms.Provision(tenantID, 1, []byte("initial-key-data-here-0123456"))

	newVer, err := kms.Rotate(tenantID)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if newVer != 2 {
		t.Errorf("new version: got %d, want 2", newVer)
	}

	// Old version should still be active (within overlap window).
	key1, active1, err := kms.VerificationKey(context.Background(), tenantID, 1)
	if err != nil {
		t.Fatalf("VerificationKey v1: %v", err)
	}
	if !active1 {
		t.Error("old key should be active within overlap window")
	}
	if len(key1) == 0 {
		t.Error("old key should not be empty")
	}

	// New version should be the signing key.
	ver, key2, err := kms.SigningKey(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("SigningKey: %v", err)
	}
	if ver != 2 {
		t.Errorf("signing version: got %d, want 2", ver)
	}
	if string(key1) == string(key2) {
		t.Error("rotated key should differ from old key")
	}
}

func TestStaticKMS_RotateUnknownTenant(t *testing.T) {
	kms := NewStaticKMS(nil)
	_, err := kms.Rotate(uuid.New())
	if err == nil {
		t.Error("expected error for unknown tenant")
	}
}

func TestStaticKMS_RotateOverlapExpiry(t *testing.T) {
	now := time.Now()
	kms := NewStaticKMS(func() time.Time { return now })
	tenantID := uuid.New()
	kms.Provision(tenantID, 1, []byte("initial-key-data-here-0123456"))

	kms.Rotate(tenantID)

	// Move time past overlap window.
	kms.now = func() time.Time { return now.Add(OverlapWindow + time.Hour) }

	_, active, err := kms.VerificationKey(context.Background(), tenantID, 1)
	if err != nil {
		t.Fatalf("VerificationKey: %v", err)
	}
	if active {
		t.Error("old key should be inactive after overlap window expires")
	}
}

func TestStaticKMS_MultipleTenants(t *testing.T) {
	kms := NewStaticKMS(nil)
	t1, t2 := uuid.New(), uuid.New()

	kms.Provision(t1, 1, []byte("tenant-one-key-data-0123456789ab"))
	kms.Provision(t2, 1, []byte("tenant-two-key-data-0123456789ab"))

	_, k1, _ := kms.SigningKey(context.Background(), t1)
	_, k2, _ := kms.SigningKey(context.Background(), t2)

	if string(k1) == string(k2) {
		t.Error("different tenants should have different keys")
	}
}

// ---------------------------------------------------------------------------
// KeyRecord — key material isolation
// ---------------------------------------------------------------------------

func TestStaticKMS_KeyIsolation(t *testing.T) {
	kms := NewStaticKMS(nil)
	tenant := uuid.New()
	key := []byte("isolation-test-key-material-012345")
	kms.Provision(tenant, 1, key)

	// Provisioning creates an internal copy (append([]byte(nil), key...)).
	// Verify the stored key differs from the caller's original after mutation.
	key[0] = 'X'

	storedKey, _, _ := kms.VerificationKey(context.Background(), tenant, 1)
	if len(storedKey) > 0 && storedKey[0] == 'X' {
		t.Error("Provision should defensively copy key material")
	}
}

// ---------------------------------------------------------------------------
// nonceKey — unit test for the pure function
// ---------------------------------------------------------------------------

func TestNonceKey_Deterministic(t *testing.T) {
	k1 := nonceKey("test-nonce-abc")
	k2 := nonceKey("test-nonce-abc")
	if k1 != k2 {
		t.Errorf("nonceKey not deterministic: %s != %s", k1, k2)
	}
}

func TestNonceKey_DomainSeparated(t *testing.T) {
	k := nonceKey("test-nonce")
	if len(k) <= len(nonceKeyPrefix) {
		t.Error("nonceKey should produce a domain-separated key")
	}
	if k[:len(nonceKeyPrefix)] != nonceKeyPrefix {
		t.Errorf("nonceKey should start with prefix %q", nonceKeyPrefix)
	}
}

func TestNonceKey_DifferentInputsDifferentOutputs(t *testing.T) {
	k1 := nonceKey("nonce-a")
	k2 := nonceKey("nonce-b")
	if k1 == k2 {
		t.Error("different nonces should produce different keys")
	}
}
