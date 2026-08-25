package derivation

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"
	"time"
)

// ---------------------------------------------------------------------------
// Retry — exponential backoff for transient failures
// ---------------------------------------------------------------------------

// RetryConfig controls retry behavior.
type RetryConfig struct {
	MaxAttempts int           // Total attempts (1 = no retry). Default 5.
	BaseDelay   time.Duration // Initial backoff. Default 1s.
	MaxDelay    time.Duration // Cap on backoff. Default 16s.
}

func (c *RetryConfig) applyDefaults() {
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 5
	}
	if c.BaseDelay <= 0 {
		c.BaseDelay = 1 * time.Second
	}
	if c.MaxDelay <= 0 {
		c.MaxDelay = 16 * time.Second
	}
}

// RetryResult captures the outcome of a retried operation.
type RetryResult struct {
	Attempts int
	LastErr  error
	Success  bool
}

// RetryWithBackoff executes fn with exponential backoff on transient errors.
// Returns immediately on success or when a permanent error is detected.
func RetryWithBackoff(ctx context.Context, cfg RetryConfig, fn func(attempt int) error) RetryResult {
	cfg.applyDefaults()

	var lastErr error
	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		if err := fn(attempt); err != nil {
			lastErr = err
			if !IsTransient(err) {
				return RetryResult{Attempts: attempt, LastErr: err, Success: false}
			}
			if attempt < cfg.MaxAttempts {
				delay := backoffDelay(attempt, cfg.BaseDelay, cfg.MaxDelay)
				select {
				case <-ctx.Done():
					return RetryResult{Attempts: attempt, LastErr: ctx.Err(), Success: false}
				case <-time.After(delay):
				}
			}
			continue
		}
		return RetryResult{Attempts: attempt, Success: true}
	}
	return RetryResult{Attempts: cfg.MaxAttempts, LastErr: lastErr, Success: false}
}

// backoffDelay computes exponential backoff with jitter.
func backoffDelay(attempt int, base, max time.Duration) time.Duration {
	delay := base
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay > max {
			delay = max
			break
		}
	}
	return delay
}

// ---------------------------------------------------------------------------
// Transient error classification
// ---------------------------------------------------------------------------

// IsTransient returns true if the error is expected to be temporary
// (network timeout, connection reset, DNS resolution, etc.).
func IsTransient(err error) bool {
	if err == nil {
		return false
	}

	// Check for common transient network errors.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	// Connection reset by peer.
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case syscall.ECONNRESET, syscall.ECONNREFUSED, syscall.EPIPE,
			syscall.ECONNABORTED, syscall.EHOSTUNREACH:
			return true
		}
	}

	// String-based heuristics for errors from third-party libraries.
	msg := strings.ToLower(err.Error())
	transientPatterns := []string{
		"timeout",
		"connection reset",
		"connection refused",
		"broken pipe",
		"no route to host",
		"network is unreachable",
		"temporary failure",
		"context deadline exceeded",
		"i/o timeout",
		"eof",
	}
	for _, p := range transientPatterns {
		if strings.Contains(msg, p) {
			return true
		}
	}

	return false
}
