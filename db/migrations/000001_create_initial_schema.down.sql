-- Down migration: 000001_create_initial_schema
DROP TABLE IF EXISTS tenants CASCADE;
DROP TYPE IF EXISTS node_type;
