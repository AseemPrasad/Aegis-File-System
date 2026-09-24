package lock

import (
	"context"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresToken implements LockToken for database advisory locks.
type PostgresToken struct {
	resource string
	id       string
	lockID   int64
	ttl      time.Duration
}

func (t *PostgresToken) Resource() string { return t.resource }
func (t *PostgresToken) ID() string       { return t.id }
func (t *PostgresToken) TTL() time.Duration { return t.ttl }

// PostgresAdvisoryLocker implements database-native PostgreSQL advisory locking.
type PostgresAdvisoryLocker struct {
	pool *pgxpool.Pool
}

func NewPostgresAdvisoryLocker(pool *pgxpool.Pool) *PostgresAdvisoryLocker {
	return &PostgresAdvisoryLocker{pool: pool}
}

func hashKey(resource string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(resource))
	return int64(h.Sum64())
}

func (p *PostgresAdvisoryLocker) Acquire(ctx context.Context, resource string, ttl time.Duration) (LockToken, error) {
	lockID := hashKey(resource)
	var acquired bool

	err := p.pool.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", lockID).Scan(&acquired)
	if err != nil {
		return nil, fmt.Errorf("pg_try_advisory_lock query error: %w", err)
	}

	if !acquired {
		return nil, ErrLockHeld
	}

	return &PostgresToken{
		resource: resource,
		id:       uuid.New().String(),
		lockID:   lockID,
		ttl:      ttl,
	}, nil
}

func (p *PostgresAdvisoryLocker) Release(ctx context.Context, token LockToken) error {
	pgToken, ok := token.(*PostgresToken)
	if !ok {
		return ErrLockExpired
	}

	var released bool
	err := p.pool.QueryRow(ctx, "SELECT pg_advisory_unlock($1)", pgToken.lockID).Scan(&released)
	if err != nil {
		return fmt.Errorf("pg_advisory_unlock query error: %w", err)
	}

	if !released {
		return ErrLockExpired
	}

	return nil
}

func (p *PostgresAdvisoryLocker) Refresh(_ context.Context, _ LockToken, _ time.Duration) error {
	// PostgreSQL advisory locks remain held until explicitly unlocked or connection dies
	return nil
}
