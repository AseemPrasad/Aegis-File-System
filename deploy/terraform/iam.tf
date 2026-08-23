# ---------------------------------------------------------------------------
# RBAC: least-privilege roles assumable via cluster OIDC (EKS workload
# identity). Mirrors §4 component scopes:
#   ingest  — PUT/GET chunks (issues + verifies signed channel)
#   worker  — GET only (derivation is read-only on CAS, contract IC-5)
#   gc      — GET + DELETE (tombstone consumer, rate-limited by app logic)
# ---------------------------------------------------------------------------

data "aws_iam_policy_document" "chunks_read" {
  statement {
    sid       = "ReadChunks"
    effect    = "Allow"
    actions   = ["s3:GetObject"]
    resources = ["${aws_s3_bucket.chunks.arn}/*"]
  }
}

data "aws_iam_policy_document" "chunks_write" {
  statement {
    sid       = "WriteChunks"
    effect    = "Allow"
    actions   = ["s3:PutObject", "s3:GetObject", "s3:AbortMultipartUpload"]
    resources = ["${aws_s3_bucket.chunks.arn}/*"]
  }
}

data "aws_iam_policy_document" "chunks_gc" {
  statement {
    sid       = "GcDeleteChunks"
    effect    = "Allow"
    actions   = ["s3:GetObject", "s3:DeleteObject", "s3:DeleteObjectVersion"]
    resources = ["${aws_s3_bucket.chunks.arn}/*"]
  }
}

locals {
  service_roles = {
    ingest = { policy = data.aws_iam_policy_document.chunks_write.json, trust = var.oidc_provider_arn }
    worker = { policy = data.aws_iam_policy_document.chunks_read.json, trust = var.oidc_provider_arn }
    gc     = { policy = data.aws_iam_policy_document.chunks_gc.json, trust = var.oidc_provider_arn }
  }
}

variable "oidc_provider_arn" {
  description = "EKS OIDC provider ARN for workload identity (empty disables IAM role creation)"
  type        = string
  default     = ""
}

resource "aws_iam_role" "service" {
  for_each = var.oidc_provider_arn != "" ? local.service_roles : {}

  name               = "aegis-${var.environment}-${each.key}"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Federated = each.value.trust }
      Action    = "sts:AssumeRoleWithWebIdentity"
    }]
  })
}

resource "aws_iam_role_policy" "service" {
  for_each = var.oidc_provider_arn != "" ? local.service_roles : {}

  name   = "chunks-${each.key}"
  role   = aws_iam_role.service[each.key].id
  policy = each.value.policy
}
