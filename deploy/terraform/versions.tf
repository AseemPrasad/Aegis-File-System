# ============================================================================
# Project Aegis — production/staging infrastructure (AWS target)
# PROMPT 1.2. Cloud-agnostic by design; AWS is the primary target per the
# stack addendum (CloudFront/Lambda@Edge, S3, RDS, ElastiCache).
# Redpanda runs on EKS via its Helm chart/operator — see deploy/helm/.
# ============================================================================

terraform {
  required_version = ">= 1.9.0"

  # Remote state backend values are supplied per-environment at init time:
  #   terraform init -backend-config="bucket=..." -backend-config="key=aegis/${env}.tfstate" \
  #                  -backend-config="region=${var.aws_region}" -backend-config="dynamodb_table=aegis-tflock"
  backend "s3" {
    encrypt = true
  }

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.70"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }
}

provider "aws" {
  region = var.aws_region

  default_tags {
    tags = merge(var.tags, {
      Project     = "aegis"
      Environment = var.environment
      ManagedBy   = "terraform"
    })
  }
}
