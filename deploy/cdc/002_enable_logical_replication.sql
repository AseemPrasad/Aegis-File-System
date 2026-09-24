-- ============================================================================
-- Project Aegis: Optional Logical Replication Setup for Debezium CDC
-- ============================================================================

-- Set WAL level to logical for Debezium CDC capturing
ALTER SYSTEM SET wal_level = 'logical';

-- Create replication slot (handled automatically by Debezium when pgoutput plugin is active)
-- SELECT pg_create_logical_replication_slot('aegis_debezium_slot', 'pgoutput');
