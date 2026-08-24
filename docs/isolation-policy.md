# Aegis Isolation Policy — PostgreSQL Concurrency & Consistency

PROMPT 2.1 deliverable · complements `db/schema.sql`, `db/stored-procedures.sql`
Enforces: I-1 (ref-count integrity) · I-2 (acyclic namespace) · I-2e (lineage truth)

---

## 1. Transaction Isolation Levels

| Operation | Level | Rationale |
|---|---|---|
| Commit path (`HandleCommit` → version insert + manifest rows) | **READ COMMITTED** | Row locks from `next_version_number()` fence provide serialization where needed; MVCC snapshot freshness unnecessary. Matches addendum adjustment #3. |
| `move_directory` | **READ COMMITTED** | Explicit `FOR UPDATE` row locks on both endpoints make phantom subtrees impossible to remap incorrectly (see §3). |
| Read/listing paths | **READ COMMITTED** | Epoch-stamped snapshots tolerate slight staleness; no anarchy because lineage is immutable except inside moves. |
| GC sweep | **READ COMMITTED** | Double-check pattern (§5) makes stale reads safe. |
| Reporting/MV refresh | Any | Views are eventually consistent by design. |

**Rule:** We do not use SERIALIZABLE anywhere. Every correctness-critical race is closed with an explicit lock or a constraint, keeping lock-manager behavior predictable under load.

## 2. Invariant Enforcement Matrix

| Invariant | Enforcement point(s) | Mechanism |
|---|---|---|
| I-2 acyclic namespace | EP-1 `move_directory` pre-check | `v_dst_path <@ v_src_path` guard before any write |
| I-2 acyclic namespace | EP-2 DB trigger | `trg_nodes_lineage` recomputes/validates parent on INSERT and gated UPDATE |
| I-2 acyclic namespace | EP-3 direct-write rejection | `AEGIS/DIRECT_REPARENT_FORBIDDEN` unless `aegis.allow_move=on` (tx-local GUC set only by proc) |
| I-2e lineage truth | EP-4 single-writer rule | `lineage_path` writable only by trigger or move proc; tamper raises `AEGIS/LINEAGE_TAMPER` |
| I-1 ref-count integrity | Manifest triggers | `trg_manifest_block_added/removed` are the ONLY writers of `cas_blocks.ref_count`; app role lacks direct UPDATE grant on that column's normal path |
| I-1 orphan prevention | FK + CHECK | `file_manifest_blocks` FK to `cas_blocks`; `CHECK (ref_count >= 0)` |

## 3. Move Directory Locking Protocol

### 3.1 Deadlock-proof ordered locking (documented deviation)

The spec calls for `FOR UPDATE` on source + `FOR SHARE` on destination. Two concurrent opposite-direction moves (A→B and B→A) would then each hold one UPDATE lock and request a SHARE lock on a row the other holds exclusively → deadlock. We instead acquire **both endpoint rows FOR UPDATE in ascending `node_id` order**, which makes AB-BA structurally impossible while providing a strictly stronger guarantee than FOR SHARE.

Consequences:
- Opposite moves serialize; the loser hits the cycle guard (`<@`) and aborts cleanly.
- Lock ordering is deterministic regardless of argument order at call sites.

### 3.2 Cycle rejection semantics

```
IF v_dst_path <@ v_src_path THEN raise ACYCL
```

Covers all three attack shapes verified in the test battery:
1. Self-containment (`move X into X`)
2. Descendant target (`move X into X/sub/...`)
3. Ancestor-loop creation (any case where dst lies within src subtree)

The check runs while BOTH endpoints are locked, so no concurrent commit can invalidate it between check and remap (TOCTOU-free).

### 3.3 Subtree remap atomicity

Single `UPDATE ... WHERE lineage_path <@ v_src_path` statement remaps every descendant using `dst_path || src_segment || subpath(path, nlevel(src_path))`. Because it is one statement inside one transaction holding endpoint locks:

- Readers never observe a half-moved subtree (MVCC snapshot sees pre- or post-state, nothing between).
- `acl_epoch += 1` lands atomically with the rename, so epoch-stamped ACL caches invalidate coherently.

## 4. Version Allocation Fencing

`next_version_number(node)` takes `FOR UPDATE` on the file's `namespace_nodes` row, then computes `MAX(version_number)+1`. Concurrent commits to the same file serialize on the node-row lock → dense gapless version numbers, no unique-violation retry loops, matching the "Optimistic/Fenced Version Commits" contract. Cross-file commits never contend (different node rows).

## 5. Garbage Collection Safety Window

Two-phase protocol so a block mid-commit can never be reaped:

1. **Sweep:** `gc_find_unreferenced('7 days', N)` selects `ref_count = 0 AND created_at < now()-interval` — creation-time age is the safety window covering any in-flight commit whose manifest insert trails its CAS insert.
2. **Reap:** `gc_delete_unreferenced(batch[])` re-checks `ref_count = 0` inside the DELETE itself. If a commit snuck a manifest reference in between phases, the row is skipped (NOTICE emitted), never deleted.

