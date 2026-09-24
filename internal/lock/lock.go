package lock

import (
	"context"
	"errors"
	"time"
)

var (
	ErrLockHeld      = errors.New("lock already held by another node")
	ErrLockExpired   = errors.New("lock has expired or token is invalid")
	ErrLeaderNotHeld = errors.New("node is not the current active leader")
)

// LockToken represents an acquired distributed lock token.
type LockToken interface {
	Resource() string
	ID() string
	TTL() time.Duration
}

// Locker abstracts distributed locking operations across storage backends (Redis / PostgreSQL).
type Locker interface {
	Acquire(ctx context.Context, resource string, ttl time.Duration) (LockToken, error)
	Release(ctx context.Context, token LockToken) error
	Refresh(ctx context.Context, token LockToken, ttl time.Duration) error
}

// LeaderElector manages cluster-wide leader election.
type LeaderElector interface {
	IsLeader() bool
	Campaign(ctx context.Context) error
	Resign(ctx context.Context) error
}
