# Subsystem Spec 04: CDC Derivation Bus & Worker Fleet

## 1. Overview
Whenever a file version commit transaction completes in PostgreSQL, a Change Data Capture (CDC) event is emitted to Kafka/Redpanda. Asynchronous worker fleets consume these events to perform background tasks without blocking file upload responses.

---

## 2. Debezium CDC WAL Event Router (`cmd/cdc-router`, `internal/cdc`)

The Debezium router intercepts raw WAL log events on table `file_versions`, extracts `tenant_id`, `version_id`, `node_id`, `mime_type`, and `content_sha256`, and publishes structured task messages to topic `aegis-derivation-tasks`.

---

## 3. Worker Fleet Pipeline Catalog (`internal/workers`)

```
[Kafka Topic: aegis-derivation-tasks]
        │
        ├──► ClamAV Worker       ──► Scans blocks for malware (<60s SLA)
        ├──► Tesseract OCR Worker ──► Extracts text from images/PDFs (<120s SLA)
        ├──► FFmpeg Video Worker  ──► Generates 720p/1080p transcodes (<300s SLA)
        └──► Vector Embed Worker  ──► Computes AI embeddings for semantic search
```

### 1. ClamAV Malware Scanner (`workers/clamav.go`)
- Assembles file chunks from CAS storage and streams bytes through the ClamAV daemon (`clamd`).
- If infected, marks `is_quarantined = TRUE` on `file_versions` in database.

### 2. Tesseract OCR Worker (`workers/ocr.go`)
- Extracts plain text snippets from image uploads (`image/png`, `image/jpeg`) and PDF documents.

### 3. FFmpeg Video Transcoder (`workers/ffmpeg.go`)
- Transcodes raw video uploads (`video/mp4`, `video/webm`) into web-optimized HLS/DASH streams and generates video thumbnail previews.

### 4. Vector AI Embedding Worker (`workers/vector.go`)
- Computes dense vector embeddings over extracted document text to support semantic similarity search in vector databases.
