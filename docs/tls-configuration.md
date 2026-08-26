# ============================================================================
# Project Aegis — TLS Configuration Guide
#
# TLS termination strategy for production deployment.
# ============================================================================

# OPTION 1: TLS at Load Balancer (RECOMMENDED for K8s)
# =====================================================
# TLS terminates at the cloud load balancer or Ingress controller.
# Internal traffic (LB → pod) is unencrypted within the VPC.
#
# Requirements:
#   - AWS ALB/NLB with ACM certificate
#   - Or nginx-ingress with cert-manager
#   - Or GCP GKE with managed certificate
#
# K8s Ingress example:
#
# apiVersion: networking.k8s.io/v1
# kind: Ingress
# metadata:
#   name: aegis-ingress
#   namespace: aegis
#   annotations:
#     nginx.ingress.kubernetes.io/ssl-redirect: "true"
#     nginx.ingress.kubernetes.io/force-ssl-redirect: "true"
#     nginx.ingress.kubernetes.io/proxy-ssl-protocols: "TLSv1.3"
#     nginx.ingress.kubernetes.io/configuration-snippet: |
#       more_set_headers "Strict-Transport-Security: max-age=63072000; includeSubDomains; preload";
#       more_set_headers "X-Content-Type-Options: nosniff";
#       more_set_headers "X-Frame-Options: DENY";
#       more_set_headers "X-XSS-Protection: 1; mode=block";
#       more_set_headers "Referrer-Policy: strict-origin-when-cross-origin";
# spec:
#   tls:
#     - hosts:
#         - aegis.example.com
#       secretName: aegis-tls
#   rules:
#     - host: aegis.example.com
#       http:
#         paths:
#           - path: /
#             pathType: Prefix
#             backend:
#               service:
#                 name: aegis-ingestion
#                 port:
#                   number: 8080


# OPTION 2: TLS at Application Level
# ====================================
# For environments without a load balancer, enable TLS in the Go binary.
# Requires adding TLS config to cmd/ingest/main.go:
#
#   srv := &http.Server{
#       Addr:    ":" + port,
#       Handler: handler,
#       TLSConfig: &tls.Config{
#           MinVersion: tls.VersionTLS13,
#           CipherSuites: []uint16{
#               tls.TLS_AES_256_GCM_SHA384,
#               tls.TLS_CHACHA20_POLY1305_SHA256,
#               tls.TLS_AES_128_GCM_SHA256,
#           },
#           PreferServerCipherSuites: true,
#       },
#   }
#   srv.ListenAndServeTLS(certFile, keyFile)


# TLS 1.3 Enforced Cipher Suites
# ================================
# TLS 1.3 ciphers are non-configurable (all are secure):
#   - TLS_AES_256_GCM_SHA384
#   - TLS_CHACHA20_POLY1305_SHA256
#   - TLS_AES_128_GCM_SHA256
#
# TLS 1.2 fallback (if needed) — ONLY these are allowed:
#   - TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384
#   - TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384
#   - TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305
#   - TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305
#   - TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256
#   - TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256


# Encryption at Rest
# ==================
# PostgreSQL: Enable via aws_kms_crypto_providers in RDS
# S3: Enable SSE-S3 or SSE-KMS bucket policy
# Redis: ElastiCache at-rest encryption enabled by default
# K8s Secrets: Enable etcd encryption at rest
#
# K8s etcd encryption config:
#   apiVersion: apiserver.config.k8s.io/v1
#   kind: EncryptionConfiguration
#   resources:
#     - resources: ["secrets"]
#       providers:
#         - aescbc:
#             keys:
#               - name: key1
#                 secret: <base64-encoded-32-byte-key>
#         - identity: {}


# HTTP Security Headers (applied at Ingress controller level)
# ============================================================
# Strict-Transport-Security: max-age=63072000; includeSubDomains; preload
# X-Content-Type-Options: nosniff
# X-Frame-Options: DENY
# X-XSS-Protection: 1; mode=block
# Referrer-Policy: strict-origin-when-cross-origin
# Content-Security-Policy: default-src 'none'; frame-ancestors 'none'
# Permissions-Policy: camera=(), microphone=(), geolocation=()
