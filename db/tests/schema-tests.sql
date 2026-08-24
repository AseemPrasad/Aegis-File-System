-- ============================================================================
-- Project Aegis: Schema Test Battery (PROMPT 2.1)
--
-- Run:  psql -U aegis_admin -d aegis -f db/tests/schema-tests.sql
-- Gate: zero occurrences of "TESTFAIL" in output.
--
-- Structure: each test runs inside BEGIN...ROLLBACK so state never leaks.
-- Expected-error tests catch exceptions internally and FAIL if the error
-- does NOT occur (or occurs with the wrong SQLSTATE).
-- Concurrency tests use dblink for genuine multi-backend coverage.
-- NOTE: schema enforces ONE live root per tenant (uq_tenant_root); every
-- hierarchy below is therefore built under that single root.
-- ============================================================================

\set ON_ERROR_STOP off

-- Zombie-proofing: any statement or abandoned transaction self-terminates,
-- so a killed CI runner cannot leave lock-holding backends behind.
SET statement_timeout = '60s';
SET idle_in_transaction_session_timeout = '30s';
SET lock_timeout = '10s';

CREATE OR REPLACE FUNCTION t_assert(p_cond BOOLEAN, p_msg TEXT) RETURNS VOID AS $$
BEGIN
    IF NOT p_cond THEN
        RAISE EXCEPTION 'TESTFAIL: %', p_msg;
    END IF;
END;
$$ LANGUAGE plpgsql;

-- Execute dynamic statement; require an exception whose SQLSTATE matches prefix.
CREATE OR REPLACE FUNCTION t_assert_raises(
    p_state TEXT,      -- exact SQLSTATE or prefix ('' = any)
    p_tag   TEXT,      -- expected message tag substring ('' = ignore)
    p_sql   TEXT
) RETURNS VOID AS $$
DECLARE
    v_caught BOOLEAN := FALSE;
    v_state  TEXT;
    v_msg    TEXT;
BEGIN
    BEGIN
        EXECUTE p_sql;
    EXCEPTION WHEN OTHERS THEN
        GET STACKED DIAGNOSTICS v_state = RETURNED_SQLSTATE,
                                v_msg   = MESSAGE_TEXT;
        v_caught := TRUE;
    END;
    IF NOT v_caught THEN
        RAISE EXCEPTION 'TESTFAIL [%]: expected error %/%, got success', p_tag, p_state, p_tag;
    END IF;
    IF p_state <> '' AND position(p_state IN v_state) <> 1 THEN
        RAISE EXCEPTION 'TESTFAIL [%]: wanted state %, got % (%)', p_tag, p_state, v_state, v_msg;
    END IF;
    IF p_tag <> '' AND position('TESTFAIL' IN v_msg) > 0 THEN
        RAISE EXCEPTION '%', v_msg;   -- propagate inner assertion failures verbatim
    END IF;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION t_uuid(p_text TEXT) RETURNS UUID AS
$$ SELECT p_text::uuid $$ LANGUAGE sql IMMUTABLE;

CREATE OR REPLACE FUNCTION t_tenant(p_id UUID, p_name TEXT) RETURNS VOID AS $$
    INSERT INTO tenants (tenant_id, name, kms_key_arn, storage_quota_bytes)
    VALUES (p_id, p_name, 'arn:aws:kms:us-east-1:111122223333:key/test', 1099511627776);
$$ LANGUAGE sql;

\echo '=== AEGIS SCHEMA TEST BATTERY ==='

-- ---------------------------------------------------------------------------
\echo '--- T01: create_root computes self-label lineage ---'
BEGIN;
DO $test$
DECLARE
    v_t uuid := t_uuid('00000000-0000-0000-0000-000000000001');
    v_r uuid;
BEGIN
    PERFORM t_tenant(v_t, 'T01');
    v_r := create_root(v_t, 'home');
    PERFORM t_assert(
        (SELECT lineage_path FROM namespace_nodes WHERE node_id = v_r) = node_label(v_r),
        'T01 root lineage must equal own label');
END $test$;
ROLLBACK;

-- ---------------------------------------------------------------------------
\echo '--- T02: child lineage = parent path ++ child label ---'
BEGIN;
DO $test$
DECLARE
    v_t uuid := t_uuid('00000000-0000-0000-0000-000000000002');
    v_r uuid; v_d uuid;
BEGIN
    PERFORM t_tenant(v_t, 'T02');
    v_r := create_root(v_t, 'root');
    INSERT INTO namespace_nodes (tenant_id, parent_id, name, type)
        VALUES (v_t, v_r, 'docs', 'DIRECTORY') RETURNING node_id INTO v_d;
    PERFORM t_assert(
        (SELECT lineage_path FROM namespace_nodes WHERE node_id = v_d)
            = node_label(v_r) || node_label(v_d),
        'T02 two-level lineage mismatch');
END $test$;
ROLLBACK;

-- ---------------------------------------------------------------------------
\echo '--- T03: direct lineage tamper rejected (I-2e EP-4) ---'
BEGIN;
DO $test$
DECLARE
    v_t uuid := t_uuid('00000000-0000-0000-0000-000000000003');
    v_r uuid;
BEGIN
    PERFORM t_tenant(v_t, 'T03');
    v_r := create_root(v_t, 'root');
    PERFORM t_assert_raises('P0001', 'LINEAGE_TAMPER',
        format('UPDATE namespace_nodes SET lineage_path=''x.y'' WHERE node_id=%L', v_r));
END $test$;
ROLLBACK;

-- ---------------------------------------------------------------------------
\echo '--- T04: direct reparent outside proc rejected (EP-3) ---'
BEGIN;
DO $test$
DECLARE
    v_t uuid := t_uuid('00000000-0000-0000-0000-000000000004');
    v_r uuid; v_kid uuid; v_other uuid;
