# Production Terraform variables — consumed by:
#   make tf-plan TF_ENV=production
#
# Change control: applies require two-person review on the plan output.
# Every deviation from staging defaults below is deliberate per the SLA
# matrix (ARCHITECTURE_VALIDATION.md §10).

environment              = "production"
aws_region               = "us-east-1"

vpc_cidr                 = "10.40.0.0/16"
az_count                 = 3

db_instance_class        = "db.r6g.xlarge"
db_multi_az              = true                    # RPO=0 / RTO<30s posture
db_backup_retention_days = 14                      # PITR depth
db_deletion_protection   = true

redis_shards             = 2
redis_replicas_per_shard = 1

chunk_bucket_name        = "aegis-chunks-production-CHANGE_ME_ACCOUNT_ID"

kms_deletion_window_days = 30                      # long recovery window for key material
alarm_email_endpoints    = ["prod-oncall@example.com", "sre-escalation@example.com"]

tags = {
  CostCenter = "aegis-production"
  Drills     = "disabled" # chaos lives in staging only
}
