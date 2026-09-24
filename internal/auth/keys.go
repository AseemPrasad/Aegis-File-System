// ============================================================================
// Project Aegis — KMS-backed signing-key management for ingress tokens.
// PROMPT 3.2 (Cryptographic Ingress & HMAC Token System).
//
// Rotation model
//
//	Every tenant has a monotonically increasing integer key version. The
//	current version signs every new token. When a rotation lands, the prior
//	version is retained for OVERLAP_WINDOW (7 days, mirroring
//	GC_SAFETY_WINDOW_DAYS) so in-flight pre-signed URLs survive rotation.
//	A version is accepted for verification while:
//
//	    v == current                                  (always)
//	    v <  current AND superseded_at + 7d > now     (overlap window)
//
//	and rejected afterwards. The version is INSIDE the HMAC message, so an
//	attacker cannot downgrade the version without breaking the signature.
//
// The production implementation of KMSClient fronts AWS KMS
// (kms:GetDataKey / local decryption cache); StaticKMS provides the
// deterministic in-process implementation used by tests and the
// IN_MEMORY_STORES=true developer profile.
// ============================================================================
package auth

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	// KeyRotationPeriod is how often tenants SHOULD rotate (policy input to
	// the scheduler; enforcement lives outside this package).
	KeyRotationPeriod = 90 * 24 * time.Hour

	// OverlapWindow keeps superseded keys verification-valid after a
	// rotation so tokens minted just before rotation still validate.
	OverlapWindow = 7 * 24 * time.Hour
)

// KeyRecord captures one tenant key version plus its lifecycle timestamps.
// Zero SupersededAt means "this is (still) the current version".
type KeyRecord struct {
	Key          []byte
	ActivatedAt  time.Time
	SupersededAt time.Time
}

// Active reports whether the version may verify signatures at time now.
func (r KeyRecord) Active(now time.Time) bool {
	if r.SupersededAt.IsZero() {
		return true
	}
	return now.Before(r.SupersededAt.Add(OverlapWindow))
}

// KMSClient abstracts per-tenant signing-key retrieval. Implementations
// MUST return distinct cryptographically strong keys per tenant — cross-
// tenant key sharing would void the isolation argument entirely.
type KMSClient interface {
	// SigningKey returns the CURRENT version and raw key material for new
	// signatures. Raw bytes are handed out because HMAC consumes them
	// directly; implementations must not retain caller-mutable state.
	SigningKey(ctx context.Context, tenantID uuid.UUID) (version int, key []byte, err error)

	// VerificationKey resolves one specific version. active=false signals
	// unknown-or-retired (outside the overlap window) and callers must
	// reject rather than fall back to another version.
	VerificationKey(ctx context.Context, tenantID uuid.UUID, version int) (key []byte, active bool, err error)
}

// StaticKMS is an in-memory KMSClient: deterministic, dependency-free, and
// sufficient for the unit/fuzz battery plus the IN_MEMORY_STORES profile.
type StaticKMS struct {
	mu            sync.RWMutex
	tenants       map[uuid.UUID]*tenantKeys
	now           func() time.Time
	autoProvision bool
}

type tenantKeys struct {
	current  int
	versions map[int]KeyRecord
}

// NewStaticKMS provisions tenant with a fresh random key at version 1.
func NewStaticKMS(now func() time.Time) *StaticKMS {
	if now == nil {
		now = time.Now
	}
	return &StaticKMS{
		tenants: make(map[uuid.UUID]*tenantKeys),
		now:     now,
	}
}

// EnableAutoProvision enables automatic key generation for unprovisioned tenants.
func (s *StaticKMS) EnableAutoProvision() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.autoProvision = true
}

// Provision registers a tenant with an explicitly provided initial key
// (tests want fixed keys for golden-vector assertions).
func (s *StaticKMS) Provision(tenantID uuid.UUID, version int, key []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tk, ok := s.tenants[tenantID]
	if !ok {
		tk = &tenantKeys{versions: make(map[int]KeyRecord)}
		s.tenants[tenantID] = tk
	}
	tk.current = version
	tk.versions[version] = KeyRecord{Key: append([]byte(nil), key...), ActivatedAt: s.now()}
}

func (s *StaticKMS) SigningKey(_ context.Context, tenantID uuid.UUID) (int, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tk, ok := s.tenants[tenantID]
	if !ok {
		if !s.autoProvision {
			return 0, nil, fmt.Errorf("auth: no keys provisioned for tenant %s", tenantID)
		}
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return 0, nil, fmt.Errorf("auth: auto-provision key entropy: %w", err)
		}
		tk = &tenantKeys{
			current:  1,
			versions: map[int]KeyRecord{1: {Key: raw, ActivatedAt: s.now()}},
		}
		s.tenants[tenantID] = tk
	}
	rec := tk.versions[tk.current]
	return tk.current, rec.Key, nil
}

func (s *StaticKMS) VerificationKey(_ context.Context, tenantID uuid.UUID, version int) ([]byte, bool, error) {
	s.mu.RLock()
	tk, ok := s.tenants[tenantID]
	s.mu.RUnlock()

	if !ok {
		if !s.autoProvision {
			return nil, false, fmt.Errorf("auth: no keys provisioned for tenant %s", tenantID)
		}
		s.mu.Lock()
		tk, ok = s.tenants[tenantID]
		if !ok {
			raw := make([]byte, 32)
			if _, err := rand.Read(raw); err != nil {
				s.mu.Unlock()
				return nil, false, fmt.Errorf("auth: auto-provision verification key entropy: %w", err)
			}
			tk = &tenantKeys{
				current:  1,
				versions: map[int]KeyRecord{1: {Key: raw, ActivatedAt: s.now()}},
			}
			s.tenants[tenantID] = tk
		}
		s.mu.Unlock()
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := tk.versions[version]
	if !ok || !rec.Active(s.now()) {
		return nil, false, nil
	}
	return rec.Key, true, nil
}

// Rotate mints a fresh random key as the next version, demoting the current
// one into its overlap window.
func (s *StaticKMS) Rotate(tenantID uuid.UUID) (newVersion int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tk, ok := s.tenants[tenantID]
	if !ok {
		return 0, fmt.Errorf("auth: no keys provisioned for tenant %s", tenantID)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return 0, fmt.Errorf("auth: rotate key entropy: %w", err)
	}
	old := tk.versions[tk.current]
	old.SupersededAt = s.now()
	tk.versions[tk.current] = old
	tk.current++
	tk.versions[tk.current] = KeyRecord{Key: raw, ActivatedAt: s.now()}
	return tk.current, nil
}
