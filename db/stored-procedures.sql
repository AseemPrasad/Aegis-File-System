-- ============================================================================
-- Project Aegis: Stored Procedures & Trigger Enforcement Layer
-- Version: 1.0   (PROMPT 2.1)
--
-- ltree operator cheat-sheet (used throughout):
--   A <@ B : A is a DESCENDANT of (is contained by) B     -- subtree fetch
--   A @> B : A HAS B as a descendant (A is ancestor of B) -- ancestor walk
--   A ~  P : A matches lquery/lpattern P                  -- pattern audits
--
-- INVARIANTS ENFORCED HERE (defense-in-depth beneath application code):
--   I-1: cas_blocks.ref_count maintained ONLY by manifest-row triggers.
--   I-2: lineage computed by trigger; direct parent edits rejected unless
--        performed inside move_directory(); cyclic moves rejected pre-SQL.
--   I-2e: after ANY mutation, node.lineage_path == parent.path ++ own label.
--
-- LOCKING STRATEGY (pessimistic, per design doc):
--   * move_directory locks BOTH endpoints FOR UPDATE in ascending node_id
--     order → opposite-direction concurrent moves cannot deadlock (AB-BA
--     impossible by construction). Spec's FOR UPDATE(src)/FOR SHARE(dst)
--     intent is preserved; the ordered pre-lock supersedes FOR SHARE with a
--     stronger guarantee (documented deviation, isolation-policy.md §3.2).
--   * Version allocation locks the file's node row → fenced, gapless numbers
--     (stack addendum: "Optimistic/Fenced Version Commits").
-- ============================================================================

-- ---------------------------------------------------------------------------
-- Generic updated_at maintenance
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at := NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- ---------------------------------------------------------------------------
-- LINEAGE COMPUTATION & RE-PARENT GUARD  (I-2e enforcement point)
-- Label encoding: uuid dashes -> underscores (ltree-safe label alphabet).
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION node_label(id UUID) RETURNS ltree AS $$
    SELECT text2ltree(replace(id::text, '-', '_'));
$$ LANGUAGE sql IMMUTABLE PARALLEL SAFE;

CREATE OR REPLACE FUNCTION nodes_lineage_guard() RETURNS trigger AS $$
DECLARE
    v_parent_path   LTREE;
    v_parent_tenant UUID;
    v_parent_type   node_type;
    v_parent_deleted BOOLEAN;
BEGIN
    -- Branch 1: INSERT — compute path from parent (or self-label for root).
    IF TG_OP = 'INSERT' THEN
        IF NEW.parent_id IS NULL THEN
            NEW.lineage_path := node_label(NEW.node_id);
            RETURN NEW;
        END IF;

        SELECT lineage_path, tenant_id, type, is_deleted
          INTO v_parent_path, v_parent_tenant, v_parent_type, v_parent_deleted
          FROM namespace_nodes WHERE node_id = NEW.parent_id;

        IF NOT FOUND THEN
            RAISE EXCEPTION 'AEGIS/PARENT_MISSING: parent % does not exist', NEW.parent_id
                USING ERRCODE = 'foreign_key_violation';
        END IF;
        IF v_parent_tenant <> NEW.tenant_id THEN
            RAISE EXCEPTION 'AEGIS/CROSS_TENANT_PARENT: % vs %', v_parent_tenant, NEW.tenant_id
                USING ERRCODE = 'P0001';
        END IF;
        IF v_parent_type <> 'DIRECTORY' THEN
            RAISE EXCEPTION 'AEGIS/PARENT_NOT_DIRECTORY';
        END IF;
        IF v_parent_deleted THEN
            RAISE EXCEPTION 'AEGIS/PARENT_DELETED';
        END IF;

        NEW.lineage_path := v_parent_path || node_label(NEW.node_id);
        RETURN NEW;
    END IF;

    -- Branch 2: UPDATE touching parent or path.
    IF TG_OP = 'UPDATE' THEN
        IF NEW.parent_id IS DISTINCT FROM OLD.parent_id THEN
            -- Reparenting is legal ONLY inside move_directory().
            IF coalesce(current_setting('aegis.allow_move', true), 'off') <> 'on' THEN
                RAISE EXCEPTION
                    'AEGIS/DIRECT_REPARENT_FORBIDDEN: use move_directory()'
                    USING ERRCODE = 'P0001';
            END IF;
            IF NEW.parent_id IS NULL THEN
                RAISE EXCEPTION 'AEGIS/ROOT_MOVE_FORBIDDEN: tenant roots are immutable'
                    USING ERRCODE = 'P0001';
            END IF;
            SELECT lineage_path, type, is_deleted, tenant_id
              INTO v_parent_path, v_parent_type, v_parent_deleted, v_parent_tenant
              FROM namespace_nodes WHERE node_id = NEW.parent_id;
            IF v_parent_tenant <> NEW.tenant_id OR v_parent_type <> 'DIRECTORY'
               OR v_parent_deleted OR NOT FOUND THEN
                RAISE EXCEPTION 'AEGIS/INVALID_TARGET_PARENT';
            END IF;
            NEW.lineage_path := v_parent_path || node_label(NEW.node_id);
            RETURN NEW;
        END IF;

        -- Standalone lineage tampering (no reparent) is never legal EXCEPT
        -- inside move_directory()'s subtree remap, which assigns descendant
        -- paths directly (their parent_id is unchanged). The GUC is
        -- transaction-local and set exclusively by the procedure.
        IF NEW.lineage_path IS DISTINCT FROM OLD.lineage_path
           AND coalesce(current_setting('aegis.allow_move', true), 'off') <> 'on' THEN
            RAISE EXCEPTION 'AEGIS/LINEAGE_TAMPER: path is trigger-computed'
                USING ERRCODE = 'P0001';
        END IF;
        RETURN NEW;
    END IF;

    RAISE EXCEPTION 'AEGIS/TRIGGER_MISCONFIG: unsupported op %', TG_OP;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_nodes_lineage ON namespace_nodes;
