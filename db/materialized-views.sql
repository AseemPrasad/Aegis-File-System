-- ============================================================================
-- Project Aegis: Materialized Views & Reporting Layer (PROMPT 2.1)
--
-- All views are REFRESH MATERIALIZED VIEW [CONCURRENTLY] targets driven by
-- the reporting scheduler (15-min cadence per design doc §7). Unique indexes
-- are mandatory for CONCURRENTLY refreshes.
-- ============================================================================

-- ---------------------------------------------------------------------------
-- MV-1: Tenant Storage Usage (SLA row: capacity dashboards, billing)
-- Drives quota enforcement alerts; authoritative numbers remain live-table.
-- ---------------------------------------------------------------------------
CREATE MATERIALIZED VIEW mv_tenant_usage AS
SELECT
    t.tenant_id,
    t.name,
    t.storage_quota_bytes,
    COALESCE(u.logical_bytes, 0)                       AS logical_bytes,
    COALESCE(u.version_count, 0)                       AS version_count,
    COALESCE(u.node_count, 0)                          AS node_count,
    ROUND(COALESCE(u.logical_bytes, 0)::numeric
          / NULLIF(t.storage_quota_bytes, 0) * 100, 2) AS quota_pct_used,
    NOW()                                              AS refreshed_at
FROM tenants t
LEFT JOIN (
    SELECT n.tenant_id,
           SUM(CASE WHEN NOT n.is_deleted THEN fv.total_size_bytes ELSE 0 END) AS logical_bytes,
           COUNT(DISTINCT CASE WHEN NOT n.is_deleted THEN fv.version_id END)   AS version_count,
           COUNT(DISTINCT CASE WHEN NOT n.is_deleted THEN n.node_id END)       AS node_count
      FROM namespace_nodes n
      JOIN file_versions fv USING (node_id)
     GROUP BY n.tenant_id
) u USING (tenant_id);

CREATE UNIQUE INDEX ux_mv_tenant_usage ON mv_tenant_usage (tenant_id);

-- ---------------------------------------------------------------------------
-- MV-2: Latest Version per Node (restore-point listings)
-- DISTINCT ON keeps newest non-superseded version per node.
-- ---------------------------------------------------------------------------
CREATE MATERIALIZED VIEW mv_node_latest_versions AS
SELECT DISTINCT ON (fv.node_id)
    fv.node_id,
    n.tenant_id,
    n.name,
    fv.version_id,
    fv.version_number,
    fv.total_size_bytes,
    fv.content_sha256,
    fv.created_at
FROM file_versions fv
JOIN namespace_nodes n USING (node_id)
WHERE NOT n.is_deleted
ORDER BY fv.node_id, fv.created_at DESC;

CREATE UNIQUE INDEX ux_mv_latest_versions ON mv_node_latest_versions (node_id);
CREATE INDEX        ix_mv_latest_tenant   ON mv_node_latest_versions (tenant_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- MV-3: Chunk Tier Statistics (storage economics / tiering feedback loop)
-- Physical footprint after dedup — the number billing actually cares about.
-- ---------------------------------------------------------------------------
CREATE MATERIALIZED VIEW mv_chunk_tier_stats AS
SELECT
    tenant_id,
    storage_tier,
    COUNT(*)                                        AS block_count,
    SUM(size_bytes)                                 AS physical_bytes,
    SUM(ref_count * size_bytes::BIGINT)             AS referenced_bytes,
    ROUND(AVG(size_bytes), 0)                       AS avg_block_bytes,
    ROUND(AVG(ref_count), 2)                        AS avg_dedup_ratio,
    COUNT(*) FILTER (WHERE ref_count = 0)           AS gc_candidates,
    NOW()                                           AS refreshed_at
FROM cas_blocks
GROUP BY tenant_id, storage_tier;

CREATE UNIQUE INDEX ux_mv_chunk_tier ON mv_chunk_tier_stats (tenant_id, storage_tier);

-- ---------------------------------------------------------------------------
-- MV-4: Upload Session Funnel (ops: abandonment + retry analytics)
-- ---------------------------------------------------------------------------
CREATE MATERIALIZED VIEW mv_upload_funnel AS
SELECT
    tenant_id,
    COUNT(*)                                            AS sessions_total,
    COUNT(*) FILTER (WHERE is_completed)                AS completed,
    COUNT(*) FILTER (WHERE NOT is_completed AND expires_at > NOW()) AS active,
    COUNT(*) FILTER (WHERE NOT is_completed AND expires_at <= NOW()) AS abandoned,
    ROUND(COUNT(*) FILTER (WHERE is_completed)::numeric
          / NULLIF(COUNT(*), 0) * 100, 1)               AS completion_pct,
    AVG(retry_count)                                    AS avg_retries,
    NOW()                                               AS refreshed_at
FROM upload_sessions
GROUP BY tenant_id;

CREATE UNIQUE INDEX ux_mv_upload_funnel ON mv_upload_funnel (tenant_id);

-- ---------------------------------------------------------------------------
-- QUERY PLANNING HINTS (prevent full matview scans on hot paths)
--
-- Every matview carries a unique index (mandatory for CONCURRENTLY refresh);
-- those indexes double as the read-path access contract:
--   * mv_tenant_usage        -> always filter  WHERE tenant_id = $1
--                               (ux_mv_tenant_usage; index-only scan)
--   * mv_node_latest_versions-> point lookup WHERE node_id = $1, or tenant
--                               dashboards WHERE tenant_id=$1 ORDER BY
--                               created_at DESC LIMIT n
--                               (ux_mv_latest_versions / ix_mv_latest_tenant)
--   * mv_chunk_tier_stats    -> WHERE tenant_id = $1 [AND storage_tier = $2]
--                               (ux_mv_chunk_tier prefix)
--   * mv_upload_funnel       -> WHERE tenant_id = $1 (ux_mv_upload_funnel)
-- Unfiltered reads are permitted ONLY from the aegis_readonly reporting pool,
-- never from the request path — a seq scan on a multi-million-row matview in
-- a latency-critical context is an SLA violation by definition.
--
-- * NEVER join base tables into matview reads for "freshness correction";
--   freshness is owned by REFRESH ... CONCURRENTLY on the scheduler cadence.
-- * refreshed_at projections let callers assert staleness budget without
--   touching catalog stats.
-- * After bulk loads run ANALYZE (see tail of indexes.sql) so plans stay
--   honest; stale statistics degrade nested-loop join choices.
-- ---------------------------------------------------------------------------

-- ---------------------------------------------------------------------------
-- PRIVILEGES: app reads; refresh reserved for migrator role (scheduler).
-- ---------------------------------------------------------------------------
GRANT SELECT ON mv_tenant_usage, mv_node_latest_versions,
                mv_chunk_tier_stats, mv_upload_funnel TO aegis_app;
GRANT SELECT ON mv_tenant_usage, mv_node_latest_versions,
                mv_chunk_tier_stats, mv_upload_funnel TO aegis_readonly;
