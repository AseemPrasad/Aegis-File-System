package database

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// CircuitBreaker state machine
// ---------------------------------------------------------------------------

func TestCircuitBreaker_ClosedByDefault(t *testing.T) {
	cb := newCircuitBreaker(nil)
	if cb.State() != "closed" {
		t.Errorf("state: got %q, want %q", cb.State(), "closed")
	}
	if !cb.Allow() {
		t.Error("closed breaker should allow")
	}
}

func TestCircuitBreaker_TripsAfterThreshold(t *testing.T) {
	cb := newCircuitBreaker(nil)

	for i := 0; i < breakerFailureThreshold; i++ {
		cb.RecordFailure()
	}

	if cb.State() != "open" {
		t.Errorf("state after %d failures: got %q, want %q",
			breakerFailureThreshold, cb.State(), "open")
	}
	if cb.Allow() {
		t.Error("open breaker should not allow")
	}
}

func TestCircuitBreaker_BelowThresholdStaysClosed(t *testing.T) {
	cb := newCircuitBreaker(nil)

	for i := 0; i < breakerFailureThreshold-1; i++ {
		cb.RecordFailure()
	}

	if cb.State() != "closed" {
		t.Errorf("state below threshold: got %q, want %q", cb.State(), "closed")
	}
}

func TestCircuitBreaker_SuccessResetsCount(t *testing.T) {
	cb := newCircuitBreaker(nil)

	cb.RecordFailure()
	cb.RecordFailure()
	cb.RecordSuccess() // resets count
	cb.RecordFailure()

	// Only 1 consecutive failure now, well below threshold.
	if cb.State() != "closed" {
		t.Errorf("state: got %q, want %q", cb.State(), "closed")
	}
}

func TestCircuitBreaker_HalfOpenAfterCooldown(t *testing.T) {
	cb := newCircuitBreaker(nil)

	// Trip the breaker.
	for i := 0; i < breakerFailureThreshold; i++ {
		cb.RecordFailure()
	}
	if cb.State() != "open" {
		t.Fatalf("expected open, got %q", cb.State())
	}

	// Wait for cooldown.
	time.Sleep(breakerCooldown + 10*time.Millisecond)

	// Allow should transition to half-open and return true for one caller.
	if !cb.Allow() {
		t.Error("half-open transition should allow one probe")
	}
	if cb.State() != "half-open" {
		t.Errorf("state: got %q, want %q", cb.State(), "half-open")
	}
}

func TestCircuitBreaker_HalfOpenRejectsConcurrentProbers(t *testing.T) {
	cb := newCircuitBreaker(nil)

	for i := 0; i < breakerFailureThreshold; i++ {
		cb.RecordFailure()
	}
	time.Sleep(breakerCooldown + 10*time.Millisecond)

	// First probe succeeds.
	cb.Allow()
	// Second concurrent probe should be rejected.
	if cb.Allow() {
		t.Error("second prober should be rejected in half-open state")
	}
}

func TestCircuitBreaker_ProbeSuccessCloses(t *testing.T) {
	cb := newCircuitBreaker(nil)

	for i := 0; i < breakerFailureThreshold; i++ {
		cb.RecordFailure()
	}
	time.Sleep(breakerCooldown + 10*time.Millisecond)

	cb.Allow() // enter half-open
	cb.RecordSuccess()

	if cb.State() != "closed" {
		t.Errorf("state after probe success: got %q, want %q", cb.State(), "closed")
	}
}

func TestCircuitBreaker_ProbeFailureStaysHalfOpen(t *testing.T) {
	cb := newCircuitBreaker(nil)

	for i := 0; i < breakerFailureThreshold; i++ {
		cb.RecordFailure()
	}
	time.Sleep(breakerCooldown + 10*time.Millisecond)

	cb.Allow() // enter half-open
	cb.RecordFailure()

	// RecordFailure only CAS from closed→open; in half-open it stays half-open.
	// The health loop would re-enter Allow() which checks cooldown, eventually
	// re-tripping. For unit test, we verify state is unchanged.
	if cb.State() != "half-open" {
		t.Errorf("state after probe failure: got %q, want %q", cb.State(), "half-open")
	}
}

func TestCircuitBreaker_OnTripCallback(t *testing.T) {
	var lastState string
	cb := newCircuitBreaker(func(state string) { lastState = state })

	for i := 0; i < breakerFailureThreshold; i++ {
		cb.RecordFailure()
	}
	if lastState != "open" {
		t.Errorf("onTrip callback: got %q, want %q", lastState, "open")
	}

	// Transition back to closed.
	time.Sleep(breakerCooldown + 10*time.Millisecond)
	cb.Allow()
	cb.RecordSuccess()
	if lastState != "closed" {
		t.Errorf("onTrip callback: got %q, want %q", lastState, "closed")
	}
}

func TestCircuitBreaker_StringValues(t *testing.T) {
	tests := []struct {
		state breakerState
		want  string
	}{
		{breakerClosed, "closed"},
		{breakerOpen, "open"},
		{breakerHalfOpen, "half-open"},
		{breakerState(99), "closed"}, // unknown defaults to closed
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("breakerState(%d).String() = %q, want %q", tt.state, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// NamespaceCache key builders (pure functions)
// ---------------------------------------------------------------------------

func TestGenerationKey(t *testing.T) {
	got := generationKey("tenant-abc")
	want := "aegis:ns:gen:tenant-abc"
	if got != want {
		t.Errorf("generationKey: got %q, want %q", got, want)
	}
}

func TestNodeKey(t *testing.T) {
	got := nodeKey("tenant-abc", 42, "node-xyz")
	want := "aegis:ns:tenant-abc:g42:node-xyz"
	if got != want {
		t.Errorf("nodeKey: got %q, want %q", got, want)
	}
}

func TestNodeKey_GenerationZero(t *testing.T) {
	got := nodeKey("t", 0, "n")
	want := "aegis:ns:t:g0:n"
	if got != want {
		t.Errorf("nodeKey: got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// atomicCounter
// ---------------------------------------------------------------------------

func TestAtomicCounter(t *testing.T) {
	var ac atomicCounter
	if ac.Load() != 0 {
		t.Error("initial value should be 0")
	}
	ac.Add(5)
	if ac.Load() != 5 {
		t.Errorf("after Add(5): got %d, want 5", ac.Load())
	}
	ac.Add(-3)
	if ac.Load() != 2 {
		t.Errorf("after Add(-3): got %d, want 2", ac.Load())
	}
}
