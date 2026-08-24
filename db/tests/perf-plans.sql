-- ============================================================================
-- Project Aegis: EXPLAIN ANALYZE capture harness (run AFTER perf-seed.sql)
-- ============================================================================

\o /tmp/plans.txt

\echo '=== AEGIS EXECUTION PLAN EVIDENCE ==='
\echo '== dataset =='
SELECT (SELECT count(*) FROM namespace_nodes WHERE tenant_id='00000000-0000-0000-0000-00000000beef') AS perf_nodes,
       (SELECT count(*) FROM cas_blocks)   AS cas_rows,
       (SELECT count(*) FROM file_versions) AS version_rows,
       (SELECT count(*) FROM file_manifest_blocks) AS manifest_rows,
       (SELECT count(*) FROM acl_entries)  AS acl_rows,
       (SELECT count(*) FROM upload_sessions) AS session_rows;

\echo '== Q1: subtree fetch — idx_namespace_lineage (GIST) =='
EXPLAIN (ANALYZE, BUFFERS)
SELECT node_id, name, type
FROM namespace_nodes
WHERE tenant_id = '00000000-0000-0000-0000-00000000beef'
  AND lineage_path <@ (SELECT lineage_path FROM namespace_nodes
                        WHERE tenant_id='00000000-0000-0000-0000-00000000beef'
                          AND parent_id IS NULL);

\echo '== Q2: ancestor walk (ACL resolution chain) — idx_namespace_lineage (GIST) =='
EXPLAIN (ANALYZE, BUFFERS)
SELECT node_id, name
FROM namespace_nodes
WHERE lineage_path @> (SELECT lineage_path FROM namespace_nodes
                        WHERE tenant_id='00000000-0000-0000-0000-00000000beef'
                          AND type='FILE' LIMIT 1);

\echo '== Q3: folder listing — idx_namespace_parent (covering, partial) =='
EXPLAIN (ANALYZE, BUFFERS)
SELECT name, type, node_id, acl_epoch
FROM namespace_nodes
WHERE tenant_id = '00000000-0000-0000-0000-00000000beef'
  AND parent_id = (SELECT node_id FROM namespace_nodes
                    WHERE tenant_id='00000000-0000-0000-0000-00000000beef'
                      AND name LIKE '%_L1%' AND type='DIRECTORY' LIMIT 1)
  AND NOT is_deleted
ORDER BY name;

\echo '== Q4: generational GC sweep — idx_cas_gc_sweep (partial, covering) =='
EXPLAIN (ANALYZE, BUFFERS)
SELECT block_hash, tenant_id, size_bytes
FROM cas_blocks
WHERE ref_count = 0
  AND created_at < NOW() - INTERVAL '7 days'
LIMIT 1000;

\echo '== Q5: chunk assembly read (R2) — PK(version_id,chunk_index) + CAS join =='
EXPLAIN (ANALYZE, BUFFERS)
SELECT m.chunk_index, m.block_hash, m.offset_bytes, m.size_bytes, c.storage_tier
FROM file_manifest_blocks m
JOIN cas_blocks c USING (block_hash)
WHERE m.version_id = (SELECT version_id FROM file_versions LIMIT 1)
ORDER BY m.chunk_index;

\echo '== Q6: dedup gate — idx_versions_content_hash =='
\echo '   NOTE: probe uses a SELECTIVE (mostly-absent) hash; probing a hash'
\echo '   shared by every seeded row would legitimately favor a seq scan.'
EXPLAIN (ANALYZE, BUFFERS)
SELECT version_id, node_id, total_size_bytes
FROM file_versions
WHERE content_sha256 = decode(repeat('ef',32),'hex')
LIMIT 1;

\echo '== Q7: latest version per node — idx_versions_node_recent (covering) =='
EXPLAIN (ANALYZE, BUFFERS)
SELECT version_number, total_size_bytes, mime_type
FROM file_versions
WHERE node_id = (SELECT node_id FROM namespace_nodes
                  WHERE tenant_id='00000000-0000-0000-0000-00000000beef'
                    AND type='FILE' LIMIT 1)
ORDER BY created_at DESC
LIMIT 1;

\echo '== Q8: ACL grant probe — idx_acl_node_principal =='
EXPLAIN (ANALYZE, BUFFERS)
SELECT role FROM acl_entries
WHERE node_id IN (SELECT node_id FROM namespace_nodes
                   WHERE tenant_id='00000000-0000-0000-0000-00000000beef'
                     AND type='DIRECTORY' LIMIT 5)
  AND principal_type = 'USER'
  AND principal_id  <> '00000000-0000-0000-0000-000000000000'
  AND (expires_at IS NULL OR expires_at > NOW());

\echo '== Q9: session reaper — idx_sessions_expiry (partial) =='
EXPLAIN (ANALYZE, BUFFERS)
SELECT session_id FROM upload_sessions
WHERE expires_at < NOW() - INTERVAL '7 days'
  AND NOT is_completed;

\echo '=== END PLAN EVIDENCE ==='

\o
