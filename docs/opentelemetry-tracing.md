# OpenTelemetry Distributed Tracing & Deep Observability Architecture

This document outlines the distributed tracing, W3C context propagation, and metric collection topology for Project Aegis.

## OpenTelemetry W3C Context Propagation (`internal/telemetry/tracing.go`)

- **Traceparent Header Extraction/Injection:** Propagates W3C `traceparent` headers across HTTP requests -> PostgreSQL queries -> Redis calls -> Kafka event publishes -> Worker executions.
- **OTLP Exporters:** Supports standard OTLP gRPC export targeting Jaeger, Grafana Tempo, or Datadog APM.

## Trace-Correlated JSON Logging (`internal/telemetry/logging.go`)

- **`TraceLoggingHandler`:** Automatically extracts context `trace_id` and `span_id` attributes, embedding them directly into all `slog` JSON log entries for log-to-trace correlation.

## Deep Observability Prometheus Collectors (`internal/telemetry/metrics.go`)

- `aegis_dedup_bytes_saved_total`: Total bytes saved via global CAS deduplication.
- `aegis_s3_io_latency_seconds`: S3/MinIO payload transfer latency distributions.
- `aegis_kafka_consumer_lag`: Real-time Kafka partition consumer lag.
- `aegis_db_pool_wait_duration_seconds`: PostgreSQL connection pool queue wait times.
