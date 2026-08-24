package database

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Circuit-breaker states.
type breakerState int32

const (
	breakerClosed   breakerState = iota // normal operation
	breakerOpen                         // failing — stop sending traffic
	breakerHalfOpen                     // cooldown elapsed — probe allowed
)

// String renders the state for logs/metrics.
func (s breakerState) String() string {
	switch s {
	case breakerOpen:
		return "open"
	case breakerHalfOpen:
		return "half-open"
	default:
		return "closed"
	}
}

// Breaker thresholds (PROMPT 2.2 failover strategy).
const (
	breakerFailureThreshold = 3                     // consecutive failures ⇒ open
	breakerCooldown         = 2 * time.Second       // open → half-open
	healthProbeTimeout      = DefaultAcquireTimeout // probe budget per ping
)

var ErrPrimaryUnavailable = errors.New("database: primary unavailable (circuit open)")

// CircuitBreaker gates traffic to the primary. Reads reroute to replicas when
// the breaker is open; writes return ErrPrimaryUnavailable immediately rather
// than piling onto a dying node (cascade-failure prevention).
//
// All fields are atomics: Allow() sits on every query path and must not take
// a lock that serializes the 1000-client design load.
type CircuitBreaker struct {
	state    atomic.Int32       // breakerState
	failures atomic.Int32       // consecutive failures while closed/half-open
	openedAt atomic.Int64       // unix nanos when opened
	probing  atomic.Bool        // one probe at a time in half-open
	onTrip   func(state string) // observability hook (optional)
}

func newCircuitBreaker(onTrip func(string)) *CircuitBreaker {
	cb := &CircuitBreaker{onTrip: onTrip}
	cb.state.Store(int32(breakerClosed))
	return cb
}

// Allow reports whether a request may proceed to the primary right now.
// In half-open state exactly one caller is elected to probe.
func (cb *CircuitBreaker) Allow() bool {
	switch breakerState(cb.state.Load()) {
	case breakerClosed:
		return true
	case breakerOpen:
		if time.Since(time.Unix(0, cb.openedAt.Load())) < breakerCooldown {
			return false
		}
		// Cooldown over: elect a single prober, transition to half-open.
		if cb.probing.CompareAndSwap(false, true) {
			cb.state.Store(int32(breakerHalfOpen))
			return true
		}
		return false
	default: // half-open: only the elected probe proceeds
		return false
	}
}

// RecordSuccess closes the breaker from half-open (probe succeeded) or clears
// the failure streak while closed.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.failures.Store(0)
	cb.probing.Store(false)
	if breakerState(cb.state.Swap(int32(breakerClosed))) != breakerClosed && cb.onTrip != nil {
		cb.onTrip("closed")
	}
}

// RecordFailure counts a primary failure; reaching the threshold opens the
// breaker and stamps the time for cooldown computation.
func (cb *CircuitBreaker) RecordFailure() {
	if n := cb.failures.Add(1); n >= breakerFailureThreshold &&
		cb.state.CompareAndSwap(int32(breakerClosed), int32(breakerOpen)) {
		cb.openedAt.Store(time.Now().UnixNano())
		if cb.onTrip != nil {
			cb.onTrip("open")
		}
	}
}

// State returns the current breaker state (for /healthz exposure).
func (cb *CircuitBreaker) State() string { return breakerState(cb.state.Load()).String() }

// healthLoop probes the primary until ctx is cancelled, feeding the breaker.
// Detection latency budget: interval × threshold ≈ 1.5s at defaults, which
// keeps RTO inside the SLA documented in docs/connection-pooling.md.
func (c *DatabaseClient) healthLoop(ctx context.Context) {
	t := time.NewTicker(c.cfg.HealthCheckInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(context.Background(), healthProbeTimeout)
			err := c.writePool.Ping(pctx)
			cancel()
			if err != nil {
				c.breaker.RecordFailure()
				continue
			}
			c.breaker.RecordSuccess()
		}
	}
}

// readTarget selects where a READ/ANALYTICAL query runs:
//
//   - replicas configured → round-robin across them (load-balanced reads;
//     the primary is preserved for the write path even when healthy)
//   - no replicas (dev)   → primary, gated by the breaker
//   - breaker open + replicas → still round-robin replicas (they ARE the
//     degradation target); without replicas the caller gets Unavailable
//     immediately instead of queueing on a dead node
//
// The atomic cursor gives even distribution so no single hot replica
// saturates its pool while siblings idle.
func (c *DatabaseClient) readTarget() (*pgxpool.Pool, bool) {
	if len(c.readPools) > 0 {
		return c.nextReplica(), true
	}
	if c.breaker.Allow() {
		return c.writePool, true // dev topology: primary doubles as read target
	}
	return nil, false
}

func (c *DatabaseClient) nextReplica() *pgxpool.Pool {
	if len(c.readPools) == 0 {
		return nil
	}
	i := c.readRR.Add(1) - 1
	return c.readPools[i%uint64(len(c.readPools))]
}