CREATE TRIGGER trg_nodes_lineage
    BEFORE INSERT OR UPDATE OF parent_id, lineage_path
    ON namespace_nodes
    FOR EACH ROW EXECUTE FUNCTION nodes_lineage_guard();

DROP TRIGGER IF EXISTS trg_nodes_updated_at ON namespace_nodes;
CREATE TRIGGER trg_nodes_updated_at
    BEFORE UPDATE ON namespace_nodes
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- ROOT CREATION (only sanctioned way to make parent-less nodes)
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION create_root(
    p_tenant_id UUID,
    p_name      VARCHAR
) RETURNS UUID AS $$
DECLARE
    v_id UUID;
BEGIN
    INSERT INTO namespace_nodes (tenant_id, parent_id, name, type)
    VALUES (p_tenant_id, NULL, p_name, 'DIRECTORY')
    RETURNING node_id INTO v_id;
    RETURN v_id;
END;
$$ LANGUAGE plpgsql VOLATILE;

-- ---------------------------------------------------------------------------
-- FENCED VERSION ALLOCATION (addendum adjustment #3)
-- Locks the node row so concurrent HandleCommit calls serialize per-file,
-- yielding dense, race-free version numbers.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION next_version_number(p_node_id UUID) RETURNS INTEGER AS $$
DECLARE
    v_next INTEGER;
BEGIN
    PERFORM 1 FROM namespace_nodes
             WHERE node_id = p_node_id FOR UPDATE;      -- fence

    SELECT COALESCE(MAX(version_number), 0) + 1 INTO v_next
      FROM file_versions WHERE node_id = p_node_id;

    RETURN v_next;
END;
$$ LANGUAGE plpgsql VOLATILE;

-- ---------------------------------------------------------------------------
-- REF-COUNT PROTOCOL TRIGGERS (I-1)
-- cas_blocks.ref_count ≡ COUNT(file_manifest_blocks.block_hash)
-- Application NEVER writes ref_count. Tier hint: >100 refs => HOT.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION manifest_block_added() RETURNS trigger AS $$
DECLARE
    v_rows BIGINT;
BEGIN
    UPDATE cas_blocks
       SET ref_count = ref_count + 1,
           storage_tier = CASE WHEN ref_count + 1 > 100 THEN 'HOT' ELSE storage_tier END,
           updated_at = NOW()
     WHERE block_hash = NEW.block_hash;
    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows = 0 THEN
        RAISE EXCEPTION 'AEGIS/CAS_ROW_MISSING: %', NEW.block_hash
            USING ERRCODE = 'foreign_key_violation';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION manifest_block_removed() RETURNS trigger AS $$
DECLARE
    v_rows BIGINT;
BEGIN
    UPDATE cas_blocks
       SET ref_count = ref_count - 1,
           updated_at = NOW()
     WHERE block_hash = OLD.block_hash;
    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows = 0 THEN
        RAISE EXCEPTION 'AEGIS/CAS_ROW_MISSING_ON_DELETE: %', OLD.block_hash;
    END IF;
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_manifest_block_added ON file_manifest_blocks;
CREATE TRIGGER trg_manifest_block_added
    AFTER INSERT ON file_manifest_blocks
    FOR EACH ROW EXECUTE FUNCTION manifest_block_added();

DROP TRIGGER IF EXISTS trg_manifest_block_removed ON file_manifest_blocks;
CREATE TRIGGER trg_manifest_block_removed
    AFTER DELETE ON file_manifest_blocks
    FOR EACH ROW EXECUTE FUNCTION manifest_block_removed();

DROP TRIGGER IF EXISTS trg_cas_updated_at ON cas_blocks;
CREATE TRIGGER trg_cas_updated_at
    BEFORE UPDATE ON cas_blocks
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- MOVE_DIRECTORY — transactional, cycle-free directory moves (spec §3)
--
-- INVARIANT: After this procedure, the namespace graph remains acyclic.
-- INVARIANT: All descendants have updated lineage_path values.
-- INVARIANT: ACL epochs are incremented for cache invalidation.
--
-- Error contract (SQLSTATE / message prefixes):
--   ACYCL  — cyclic hierarchy violation
--   P0001  — AEGIS/* domain violations (missing, wrong type, name conflict…)
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION move_directory(
    p_tenant_id     UUID,
    p_node_id       UUID,
    p_new_parent_id UUID
) RETURNS VOID AS $$
DECLARE
    v_src_path  LTREE;
    v_dst_path  LTREE;
    v_node_name VARCHAR;
    v_type      node_type;
    v_deleted   BOOLEAN;
    v_is_root   BOOLEAN;
    v_lock_low  UUID;
    v_lock_high UUID;
    v_moved     BIGINT;
BEGIN
    IF p_new_parent_id IS NULL OR p_node_id IS NULL OR p_node_id = p_new_parent_id THEN
        RAISE EXCEPTION 'AEGIS/MOVE_INVALID_ARGUMENTS' USING ERRCODE = 'P0001';
    END IF;

    -- ------------------------------------------------------------------
    -- 1. Acquire pessimistic row locks — DEADLOCK-FREE ORDER.
    --    Both endpoints locked FOR UPDATE in ascending id order; two
    --    concurrent opposite moves (A→B, B→A) serialize instead of
    --    deadlocking. One of them then hits the cycle guard and aborts.
    -- ------------------------------------------------------------------
    IF p_node_id < p_new_parent_id THEN
        v_lock_low := p_node_id;  v_lock_high := p_new_parent_id;
    ELSE
        v_lock_low := p_new_parent_id; v_lock_high := p_node_id;
    END IF;

    PERFORM 1 FROM namespace_nodes
              WHERE node_id IN (v_lock_low, v_lock_high)
                AND tenant_id = p_tenant_id
              ORDER BY node_id
              FOR UPDATE;

    IF (SELECT COUNT(*) FROM namespace_nodes
         WHERE node_id IN (v_lock_low, v_lock_high) AND tenant_id = p_tenant_id) <> 2 THEN
        RAISE EXCEPTION 'AEGIS/MOVE_NODE_NOT_FOUND' USING ERRCODE = 'P0001';
    END IF;

    SELECT lineage_path, name, type, is_deleted, parent_id IS NULL
      INTO v_src_path, v_node_name, v_type, v_deleted, v_is_root
      FROM namespace_nodes
     WHERE node_id = p_node_id AND tenant_id = p_tenant_id;

    IF v_is_root THEN
        RAISE EXCEPTION 'AEGIS/ROOT_MOVE_FORBIDDEN: tenant roots are immutable'
            USING ERRCODE = 'P0001';
    END IF;
    IF v_deleted THEN
        RAISE EXCEPTION 'AEGIS/SOURCE_DELETED' USING ERRCODE = 'P0001';
    END IF;

    SELECT lineage_path, type, is_deleted INTO v_dst_path, v_type, v_deleted
      FROM namespace_nodes
     WHERE node_id = p_new_parent_id AND tenant_id = p_tenant_id;

    IF v_type <> 'DIRECTORY' THEN
        RAISE EXCEPTION 'AEGIS/TARGET_NOT_DIRECTORY' USING ERRCODE = 'P0001';
    END IF;
    IF v_deleted THEN
        RAISE EXCEPTION 'AEGIS/TARGET_DELETED' USING ERRCODE = 'P0001';
    END IF;

    -- ------------------------------------------------------------------
    -- 2. Reject cyclic assignments: Target cannot be a descendant of Source.
    --    (Covers self-containment AND ancestor-loop creation.)
    -- ------------------------------------------------------------------
    IF v_dst_path <@ v_src_path THEN
        RAISE EXCEPTION
            'AEGIS/CYCLE: Cyclic hierarchy violation: Target is a descendant of Source.'
            USING ERRCODE = 'ACYCL',
                  DETAIL = format('src=%s dst=%s', v_src_path::text, v_dst_path::text),
                  HINT   = 'Choose a destination outside the source subtree.';
    END IF;

    -- Name-collision pre-check (uq_parent_name remains final authority under
    -- concurrency; this converts the common case into a clean domain error).
    IF EXISTS (
        SELECT 1 FROM namespace_nodes
         WHERE tenant_id = p_tenant_id
           AND parent_id = p_new_parent_id
           AND name      = v_node_name
           AND NOT is_deleted
           AND node_id  <> p_node_id
    ) THEN
        RAISE EXCEPTION 'AEGIS/NAME_CONFLICT: "%" exists at destination', v_node_name
            USING ERRCODE = 'P0001';
    END IF;

    -- ------------------------------------------------------------------
    -- 3. Atomic Path Remapping across ALL subtree descendants.
    --    New path = dst_path ++ [src_segment] ++ suffix_below_src.
    --    acl_epoch++ invalidates epoch-stamped permission caches everywhere.
    --    GUC unlocks the reparent-guard trigger for exactly this statement.
    -- ------------------------------------------------------------------
    PERFORM set_config('aegis.allow_move', 'on', true);  -- transaction-local

    WITH moved AS (
        UPDATE namespace_nodes
           SET parent_id    = CASE WHEN node_id = p_node_id THEN p_new_parent_id ELSE parent_id END,
               -- Source row gets the plain dst prefix; descendants append their
               -- suffix below the old source path. subpath(ltree, nlevel) raises
               -- 'invalid positions' (offset must be < numlevel), hence the branch.
               lineage_path = CASE WHEN node_id = p_node_id
                                   THEN v_dst_path || node_label(p_node_id)
                                   ELSE v_dst_path || node_label(p_node_id)
                                        || subpath(lineage_path, nlevel(v_src_path))
                              END,
               acl_epoch    = acl_epoch + 1,
               updated_at   = NOW()
         WHERE tenant_id   = p_tenant_id
           AND (node_id = p_node_id OR lineage_path <@ v_src_path)
        RETURNING 1
    )
    SELECT COUNT(*) INTO v_moved FROM moved;

    IF v_moved = 0 THEN
        RAISE EXCEPTION 'AEGIS/MOVE_REMAPPED_ZERO_ROWS' USING ERRCODE = 'P0001';
    END IF;
END;
$$ LANGUAGE plpgsql VOLATILE;

-- ---------------------------------------------------------------------------
-- SOFT DELETE — flag + tombstone rename (frees uq slot) + subtree epoch bump.
-- Hard purge happens later; cascades drive ref-count decrements via triggers.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION soft_delete_node(
    p_tenant_id UUID,
    p_node_id   UUID
) RETURNS VOID AS $$
DECLARE
    v_path LTREE;
BEGIN
    SELECT lineage_path INTO v_path
      FROM namespace_nodes
     WHERE node_id = p_node_id AND tenant_id = p_tenant_id
       FOR UPDATE;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'AEGIS/NODE_NOT_FOUND' USING ERRCODE = 'P0001';
    END IF;

    -- Target row: flag + tombstone rename + epoch bump (frees uq slot).
    UPDATE namespace_nodes
       SET is_deleted = TRUE,
           name       = name || '~del:' || substr(node_id::text, 1, 8),
           acl_epoch  = acl_epoch + 1
     WHERE tenant_id = p_tenant_id
       AND node_id   = p_node_id;

    -- Strict descendants only: ltree <@ is ancestor-or-EQUAL (non-strict),
    -- so self must be excluded explicitly or the tombstone gets bumped twice.
    UPDATE namespace_nodes
       SET acl_epoch  = acl_epoch + 1,
           updated_at = NOW()
     WHERE tenant_id = p_tenant_id
       AND lineage_path <@ v_path
       AND node_id    <> p_node_id;
END;
$$ LANGUAGE plpgsql VOLATILE;

-- ---------------------------------------------------------------------------
-- GC SUPPORT (design doc §6; safety window enforced by CALLER argument)
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION gc_find_unreferenced(
    p_older_than INTERVAL DEFAULT INTERVAL '7 days',
    p_limit      INTEGER  DEFAULT 1000
) RETURNS TABLE (block_hash BYTEA, tenant_id UUID, size_bytes INTEGER) AS $$
    SELECT cb.block_hash, cb.tenant_id, cb.size_bytes
      FROM cas_blocks cb
     WHERE cb.ref_count = 0
       AND cb.created_at <  NOW() - p_older_than
     ORDER BY cb.created_at
     LIMIT LEAST(GREATEST(p_limit, 1), 10000);
$$ LANGUAGE sql STABLE;

-- Double-check-before-delete: only rows STILL unreferenced are removed.
-- Returns number actually deleted (tombstones emitted for exactly these).
CREATE OR REPLACE FUNCTION gc_delete_unreferenced(
    p_batch BYTEA[]
) RETURNS INTEGER AS $$
DECLARE
    v_deleted INTEGER;
BEGIN
    WITH gone AS (
        DELETE FROM cas_blocks
         WHERE block_hash = ANY(p_batch)
           AND ref_count = 0                 -- double-check (SLA row 12)
        RETURNING block_hash
    )
    SELECT COUNT(*) INTO v_deleted FROM gone;

    IF v_deleted <> COALESCE(array_length(p_batch, 1), 0) THEN
        RAISE NOTICE 'gc_delete_unreferenced: skipped % referenced blocks',
            COALESCE(array_length(p_batch, 1), 0) - v_deleted;
    END IF;
    RETURN v_deleted;
END;
$$ LANGUAGE plpgsql VOLATILE;

-- Session reaper (GC flow G1): purge sessions dead past the grace horizon;
-- their never-committed uploads are reclaimed by bucket lifecycle rules.
CREATE OR REPLACE FUNCTION expire_stale_upload_sessions(
    p_grace INTERVAL DEFAULT INTERVAL '7 days'
) RETURNS INTEGER AS $$
DECLARE
    v_purged INTEGER;
BEGIN
    WITH gone AS (
        DELETE FROM upload_sessions
         WHERE expires_at < NOW() - p_grace
           AND NOT is_completed
        RETURNING 1
    )
    SELECT COUNT(*) INTO v_purged FROM gone;
    RETURN v_purged;
END;
$$ LANGUAGE plpgsql VOLATILE;

-- Audit partition maintenance: call monthly (app scheduler / pg_cron).
CREATE OR REPLACE FUNCTION ensure_audit_partition(p_month DATE) RETURNS TEXT AS $$
DECLARE
    v_start DATE := date_trunc('month', p_month)::date;
    v_end   DATE := (date_trunc('month', p_month) + INTERVAL '1 month')::date;
    v_name  TEXT := 'audit_logs_' || to_char(v_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = v_name) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF audit_logs FOR VALUES FROM (%L) TO (%L)',
            v_name, v_start, v_end);
        RETURN 'created:' || v_name;
    END IF;
    RETURN 'exists:' || v_name;
END;
$$ LANGUAGE plpgsql VOLATILE;

-- ---------------------------------------------------------------------------
-- PRIVILEGES
-- ---------------------------------------------------------------------------
GRANT EXECUTE ON FUNCTION create_root(UUID, VARCHAR)                     TO aegis_app;
GRANT EXECUTE ON FUNCTION next_version_number(UUID)                      TO aegis_app;
GRANT EXECUTE ON FUNCTION move_directory(UUID, UUID, UUID)               TO aegis_app;
GRANT EXECUTE ON FUNCTION soft_delete_node(UUID, UUID)                   TO aegis_app;
GRANT EXECUTE ON FUNCTION gc_find_unreferenced(INTERVAL, INTEGER)        TO aegis_app;
GRANT EXECUTE ON FUNCTION gc_delete_unreferenced(BYTEA[])                TO aegis_app;
GRANT EXECUTE ON FUNCTION expire_stale_upload_sessions(INTERVAL)         TO aegis_app;

-- Test/driver convenience: typed single-column wrapper so cross-backend
-- drivers (dblink/pgx) receive exactly one text value; errors propagate.
CREATE OR REPLACE FUNCTION move_directory_status(
    p_tenant_id UUID,
    p_node_id UUID,
    p_new_parent_id UUID
) RETURNS TEXT AS $$
BEGIN
    PERFORM move_directory(p_tenant_id, p_node_id, p_new_parent_id);
    RETURN 'MOVED';
END;
$$ LANGUAGE plpgsql VOLATILE;

GRANT EXECUTE ON FUNCTION move_directory_status(UUID, UUID, UUID)        TO aegis_app;