BEGIN
    PERFORM t_tenant(v_t, 'T04');
    v_r := create_root(v_t, 'root');
    INSERT INTO namespace_nodes (tenant_id, parent_id, name, type)
        VALUES (v_t, v_r, 'kid', 'DIRECTORY') RETURNING node_id INTO v_kid;
    INSERT INTO namespace_nodes (tenant_id, parent_id, name, type)
        VALUES (v_t, v_r, 'other', 'DIRECTORY') RETURNING node_id INTO v_other;
    PERFORM t_assert_raises('P0001', 'DIRECT_REPARENT',
        format('UPDATE namespace_nodes SET parent_id=%L WHERE node_id=%L', v_other, v_kid));
END $test$;
ROLLBACK;

-- ---------------------------------------------------------------------------
\echo '--- T05: cross-tenant parent / non-directory parent rejected ---'
BEGIN;
DO $test$
DECLARE
    v_t1 uuid := t_uuid('00000000-0000-0000-0000-000000000005');
    v_t2 uuid := t_uuid('00000000-0000-0000-0000-000000000006');
    v_r1 uuid; v_f uuid;
BEGIN
    PERFORM t_tenant(v_t1, 'T05a');
    PERFORM t_tenant(v_t2, 'T05b');
    v_r1 := create_root(v_t1, 'r1');
    PERFORM t_assert_raises('P0001', 'CROSS_TENANT_PARENT',
        format('INSERT INTO namespace_nodes (tenant_id,parent_id,name,type)
                VALUES (%L,%L,''bad'',''FILE'')', v_t2, v_r1));
    INSERT INTO namespace_nodes (tenant_id, parent_id, name, type)
        VALUES (v_t1, v_r1, 'f.txt', 'FILE') RETURNING node_id INTO v_f;
    PERFORM t_assert_raises('P0001', 'PARENT_NOT_DIRECTORY',
        format('INSERT INTO namespace_nodes (tenant_id,parent_id,name,type)
                VALUES (%L,%L,''nested'',''FILE'')', v_t1, v_f));
END $test$;
ROLLBACK;

-- ---------------------------------------------------------------------------
\echo '--- T06: duplicate live sibling names rejected by uq_parent_name ---'
BEGIN;
DO $test$
DECLARE
    v_t uuid := t_uuid('00000000-0000-0000-0000-000000000007');
    v_r uuid;
BEGIN
    PERFORM t_tenant(v_t, 'T06');
    v_r := create_root(v_t, 'r');
    INSERT INTO namespace_nodes (tenant_id, parent_id, name, type)
        VALUES (v_t, v_r, 'same', 'DIRECTORY');
    PERFORM t_assert_raises('23505', NULL,
        format('INSERT INTO namespace_nodes (tenant_id,parent_id,name,type)
                VALUES (%L,%L,''same'',''DIRECTORY'')', v_t, v_r));
END $test$;
ROLLBACK;

-- ---------------------------------------------------------------------------
\echo '--- T07: second tenant root rejected (uq_tenant_root) ---'
BEGIN;
DO $test$
DECLARE
    v_t uuid := t_uuid('00000000-0000-0000-0000-000000000008');
    v_r uuid;
BEGIN
    PERFORM t_tenant(v_t, 'T07');
    v_r := create_root(v_t, 'first-root');
    PERFORM t_assert_raises('23505', NULL,
        format('SELECT create_root(%L, ''second-root'')', v_t));
END $test$;
ROLLBACK;

-- ---------------------------------------------------------------------------
\echo '--- T08: move_directory success — subtree remap + acl_epoch bump ---'
BEGIN;
DO $test$
DECLARE
    v_t   uuid := t_uuid('00000000-0000-0000-0000-000000000009');
    v_r   uuid; v_src uuid; v_dst uuid; v_sub uuid; v_deep uuid;
BEGIN
    PERFORM t_tenant(v_t, 'T08');
    v_r := create_root(v_t, 'home');
    INSERT INTO namespace_nodes (tenant_id,parent_id,name,type)
        VALUES (v_t,v_r,'src','DIRECTORY') RETURNING node_id INTO v_src;
    INSERT INTO namespace_nodes (tenant_id,parent_id,name,type)
        VALUES (v_t,v_r,'dst','DIRECTORY') RETURNING node_id INTO v_dst;
    INSERT INTO namespace_nodes (tenant_id,parent_id,name,type)
        VALUES (v_t,v_src,'sub','DIRECTORY') RETURNING node_id INTO v_sub;
    INSERT INTO namespace_nodes (tenant_id,parent_id,name,type)
        VALUES (v_t,v_sub,'deep.txt','FILE');

    UPDATE namespace_nodes SET acl_epoch = 100 WHERE node_id IN (v_src,v_sub,v_deep);

    PERFORM move_directory(v_t, v_src, v_dst);

    -- Full expected paths include the root label prefix.
    PERFORM t_assert(
        (SELECT lineage_path FROM namespace_nodes WHERE node_id=v_src)
            = node_label(v_r) || node_label(v_dst) || node_label(v_src),
        'T08 src path | got: ' ||
        (SELECT lineage_path::text FROM namespace_nodes WHERE node_id=v_src));
    PERFORM t_assert(
        (SELECT lineage_path FROM namespace_nodes WHERE node_id=v_sub)
            = node_label(v_r) || node_label(v_dst) || node_label(v_src) || node_label(v_sub),
        'T08 sub path | got: ' ||
        (SELECT lineage_path::text FROM namespace_nodes WHERE node_id=v_sub));
    PERFORM t_assert(
        (SELECT lineage_path FROM namespace_nodes WHERE node_id=v_deep)
            -- NOTE: ltree || text PARSES the text operand as labels, so no
            -- explicit '.' separator is ever inserted between labels.
            = node_label(v_r) || node_label(v_dst) || node_label(v_src)
              || node_label(v_sub) || node_label(v_deep),
        'T08 deep path | got: ' ||
        (SELECT lineage_path::text FROM namespace_nodes WHERE node_id=v_deep));
    PERFORM t_assert((SELECT parent_id FROM namespace_nodes WHERE node_id=v_src)=v_dst,
                     'T08 src.parent updated');
    PERFORM t_assert((SELECT parent_id FROM namespace_nodes WHERE node_id=v_sub)=v_src,
                     'T08 descendant parent untouched');
    PERFORM t_assert((SELECT acl_epoch FROM namespace_nodes WHERE node_id=v_deep)=101,
                     'T08 descendant acl_epoch incremented');
    PERFORM t_assert((SELECT acl_epoch FROM namespace_nodes WHERE node_id=v_src)=101,
                     'T08 source acl_epoch incremented');
END $test$;
ROLLBACK;

-- ---------------------------------------------------------------------------
\echo '--- T09/T10/T11: cyclic + argument rejections (ACYCL / invalid / root) ---'
BEGIN;
DO $test$
DECLARE
    v_t uuid := t_uuid('00000000-0000-0000-0000-000000000010');
    v_r uuid; v_p uuid; v_c uuid;
BEGIN
    PERFORM t_tenant(v_t, 'T09-11');
    v_r := create_root(v_t, 'home');
    INSERT INTO namespace_nodes (tenant_id,parent_id,name,type)
        VALUES (v_t,v_r,'p','DIRECTORY') RETURNING node_id INTO v_p;
    INSERT INTO namespace_nodes (tenant_id,parent_id,name,type)
        VALUES (v_t,v_p,'c','DIRECTORY') RETURNING node_id INTO v_c;

    -- self-move: invalid arguments (node == target)
    PERFORM t_assert_raises('', 'MOVE_INVALID',
        format('SELECT move_directory(%L,%L,%L)', v_t, v_p, v_p));

    -- descendant target: moving p beneath its own child => ancestor loop
    PERFORM t_assert_raises('', 'CYCLE',
        format('SELECT move_directory(%L,%L,%L)', v_t, v_p, v_c));

    -- root move forbidden even though destination is legal
    INSERT INTO namespace_nodes (tenant_id,parent_id,name,type)
        VALUES (v_t,v_r,'elsewhere','DIRECTORY');
    PERFORM t_assert_raises('P0001', 'ROOT_MOVE_FORBIDDEN',
        format('SELECT move_directory(%L,%L,
                 (SELECT node_id FROM namespace_nodes WHERE name=''elsewhere''))', v_t, v_r));
END $test$;
ROLLBACK;

-- ---------------------------------------------------------------------------
\echo '--- T12: name conflict at destination ---'
BEGIN;
DO $test$
DECLARE
    v_t uuid := t_uuid('00000000-0000-0000-0000-000000000011');
    v_r uuid; v_a uuid; v_b uuid; v_kid uuid;
BEGIN
    PERFORM t_tenant(v_t, 'T12');
    v_r := create_root(v_t, 'home');
    INSERT INTO namespace_nodes (tenant_id,parent_id,name,type)
        VALUES (v_t,v_r,'a','DIRECTORY') RETURNING node_id INTO v_a;
    INSERT INTO namespace_nodes (tenant_id,parent_id,name,type)
        VALUES (v_t,v_r,'b','DIRECTORY') RETURNING node_id INTO v_b;
    INSERT INTO namespace_nodes (tenant_id,parent_id,name,type)
        VALUES (v_t,v_a,'clash','DIRECTORY') RETURNING node_id INTO v_kid;
    INSERT INTO namespace_nodes (tenant_id,parent_id,name,type)
        VALUES (v_t,v_b,'clash','DIRECTORY');
    PERFORM t_assert_raises('P0001', 'NAME_CONFLICT',
        format('SELECT move_directory(%L,%L,%L)', v_t, v_kid, v_b));
END $test$;
ROLLBACK;

-- ---------------------------------------------------------------------------
\echo '--- T13: soft delete tombstone rename frees unique slot ---'
BEGIN;
DO $test$
DECLARE
    v_t uuid := t_uuid('00000000-0000-0000-0000-000000000012');
    v_r uuid; v_kid uuid; v_new uuid;
BEGIN
    PERFORM t_tenant(v_t, 'T13');
    v_r := create_root(v_t, 'home');
    INSERT INTO namespace_nodes (tenant_id,parent_id,name,type)
        VALUES (v_t,v_r,'victim','DIRECTORY') RETURNING node_id INTO v_kid;
    UPDATE namespace_nodes SET acl_epoch=7 WHERE node_id IN (v_r,v_kid);

    PERFORM soft_delete_node(v_t, v_kid);

    PERFORM t_assert((SELECT is_deleted FROM namespace_nodes WHERE node_id=v_kid),
                     'T13 flag must be set');
    PERFORM t_assert((SELECT name FROM namespace_nodes WHERE node_id=v_kid) LIKE 'victim~del:%',
                     'T13 tombstone rename required');
    PERFORM t_assert((SELECT acl_epoch FROM namespace_nodes WHERE node_id=v_kid)=8,
                     'T13 epoch bumped on tombstone');
    -- slot freed: recreate same name succeeds
    INSERT INTO namespace_nodes (tenant_id,parent_id,name,type)
        VALUES (v_t,v_r,'victim','DIRECTORY') RETURNING node_id INTO v_new;
    PERFORM t_assert(v_new <> v_kid, 'T13 recreation yields fresh id');
END $test$;
ROLLBACK;

-- ---------------------------------------------------------------------------
\echo '--- T14: ref-count protocol — manifest rows drive cas_blocks (I-1) ---'
BEGIN;
DO $test$
DECLARE
    v_t  uuid := t_uuid('00000000-0000-0000-0000-000000000013');
    v_r  uuid; v_bh bytea := decode('aa'||repeat('bb',31), 'hex'); v_vid uuid; v_vn int;
BEGIN
    PERFORM t_tenant(v_t, 'T14');
    v_r := create_root(v_t, 'file.bin');
    INSERT INTO cas_blocks (block_hash, tenant_id, size_bytes) VALUES (v_bh, v_t, 1024);
    PERFORM t_assert((SELECT ref_count FROM cas_blocks WHERE block_hash=v_bh)=0,
                     'T14 initial ref_count=0');

    v_vn := next_version_number(v_r);
    INSERT INTO file_versions (node_id, version_number, total_size_bytes, content_sha256, created_by)
        VALUES (v_r, v_vn, 1024, decode('cc'||repeat('dd',31),'hex'), gen_random_uuid())
        RETURNING version_id INTO v_vid;
    INSERT INTO file_manifest_blocks (version_id, chunk_index, block_hash, offset_bytes, size_bytes)
        VALUES (v_vid, 0, v_bh, 0, 1024);
    PERFORM t_assert((SELECT ref_count FROM cas_blocks WHERE block_hash=v_bh)=1,
                     'T14 manifest insert -> ref_count=1');

    DELETE FROM file_versions WHERE version_id=v_vid;      -- cascade clears manifest
    PERFORM t_assert((SELECT ref_count FROM cas_blocks WHERE block_hash=v_bh)=0,
                     'T14 cascade delete -> ref_count=0');
END $test$;
ROLLBACK;

-- ---------------------------------------------------------------------------
\echo '--- T15: referenced CAS row protected by FK RESTRICT ---'
BEGIN;
DO $test$
DECLARE
    v_t  uuid := t_uuid('00000000-0000-0000-0000-000000000014');
    v_r  uuid; v_bh bytea := decode('ee'||repeat('ff',31),'hex'); v_vid uuid; v_vn int;
BEGIN
    PERFORM t_tenant(v_t, 'T15');
    v_r := create_root(v_t, 'f');
    INSERT INTO cas_blocks (block_hash, tenant_id, size_bytes) VALUES (v_bh, v_t, 10);
    v_vn := next_version_number(v_r);
    INSERT INTO file_versions (node_id, version_number, total_size_bytes, content_sha256, created_by)
        VALUES (v_r, v_vn, 10, decode('01'||repeat('02',31),'hex'), gen_random_uuid())
        RETURNING version_id INTO v_vid;
    INSERT INTO file_manifest_blocks (version_id, chunk_index, block_hash, offset_bytes, size_bytes)
        VALUES (v_vid, 0, v_bh, 0, 10);
    -- GC can never reap a block still backing a live manifest:
    PERFORM t_assert_raises('23503', NULL,
        format('DELETE FROM cas_blocks WHERE block_hash=decode(%L,''hex'')',
               encode(v_bh, 'hex')));
END $test$;
ROLLBACK;

-- ---------------------------------------------------------------------------
\echo '--- T16: dedup — two versions share block; deletes decrement exactly once each ---'
BEGIN;
DO $test$
DECLARE
    v_t  uuid := t_uuid('00000000-0000-0000-0000-000000000015');
    v_r  uuid; v_bh bytea := decode('11'||repeat('22',31),'hex');
    v_v1 uuid; v_v2 uuid; v_n int;
BEGIN
    PERFORM t_tenant(v_t, 'T16');
    v_r := create_root(v_t, 'dedup.bin');
    INSERT INTO cas_blocks (block_hash, tenant_id, size_bytes) VALUES (v_bh, v_t, 5);

    v_n := next_version_number(v_r);
    INSERT INTO file_versions (node_id, version_number, total_size_bytes, content_sha256, created_by)
        VALUES (v_r, v_n, 5, decode('a1'||repeat('a1',31),'hex'), gen_random_uuid())
        RETURNING version_id INTO v_v1;
    INSERT INTO file_manifest_blocks (version_id, chunk_index, block_hash, offset_bytes, size_bytes)
        VALUES (v_v1, 0, v_bh, 0, 5);

    v_n := next_version_number(v_r);
    INSERT INTO file_versions (node_id, version_number, total_size_bytes, content_sha256, created_by)
        VALUES (v_r, v_n, 5, decode('b2'||repeat('b2',31),'hex'), gen_random_uuid())
        RETURNING version_id INTO v_v2;
    INSERT INTO file_manifest_blocks (version_id, chunk_index, block_hash, offset_bytes, size_bytes)
        VALUES (v_v2, 0, v_bh, 0, 5);

    PERFORM t_assert((SELECT ref_count FROM cas_blocks WHERE block_hash=v_bh)=2,
                     'T16 dedup reaches ref_count=2');
    DELETE FROM file_versions WHERE version_id=v_v1;
    PERFORM t_assert((SELECT ref_count FROM cas_blocks WHERE block_hash=v_bh)=1,
                     'T16 first delete decrements exactly once');
    DELETE FROM file_versions WHERE version_id=v_v2;
    PERFORM t_assert((SELECT ref_count FROM cas_blocks WHERE block_hash=v_bh)=0,
                     'T16 second delete reaches zero');
END $test$;
ROLLBACK;

-- ---------------------------------------------------------------------------
\echo '--- T17: fenced version allocation is dense across mini-commits ---'
BEGIN;
DO $test$
DECLARE
    v_t uuid := t_uuid('00000000-0000-0000-0000-000000000016');
    v_r uuid; v_n int; i int; v_vid uuid;
BEGIN
    PERFORM t_tenant(v_t, 'T17');
    v_r := create_root(v_t, 'r');
    -- Mimic the real commit path: allocate, insert version, repeat.
    FOR i IN 1..3 LOOP
        v_n := next_version_number(v_r);
        PERFORM t_assert(v_n = i, 'T17 allocation ' || i || ' expected ' || i || ', got ' || v_n);
        INSERT INTO file_versions (node_id, version_number, total_size_bytes,
                                   content_sha256, created_by)
        VALUES (v_r, v_n, 1, decode(lpad(i::text,2,'0') || repeat('7f',31), 'hex'),
                gen_random_uuid())
        RETURNING version_id INTO v_vid;
    END LOOP;
END $test$;
ROLLBACK;

-- ---------------------------------------------------------------------------
\echo '--- T18: GC window + double-check reap safety ---'
BEGIN;
DO $test$
DECLARE
    v_t   uuid := t_uuid('00000000-0000-0000-0000-000000000017');
    v_old bytea := decode('31'||repeat('41',31),'hex');   -- aged, unreferenced
    v_new bytea := decode('51'||repeat('61',31),'hex');   -- too recent
    v_ref bytea := decode('71'||repeat('81',31),'hex');   -- re-referenced mid-GC
    v_r uuid; v_vid uuid; v_vn int; n int;
BEGIN
    PERFORM t_tenant(v_t, 'T18');
    v_r := create_root(v_t, 'r');
    INSERT INTO cas_blocks (block_hash,tenant_id,size_bytes,created_at)
        VALUES (v_old,v_t,1, NOW()-INTERVAL '30 days'),
               (v_new,v_t,1, NOW()),
               (v_ref,v_t,1, NOW()-INTERVAL '30 days');
    v_vn := next_version_number(v_r);
    INSERT INTO file_versions (node_id,version_number,total_size_bytes,content_sha256,created_by)
        VALUES (v_r,v_vn,1,decode('91'||repeat('a9',31),'hex'),gen_random_uuid())
        RETURNING version_id INTO v_vid;
    INSERT INTO file_manifest_blocks (version_id,chunk_index,block_hash,offset_bytes,size_bytes)
        VALUES (v_vid,0,v_ref,0,1);
    DELETE FROM file_versions WHERE version_id=v_vid;      -- v_ref drops to 0, is old

    SELECT count(*) INTO n FROM gc_find_unreferenced(INTERVAL '7 days')
     WHERE block_hash IN (v_old, v_new, v_ref);
    PERFORM t_assert(n=2, 'T18 sweep: only aged unreferenced eligible');

    -- Simulate a commit racing the GC window: AFTER the sweep phase selected
    -- v_ref as a candidate, a new version re-references it. The reap phase
    -- MUST re-check ref_count inside the DELETE and skip this block.
    v_vn := next_version_number(v_r);
    INSERT INTO file_versions (node_id,version_number,total_size_bytes,content_sha256,created_by)
        VALUES (v_r,v_vn,1,decode('a1'||repeat('b9',31),'hex'),gen_random_uuid())
        RETURNING version_id INTO v_vid;
    INSERT INTO file_manifest_blocks (version_id,chunk_index,block_hash,offset_bytes,size_bytes)
        VALUES (v_vid,0,v_ref,0,1);

    RAISE NOTICE 'T18 diag: pre-reap rc=%',
        (SELECT ref_count FROM cas_blocks WHERE block_hash=v_ref);
    n := gc_delete_unreferenced(ARRAY[v_ref]);
    PERFORM t_assert(n=0, 'T18 double-check skips newly-referenced block');
    PERFORM t_assert((SELECT ref_count FROM cas_blocks WHERE block_hash=v_ref)=1,
                     'T18 re-referenced block survives reap intact');
END $test$;
ROLLBACK;

-- ---------------------------------------------------------------------------
\echo '--- T19: session reaper honors grace horizon ---'
BEGIN;
DO $test$
DECLARE
    v_t uuid := t_uuid('00000000-0000-0000-0000-000000000018');
    v_r uuid; s_done uuid; n int;
BEGIN
    PERFORM t_tenant(v_t, 'T19');
    v_r := create_root(v_t, 'r');
    INSERT INTO upload_sessions (tenant_id,node_id,total_size,expected_chunks,expires_at)
        VALUES (v_t,v_r,10,1,NOW()-INTERVAL '8 days');
    INSERT INTO upload_sessions (tenant_id,node_id,total_size,expected_chunks,expires_at)
        VALUES (v_t,v_r,10,1,NOW()-INTERVAL '8 days');
    INSERT INTO upload_sessions (tenant_id,node_id,total_size,expected_chunks,expires_at)
        VALUES (v_t,v_r,10,1,NOW()-INTERVAL '1 day');            -- expired but inside grace
    INSERT INTO upload_sessions (tenant_id,node_id,total_size,expected_chunks,
                                 expires_at,is_completed)
        VALUES (v_t,v_r,10,1,NOW()-INTERVAL '8 days',TRUE)       -- completed: never purged
        RETURNING session_id INTO s_done;

    -- The reaper's return value is intentionally GLOBAL (maintenance job);
    -- assertions below are scoped to THIS tenant so the test stays valid even
    -- when other tenants hold reapable rows (e.g., perf-seed leftovers).
    PERFORM expire_stale_upload_sessions(INTERVAL '7 days');
    PERFORM t_assert(NOT EXISTS (
        SELECT 1 FROM upload_sessions
         WHERE tenant_id = v_t AND NOT is_completed
           AND expires_at < NOW() - INTERVAL '7 days'),
        'T19 expired-uncompleted past grace must be purged');
    PERFORM t_assert((SELECT count(*) FROM upload_sessions WHERE tenant_id=v_t)=2,
                     'T19 survivors: in-grace + completed');
    PERFORM t_assert(EXISTS (SELECT 1 FROM upload_sessions WHERE session_id=s_done),
                     'T19 completed session retained');
END $test$;
ROLLBACK;

-- ===========================================================================
-- CONCURRENCY SUITE (real multi-backend via dblink)
-- ===========================================================================
DO $setup$
BEGIN
    EXECUTE 'CREATE EXTENSION IF NOT EXISTS dblink';
    RAISE NOTICE 'dblink available — running concurrency suite';
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'SKIP: dblink unavailable (%)', SQLERRM;
END $setup$;

-- ---------------------------------------------------------------------------
-- CONCURRENCY PRELUDE: dblink backends can only see COMMITTED data.
-- Fixtures below are committed outside any test transaction; the trailing
-- cleanup statement (after T24) removes them regardless of test outcome.
-- ---------------------------------------------------------------------------
DELETE FROM tenants WHERE name IN ('CT20-fence', 'CT21-move', 'CT23-sibling');
INSERT INTO tenants (tenant_id, name, kms_key_arn, storage_quota_bytes)
VALUES ('00000000-0000-0000-0000-000000000c20', 'CT20-fence',
        'arn:aws:kms:us-east-1:111122223333:key/test', 1099511627776),
       ('00000000-0000-0000-0000-000000000c21', 'CT21-move',
        'arn:aws:kms:us-east-1:111122223333:key/test', 1099511627776),
       ('00000000-0000-0000-0000-000000000c23', 'CT23-sibling',
        'arn:aws:kms:us-east-1:111122223333:key/test', 1099511627776);
SELECT create_root('00000000-0000-0000-0000-000000000c20', 'file.bin');
SELECT create_root('00000000-0000-0000-0000-000000000c21', 'home');
SELECT create_root('00000000-0000-0000-0000-000000000c23', 'home');
INSERT INTO namespace_nodes (tenant_id, parent_id, name, type)
SELECT '00000000-0000-0000-0000-000000000c21', node_id, 'alpha', 'DIRECTORY'
  FROM namespace_nodes
 WHERE tenant_id = '00000000-0000-0000-0000-000000000c21';
INSERT INTO namespace_nodes (tenant_id, parent_id, name, type)
SELECT '00000000-0000-0000-0000-000000000c21', node_id, 'beta', 'DIRECTORY'
  FROM namespace_nodes
 WHERE tenant_id = '00000000-0000-0000-0000-000000000c21'
   AND name = 'home';

-- ---------------------------------------------------------------------------
\echo '--- T20 [concurrent]: fenced version allocation serializes two backends ---'
BEGIN;
DO $test$
DECLARE
    v_r uuid; v_conn text; v_n1 int; v_n2 int; v_wait int := 0;
BEGIN
    SELECT node_id INTO v_r FROM namespace_nodes
     WHERE tenant_id = '00000000-0000-0000-0000-000000000c20';
    v_conn := 'dbname=' || current_database() ||
              ' host=127.0.0.1 user=aegis_app password=dev_app_only';

    PERFORM dblink_connect('c1', v_conn);
    PERFORM dblink_connect('c2', v_conn);
    PERFORM dblink_exec('c1', 'BEGIN');
    PERFORM dblink_exec('c2', 'BEGIN');

    -- Backend A fences the committed node row.
    SELECT n::int INTO v_n1
      FROM dblink('c1', format('SELECT next_version_number(%L) AS n', v_r)) AS t(n bigint);
    PERFORM t_assert(v_n1 = 1, 'T20 backend A gets 1');

    -- Backend B issues the same call; must block on A''s fence, not race.
    PERFORM dblink_send_query('c2', format('SELECT next_version_number(%L) AS n', v_r));
    PERFORM pg_sleep(0.5);
    PERFORM t_assert(dblink_is_busy('c2') = 1,
                     'T20 backend B blocked on fence (no race)');

    -- A finishes the REAL commit path while still holding the fence: insert
    -- version 1, then release. Without this write both backends would compute
    -- MAX+1 = 1 and "dense" would be unfalsifiable.
    PERFORM dblink_exec('c1', format(
        'INSERT INTO file_versions (node_id,version_number,total_size_bytes,
                                    content_sha256,created_by)
         VALUES (%L,%L,1,decode(''7a7a''||repeat(''11'',30),''hex''),gen_random_uuid())',
        v_r, v_n1));
    PERFORM dblink_exec('c1', 'COMMIT');
    v_wait := 0;
    WHILE v_wait < 100 AND dblink_is_busy('c2') = 1 LOOP
        PERFORM pg_sleep(0.05); v_wait := v_wait + 1;
    END LOOP;
    SELECT n::int INTO v_n2 FROM dblink_get_result('c2') AS t(n bigint);
    -- Drain the end-of-results marker; skipping this leaves the connection
    -- with a pending async result and the COMMIT below fails with
    -- "another command is already in progress".
    PERFORM * FROM dblink_get_result('c2') AS t(n bigint);
    PERFORM dblink_exec('c2', 'COMMIT');
    PERFORM t_assert(v_n2 = 2, 'T20 backend B gets dense 2, got ' || v_n2);

    PERFORM dblink_disconnect('c1');
    PERFORM dblink_disconnect('c2');
EXCEPTION WHEN OTHERS THEN
    BEGIN PERFORM dblink_disconnect('c1'); EXCEPTION WHEN OTHERS THEN NULL; END;
    BEGIN PERFORM dblink_disconnect('c2'); EXCEPTION WHEN OTHERS THEN NULL; END;
    RAISE;
END $test$;
ROLLBACK;

-- ---------------------------------------------------------------------------
\echo '--- T21 [concurrent]: opposite moves serialize cleanly; loser gets ACYCL ---'
BEGIN;
DO $test$
DECLARE
    v_t uuid := '00000000-0000-0000-0000-000000000c21';
    v_a uuid; v_b uuid;
    v_conn text; v_wait int := 0; v_err text;
BEGIN
    -- Operates on the COMMITTED CT21 fixture (home/alpha/beta).
    SELECT node_id INTO v_a FROM namespace_nodes WHERE tenant_id=v_t AND name='alpha';
    SELECT node_id INTO v_b FROM namespace_nodes WHERE tenant_id=v_t AND name='beta';
    PERFORM t_assert(v_a IS NOT NULL AND v_b IS NOT NULL, 'T21 fixture missing');
    v_conn := 'dbname=' || current_database() ||
              ' host=127.0.0.1 user=aegis_app password=dev_app_only';

    -- Backend M: move alpha under beta via SYNCHRONOUS dblink inside an
    -- open transaction — statement completes instantly (locks acquired),
    -- and its row locks stay held until we COMMIT below.
    PERFORM dblink_connect('m', v_conn);
    PERFORM dblink_exec('m', 'BEGIN');
    SELECT s INTO v_err
      FROM dblink('m', format(
        'SELECT move_directory_status(%L,%L,%L) AS s', v_t, v_a, v_b)) AS t(s text);
    PERFORM t_assert(v_err = 'MOVED', 'T21 M move completed, got: ' || coalesce(v_err,'null'));

    -- Backend W: opposite move beta under alpha, issued ASYNC.
    -- Must block on M''s row locks WITHOUT deadlocking or erroring early.
    PERFORM dblink_connect('w', v_conn);
    PERFORM dblink_send_query('w', format(
        'SELECT move_directory_status(%L,%L,%L) AS s', v_t, v_b, v_a));
    PERFORM pg_sleep(1.0);
    PERFORM t_assert(dblink_is_busy('w') = 1,
        'T21 W still waiting after 1s (blocked, not aborted)');

    -- M commits => alpha now lives beneath beta. W wakes and MUST see
    -- dst(alpha-path) <@ src(beta-subtree) => clean ACYCL rejection.
    PERFORM dblink_exec('m', 'COMMIT');
    v_wait := 0;
    WHILE v_wait < 100 AND dblink_is_busy('w') = 1 LOOP
        PERFORM pg_sleep(0.05); v_wait := v_wait + 1;
    END LOOP;
    PERFORM t_assert(dblink_is_busy('w') = 0, 'T21 W woke after M commit');

    BEGIN
        SELECT s INTO v_err
          FROM dblink_get_result('w') AS r(s text);
        PERFORM t_assert(FALSE,
            'T21 expected remote error for loser, got success: ' || coalesce(v_err,''));
    EXCEPTION WHEN OTHERS THEN
        GET STACKED DIAGNOSTICS v_err = MESSAGE_TEXT;
        PERFORM t_assert(position('CYCLE' IN v_err) > 0,
                         'T21 expected ACYCL for loser, got: ' || v_err);
    END;

    PERFORM dblink_disconnect('w');
    PERFORM dblink_disconnect('m');
END $test$;
ROLLBACK;

-- ---------------------------------------------------------------------------
\echo '--- T22: materialized views refresh cleanly ---'
BEGIN;
DO $test$
DECLARE
    v_t uuid := t_uuid('00000000-0000-0000-0000-000000000021');
    v_r uuid; c numeric;
BEGIN
    PERFORM t_tenant(v_t, 'T22');
    v_r := create_root(v_t, 'r');
    REFRESH MATERIALIZED VIEW mv_tenant_usage;
    REFRESH MATERIALIZED VIEW mv_chunk_tier_stats;
    REFRESH MATERIALIZED VIEW mv_upload_funnel;
    REFRESH MATERIALIZED VIEW mv_node_latest_versions;
    SELECT quota_pct_used INTO c FROM mv_tenant_usage WHERE tenant_id=v_t;
    PERFORM t_assert(c = 0, 'T22 empty tenant shows 0% used');
END $test$;
ROLLBACK;

-- ---------------------------------------------------------------------------
\echo '--- T23 [concurrent]: duplicate sibling inserts — exactly one winner ---'
-- Spec test: "Concurrent inserts to namespace_nodes (no duplicates)".
-- Two backends race the same (tenant, parent, name) slot. The unique index
-- arbiter blocks the loser on the winner''s uncommitted slot; after commit
-- the loser MUST fail 23505 and NO duplicate row may exist.
BEGIN;
DO $test$
DECLARE
    v_t uuid := '00000000-0000-0000-0000-000000000c23';
    v_r uuid;
    v_conn text; v_wait int := 0; v_err text; n int;
BEGIN
    -- Operates on the COMMITTED CT23 fixture (single live root).
    SELECT node_id INTO v_r FROM namespace_nodes
     WHERE tenant_id = v_t AND parent_id IS NULL;
    PERFORM t_assert(v_r IS NOT NULL, 'T23 fixture root missing');
    v_conn := 'dbname=' || current_database() ||
              ' host=127.0.0.1 user=aegis_app password=dev_app_only';

    -- Backend A: insert sibling 'racer' inside an open tx (uncommitted).
    PERFORM dblink_connect('a', v_conn);
    PERFORM dblink_exec('a', 'BEGIN');
    PERFORM dblink_exec('a', format(
        'INSERT INTO namespace_nodes (tenant_id,parent_id,name,type)
         VALUES (%L,%L,''racer'',''DIRECTORY'')', v_t, v_r));

    -- Backend B: identical insert, async. Must BLOCK on A''s uncommitted
    -- unique-arbiter entry instead of creating a phantom duplicate.
    PERFORM dblink_connect('b', v_conn);
    PERFORM dblink_send_query('b', format(
        'INSERT INTO namespace_nodes (tenant_id,parent_id,name,type)
         VALUES (%L,%L,''racer'',''DIRECTORY'')', v_t, v_r));
    PERFORM pg_sleep(1.0);
    PERFORM t_assert(dblink_is_busy('b') = 1,
        'T23 loser blocked on unique arbiter (no duplicate visible)');

    -- A commits => B wakes with a clean 23505 on uq_parent_name.
    PERFORM dblink_exec('a', 'COMMIT');
    v_wait := 0;
    WHILE v_wait < 100 AND dblink_is_busy('b') = 1 LOOP
        PERFORM pg_sleep(0.05); v_wait := v_wait + 1;
    END LOOP;
    PERFORM t_assert(dblink_is_busy('b') = 0, 'T23 loser woke after winner commit');

    BEGIN
        PERFORM * FROM dblink_get_result('b') AS r(x int);
        PERFORM t_assert(FALSE, 'T23 expected remote 23505 for losing insert, got success');
    EXCEPTION WHEN OTHERS THEN
        GET STACKED DIAGNOSTICS v_err = MESSAGE_TEXT;
        PERFORM t_assert(position('uq_parent_name' IN v_err) > 0,
                         'T23 expected uq_parent_name violation, got: ' || v_err);
    END;

    -- Exactly one 'racer' row exists under the root.
    SELECT count(*) INTO n FROM namespace_nodes
     WHERE tenant_id = v_t AND parent_id = v_r AND name = 'racer';
    PERFORM t_assert(n = 1,
        'T23 exactly one racer row must survive, found ' || n::text);

    PERFORM dblink_disconnect('a');
    PERFORM dblink_disconnect('b');
EXCEPTION WHEN OTHERS THEN
    BEGIN PERFORM dblink_disconnect('a'); EXCEPTION WHEN OTHERS THEN NULL; END;
    BEGIN PERFORM dblink_disconnect('b'); EXCEPTION WHEN OTHERS THEN NULL; END;
    RAISE;
END $test$;
ROLLBACK;

-- ---------------------------------------------------------------------------
\echo '--- T24: cascade chain node->versions->manifest->ref_count + RESTRICT guard ---'
-- Spec test: "Cascade deletes (file deletion -> version deletion -> block
-- cleanup)". Also proves parent RESTRICT prevents silent subtree loss.
BEGIN;
DO $test$
DECLARE
    v_t   uuid := t_uuid('00000000-0000-0000-0000-000000000024');
    v_root uuid; v_dir uuid; v_file uuid;
    v_bh  bytea := decode('c3'||repeat('d4',31),'hex');
    v_vid uuid; v_vn int;
BEGIN
    PERFORM t_tenant(v_t, 'T24');
    v_root := create_root(v_t, 'r');
    INSERT INTO namespace_nodes (tenant_id,parent_id,name,type)
        VALUES (v_t,v_root,'dir','DIRECTORY') RETURNING node_id INTO v_dir;
    INSERT INTO namespace_nodes (tenant_id,parent_id,name,type)
        VALUES (v_t,v_dir,'f.bin','FILE') RETURNING node_id INTO v_file;

    INSERT INTO cas_blocks (block_hash,tenant_id,size_bytes) VALUES (v_bh,v_t,64);
    v_vn := next_version_number(v_file);
    INSERT INTO file_versions (node_id,version_number,total_size_bytes,content_sha256,created_by)
        VALUES (v_file,v_vn,64,decode('e5'||repeat('f6',31),'hex'),gen_random_uuid())
        RETURNING version_id INTO v_vid;
    INSERT INTO file_manifest_blocks (version_id,chunk_index,block_hash,offset_bytes,size_bytes)
        VALUES (v_vid,0,v_bh,0,64);
    PERFORM t_assert((SELECT ref_count FROM cas_blocks WHERE block_hash=v_bh)=1,
                     'T24 precondition: manifest present, ref_count=1');

    -- Directory with a live child is RESTRICT-protected (23503): no silent
    -- subtree loss; clients must soft-delete or empty directories first.
    PERFORM t_assert_raises('23503', NULL,
        format('DELETE FROM namespace_nodes WHERE node_id=%L', v_dir));

    -- Leaf delete cascades the full chain:
    --   namespace_nodes -> file_versions (CASCADE)
    --     -> file_manifest_blocks (CASCADE, fires ref_count decrement)
    DELETE FROM namespace_nodes WHERE node_id = v_file;
    PERFORM t_assert(NOT EXISTS (SELECT 1 FROM file_versions WHERE version_id=v_vid),
                     'T24 version cascade-deleted with node');
    PERFORM t_assert(NOT EXISTS
                     (SELECT 1 FROM file_manifest_blocks WHERE version_id=v_vid),
                     'T24 manifest cascade-deleted with version');
    PERFORM t_assert((SELECT ref_count FROM cas_blocks WHERE block_hash=v_bh)=0,
                     'T24 ref_count decremented to 0 through full chain');
    PERFORM t_assert(EXISTS (SELECT 1 FROM cas_blocks WHERE block_hash=v_bh),
                     'T24 CAS block retained for GC sweep (never auto-deleted)');
END $test$;
ROLLBACK;

-- Concurrency fixture cleanup (committed; runs regardless of test outcome).
DELETE FROM tenants WHERE name IN ('CT20-fence', 'CT21-move', 'CT23-sibling');

\echo '=== BATTERY COMPLETE — scan output above for TESTFAIL ==='
