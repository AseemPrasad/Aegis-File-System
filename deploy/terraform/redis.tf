# ---------------------------------------------------------------------------
# Redis cache pool: cluster mode ON, encrypted in transit + at rest,
# AUTH token required. Holds only derivable/ephemeral state (Bloom filter,
# nonces, session index) — loss degrades latency, never correctness (B: §4.5).
# ---------------------------------------------------------------------------

resource "random_password" "redis_auth" {
  length  = 40
  special = false # ElastiCache AUTH token charset restrictions
}

resource "aws_elasticache_subnet_group" "aegis" {
  name       = "aegis-${var.environment}"
  subnet_ids = aws_subnet.private[*].id
}

resource "aws_elasticache_replication_group" "cache" {
  replication_group_id       = "aegis-cache-${var.environment}"
  description                = "Aegis fast-index/nonce/bloom pool"
  engine                     = "redis"
  engine_version             = "7.1"
  node_type                  = "cache.r6g.large"

  num_node_groups         = var.redis_shards
  replicas_per_node_group = var.redis_replicas_per_shard
  automatic_failover_enabled = true
  multi_az_enabled           = true

  subnet_group_name          = aws_elasticache_subnet_group.aegis.name
  security_group_ids         = [aws_security_group.metadata.id]

  at_rest_encryption_enabled = true
  transit_encryption_enabled = true # RESP over TLS; clients must use rediss://
  auth_token                 = random_password.redis_auth.result

  snapshot_retention_limit = var.environment == "production" ? 7 : 1
  snapshot_window          = "03:00-05:00"
  maintenance_window       = "sun:05:30-sun:06:30"

  notification_topic_arn = aws_sns_topic.alarms.arn

  log_delivery_configuration {
    destination      = aws_cloudwatch_log_group.redis_slowlog.name
    destination_type = "cloudwatch-logs"
    log_format       = "text"
    log_type         = "slow-log"
  }
}

resource "aws_cloudwatch_log_group" "redis_slowlog" {
  name              = "/aegis/${var.environment}/redis/slowlog"
  retention_in_days = 30
  kms_key_id        = aws_kms_key.db.arn
}
