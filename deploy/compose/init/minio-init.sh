#!/bin/sh
# ============================================================================
# MinIO bucket initialization (integration/local stack only).
# Production buckets + lifecycle policies are Terraform-managed (s3.tf).
# set -e: fail the container on first error so compose --wait reports truth.
# ============================================================================
set -e

mc alias set local "http://minio:9000" "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD"

mc mb --ignore-existing local/aegis-chunks

# Versioning: last-resort recovery surface for GC false deletions
# (SLA row 12 fallback behavior).
mc version enable local/aegis-chunks

# Incomplete-upload abort after 1 day mirrors the production lifecycle rule:
# design doc runbook #1 — client drop-off leaks are reclaimed by lifecycle +
# session reaper, then become eligible for the 7-day generational sweep.
mc ilm rule add local/aegis-chunks \
  --abort-incomplete-multipart-upload-days 1 >/dev/null

echo "minio-init: bucket aegis-chunks ready"
