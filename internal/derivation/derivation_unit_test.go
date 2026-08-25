package derivation

import (
	"context"
	"errors"
	"net"
	"syscall"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// RetryWithBackoff
// ---------------------------------------------------------------------------

func TestRetryWithBackoff_SuccessFirstAttempt(t *testing.T) {
	calls := 0
	result := RetryWithBackoff(context.Background(), RetryConfig{}, func(attempt int) error {
		calls++
		return nil
	})
	if !result.Success {
		t.Error("expected success")
	}
	if result.Attempts != 1 {
		t.Errorf("attempts: got %d, want 1", result.Attempts)
	}
	if calls != 1 {
		t.Errorf("fn calls: got %d, want 1", calls)
	}
}

func TestRetryWithBackoff_SuccessAfterRetries(t *testing.T) {
	calls := 0
	result := RetryWithBackoff(context.Background(), RetryConfig{
		MaxAttempts: 3,
		BaseDelay:   time.Millisecond,
		MaxDelay:    time.Millisecond,
	}, func(attempt int) error {
		calls++
		if attempt < 3 {
			return &net.OpError{Op: "read", Err: errors.New("timeout")}
		}
		return nil
	})
	if !result.Success {
		t.Error("expected success after retries")
	}
	if result.Attempts != 3 {
		t.Errorf("attempts: got %d, want 3", result.Attempts)
	}
}

func TestRetryWithBackoff_PermanentErrorStopsImmediately(t *testing.T) {
	calls := 0
	permErr := errors.New("permanent: invalid argument")
	result := RetryWithBackoff(context.Background(), RetryConfig{
		MaxAttempts: 5,
		BaseDelay:   time.Millisecond,
	}, func(attempt int) error {
		calls++
		return permErr
	})
	if result.Success {
		t.Error("expected failure")
	}
	if result.Attempts != 1 {
		t.Errorf("attempts: got %d, want 1 (should stop on first permanent error)", result.Attempts)
	}
	if calls != 1 {
		t.Errorf("fn calls: got %d, want 1", calls)
	}
}

func TestRetryWithBackoff_ExhaustsAttempts(t *testing.T) {
	calls := 0
	result := RetryWithBackoff(context.Background(), RetryConfig{
		MaxAttempts: 3,
		BaseDelay:   time.Millisecond,
		MaxDelay:    time.Millisecond,
	}, func(attempt int) error {
		calls++
		return &net.OpError{Op: "read", Err: errors.New("timeout")}
	})
	if result.Success {
		t.Error("expected failure after exhausting attempts")
	}
	if result.Attempts != 3 {
		t.Errorf("attempts: got %d, want 3", result.Attempts)
	}
	if calls != 3 {
		t.Errorf("fn calls: got %d, want 3", calls)
	}
}

func TestRetryWithBackoff_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	go func() {
		time.Sleep(15 * time.Millisecond)
		cancel()
	}()

	result := RetryWithBackoff(ctx, RetryConfig{
		MaxAttempts: 100,
		BaseDelay:   10 * time.Millisecond,
		MaxDelay:    10 * time.Millisecond,
	}, func(attempt int) error {
		calls++
		return &net.OpError{Op: "read", Err: errors.New("timeout")}
	})
	if result.Success {
		t.Error("expected failure on context cancellation")
	}
	if calls < 2 {
		t.Errorf("expected at least 2 attempts before cancellation, got %d", calls)
	}
}

func TestRetryWithBackoff_DefaultConfig(t *testing.T) {
	cfg := RetryConfig{}
	cfg.applyDefaults()
	if cfg.MaxAttempts != 5 {
		t.Errorf("default MaxAttempts: got %d, want 5", cfg.MaxAttempts)
	}
	if cfg.BaseDelay != time.Second {
		t.Errorf("default BaseDelay: got %v, want 1s", cfg.BaseDelay)
	}
	if cfg.MaxDelay != 16*time.Second {
		t.Errorf("default MaxDelay: got %v, want 16s", cfg.MaxDelay)
	}
}

// ---------------------------------------------------------------------------
// backoffDelay
// ---------------------------------------------------------------------------

