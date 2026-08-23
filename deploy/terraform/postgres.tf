# ---------------------------------------------------------------------------
# Metadata control plane: PostgreSQL 16+, encrypted, multi-AZ, PITR.
# The ltree extension is installed by the migrator role at first migration
# (PROMPT 2.1 owns schema.sql; RDS allows CREATE EXTENSION for rds_superuser
# granted roles). Force-SSL matches security-group intent.
# ---------------------------------------------------------------------------

resource "aws_kms_key" "db" {
  description             = "aegis ${var.environment} storage-at-rest key (RDS/Redis)"
  deletion_window_in_days = var.kms_deletion_window_days
  enable_key_rotation     = true
}

resource "aws_db_parameter_group" "pg16" {
  family = "postgres16"
  name   = "aegis-${var.environment}-pg16"

  parameter {
    name  = "rds.force_ssl"
    value = "1"
  }

  parameter {
    name         = "log_min_duration_statement"
    value        = "50" # IC-3 slow-query contract (>50ms logged)
    apply_method = "immediately"
  }

  parameter {
    name         = "shared_preload_libraries"
    value        = "pg_stat_statements"
    apply_method = "pending-reboot"
  }
}

resource "aws_db_subnet_group" "aegis" {
  name       = "aegis-${var.environment}"
  subnet_ids = aws_subnet.private[*].id
}

resource "random_password" "db_master" {
  length           = 32
  special          = true
  override_special = "!#$%*()-_=+[]{}:?"
}

resource "aws_secretsmanager_secret" "db_master" {
  name                    = "aegis/${var.environment}/db-master"
  kms_key_id              = aws_kms_key.db.arn
  recovery_window_in_days = var.environment == "production" ? 30 : 0
}

resource "aws_secretsmanager_secret_version" "db_master" {
  secret_id = aws_secretsmanager_secret.db_master.id
  secret_string = jsonencode({
    username = "aegis_admin"
    password = random_password.db_master.result
    engine   = "postgres"
    host     = aws_db_instance.metadata.address
    port     = aws_db_instance.metadata.port
    dbname   = "aegis"
  })
}

resource "aws_db_instance" "metadata" {
  identifier                   = "aegis-metadata-${var.environment}"
  engine                       = "postgres"
  engine_version               = "16.4"
  instance_class               = var.db_instance_class
  allocated_storage            = 200
  max_allocated_storage        = 8000 # autogrow toward the 8Ti ceiling before sharding decision
  storage_type                 = "gp3"
  storage_encrypted            = true
  kms_key_id                   = aws_kms_key.db.arn

  db_name  = "aegis"
  username = "aegis_admin"
  password = random_password.db_master.result

  multi_az               = var.db_multi_az          # synchronous standby: RPO=0, promotion <30s
  backup_retention_period = var.db_backup_retention_period # PITR window
  backup_window           = "03:10-03:40"
  maintenance_window      = "sun:04:00-sun:05:00"

  db_subnet_group_name   = aws_db_subnet_group.aegis.name
  vpc_security_group_ids = [aws_security_group.metadata.id]
  parameter_group_name   = aws_db_parameter_group.pg16.name
  port                   = 5432

  deletion_protection       = var.db_deletion_protection
  delete_automated_backups  = false
  copy_tags_to_snapshot     = true
  auto_minor_version_upgrade = true

  performance_insights_enabled          = true
  performance_insights_retention_period = 7

  depends_on = [aws_secretsmanager_secret.db_master]
}
