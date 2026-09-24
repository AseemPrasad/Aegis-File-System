# Cloud Infrastructure, Terraform IaC & Helm 3 Kubernetes HPA Topology

This document details the Infrastructure-as-Code (IaC) and Kubernetes deployment manifests for Project Aegis.

## Terraform IaC Modules (`infra/terraform/`)

1. **VPC (`infra/terraform/modules/vpc`):** Multi-AZ 3-tier VPC network (3 Public, 3 Private Subnets, Internet & NAT Gateways).
2. **EKS (`infra/terraform/modules/eks`):** AWS EKS v1.30 Kubernetes Cluster with auto-scaling managed node groups (min 3, max 20 nodes).
3. **S3 (`infra/terraform/modules/s3`):** KMS SSE-KMS encrypted private S3 bucket with 90-day Glacier lifecycle rules.

## Production Helm 3 Chart (`deploy/helm/aegis/`)

- **HPA Scaling (`deploy/helm/aegis/templates/hpa.yaml`):** Scales `aegis-ingress` pods (3–50) based on 70% target CPU utilization and `aegis-worker` pods (2–30) based on Kafka consumer lag.
- **PodDisruptionBudget (`deploy/helm/aegis/templates/pdb.yaml`):** Guarantees a minimum of 2 ready API pods during node maintenance.
