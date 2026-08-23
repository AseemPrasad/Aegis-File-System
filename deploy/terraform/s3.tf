# ---------------------------------------------------------------------------
# CAS data plane bucket: content-addressed chunks.
#   * key = SHA-256(block bytes)            (invariant I-1)
#   * SSE-KMS default + explicit deny of unencrypted/non-TLS puts
#   * versioning enabled (GC false-deletion recovery surface, SLA row 12)
#   * lifecycle mirrors the design doc: abort incomplete multipart @24h,
#     WARM (STANDARD_IA) @7d, COLD (GLACIER_IR) @30d
# ---------------------------------------------------------------------------

resource "aws_kms_key" "chunks" {
  description             = "aegis ${var.environment} CAS chunk encryption key"
  deletion_window_in_days = var.kms_deletion_window_days
  enable_key_rotation     = true
}

resource "aws_kms_alias" "chunks" {
  name          = "alias/aegis-${var.environment}-chunks"
  target_key_id = aws_kms_key.chunks.key_id
}

resource "aws_s3_bucket" "chunks" {
  bucket        = var.chunk_bucket_name
  force_destroy = var.environment != "production"
}

resource "aws_s3_bucket_versioning" "chunks" {
  bucket = aws_s3_bucket.chunks.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "chunks" {
  bucket = aws_s3_bucket.chunks.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = aws_kms_key.chunks.arn
    }
    bucket_key_enabled = true # cuts KMS API costs on chunk-scale request volume
  }
}

resource "aws_s3_bucket_public_access_block" "chunks" {
  bucket                  = aws_s3_bucket.chunks.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_lifecycle_configuration" "chunks" {
  bucket = aws_s3_bucket.chunks.id

  rule {
    id     = "tier-hot-warm-cold"
    status = "Enabled"

    transition {
      days          = 7
      storage_class = "STANDARD_IA"
    }

    transition {
      days          = 30
      storage_class = "GLACIER_IR" # instant retrieval keeps read SLAs intact
    }

    noncurrent_version_expiration {
      noncurrent_days = 90
    }

    abort_incomplete_multipart_upload {
      days_after_initiation = 1 # design doc runbook #1 (24h leak reclamation)
    }
  }
}

data "aws_iam_policy_document" "chunks_tls_only" {
  statement {
    sid       = "DenyInsecureTransport"
    effect    = "Deny"
    actions   = ["s3:*"]
    resources = [aws_s3_bucket.chunks.arn, "${aws_s3_bucket.chunks.arn}/*"]
    principals {
      type        = "*"
      identifiers = ["*"]
    }
    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }
}

resource "aws_s3_bucket_policy" "chunks" {
  bucket = aws_s3_bucket.chunks.id
  policy = data.aws_iam_policy_document.chunks_tls_only.json
}
