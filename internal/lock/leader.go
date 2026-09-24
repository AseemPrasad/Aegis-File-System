package lock

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// LeaderConfig configures cluster-wide leader election.
type LeaderConfig struct {
	Resource      string        // Leader key e.g. "aegis:gc:leader"
	LeaseDuration time.Duration // Lease TTL e.g. 15s
	RenewInterval time.Duration // Heartbeat interval e.g. 5s
	Locker        Locker
	Logger        *slog.Logger
}

// LeaderElection manages active node status in a horizontally scaled cluster.
type LeaderElection struct {
	cfg       LeaderConfig
	isLeader  int32
	activeTok LockToken
	mu        sync.Mutex
	stopChan  chan struct{}
}

func NewLeaderElection(cfg LeaderConfig) *LeaderElection {
	if cfg.Resource == "" {
		cfg.Resource = "aegis:gc:leader"
	}
	if cfg.LeaseDuration <= 0 {
		cfg.LeaseDuration = 15 * time.Second
	}
	if cfg.RenewInterval <= 0 {
		cfg.RenewInterval = 5 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	return &LeaderElection{
		cfg:      cfg,
		stopChan: make(chan struct{}),
	}
}

func (l *LeaderElection) IsLeader() bool {
	return atomic.LoadInt32(&l.isLeader) == 1
}

// StartCampaign begins continuous leader election heartbeat loop.
func (l *LeaderElection) StartCampaign(ctx context.Context) {
	ticker := time.NewTicker(l.cfg.RenewInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = l.Resign(context.Background())
			return
		case <-l.stopChan:
			_ = l.Resign(context.Background())
			return
		case <-ticker.C:
			l.step(ctx)
		}
	}
}

func (l *LeaderElection) step(ctx context.Context) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if atomic.LoadInt32(&l.isLeader) == 1 && l.activeTok != nil {
		// Refresh existing lease
		err := l.cfg.Locker.Refresh(ctx, l.activeTok, l.cfg.LeaseDuration)
		if err != nil {
			l.cfg.Logger.Warn("failed to refresh leader lease; step down", "err", err)
			atomic.StoreInt32(&l.isLeader, 0)
			l.activeTok = nil
		}
		return
	}

	// Campaign for leadership
	tok, err := l.cfg.Locker.Acquire(ctx, l.cfg.Resource, l.cfg.LeaseDuration)
	if err == nil {
		l.cfg.Logger.Info("acquired cluster leader election lock", "resource", l.cfg.Resource)
		atomic.StoreInt32(&l.isLeader, 1)
		l.activeTok = tok
	} else {
		atomic.StoreInt32(&l.isLeader, 0)
	}
}

func (l *LeaderElection) Resign(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if atomic.LoadInt32(&l.isLeader) == 1 && l.activeTok != nil {
		l.cfg.Logger.Info("resigning cluster leadership", "resource", l.cfg.Resource)
		err := l.cfg.Locker.Release(ctx, l.activeTok)
		atomic.StoreInt32(&l.isLeader, 0)
		l.activeTok = nil
		return err
	}
	return nil
}
