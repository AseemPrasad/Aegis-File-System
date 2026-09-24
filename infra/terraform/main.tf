provider "aws" {
  region = var.aws_region
}

module "vpc" {
  source      = "./modules/vpc"
  aws_region  = var.aws_region
  environment = var.environment
}

module "eks" {
  source     = "./modules/eks"
  subnet_ids = module.vpc.private_subnet_ids
}

module "s3" {
  source = "./modules/s3"
}