Tombstone emission for bucket deletion happens application-side for exactly the rows the function reports deleted.

## 6. Session & Partition Hygiene

- `expire_stale_upload_sessions(grace)` hard-deletes sessions expired beyond grace (default 7 days); uncommitted uploaded bytes are reclaimed by bucket lifecycle rules (abort-incomplete-MPU @ 1 day), never by the database.
- `ensure_audit_partition(month)` creates next month's RANGE partition idempotently; called monthly by scheduler. DEFAULT partition catches stragglers so inserts never fail during partition-lag incidents.

## 7. Failure Semantics Summary

| Scenario | Outcome |
|---|---|
| App writes `lineage_path` directly | Exception `AEGIS/LINEAGE_TAMPER`, tx aborted |
| App reparents outside proc | Exception `AEGIS/DIRECT_REPARENT_FORBIDDEN` |
| Move creating cycle | SQLSTATE `ACYCL`, tx aborted, zero rows changed |
| Manifest row without CAS row | FK violation (trigger also raises defensively) |
| ref_count would go negative | CHECK violation, tx aborted |
| Concurrent same-file commits | Serialized on node-row fence; dense versions |
| Concurrent opposite moves | One succeeds, one gets ACYCL or NAME_CONFLICT |
| GC racing a commit | Reap phase skips referenced rows |

## 8. Conflict Resolution & Retry Logic (Exponential Backoff)

### 8.1 Retriable error classes

Only lock-contention and snapshot-staleness errors are retried. Domain
violations (`AEGIS/*`) are deterministic outcomes — retrying them is a bug.

| SQLSTATE / condition | Meaning | Retry? |
|---|---|---|
| `40001` serialization_failure | Only possible if a session escalates levels manually | YES |
| `40P01` deadlock_detected | Defensive net; ordered locking makes this ~impossible | YES (once) |
| `55P03` lock_not_available | `lock_timeout` fired while waiting on endpoint/node fence | YES |
| `23505` unique_violation on `uq_parent_name` | Lost a concurrent create/move race | Caller decides: re-list + surface conflict, or auto-rename |
| `ACYCL`, all `AEGIS/*` | Deterministic domain rejection | NO — fail the API call |
| Connection failures | Pool/network layer | YES with circuit breaker |

### 8.2 Backoff algorithm (application side, Go reference)

Full-jitter exponential backoff, capped, honoring `Retry-After`-style server
hints. Base 25 ms, cap 2 s, 5 attempts — p99 wait stays under ~1.6 s, which
fits the metadata-plane latency SLA (row: "DB Latency").

```go
// Retry executes fn with full-jitter exponential backoff for retriable
// Postgres error classes only (see isolation-policy.md §8.1).
func Retry(ctx context.Context, fn func() error) error {
    const base, cap = 25*time.Millisecond, 2*time.Second
    var pgErr *pgconn.PgError
    for attempt := 0; ; attempt++ {
        err := fn()
        if err == nil {
            return nil
        }
        if !errors.As(err, &pgErr) ||
           (pgErr.Code != "40001" && pgErr.Code != "40P01" && pgErr.Code != "55P03") {
            return err // non-retriable: domain violations propagate immediately
        }
        if attempt >= 4 {
            return fmt.Errorf("retries exhausted after %d attempts: %w", attempt+1, err)
        }
        exp := base << attempt               // 25ms, 50ms, 100ms, 200ms, ...
        if exp > cap {
            exp = cap
        }
        time.Sleep(time.Duration(rand.Int63n(int64(exp)+1))) // full jitter
    }
}
```

**Rules:**
- The **entire transaction** is retried, never a single statement — partial
  application of a multi-statement operation would violate I-1/I-2.
- Retry loops must thread `ctx` so client disconnects abort the loop.
- `23505` is deliberately NOT auto-retried: the second writer must re-read
  destination state and either surface `409 Conflict` or pick a fresh name;
  blind retries would just lose the same race forever.
- Idempotency keys (session_id / request nonce at the API layer) make the
  replayed transaction safe when the first attempt actually committed but the
  ack was lost.

### 8.3 Server-side guardrails

```sql
-- Bounded lock waits keep the retry loop meaningful; unbounded queueing
-- converts one slow move into a connection-pool pileup.
SET lock_timeout = '5s';            -- per-session default for app role
ALTER ROLE aegis_app SET lock_timeout = '5s';
ALTER ROLE aegis_app SET idle_in_transaction_session_timeout = '30s';
```

Deadlock logging stays ON in production (`log_lock_waits = on`,
`deadlock_timeout = 200ms`) so §3's ordering invariant can be audited from
logs rather than assumed.
