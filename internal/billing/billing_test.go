package billing

import (
	"context"
	"testing"
	"time"
)

func TestAPIKeyGenerationAndValidation(t *testing.T) {
	tenantID := "tenant_acme_corp"
	record, rawKey, err := GenerateAPIKey(tenantID, "Production Key", false, []string{"read", "write"}, nil)
	if err != nil {
		t.Fatalf("GenerateAPIKey failed: %v", err)
	}

	if record.TenantID != tenantID {
		t.Errorf("expected tenant ID %s, got %s", tenantID, record.TenantID)
	}

	if HashAPIKey(rawKey) != record.KeyHash {
		t.Errorf("API key hash mismatch")
	}

	if err := ValidateAPIKey(record, "read"); err != nil {
		t.Errorf("ValidateAPIKey failed for valid scope: %v", err)
	}

	if err := ValidateAPIKey(record, "admin"); err == nil {
		t.Errorf("expected error for unauthorized scope, got nil")
	}

	past := time.Now().Add(-1 * time.Hour)
	record.ExpiresAt = &past
	if err := ValidateAPIKey(record, "read"); err == nil {
		t.Errorf("expected error for expired key, got nil")
	}
}

func TestEnterpriseLicenseKeyVerification(t *testing.T) {
	secret := []byte("super-secret-enterprise-signing-key-32b")
	verifier := NewLicenseVerifier(secret)

	payload := &EnterpriseLicensePayload{
		LicenseID:    "lic_12345",
		TenantID:     "tenant_stark_ind",
		CustomerName: "Stark Industries",
		PlanTier:     PlanEnterprise,
		MaxStorageTB: 500,
		MaxNodes:     100,
		Features:     []string{"sso", "audit_logs", "air_gapped"},
		IssuedAt:     time.Now().Unix(),
		ExpiresAt:    time.Now().Add(24 * time.Hour).Unix(),
	}

	licenseKey, err := verifier.IssueLicense(payload)
	if err != nil {
		t.Fatalf("IssueLicense failed: %v", err)
	}

	verified, err := verifier.VerifyLicense(licenseKey)
	if err != nil {
		t.Fatalf("VerifyLicense failed: %v", err)
	}

	if verified.TenantID != payload.TenantID {
		t.Errorf("expected tenant ID %s, got %s", payload.TenantID, verified.TenantID)
	}

	// Tampered key test
	tamperedKey := licenseKey + "x"
	if _, err := verifier.VerifyLicense(tamperedKey); err == nil {
		t.Errorf("expected signature error for tampered key, got nil")
	}
}

func TestMeteringAndQuotaEnforcement(t *testing.T) {
	store := NewInMemMeterStore()
	tenantID := "tenant_cyberdyne"

	store.SetQuotaLimit(tenantID, MeterBandwidthBytes, 100)

	ctx := context.Background()
	evt1 := UsageEvent{
		TenantID:  tenantID,
		MeterType: MeterBandwidthBytes,
		Quantity:  60,
		Timestamp: time.Now(),
	}

	if err := store.RecordUsage(ctx, evt1); err != nil {
		t.Fatalf("RecordUsage failed: %v", err)
	}

	if store.GetUsage(tenantID, MeterBandwidthBytes) != 60 {
		t.Errorf("expected usage 60, got %d", store.GetUsage(tenantID, MeterBandwidthBytes))
	}

	evt2 := UsageEvent{
		TenantID:  tenantID,
		MeterType: MeterBandwidthBytes,
		Quantity:  50,
		Timestamp: time.Now(),
	}

	err := store.RecordUsage(ctx, evt2)
	if err == nil {
		t.Fatalf("expected QuotaExceededError when exceeding limit 100, got nil")
	}
}

func TestStripeWebhookSync(t *testing.T) {
	updated := false
	handler := NewStripeWebhookHandler("", func(sub *SubscriptionRecord) error {
		updated = true
		if sub.TenantID != "cus_stripe_123" {
			t.Errorf("expected customer ID cus_stripe_123, got %s", sub.TenantID)
		}
		return nil
	})

	evt := &StripeWebhookEvent{
		ID:   "evt_123",
		Type: "customer.subscription.created",
		Data: StripeWebhookEventData{
			Object: map[string]interface{}{
				"id":       "sub_999",
				"customer": "cus_stripe_123",
				"status":   "active",
			},
		},
	}

	if err := handler.HandleEvent(evt); err != nil {
		t.Fatalf("HandleEvent failed: %v", err)
	}

	if !updated {
		t.Errorf("expected callback to be called on subscription creation")
	}
}
