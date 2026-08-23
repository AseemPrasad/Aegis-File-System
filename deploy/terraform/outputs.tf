output "metadata_store_endpoint" {
  description = "PostgreSQL endpoint (TLS required; creds in Secrets Manager)"
  value       = aws_db_instance.metadata.address
}

output "db_master_secret_arn" {
  value = aws_secretsmanager_secret.db_master.arn
}

output "cache_endpoint" {
  description = "Redis configuration endpoint (use rediss:// with AUTH token)"
  value       = aws_elasticache_replication_group.cache.configuration_endpoint_address
}

output "chunk_bucket" {
  value = aws_s3_bucket.chunks.id
}

output "chunk_bucket_kms_key" {
  value = aws_kms_key.chunks.arn
}

output "service_roles" {
  description = "IAM role ARNs for ingest/worker/gc workload identities"
  value       = { for k, r in aws_iam_role.service : k => r.arn }
}

output "alarm_topic" {
  value = aws_sns_topic.alarms.arn
}
