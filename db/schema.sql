-- ============================================================================
-- Project Aegis: PostgreSQL Schema
-- Version: 1.0   (PROMPT 2.1)
-- Invariants: Bit-Perfect CAS (I-1), Acyclic Namespace (I-2), Cryptographic
--             Ingress (I-3)
--
-- Apply order: 001_bootstrap.sql (extension+roles, compose init)
--              -> schema.sql (this file)
--              -> indexes.sql -> stored-procedures.sql -> materialized-views.sql
-- Execute as role with CREATE privilege (aegis_migrator in production).
--
-- DESIGN RULES ENFORCED HERE:
--   * Every table: explicit NOT NULL, defaults (gen_random_uuid()/NOW()),
--     FKs with deliberate CASCADE vs RESTRICT semantics, CHECK constraints.
--   * ref_count protocol (I-1): cas_blocks.ref_count ≡ number of live rows in
--     file_manifest_blocks referencing that block. Enforced symmetrically by
--     triggers in stored-procedures.sql — application code NEVER adjusts
--     ref_count directly.
--   * lineage_path is COMPUTED by trigger (never trusted from caller) making
--     broken-lineage states unrepresentable (I-2e).
--   * Partition strategy: audit_logs is RANGE-partitioned monthly at creation
--     (append-only growth). cas_blocks / file_manifest_blocks carry documented
--     hash-partition migration plans (performance-report.md) triggered at the
--     10GB threshold.
-- ============================================================================

-- ---------------------------------------------------------------------------
-- Extensions (idempotent; ltree also pre-created by compose bootstrap)
-- ---------------------------------------------------------------------------
CREATE EXTENSION IF NOT EXISTS pgcrypto;   -- gen_random_uuid()
CREATE EXTENSION IF NOT EXISTS ltree;      -- I-2 materialized lineage paths

-- ---------------------------------------------------------------------------
-- Shared enum + domain types
-- ---------------------------------------------------------------------------
CREATE TYPE node_type AS ENUM ('FILE', 'DIRECTORY');

COMMENT ON TYPE node_type IS 'Namespace node kind; only DIRECTORY may parent children.';

-- ---------------------------------------------------------------------------
-- tenants — workspace & tenant scoping (quota authority for IC-1 402s)
-- ---------------------------------------------------------------------------
CREATE TABLE tenants (
    tenant_id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                VARCHAR(255) NOT NULL UNIQUE,
    kms_key_arn         VARCHAR(255) NOT NULL,
    kms_key_version     INTEGER      NOT NULL DEFAULT 1,
    storage_quota_bytes BIGINT       NOT NULL CHECK (storage_quota_bytes >= 0),
    used_bytes          BIGINT       NOT NULL DEFAULT 0 CHECK (used_bytes >= 0),
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

    CONSTRAINT tenants_kms_key_format CHECK (kms_key_arn LIKE 'arn:%')
);

COMMENT ON TABLE  tenants IS 'Tenant workspaces; per-tenant KMS key version supports I-3 EP-5 rotation.';
COMMENT ON COLUMN tenants.used_bytes IS 'Denormalized usage maintained transactionally alongside quota-affecting commits.';

-- ---------------------------------------------------------------------------
-- namespace_nodes — hierarchical directory graph (I-2 heart)
-- lineage_path label = uuid with dashes replaced by underscores, e.g.
--   root node:      '9f8b...'            child: '9f8b....c2d1...'
-- Computed exclusively by trigger trg_nodes_lineage (see stored-procedures).
-- ---------------------------------------------------------------------------
CREATE TABLE namespace_nodes (
    node_id      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID         NOT NULL REFERENCES tenants(tenant_id) ON DELETE CASCADE,
    parent_id    UUID         REFERENCES namespace_nodes(node_id) ON DELETE RESTRICT,
    name         VARCHAR(255) NOT NULL,
    type         node_type    NOT NULL,

    -- Materialized ancestor path representation: "root_uuid.parent_uuid.node_uuid"
    lineage_path LTREE        NOT NULL,
    acl_epoch    BIGINT       NOT NULL DEFAULT 1 CHECK (acl_epoch >= 1),
    is_deleted   BOOLEAN      NOT NULL DEFAULT FALSE,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

    -- Spec-mandated sibling uniqueness (live AND tombstone slots distinct via is_deleted flag;
    -- soft-delete renames tombstones so the slot frees — see soft_delete_node()).
    CONSTRAINT uq_parent_name UNIQUE (tenant_id, parent_id, name, is_deleted),

    -- A FILE must have a parent; roots are created only via create_root().
    CONSTRAINT nodes_file_has_parent
        CHECK (type = 'DIRECTORY' OR parent_id IS NOT NULL),

    CONSTRAINT nodes_name_not_blank CHECK (length(btrim(name)) > 0)
);