func TestBackoffDelay(t *testing.T) {
	tests := []struct {
		attempt int
		base    time.Duration
		max     time.Duration
		want    time.Duration
	}{
		{1, time.Second, 16 * time.Second, time.Second},       // attempt 1: base
		{2, time.Second, 16 * time.Second, 2 * time.Second},   // attempt 2: 2x
		{3, time.Second, 16 * time.Second, 4 * time.Second},   // attempt 3: 4x
		{4, time.Second, 16 * time.Second, 8 * time.Second},   // attempt 4: 8x
		{5, time.Second, 16 * time.Second, 16 * time.Second},  // attempt 5: capped
		{6, time.Second, 16 * time.Second, 16 * time.Second},  // attempt 6: still capped
	}
	for _, tt := range tests {
		got := backoffDelay(tt.attempt, tt.base, tt.max)
		if got != tt.want {
			t.Errorf("backoffDelay(%d, %v, %v) = %v, want %v",
				tt.attempt, tt.base, tt.max, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// IsTransient
// ---------------------------------------------------------------------------

func TestIsTransient_NilError(t *testing.T) {
	if IsTransient(nil) {
		t.Error("nil error should not be transient")
	}
}

func TestIsTransient_NetError(t *testing.T) {
	err := &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	if !IsTransient(err) {
		t.Error("net.OpError should be transient")
	}
}

func TestIsTransient_TimeoutString(t *testing.T) {
	err := errors.New("request timeout after 30s")
	if !IsTransient(err) {
		t.Error("timeout error should be transient")
	}
}

func TestIsTransient_BrokenPipe(t *testing.T) {
	err := errors.New("write: broken pipe")
	if !IsTransient(err) {
		t.Error("broken pipe should be transient")
	}
}

func TestIsTransient_EOF(t *testing.T) {
	err := errors.New("unexpected EOF")
	if !IsTransient(err) {
		t.Error("EOF should be transient")
	}
}

func TestIsTransient_PermanentError(t *testing.T) {
	err := errors.New("invalid argument")
	if IsTransient(err) {
		t.Error("invalid argument should NOT be transient")
	}
}

func TestIsTransient_ECONNRESET(t *testing.T) {
	err := syscall.ECONNRESET
	if !IsTransient(err) {
		t.Error("ECONNRESET should be transient")
	}
}

func TestIsTransient_ECONNREFUSED(t *testing.T) {
	err := syscall.ECONNREFUSED
	if !IsTransient(err) {
		t.Error("ECONNREFUSED should be transient")
	}
}

func TestIsTransient_EPIPE(t *testing.T) {
	err := syscall.EPIPE
	if !IsTransient(err) {
		t.Error("EPIPE should be transient")
	}
}

func TestIsTransient_EHOSTUNREACH(t *testing.T) {
	err := syscall.EHOSTUNREACH
	if !IsTransient(err) {
		t.Error("EHOSTUNREACH should be transient")
	}
}

func TestIsTransient_ContextDeadlineExceeded(t *testing.T) {
	err := context.DeadlineExceeded
	if !IsTransient(err) {
		t.Error("context.DeadlineExceeded should be transient")
	}
}

func TestIsTransient_CaseInsensitive(t *testing.T) {
	err := errors.New("TIMEOUT occurred")
	if !IsTransient(err) {
		t.Error("should match case-insensitively")
	}
}

// ---------------------------------------------------------------------------
// MemoryDLQ — standalone tests
// ---------------------------------------------------------------------------

func TestMemoryDLQ_EnqueueAndDrain(t *testing.T) {
	dlq := NewMemoryDLQ(nil)
	ctx := context.Background()

	entry := DeadLetterEntry{
		Event:    Event{VersionID: "v1", TenantID: "t1"},
		Worker:   "test-worker",
		Error:    "something failed",
		Attempts: 3,
	}
	dlq.Enqueue(ctx, entry)

	if dlq.Size() != 1 {
		t.Errorf("size: got %d, want 1", dlq.Size())
	}

	entries, err := dlq.Drain(ctx, 10)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("drained: got %d, want 1", len(entries))
	}
	if entries[0].Worker != "test-worker" {
		t.Errorf("worker: got %q, want %q", entries[0].Worker, "test-worker")
	}
	if dlq.Size() != 0 {
		t.Errorf("size after drain: got %d, want 0", dlq.Size())
	}
}

func TestMemoryDLQ_DrainLimit(t *testing.T) {
	dlq := NewMemoryDLQ(nil)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		dlq.Enqueue(ctx, DeadLetterEntry{
			Event:  Event{VersionID: "v" + string(rune('0'+i))},
			Worker: "w",
		})
	}

	entries, _ := dlq.Drain(ctx, 3)
	if len(entries) != 3 {
		t.Errorf("drain limit: got %d, want 3", len(entries))
	}
	if dlq.Size() != 2 {
		t.Errorf("remaining: got %d, want 2", dlq.Size())
	}
}

func TestMemoryDLQ_DrainZeroLimit(t *testing.T) {
	dlq := NewMemoryDLQ(nil)
	ctx := context.Background()
	dlq.Enqueue(ctx, DeadLetterEntry{Event: Event{VersionID: "v1"}})

	entries, _ := dlq.Drain(ctx, 0)
	if len(entries) != 1 {
		t.Error("limit=0 should return all entries")
	}
}

func TestMemoryDLQ_DrainEmpty(t *testing.T) {
	dlq := NewMemoryDLQ(nil)
	entries, err := dlq.Drain(context.Background(), 10)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty, got %d", len(entries))
	}
}

func TestMemoryDLQ_SizeConcurrent(t *testing.T) {
	dlq := NewMemoryDLQ(nil)
	ctx := context.Background()

	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				dlq.Enqueue(ctx, DeadLetterEntry{Event: Event{VersionID: "v"}})
				dlq.Size()
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
	if dlq.Size() != 1000 {
		t.Errorf("size: got %d, want 1000", dlq.Size())
	}
}

func TestMemoryDLQ_SetCreatedAtIfZero(t *testing.T) {
	dlq := NewMemoryDLQ(nil)
	ctx := context.Background()

	entry := DeadLetterEntry{Event: Event{VersionID: "v1"}}
	dlq.Enqueue(ctx, entry)

	entries, _ := dlq.Drain(ctx, 1)
	if entries[0].CreatedAt.IsZero() {
		t.Error("CreatedAt should be set when initially zero")
	}
}

func TestFakeResultStore(t *testing.T) {
	store := NewFakeResultStore()
	ctx := context.Background()

	result := ProcessingResult{
		VersionID: "v1",
		Status:    StatusSuccess,
	}
	err := store.SaveResult(ctx, result)
	if err != nil {
		t.Fatalf("SaveResult: %v", err)
	}

	saved, err := store.GetResultsByVersion(ctx, "v1")
	if err != nil {
		t.Fatalf("GetResultsByVersion: %v", err)
	}
	if len(saved) != 1 {
		t.Fatalf("expected 1 result, got %d", len(saved))
	}
	if saved[0].VersionID != "v1" {
		t.Errorf("VersionID: got %q, want %q", saved[0].VersionID, "v1")
	}

	empty, _ := store.GetResultsByVersion(ctx, "nonexistent")
	if len(empty) != 0 {
		t.Error("expected empty for nonexistent key")
	}
}
