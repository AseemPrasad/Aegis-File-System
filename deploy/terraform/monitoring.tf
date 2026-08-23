# ---------------------------------------------------------------------------
# Monitoring: SNS alarm topic + CloudWatch alarms covering the SLA matrix's
# infrastructure rows. Application-level SLAs (latency histograms, GC safety,
# derivation lag) are Prometheus/Grafana-managed and arrive with PROMPTS 4.x/7.
# ---------------------------------------------------------------------------

resource "aws_sns_topic" "alarms" {
  name              = "aegis-${var.environment}-alarms"
  kms_master_key_id = aws_kms_key.db.arn
}

resource "aws_sns_topic_subscription" "email" {
  count     = length(var.alarm_email_endpoints)
  topic_arn = aws_sns_topic.alarms.arn
  protocol  = "email"
  endpoint  = var.alarm_email_endpoints[count.index]
}

resource "aws_cloudwatch_metric_alarm" "rds_cpu" {
  alarm_name          = "aegis-${var.environment}-rds-cpu"
  alarm_description   = "Metadata store sustained CPU pressure"
  namespace           = "AWS/RDS"
  metric_name         = "CPUUtilization"
  statistic           = "Average"
  comparison_operator = "GreaterThanThreshold"
  threshold           = 80
  period              = 300
  evaluation_periods  = 2
  dimensions = { DBInstanceIdentifier = aws_db_instance.metadata.identifier }
  alarm_actions     = [aws_sns_topic.alarms.arn]
  ok_actions        = [aws_sns_topic.alarms.arn]
}

resource "aws_cloudwatch_metric_alarm" "rds_freeable" {
  alarm_name          = "aegis-${var.environment}-rds-freeable-mem"
  alarm_description   = "Freeable memory below 512MiB"
  namespace           = "AWS/RDS"
  metric_name         = "FreeableMemory"
  statistic           = "Average"
  comparison_operator = "LessThanThreshold"
  threshold           = 536870912 # bytes
  period              = 300
  evaluation_periods  = 2
  dimensions = { DBInstanceIdentifier = aws_db_instance.metadata.identifier }
  alarm_actions = [aws_sns_topic.alarms.arn]
}

resource "aws_cloudwatch_metric_alarm" "rds_replica_lag" {
  count               = var.db_multi_az ? 1 : 0
  alarm_name          = "aegis-${var.environment}-rds-replica-lag"
  alarm_description   = "Standby lag threatens RPO=0 failover posture"
  namespace           = "AWS/RDS"
  metric_name         = "ReplicaLag"
  statistic           = "Maximum"
  comparison_operator = "GreaterThanThreshold"
  threshold           = 1 # seconds
  period              = 60
  evaluation_periods  = 5
  dimensions = { DBInstanceIdentifier = aws_db_instance.metadata.identifier }
  alarm_actions = [aws_sns_topic.alarms.arn]
}

resource "aws_cloudwatch_metric_alarm" "redis_evictions" {
  alarm_name          = "aegis-${var.environment}-redis-evictions"
  alarm_description   = "Cache evictions degrade Bloom/nonce fast path"
  namespace           = "AWS/ElastiCache"
  metric_name         = "Evictions"
  statistic           = "Sum"
  comparison_operator = "GreaterThanThreshold"
  threshold           = 0
  period              = 300
  evaluation_periods  = 1
  dimensions = { ReplicationGroupId = aws_elasticache_replication_group.cache.id }
  alarm_actions = [aws_sns_topic.alarms.arn]
}

resource "aws_cloudwatch_metric_alarm" "redis_failover_count" {
  alarm_name          = "aegis-${var.environment}-redis-failovers"
  alarm_description   = "Failover events in the cache pool"
  namespace           = "AWS/ElastiCache"
  metric_name         = "FailOverHostReplacements"
  statistic           = "Sum"
  comparison_operator = "GreaterThanThreshold"
  threshold           = 0
  period              = 60
  evaluation_periods  = 1
  dimensions = { ReplicationGroupId = aws_elasticache_replication_group.cache.id }
  alarm_actions = [aws_sns_topic.alarms.arn]
}