COMMENT ON TABLE  namespace_nodes IS 'Immutable-DAG namespace; cycles structurally rejected (I-2).';
COMMENT ON COLUMN namespace_nodes.lineage_path IS 'Materialized path; computed by trigger, never caller-supplied (I-2e).';
COMMENT ON COLUMN namespace_nodes.acl_epoch  IS 'Bumped on subtree moves to invalidate epoch-stamped permission caches.';

-- Exactly one live root per tenant (parent_id IS NULL).
CREATE UNIQUE INDEX uq_tenant_root ON namespace_nodes (tenant_id) WHERE parent_id IS NULL;

-- ---------------------------------------------------------------------------
-- acl_entries — role-based permissions attached at any node; inheritance
-- resolved by walking ancestor lineage (ltree), never recursive CTEs.
-- Roles are coarse on purpose: fine-grained bitmasks belong above this layer.
-- ---------------------------------------------------------------------------
CREATE TABLE acl_entries (
    acl_id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID        NOT NULL REFERENCES tenants(tenant_id) ON DELETE CASCADE,
    node_id        UUID        NOT NULL REFERENCES namespace_nodes(node_id) ON DELETE CASCADE,
    principal_type VARCHAR(16) NOT NULL CHECK (principal_type IN ('USER', 'GROUP', 'SERVICE')),
    principal_id   UUID        NOT NULL,
    role           VARCHAR(32) NOT NULL CHECK (role IN ('OWNER', 'EDITOR', 'VIEWER')),
    granted_by     UUID        NOT NULL,
    expires_at     TIMESTAMPTZ,                       -- NULL = non-expiring
    granted_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_acl_grant UNIQUE (tenant_id, node_id, principal_type, principal_id, role),
    CONSTRAINT acl_expiry_sane CHECK (expires_at IS NULL OR expires_at > granted_at)
);

COMMENT ON TABLE acl_entries IS 'Role-based ACLs; resolution walks node lineage_path ancestors.';

-- ---------------------------------------------------------------------------
-- file_versions — immutable file revisions (+ version metadata columns)
-- ---------------------------------------------------------------------------
CREATE TABLE file_versions (
    version_id       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id          UUID         NOT NULL REFERENCES namespace_nodes(node_id) ON DELETE CASCADE,
    version_number   INTEGER      NOT NULL CHECK (version_number >= 1),
    total_size_bytes BIGINT       NOT NULL CHECK (total_size_bytes >= 0),
    mime_type        VARCHAR(128) NOT NULL DEFAULT 'application/octet-stream',
    content_sha256   BYTEA        NOT NULL CHECK (octet_length(content_sha256) = 32), -- I-1 address identity

    created_by       UUID         NOT NULL,           -- actor identity (audit join key)
    last_accessed_at TIMESTAMPTZ,                     -- read-path metadata (lazy update)
    is_quarantined   BOOLEAN      NOT NULL DEFAULT FALSE, -- set by ClamAV worker verdict
    metadata         JSONB        NOT NULL DEFAULT '{}'::jsonb,

    created_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_node_version UNIQUE (node_id, version_number)
);

COMMENT ON TABLE  file_versions IS 'Append-only revisions; content_sha256 enables whole-file dedup + read-back verification (I-1 EP-6).';
COMMENT ON COLUMN file_versions.metadata IS 'Owner display info & custom properties; queryable via JSONB operators.';

