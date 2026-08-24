-- ============================================================================
-- Project Aegis: Index Definitions
-- Version: 1.0   (PROMPT 2.1)
--
-- Every index documents the exact query patterns it serves. An index without
-- a named pattern is deleted in review — no speculative indexing.
-- Selectivity evidence (EXPLAIN ANALYZE) lives in performance-report.md.
-- ============================================================================

-- ---------------------------------------------------------------------------
-- NAMESPACE
-- ---------------------------------------------------------------------------

-- Spec-mandated: GIST on materialized lineage.
-- Supports (all O(matches), no recursive CTE):
--   * Subtree fetch:      WHERE lineage_path <@ 'root.A.B'          -- descendants
--   * Ancestor walk:      WHERE lineage_path @> 'root.A.B.C.leaf'   -- ACL resolution chain
--   * Pattern audit:      WHERE lineage_path ~ 'root.*.{2}'
CREATE INDEX idx_namespace_lineage ON namespace_nodes USING GIST (lineage_path);

-- Spec-mandated: sibling listing + parent joins.
-- Supports:
--   * List folder:   WHERE tenant_id=$1 AND parent_id=$2 AND NOT is_deleted ORDER BY name
--   * FK validation on child inserts (parent existence check)
-- INCLUDE turns listings into index-only scans (anti-N+1: one index read per
-- directory listing, zero heap visits).
CREATE INDEX idx_namespace_parent ON namespace_nodes (tenant_id, parent_id)
    INCLUDE (name, type, node_id, acl_epoch)
    WHERE is_deleted = FALSE;

-- ---------------------------------------------------------------------------
-- CAS REGISTRY / GC
-- ---------------------------------------------------------------------------

-- Spec-mandated name, generalized purpose: tenant usage sweeps & attribution.
-- Supports:
--   * Tenant byte accounting:  SELECT sum(size_bytes) … WHERE tenant_id=$1 GROUP BY storage_tier
--   * Tenant-scoped audits of block age distribution
CREATE INDEX idx_cas_tenant_created ON cas_blocks (tenant_id, created_at);

-- Generational GC sweep — partial tiny index holding ONLY deletable rows.
-- Supports:
--   * WHERE ref_count = 0 AND created_at < NOW() - INTERVAL '7 days'
--     (design doc §6 step 1). Partial predicate keeps it at near-zero size in
--     steady state; INCLUDE covers the sweep's entire output projection so
--     candidate discovery is index-only.
CREATE INDEX idx_cas_gc_sweep ON cas_blocks (created_at)
    INCLUDE (block_hash, tenant_id, size_bytes)
    WHERE ref_count = 0;

-- Reverse mapping for GC double-check & tombstone auditing:
--   * "Which versions reference this block?" before physical delete (SLA row 12).
CREATE INDEX idx_manifest_block_hash ON file_manifest_blocks (block_hash);

-- ---------------------------------------------------------------------------
-- VERSIONS / MANIFEST READS
-- ---------------------------------------------------------------------------

-- Chunk assembly read path (Flow R2): manifest rows for a version in chunk
-- order. The PRIMARY KEY (version_id, chunk_index) IS this contract's index —
-- a separate idx_file_manifest_version would be a duplicate structure; the PK
-- is explicitly documented here as serving that role (selectivity = perfect,
-- unique prefix).
--   SELECT block_hash, offset_bytes, size_bytes
--   FROM file_manifest_blocks WHERE version_id=$1 ORDER BY chunk_index;

-- Whole-file dedup gate (zero-transfer duplicate uploads):
--   * Initiate fast-path: WHERE content_sha256 = $1 → reuse existing version
CREATE INDEX idx_versions_content_hash ON file_versions (content_sha256);

-- Latest-version lookups for listings that need sizes without matview lag:
--   * WHERE node_id=$1 ORDER BY version_number DESC LIMIT 1 (uq_node_version serves;
--   * covering extension below supports "top N versions" UI panels index-only)
CREATE INDEX idx_versions_node_recent ON file_versions (node_id, created_at DESC)
    INCLUDE (version_number, total_size_bytes, mime_type);

-- ---------------------------------------------------------------------------
-- ACL RESOLUTION
-- ---------------------------------------------------------------------------
-- Permission check walks ancestor chain then probes grants:
--   SELECT role FROM acl_entries
--   WHERE node_id = ANY($ancestor_ids) AND principal_id=$p AND (expires_at IS NULL OR expires_at>NOW())
CREATE INDEX idx_acl_node_principal ON acl_entries (node_id, principal_type, principal_id);

-- ---------------------------------------------------------------------------
-- SESSIONS / REAPER
-- ---------------------------------------------------------------------------
-- Hourly reaper (GC flow G1): only incomplete sessions matter.
CREATE INDEX idx_sessions_expiry ON upload_sessions (expires_at) WHERE is_completed = FALSE;

-- Commit-path session fetch is served by PK(session_id).

-- ---------------------------------------------------------------------------
-- AUDIT (partitioned parent — indexes propagate to all partitions)
-- ---------------------------------------------------------------------------
CREATE INDEX idx_audit_time ON audit_logs (occurred_at DESC);
CREATE INDEX idx_audit_resource ON audit_logs (tenant_id, resource_type, resource_id);

ANALYZE; -- baseline statistics so first plans are honest
