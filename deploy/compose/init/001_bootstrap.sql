-- ============================================================================
-- Project Aegis — container bootstrap (PROMPT 1.2 scope ONLY).
--
-- Deliberately NOT the canonical schema: tables, indexes, stored procedures,
-- and tests arrive with PROMPT 2.1 via migrations run as aegis_migrator.
-- This bootstrap guarantees that every fresh database has:
--   1. the ltree extension (invariant I-2 prerequisite), and
--   2. least-privilege roles matching the RBAC contract (IC-3).
-- ============================================================================

-- I-2 prerequisite: materialized lineage paths + GiST-indexable operators.
CREATE EXTENSION IF NOT EXISTS ltree;

-- ---------------------------------------------------------------------------
-- Roles. Passwords below are DEV-ONLY placeholders; real deployments inject
-- secrets from Vault/KMS at provision time (see deploy/terraform/iam.tf).
-- ---------------------------------------------------------------------------
DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'aegis_app') THEN
    -- Hot-path service role: read/write on application tables only.
    CREATE ROLE aegis_app LOGIN PASSWORD 'dev_app_only'
      NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION;
  END IF;

  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'aegis_readonly') THEN
    -- Dashboards / analytics pool (IC-3 ANALYTICAL pool): SELECT only.
    CREATE ROLE aegis_readonly LOGIN PASSWORD 'dev_readonly_only'
      NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION;
  END IF;

  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'aegis_migrator') THEN
    -- Schema migration role (PROMPT 2.1 owns DDL); never used at runtime.
    CREATE ROLE aegis_migrator LOGIN PASSWORD 'dev_migrate_only'
      NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION;
  END IF;
END
$$;

GRANT CONNECT ON DATABASE aegis TO aegis_app, aegis_readonly, aegis_migrator;
GRANT CREATE ON SCHEMA public TO aegis_migrator;
ALTER DEFAULT PRIVILEGES FOR ROLE aegis_migrator IN SCHEMA public
  GRANT SELECT ON TABLES TO aegis_readonly;
ALTER DEFAULT PRIVILEGES FOR ROLE aegis_migrator IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO aegis_app;