-- ---------------------------------------------------------------------------
-- cas_blocks — Content-Addressable Storage registry (I-1 registry plane)
-- NOTE (stack addendum adjustment): block_hash alone is PK → cross-tenant
-- dedup maximized. tenant_id remains as attribution for sweeps/tiering but is
-- NOT part of identity.
-- ---------------------------------------------------------------------------
CREATE TABLE cas_blocks (
    block_hash    BYTEA PRIMARY KEY CHECK (octet_length(block_hash) = 32),
    tenant_id     UUID        NOT NULL REFERENCES tenants(tenant_id),
    size_bytes    INTEGER     NOT NULL CHECK (size_bytes > 0),
    storage_tier  VARCHAR(32) NOT NULL DEFAULT 'HOT' CHECK (storage_tier IN ('HOT', 'WARM', 'COLD')),
    ref_count     BIGINT      NOT NULL DEFAULT 0 CHECK (ref_count >= 0), -- trigger-maintained; see protocol note
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE  cas_blocks IS 'One row per unique chunk; ref_count maintained exclusively by manifest triggers.';
COMMENT ON COLUMN cas_blocks.ref_count IS '≡ COUNT(file_manifest_blocks WHERE block_hash=…); GC deletes only after 7-day zero window.';

-- ---------------------------------------------------------------------------
-- file_manifest_blocks — ordered Merkle manifest (chunk assembly truth)
-- ---------------------------------------------------------------------------
CREATE TABLE file_manifest_blocks (
    version_id   UUID    NOT NULL REFERENCES file_versions(version_id) ON DELETE CASCADE,
    chunk_index  INTEGER NOT NULL CHECK (chunk_index >= 0),
    block_hash   BYTEA   NOT NULL REFERENCES cas_blocks(block_hash) ON DELETE RESTRICT,
    offset_bytes BIGINT  NOT NULL CHECK (offset_bytes >= 0),
    size_bytes   INTEGER NOT NULL CHECK (size_bytes > 0),

    PRIMARY KEY (version_id, chunk_index)
);

COMMENT ON TABLE file_manifest_blocks IS 'Ordered chunks per version; INSERT/DELETE here drive cas ref_count via triggers.';

-- ---------------------------------------------------------------------------
-- upload_sessions — ephemeral ingress sessions (+ session metadata folded in:
-- same write path, same TTL lifecycle; a separate table would force joins on
-- every initiate for zero modeling gain)
-- ---------------------------------------------------------------------------
CREATE TABLE upload_sessions (
    session_id      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID        NOT NULL REFERENCES tenants(tenant_id) ON DELETE CASCADE,
    node_id         UUID        REFERENCES namespace_nodes(node_id) ON DELETE SET NULL,
    total_size      BIGINT      NOT NULL CHECK (total_size >= 0),
    expected_chunks INTEGER     NOT NULL CHECK (expected_chunks > 0),
    chunks_received INTEGER     NOT NULL DEFAULT 0,
    expires_at      TIMESTAMPTZ NOT NULL,                    -- now()+24h per design doc
    is_completed    BOOLEAN     NOT NULL DEFAULT FALSE,

    client_ip       INET        NOT NULL DEFAULT '0.0.0.0',
    user_agent      TEXT        NOT NULL DEFAULT '',
    retry_count     SMALLINT    NOT NULL DEFAULT 0 CHECK (retry_count >= 0),

    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT sessions_completed_have_node CHECK (NOT is_completed OR node_id IS NOT NULL)
);

COMMENT ON TABLE upload_sessions IS '24h ephemeral upload leases; expired rows reaped hourly, orphan blocks swept after 7-day window.';

-- ---------------------------------------------------------------------------
-- audit_logs — who accessed what, when. PARTITIONED BY RANGE(occurred_at):
-- first table guaranteed to blow past 10GB; monthly partitions keep indexes
-- small and make retention = DROP PARTITION (no bloat, no vacuum pressure).
-- PK must include partition key on partitioned tables.
-- ---------------------------------------------------------------------------
CREATE TABLE audit_logs (
    audit_id      UUID        NOT NULL DEFAULT gen_random_uuid(),
    tenant_id     UUID        NOT NULL,
    actor_id      UUID        NOT NULL,
    action        VARCHAR(64) NOT NULL,
    resource_type VARCHAR(32) NOT NULL CHECK (resource_type IN ('TENANT','NODE','VERSION','BLOCK','ACL','SESSION')),
    resource_id   UUID,
    detail        JSONB       NOT NULL DEFAULT '{}'::jsonb,
    occurred_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (audit_id, occurred_at),
    CONSTRAINT audit_action_not_blank CHECK (length(btrim(action)) > 0)
) PARTITION BY RANGE (occurred_at);

COMMENT ON TABLE audit_logs IS 'Append-only audit trail; range-partitioned monthly. Retention = DROP PARTITION.';

-- Initial partitions (maintenance procedure creates future ones).
CREATE TABLE audit_logs_2026_08 PARTITION OF audit_logs
    FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
CREATE TABLE audit_logs_2026_09 PARTITION OF audit_logs
    FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');
CREATE TABLE audit_logs_default PARTITION OF audit_logs DEFAULT;

COMMENT ON TABLE audit_logs_default IS 'Catch-all partition; alerts fire if rows land here (means maintenance lagged).';

-- ============================================================================
-- GRANTS — least privilege per IC-3 role split.
-- aegis_app      : DML on app tables + procedure EXECUTE
-- aegis_readonly : SELECT only
-- (DDL stays with migrator/admin roles.)
-- ============================================================================
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES          IN SCHEMA public TO aegis_app;
GRANT SELECT                          ON ALL TABLES         IN SCHEMA public TO aegis_readonly;
GRANT USAGE, SELECT                   ON ALL SEQUENCES      IN SCHEMA public TO aegis_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO aegis_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO aegis_readonly;
