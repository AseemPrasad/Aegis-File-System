package billing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// StripeWebhookEvent represents inbound Stripe webhook payload JSON.
type StripeWebhookEvent struct {
	ID        string                 `json:"id"`
	Type      string                 `json:"type"`
	Created   int64                  `json:"created"`
	Data      StripeWebhookEventData `json:"data"`
	LiveMode  bool                   `json:"livemode"`
}

type StripeWebhookEventData struct {
	Object map[string]interface{} `json:"object"`
}

// StripeWebhookHandler manages event routing and webhook secret signature verification.
type StripeWebhookHandler struct {
	webhookSecret string
	onSubUpdated  func(sub *SubscriptionRecord) error
}

func NewStripeWebhookHandler(webhookSecret string, onSubUpdated func(sub *SubscriptionRecord) error) *StripeWebhookHandler {
	return &StripeWebhookHandler{
		webhookSecret: webhookSecret,
		onSubUpdated:  onSubUpdated,
	}
}

// VerifyHeader checks Stripe-Signature header against the raw payload bytes.
func (h *StripeWebhookHandler) VerifyHeader(payload []byte, sigHeader string) error {
	if h.webhookSecret == "" {
		return nil // secret check disabled in dev/mock environment
	}

	parts := strings.Split(sigHeader, ",")
	var timestampStr string
	var signatures []string

	for _, part := range parts {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		if kv[0] == "t" {
			timestampStr = kv[1]
		} else if kv[0] == "v1" {
			signatures = append(signatures, kv[1])
		}
	}

	if timestampStr == "" || len(signatures) == 0 {
		return fmt.Errorf("invalid stripe signature header format")
	}

	ts, err := strconv.ParseInt(timestampStr, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid signature timestamp: %w", err)
	}

	// Reject payloads older than 5 minutes to prevent replay attacks
	if time.Now().Unix()-ts > 300 {
		return fmt.Errorf("stripe webhook payload timestamp too old")
	}

	mac := hmac.New(sha256.New, []byte(h.webhookSecret))
	mac.Write([]byte(timestampStr))
	mac.Write([]byte("."))
	mac.Write(payload)
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	match := false
	for _, sig := range signatures {
		if hmac.Equal([]byte(sig), []byte(expectedSig)) {
			match = true
			break
		}
	}

	if !match {
		return fmt.Errorf("stripe webhook signature verification failed")
	}

	return nil
}

// HandleEvent processes parsed Stripe lifecycle events and syncs subscription state.
func (h *StripeWebhookHandler) HandleEvent(event *StripeWebhookEvent) error {
	if event == nil {
		return fmt.Errorf("event payload is nil")
	}

	switch event.Type {
	case "customer.subscription.created", "customer.subscription.updated":
		obj := event.Data.Object
		subID, _ := obj["id"].(string)
		custID, _ := obj["customer"].(string)
		statusStr, _ := obj["status"].(string)

		rec := &SubscriptionRecord{
			SubscriptionID:       subID,
			TenantID:             custID, // mapped to tenant
			StripeCustomerID:     custID,
			StripeSubscriptionID: subID,
			PlanTier:             PlanEnterprise,
			Status:               statusStr,
		}

		if h.onSubUpdated != nil {
			return h.onSubUpdated(rec)
		}

	case "customer.subscription.deleted":
		obj := event.Data.Object
		subID, _ := obj["id"].(string)
		custID, _ := obj["customer"].(string)

		rec := &SubscriptionRecord{
			SubscriptionID:       subID,
			TenantID:             custID,
			StripeCustomerID:     custID,
			StripeSubscriptionID: subID,
			PlanTier:             PlanFree,
			Status:               "CANCELED",
		}

		if h.onSubUpdated != nil {
			return h.onSubUpdated(rec)
		}
	}

	return nil
}
