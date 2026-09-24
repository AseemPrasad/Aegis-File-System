package lock

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// RedisToken implements LockToken for Redis locks.
type RedisToken struct {
	resource string
	id       string
	ttl      time.Duration
}

func (t *RedisToken) Resource() string { return t.resource }
func (t *RedisToken) ID() string       { return t.id }
func (t *RedisToken) TTL() time.Duration { return t.ttl }

// RedisLocker implements atomic Redis Redlock operations using Lua scripts.
type RedisLocker struct {
	client *redis.Client
}

func NewRedisLocker(client *redis.Client) *RedisLocker {
	return &RedisLocker{client: client}
}

// Lua Script for atomic release
var unlockScript = redis.NewScript(`
	if redis.call("get", KEYS[1]) == ARGV[1] then
		return redis.call("del", KEYS[1])
	else
		return 0
	end
`)

// Lua Script for atomic TTL refresh
var refreshScript = redis.NewScript(`
	if redis.call("get", KEYS[1]) == ARGV[1] then
		return redis.call("pexpire", KEYS[1], ARGV[2])
	else
		return 0
	end
`)

func (r *RedisLocker) Acquire(ctx context.Context, resource string, ttl time.Duration) (LockToken, error) {
	tokenID := uuid.New().String()
	key := fmt.Sprintf("aegis:lock:%s", resource)

	ok, err := r.client.SetNX(ctx, key, tokenID, ttl).Result()
	if err != nil {
		return nil, fmt.Errorf("redis setnx error: %w", err)
	}
	if !ok {
		return nil, ErrLockHeld
	}

	return &RedisToken{
		resource: resource,
		id:       tokenID,
		ttl:      ttl,
	}, nil
}

func (r *RedisLocker) Release(ctx context.Context, token LockToken) error {
	key := fmt.Sprintf("aegis:lock:%s", token.Resource())
	res, err := unlockScript.Run(ctx, r.client, []string{key}, token.ID()).Int64()
	if err != nil {
		return fmt.Errorf("redis unlock script error: %w", err)
	}
	if res == 0 {
		return ErrLockExpired
	}
	return nil
}

func (r *RedisLocker) Refresh(ctx context.Context, token LockToken, ttl time.Duration) error {
	key := fmt.Sprintf("aegis:lock:%s", token.Resource())
	res, err := refreshScript.Run(ctx, r.client, []string{key}, token.ID(), ttl.Milliseconds()).Int64()
	if err != nil {
		return fmt.Errorf("redis refresh script error: %w", err)
	}
	if res == 0 {
		return ErrLockExpired
	}
	return nil
}
