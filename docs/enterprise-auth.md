# Enterprise Authentication, Single Sign-On (SSO) & Multi-Tenant Authorization

This document details the enterprise security and identity infrastructure for Project Aegis.

## Enterprise Single Sign-On (SSO) & IdP Mapping (`internal/auth/sso.go`)

- **Supported Protocols:** OpenID Connect (OIDC) and SAML 2.0.
- **Domain Identity Routing (`ResolveIdPByEmail`):** Corporate email addresses (`user@acme.com`) automatically resolve to the registered tenant SAML/OIDC configuration.

## SCIM 2.0 Identity Lifecycle Management (`internal/auth/scim.go`)

- Implements standard RFC 7644 SCIM endpoints (`/scim/v2/Users`, `/scim/v2/Groups`) for Okta / Azure AD identity syncing and instant user deprovisioning.

## Role-Based Access Control (RBAC) (`internal/auth/rbac.go`)

- **Permissions:** `workspace:read`, `file:write`, `file:delete`, `admin:keys`, `audit:export`.
- **Tenant Isolation (`RBACMiddleware`):** Enforces multi-tenant boundary headers (`X-Tenant-ID`) and RBAC scope permission checks.
