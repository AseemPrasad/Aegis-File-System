package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aegis-dev/aegis/internal/auth"
)

func TestSSODomainRouting(t *testing.T) {
	registry := auth.NewSSORegistry()
	err := registry.RegisterTenantIdP(auth.TenantSSOConfig{
		TenantID:     "tenant-acme",
		Domain:       "acme.com",
		ProviderType: auth.ProviderOIDC,
		IssuerURL:    "https://auth.acme.com",
		ClientID:     "client-123",
	})
	if err != nil {
		t.Fatalf("failed to register IdP: %v", err)
	}

	cfg, err := registry.ResolveIdPByEmail(context.Background(), "employee@acme.com")
	if err != nil {
		t.Fatalf("failed to resolve IdP: %v", err)
	}
	if cfg.TenantID != "tenant-acme" {
		t.Errorf("tenantID = %s, want tenant-acme", cfg.TenantID)
	}

	url := cfg.GenerateAuthRedirectURL("state-123")
	if url == "" {
		t.Error("redirect URL should not be empty")
	}
}

func TestRBACMiddlewareEnforcement(t *testing.T) {
	handler := auth.RBACMiddleware(auth.PermissionFileDelete, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// 1. Viewer attempt to delete file -> 403 Forbidden
	req := httptest.NewRequest("DELETE", "/api/v1/files/file-1", nil)
	ctx := auth.InjectUserContext(req.Context(), &auth.UserContext{
		UserID:   "user-1",
		TenantID: "tenant-1",
		Role:     auth.RoleViewer,
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req.WithContext(ctx))

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 Forbidden", rec.Code)
	}

	// 2. Owner attempt to delete file -> 200 OK
	recOwner := httptest.NewRecorder()
	ctxOwner := auth.InjectUserContext(req.Context(), &auth.UserContext{
		UserID:   "user-2",
		TenantID: "tenant-1",
		Role:     auth.RoleOwner,
	})
	handler.ServeHTTP(recOwner, req.WithContext(ctxOwner))

	if recOwner.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 OK", recOwner.Code)
	}
}
