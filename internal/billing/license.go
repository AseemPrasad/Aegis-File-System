package billing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// EnterpriseLicensePayload defines the signed offline enterprise license key metadata structure.
type EnterpriseLicensePayload struct {
	LicenseID    string            `json:"license_id"`
	TenantID     string            `json:"tenant_id"`
	CustomerName string            `json:"customer_name"`
	PlanTier     PlanTier          `json:"plan_tier"`
	MaxStorageTB int64             `json:"max_storage_tb"`
	MaxNodes     int               `json:"max_nodes"`
	Features     []string          `json:"features"`
	IssuedAt     int64             `json:"issued_at"`
	ExpiresAt    int64             `json:"expires_at"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// SignedLicense Key string format: "<base64_payload>.<hex_hmac_signature>"
type LicenseVerifier struct {
	secretKey []byte
}

func NewLicenseVerifier(secretKey []byte) *LicenseVerifier {
	return &LicenseVerifier{secretKey: secretKey}
}

// IssueLicense creates a cryptographically signed license key string.
func (v *LicenseVerifier) IssueLicense(payload *EnterpriseLicensePayload) (string, error) {
	if payload == nil {
		return "", fmt.Errorf("payload cannot be nil")
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal license payload: %w", err)
	}

	encodedPayload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	sig := v.computeSignature(encodedPayload)

	return fmt.Sprintf("%s.%s", encodedPayload, sig), nil
}

// VerifyLicense decodes, checks signature, and validates expiration of an enterprise license key.
func (v *LicenseVerifier) VerifyLicense(licenseKey string) (*EnterpriseLicensePayload, error) {
	parts := strings.Split(strings.TrimSpace(licenseKey), ".")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid license key format: expected payload.signature")
	}

	encodedPayload, providedSig := parts[0], parts[1]
	expectedSig := v.computeSignature(encodedPayload)

	if !hmac.Equal([]byte(providedSig), []byte(expectedSig)) {
		return nil, fmt.Errorf("license signature verification failed: invalid signature")
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		return nil, fmt.Errorf("failed to decode license payload base64: %w", err)
	}

	var payload EnterpriseLicensePayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return nil, fmt.Errorf("failed to parse license payload json: %w", err)
	}

	now := time.Now().Unix()
	if payload.ExpiresAt > 0 && now > payload.ExpiresAt {
		return nil, fmt.Errorf("enterprise license expired on %s", time.Unix(payload.ExpiresAt, 0).Format(time.RFC3339))
	}

	return &payload, nil
}

func (v *LicenseVerifier) computeSignature(encodedPayload string) string {
	mac := hmac.New(sha256.New, v.secretKey)
	mac.Write([]byte(encodedPayload))
	return fmt.Sprintf("%x", mac.Sum(nil))
}
