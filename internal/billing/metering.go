package billing

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// UsageEvent represents a granular metering event emitted by system components.
type UsageEvent struct {
	TenantID   string            `json:"tenant_id"`
	MeterType  MeterType         `json:"meter_type"`
	Quantity   int64             `json:"quantity"`
	Timestamp  time.Time         `json:"timestamp"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// QuotaExceededError is returned when a tenant exceeds their active subscription quota limit.
type QuotaExceededError struct {
	TenantID  string
	MeterType MeterType
	Limit     int64
	Current   int64
}

func (e *QuotaExceededError) Error() string {
	return fmt.Errorf("quota exceeded for tenant %s on meter %s: usage %d exceeds limit %d",
		e.TenantID, e.MeterType, e.Current, e.Limit).Error()
}

// InMemMeterStore provides thread-safe real-time meter tracking in memory.
type InMemMeterStore struct {
	mu     sync.RWMutex
	totals map[string]map[MeterType]int64
	limits map[string]map[MeterType]int64
}

func NewInMemMeterStore() *InMemMeterStore {
	return &InMemMeterStore{
		totals: make(map[string]map[MeterType]int64),
		limits: make(map[string]map[MeterType]int64),
	}
}

// SetQuotaLimit configures maximum allowance for a given tenant and meter type.
func (s *InMemMeterStore) SetQuotaLimit(tenantID string, meterType MeterType, limit int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.limits[tenantID]; !ok {
		s.limits[tenantID] = make(map[MeterType]int64)
	}
	s.limits[tenantID][meterType] = limit
}

// RecordUsage records usage and checks if quota limit is exceeded.
func (s *InMemMeterStore) RecordUsage(ctx context.Context, evt UsageEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.totals[evt.TenantID]; !ok {
		s.totals[evt.TenantID] = make(map[MeterType]int64)
	}

	current := s.totals[evt.TenantID][evt.MeterType]
	limit, maxSet := s.limits[evt.TenantID][evt.MeterType]

	if maxSet && limit > 0 && (current+evt.Quantity) > limit {
		return &QuotaExceededError{
			TenantID:  evt.TenantID,
			MeterType: evt.MeterType,
			Limit:     limit,
			Current:   current + evt.Quantity,
		}
	}

	s.totals[evt.TenantID][evt.MeterType] += evt.Quantity
	return nil
}

// GetUsage retrieves current accumulated usage for a tenant and meter type.
func (s *InMemMeterStore) GetUsage(tenantID string, meterType MeterType) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if tenantMeters, ok := s.totals[tenantID]; ok {
		return tenantMeters[meterType]
	}
	return 0
}

// ResetUsage clears accumulated usage for a new billing cycle.
func (s *InMemMeterStore) ResetUsage(tenantID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.totals, tenantID)
}
