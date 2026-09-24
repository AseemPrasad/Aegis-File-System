# Apache Kafka Event Streaming Configuration for Project Aegis

This document outlines the topic partition strategies, consumer group offset handling, and DLQ routing for Project Aegis.

## Topic Topology

1. `aegis.file.commits`: Keyed by `tenant_id` to enforce strict FIFO ordering for directory DAG mutations.
2. `aegis.derivation.tasks`: Keyed by `block_hash` to distribute malware scan, OCR, and FFmpeg tasks across worker pods.
3. `aegis.gc.tombstones`: Keyed by `block_hash` for idempotent reference counting cleanup.
4. `aegis.audit.events`: Keyed by `tenant_id` for append-only audit trail logging.
5. `aegis.derivation.dlq`: Dead Letter Queue for unprocessable or corrupt payloads.

## Running Worker Microservices

```bash
# Start standalone worker consumer microservice
go run ./cmd/worker/main.go --kafka-brokers="localhost:9092" --group-id="aegis-derivation-workers"
```
