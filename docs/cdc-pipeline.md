# Debezium Change Data Capture (CDC) Architecture

This document details the zero-latency CDC derivation router pipeline for Project Aegis.

## Architecture

1. **PostgreSQL Write-Ahead Log (WAL):** Transactional inserts into `file_versions` and `file_manifest_blocks` are captured via PostgreSQL logical replication (`wal_level = logical`).
2. **Debezium Connector (`deploy/cdc/debezium-postgresql-connector.json`):** Streams raw PostgreSQL WAL changes into Kafka topic `aegis-db.public.file_versions`.
3. **CDC Router Microservice (`cmd/cdc-router/main.go`):** Consumes raw Debezium WAL events, strips database envelopes, and transforms them into typed `DerivationTaskEvent` messages targeting `aegis.derivation.tasks`.

## Benefits

- **Decoupled API Path:** Synchronous worker event publishing is removed from `/api/v1/ingest/commit`, reducing p99 API latencies to under 20ms.
- **Asynchronous Scalability:** Derivation tasks are emitted asynchronously directly from database WAL commits.
