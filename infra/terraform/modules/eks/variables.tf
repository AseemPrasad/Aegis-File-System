variable "cluster_name" {
  type        = string
  default     = "aegis-eks-cluster"
  description = "EKS Cluster Name"
}

variable "subnet_ids" {
  type        = list(string)
  description = "Private Subnet IDs for EKS Nodes"
}
