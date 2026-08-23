variable "environment" {
  description = "Deployment environment key (staging | production)"
  type        = string
}

variable "aws_region" {
  description = "Primary region"
  type        = string
  default     = "us-east-1"
}

variable "vpc_cidr" {
  description = "VPC CIDR block"
  type        = string
  default     = "10.40.0.0/16"
}

variable "az_count" {
  description = "Number of availability zones to span (>=2 required for multi-AZ SLAs)"
  type        = number
  default     = 3
}

variable "db_instance_class" {
  description = "RDS instance class"
  type        = string
  default     = "db.r6g.xlarge"
}

variable "db_multi_az" {
  description = "Synchronous standby in a second AZ (RPO=0 / RTO<30s posture)"
  type        = bool
  default     = true
}

variable "db_backup_retention_days" {
  description = "PITR retention window"
  type        = number
  default     = 14
}

variable "db_deletion_protection" {
  description = "Guard against accidental instance deletion"
  type        = bool
  default     = true
}

variable "redis_shards" {
  description = "ElastiCache cluster-mode shard count"
  type        = number
  default     = 2
}

variable "redis_replicas_per_shard" {
  description = "Replicas per shard (failover targets)"
  type        = number
  default     = 1
}

variable "chunk_bucket_name" {
  description = "Globally unique name of the CAS chunk bucket"
  type        = string
}

variable "kms_deletion_window_days" {
  description = "KMS key pending-deletion window"
  type        = number
  default     = 30
}

variable "alarm_email_endpoints" {
  description = "Emails subscribed to the alarm topic"
  type        = list(string)
  default     = []
}

variable "tags" {
  description = "Additional resource tags"
  type        = map(string)
  default     = {}
}
