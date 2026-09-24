# Database Engineering & Zero-Downtime Migration Pipeline

This document details the versioned database migration runner, PostgreSQL declarative table partitioning, and expand-contract schema evolution patterns for Project Aegis.

## Automated Migration Engine (`internal/database/migrate.go`)

- **Tooling:** Powered by `golang-migrate/migrate`.
- **Versioned Scripts (`db/migrations/`):** Lock-protected execution with version checksum tracking.
  - `000001_create_initial_schema`: Base tenant and schema setup.
  - `000003_partition_cas_tables`: 16-way hash partitioning for CAS blocks and monthly range partitioning for audit logs.

## PostgreSQL Declarative Table Partitioning (`db/migrations/000003_partition_cas_tables.up.sql`)

- **Hash Partitioning (`cas_blocks_partitioned`):** 16 partitions (`MODULUS 16, REMAINDER 0..15`) on `block_hash` preventing B-Tree index bloat on 100M+ CAS blocks.
- **Range Partitioning (`audit_logs_partitioned`):** Monthly partitions on `created_at` enabling instantaneous drop of obsolete audit logs.

## Expand-Contract Schema Evolution Pattern

1. **Expand:** Add new columns/tables as nullable or with defaults without breaking running services.
2. **Migrate:** Deploy updated code that writes to both old and new schema structures.
3. **Contract:** Drop legacy columns/tables in a subsequent release after all services run on the updated code.
