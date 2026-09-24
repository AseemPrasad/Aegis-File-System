package auth

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

type SSOProviderType string

const (
	ProviderOIDC SSOProviderType = "OIDC"
	ProviderSAML SSOProviderType = "SAML2"
)

// TenantSSOConfig holds identity provider details for enterprise SSO login.
type TenantSSOConfig struct {
	TenantID     string          `json:"tenant_id"`
	Domain       string          `json:"domain"` // e.g., "acme.com"
	ProviderType SSOProviderType `json:"provider_type"`
	IssuerURL    string          `json:"issuer_url"`
	ClientID     string          `json:"client_id"`
	ClientSecret string          `json:"client_secret,omitempty"`
	SAMLMetadata string          `json:"saml_metadata,omitempty"`
}

// SSORegistry manages tenant identity provider mappings.
type SSORegistry struct {
	configs map[string]TenantSSOConfig // domain -> config
	mu      sync.RWMutex
}

func NewSSORegistry() *SSORegistry {
	return &SSORegistry{
		configs: make(map[string]TenantSSOConfig),
	}
}

// RegisterTenantIdP registers a tenant identity provider configuration.
func (r *SSORegistry) RegisterTenantIdP(cfg TenantSSOConfig) error {
	if cfg.TenantID == "" || cfg.Domain == "" {
		return fmt.Errorf("tenant_id and domain are required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.configs[strings.ToLower(cfg.Domain)] = cfg
	return nil
}

// ResolveIdPByEmail automatically routes a user's corporate email to their Tenant SAML/OIDC configuration.
func (r *SSORegistry) ResolveIdPByEmail(_ context.Context, email string) (*TenantSSOConfig, error) {
	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid email format")
	}

	domain := strings.ToLower(parts[1])

	r.mu.RLock()
	defer r.mu.RUnlock()

	cfg, exists := r.configs[domain]
	if !exists {
		return nil, fmt.Errorf("no enterprise IdP registered for domain %s", domain)
	}

	return &cfg, nil
}

// GenerateAuthRedirectURL constructs the SSO login redirect URL for OIDC or SAML.
func (cfg *TenantSSOConfig) GenerateAuthRedirectURL(state string) string {
	if cfg.ProviderType == ProviderOIDC {
		return fmt.Sprintf("%s/v1/authorize?client_id=%s&response_type=code&scope=openid+profile+email&state=%s",
			cfg.IssuerURL, cfg.ClientID, state)
	}
	return fmt.Sprintf("%s/sso/saml/login?tenant=%s&state=%s",
		cfg.IssuerURL, cfg.TenantID, state)
}
