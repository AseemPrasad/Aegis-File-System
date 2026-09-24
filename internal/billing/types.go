package billing

import "time"

type PlanTier string

const (
	PlanFree       PlanTier = "FREE"
	PlanPro        PlanTier = "PRO"
	PlanEnterprise PlanTier = "ENTERPRISE"
)

type MeterType string

const (
	MeterStorageBytes   MeterType = "STORAGE_BYTES"
	MeterBandwidthBytes MeterType = "BANDWIDTH_BYTES"
	MeterWorkerCredits  MeterType = "WORKER_CREDITS"
)

// SubscriptionRecord represents tenant billing plan details.
type SubscriptionRecord struct {
	SubscriptionID       string    `json:"subscription_id"`
	TenantID             string    `json:"tenant_id"`
	PlanTier             PlanTier  `json:"plan_tier"`
	StripeCustomerID     string    `json:"stripe_customer_id,omitempty"`
	StripeSubscriptionID string    `json:"stripe_subscription_id,omitempty"`
	Status               string    `json:"status"`
	CurrentPeriodStart   time.Time `json:"current_period_start"`
	CurrentPeriodEnd     time.Time `json:"current_period_end"`
}

// APIKeyRecord represents a developer API key token.
type APIKeyRecord struct {
	KeyID     string     `json:"key_id"`
	TenantID  string     `json:"tenant_id"`
	KeyPrefix string     `json:"key_prefix"`
	KeyHash   string     `json:"key_hash"`
	Name      string     `json:"name"`
	Scopes    string     `json:"scopes"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// MeterRecord represents a single usage telemetry record.
type MeterRecord struct {
	MeterID         string    `json:"meter_id"`
	TenantID        string    `json:"tenant_id"`
	MeterType       MeterType `json:"meter_type"`
	Quantity        int64     `json:"quantity"`
	PeriodTimestamp time.Time `json:"period_timestamp"`
}
