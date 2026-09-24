variable "aws_region" {
  type        = string
  default     = "us-east-1"
  description = "AWS Region for Aegis Cloud Deployment"
}

variable "vpc_cidr" {
  type        = string
  default     = "10.0.0.0/16"
  description = "VPC CIDR Block"
}

variable "environment" {
  type        = string
  default     = "production"
  description = "Environment name"
}
