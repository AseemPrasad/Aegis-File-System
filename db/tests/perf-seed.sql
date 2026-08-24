-- ============================================================================
-- Project Aegis: Performance Seed (EXPLAIN ANALYZE harness)
-- Builds a deterministic, sizeable fixture so planner choices are honest:
--   * ~40k namespace_nodes (depth-4 tree)   -> GIST/B-tree evidence
--   * 10k file_versions w/ 3-chunk manifests over 15k cas_blocks
-- Idempotent: wipes its own tenant first. Run before performance captures.
-- ============================================================================

BEGIN;

-- Idempotent reset in FK-safe order: subtree rows cascade
-- (nodes -> versions -> manifests, decrementing ref_counts), then the
-- RESTRICT-protected block attribution rows may go, then the tenant.
DELETE FROM namespace_nodes
WHERE tenant_id = '00000000-0000-0000-0000-00000000beef';
DELETE FROM upload_sessions  WHERE tenant_id = '00000000-0000-0000-0000-00000000beef';
DELETE FROM acl_entries      WHERE tenant_id = '00000000-0000-0000-0000-00000000beef';
DELETE FROM audit_logs       WHERE tenant_id = '00000000-0000-0000-0000-00000000beef';
DELETE FROM cas_blocks       WHERE tenant_id = '00000000-0000-0000-0000-00000000beef';
DELETE FROM tenants          WHERE name      = 'PERF-SEED';

INSERT INTO tenants (tenant_id, name, kms_key_arn, storage_quota_bytes)
VALUES ('00000000-0000-0000-0000-00000000beef', 'PERF-SEED',
        'arn:aws:kms:us-east-1:111122223333:key/test', 10995116277760);

-- Level 0: root
SELECT create_root('00000000-0000-0000-0000-00000000beef', 'root');

-- Level 1: 20 directories under the root
INSERT INTO namespace_nodes (tenant_id, parent_id, name, type)
SELECT n.tenant_id, n.node_id, 'd' || g || '_L1', 'DIRECTORY'
FROM namespace_nodes n
CROSS JOIN generate_series(1, 20) g
WHERE n.tenant_id  = '00000000-0000-0000-0000-00000000beef'
  AND n.parent_id IS NULL;

-- Level 2: 20 subdirectories under each level-1 directory => 400.
-- Depth selected structurally (nlevel): root=1, L1=2, L2=3, files under L2.
INSERT INTO namespace_nodes (tenant_id, parent_id, name, type)
SELECT p.tenant_id, p.node_id, p.name || '_' || g, 'DIRECTORY'
FROM namespace_nodes p
CROSS JOIN generate_series(1, 20) g
WHERE p.tenant_id = '00000000-0000-0000-0000-00000000beef'
  AND p.type      = 'DIRECTORY'
  AND nlevel(p.lineage_path) = 2;

-- Level 3: 100 files under each level-2 directory => 40,000
INSERT INTO namespace_nodes (tenant_id, parent_id, name, type)
SELECT p.tenant_id, p.node_id, 'f' || g || '.bin', 'FILE'
FROM namespace_nodes p
CROSS JOIN generate_series(1, 100) g
WHERE p.tenant_id = '00000000-0000-0000-0000-00000000beef'
  AND p.type      = 'DIRECTORY'
  AND nlevel(p.lineage_path) = 3;

-- CAS pool: 15,000 unique blocks
INSERT INTO cas_blocks (block_hash, tenant_id, size_bytes)
SELECT decode(lpad(to_hex(g), 8, '0') || repeat('ab', 28), 'hex'),
       '00000000-0000-0000-0000-00000000beef',
       (g % 65536) + 1
FROM generate_series(1, 15000) g;

-- Versions: one per file for the first 10k files (dense numbers via fence)
INSERT INTO file_versions (node_id, version_number, total_size_bytes,
                           content_sha256, created_by)
SELECT n.node_id, next_version_number(n.node_id),
       3072, decode(repeat('cd', 32), 'hex'), gen_random_uuid()
FROM (SELECT node_id FROM namespace_nodes
      WHERE tenant_id = '00000000-0000-0000-0000-00000000beef'
        AND type = 'FILE' LIMIT 10000) n;

-- Manifests: 3 chunks per seeded version, hashes drawn from the CAS pool.
-- Block index derived deterministically from version uuid so lookups vary.
INSERT INTO file_manifest_blocks (version_id, chunk_index, block_hash,
                                  offset_bytes, size_bytes)
SELECT v.version_id, c.k - 1,
       decode(lpad(to_hex(((('x' || translate(v.version_id::text, '-', ''))::bit(32)::int
                             & 2147483647) + c.k * 7919) % 15000 + 1),
                   8, '0') || repeat('ab', 28), 'hex'),
       (c.k - 1) * 1024, 1024
FROM file_versions v
CROSS JOIN generate_series(1, 3) c(k)
WHERE v.content_sha256 = decode(repeat('cd', 32), 'hex');

COMMIT;
ANALYZE;
