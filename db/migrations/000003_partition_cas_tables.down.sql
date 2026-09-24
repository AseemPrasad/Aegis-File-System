-- Down migration: 000003_partition_cas_tables
DROP TABLE IF EXISTS cas_blocks_partitioned CASCADE;
DROP TABLE IF EXISTS audit_logs_partitioned CASCADE;
