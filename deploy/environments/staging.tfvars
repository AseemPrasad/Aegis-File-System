# Staging Terraform variables — consumed by:
#   make tf-plan TF_ENV=staging
#
# Posture: production parity, reduced scale, chaos enabled. Two regional
# stacks (region-a/region-b) may be provisioned from the same file with
# distinct aws_region/chunk_bucket_name values for multi-region drills.

environment              = "staging"
aws_region               = "us-east-1"

vpc_cidr                 = "10.44.0.0/16"
az_count                 = 3

db_instance_class        = "db.t4g.medium"         # scaled down vs production
db_multi_az              = true                    # keep failover drills honest
db_backup_retention_days = 7
db_deletion_protection   = false                   # stacks rebuilt freely during experiments

redis_shards             = 1
redis_replicas_per_shard = 1

chunk_bucket_name        = "aegis-chunks-staging-CHANGE_ME_ACCOUNT_ID"

kms_deletion_window_days = 7                       # faster experiment turnaround
alarm_email_endpoints    = ["stage-oncall@example.com"]

tags = {
  CostCenter = "aegis-staging"
  Drills     = "enabled" # CHAOS_EXPERIMENTS_ENABLED=true pairs with this
}
