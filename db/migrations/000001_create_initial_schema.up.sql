-- Up migration: 000001_create_initial_schema
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS ltree;

CREATE TYPE node_type AS ENUM ('FILE', 'DIRECTORY');

CREATE TABLE tenants (
    tenant_id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                VARCHAR(255) NOT NULL UNIQUE,
    kms_key_arn         VARCHAR(255) NOT NULL,
    kms_key_version     INTEGER      NOT NULL DEFAULT 1,
    storage_quota_bytes BIGINT       NOT NULL CHECK (storage_quota_bytes >= 0),
    used_bytes          BIGINT       NOT NULL DEFAULT 0 CHECK (used_bytes >= 0),
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
