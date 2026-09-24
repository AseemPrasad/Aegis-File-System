package lock_test

import (
	"context"
	"testing"
	"time"

	"github.com/aegis-dev/aegis/internal/lock"
)

type MockLocker struct {
	acquired bool
}

func (m *MockLocker) Acquire(_ context.Context, _ string, ttl time.Duration) (lock.LockToken, error) {
	if m.acquired {
		return nil, lock.ErrLockHeld
	}
	m.acquired = true
	return &MockToken{}, nil
}

func (m *MockLocker) Release(_ context.Context, _ lock.LockToken) error {
	m.acquired = false
	return nil
}

func (m *MockLocker) Refresh(_ context.Context, _ lock.LockToken, _ time.Duration) error {
	return nil
}

type MockToken struct{}

func (t *MockToken) Resource() string { return "test-resource" }
func (t *MockToken) ID() string       { return "test-id" }
func (t *MockToken) TTL() time.Duration { return 10 * time.Second }

func TestLeaderElectionMock(t *testing.T) {
	locker := &MockLocker{}
	elector := lock.NewLeaderElection(lock.LeaderConfig{
		Resource:      "aegis:test:leader",
		LeaseDuration: 5 * time.Second,
		RenewInterval: 1 * time.Second,
		Locker:        locker,
	})

	if elector.IsLeader() {
		t.Error("new elector should not be leader before campaign")
	}
}
