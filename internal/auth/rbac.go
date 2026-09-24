package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

type Permission string

const (
	PermissionWorkspaceRead Permission = "workspace:read"
	PermissionFileWrite     Permission = "file:write"
	PermissionFileDelete    Permission = "file:delete"
	PermissionAdminKeys     Permission = "admin:keys"
	PermissionAuditExport   Permission = "audit:export"
)

type Role string

const (
	RoleOwner  Role = "OWNER"
	RoleEditor Role = "EDITOR"
	RoleViewer Role = "VIEWER"
)

// RolePermissionMap defines granular RBAC scope mappings per role.
var RolePermissionMap = map[Role][]Permission{
	RoleOwner: {
		PermissionWorkspaceRead,
		PermissionFileWrite,
		PermissionFileDelete,
		PermissionAdminKeys,
		PermissionAuditExport,
	},
	RoleEditor: {
		PermissionWorkspaceRead,
		PermissionFileWrite,
	},
	RoleViewer: {
		PermissionWorkspaceRead,
	},
}

// UserContext carries authenticated user metadata across HTTP handlers.
type UserContext struct {
	UserID   string
	TenantID string
	Role     Role
}

type contextKey string

const UserContextKey contextKey = "aegis_user_context"

// HasPermission checks if the role grants the required permission scope.
func (r Role) HasPermission(required Permission) bool {
	perms, exists := RolePermissionMap[r]
	if !exists {
		return false
	}
	for _, p := range perms {
		if p == required {
			return true
		}
	}
	return false
}

// RBACMiddleware enforces multi-tenant boundary checks and permission evaluation.
func RBACMiddleware(required Permission, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userCtx, ok := r.Context().Value(UserContextKey).(*UserContext)
		if !ok || userCtx == nil {
			http.Error(w, `{"error":"unauthorized_no_context"}`, http.StatusUnauthorized)
			return
		}

		// Enforce tenant boundary check
		tenantHeader := r.Header.Get("X-Tenant-ID")
		if tenantHeader != "" && !strings.EqualFold(tenantHeader, userCtx.TenantID) {
			http.Error(w, `{"error":"forbidden_cross_tenant_access"}`, http.StatusForbidden)
			return
		}

		// Evaluate RBAC permission scope
		if !userCtx.Role.HasPermission(required) {
			http.Error(w, fmt.Sprintf(`{"error":"forbidden_insufficient_permissions","required":"%s"}`, required), http.StatusForbidden)
			return
		}

		next(w, r)
	}
}

// InjectUserContext injects UserContext into request context (helper for testing/auth chain).
func InjectUserContext(ctx context.Context, u *UserContext) context.Context {
	return context.WithValue(ctx, UserContextKey, u)
}
