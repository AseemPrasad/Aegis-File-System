# Distributed Consensus, Locking & Race-Free Garbage Collection Architecture

This document details the distributed locking, leader election, and race-free Garbage Collection (GC) sweep protocols for Project Aegis.

## Distributed Lock Engine (`internal/lock/`)

1. **Redis Redlock (`internal/lock/redlock.go`):** Atomic Redis Lua scripts (`SET resource_key token_uuid NX PX ttl`) with automated renewal heartbeats.
2. **PostgreSQL Advisory Locks (`internal/lock/advisory.go`):** Database-native `pg_try_advisory_lock` for zero-dependency locking.
3. **Leader Election (`internal/lock/leader.go`):** Ensures only a single active leader pod runs cluster-wide GC mark-and-sweep routines.

## Race-Free Two-Phase Deletion Protocol (`internal/gc/leader.go`)

- **Phase 1 (Mark Phase):** Identify unreferenced blocks (`ref_count = 0`) where `updated_at < NOW() - INTERVAL '24 hours'`.
- **Phase 2 (Sweep Phase):** Acquire distributed lock per `block_hash`, re-verify `ref_count == 0` under lock protection, publish tombstone event, and execute database hard deletion inside a serializable transaction.
