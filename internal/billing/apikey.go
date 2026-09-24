package billing

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const (
	APIKeyPrefixLive = "aegis_live_sk_"
	APIKeyPrefixTest = "aegis_test_sk_"
)

// GenerateAPIKey creates a secure random API key, hashes it for database storage,
// and returns both the domain record and the unhashed raw key string (shown once to user).
func GenerateAPIKey(tenantID, name string, isTest bool, scopes []string, expiresAt *time.Time) (*APIKeyRecord, string, error) {
	bytes := make([]byte, 24)
	if _, err := rand.Read(bytes); err != nil {
		return nil, "", fmt.Errorf("failed to generate random secret bytes: %w", err)
	}

	prefix := APIKeyPrefixLive
	if isTest {
		prefix = APIKeyPrefixTest
	}

	rawKey := prefix + hex.EncodeToString(bytes)
	keyHash := HashAPIKey(rawKey)
	keyID := "key_" + hex.EncodeToString(bytes[:8])

	if len(scopes) == 0 {
		scopes = []string{"read", "write"}
	}

	record := &APIKeyRecord{
		ID:        keyID,
		TenantID:  tenantID,
		Name:      name,
		KeyHash:   keyHash,
		Prefix:    rawKey[:16] + "...",
		Scopes:    scopes,
		IsActive:  true,
		CreatedAt: time.Now().UTC(),
		ExpiresAt: expiresAt,
	}

	return record, rawKey, nil
}

// HashAPIKey computes the SHA-256 hex checksum of a raw secret API key string.
func HashAPIKey(rawKey string) string {
	h := sha256.Sum256([]byte(strings.TrimSpace(rawKey)))
	return hex.EncodeToString(h[:])
}

// ValidateAPIKey checks key format validity, status, expiration, and scope requirements.
func ValidateAPIKey(keyRecord *APIKeyRecord, requiredScope string) error {
	if keyRecord == nil {
		return fmt.Errorf("api key record is nil")
	}

	if !keyRecord.IsActive {
		return fmt.Errorf("api key %s is inactive or revoked", keyRecord.ID)
	}

	if keyRecord.ExpiresAt != nil && time.Now().UTC().After(*keyRecord.ExpiresAt) {
		return fmt.Errorf("api key %s expired at %s", keyRecord.ID, keyRecord.ExpiresAt.Format(time.RFC3339))
	}

	if requiredScope != "" {
		hasScope := false
		for _, scope := range keyRecord.Scopes {
			if scope == requiredScope || scope == "admin" || scope == "*" {
				hasScope = true
				break
			}
		}
		if !hasScope {
			return fmt.Errorf("api key %s lacks required scope: %s", keyRecord.ID, requiredScope)
		}
	}

	return nil
}
