// ============================================================================
// Project Aegis — CORS Middleware
//
// Configurable Cross-Origin Resource Sharing. In production, restrict to
// the actual frontend domain. In dev, allow localhost origins.
//
// Defaults: no CORS (empty AllowedOrigins) → middleware is a no-op.
// ============================================================================

package ingress

import (
	"net/http"
	"strconv"
	"strings"
)

// CORSConfig controls CORS behavior.
type CORSConfig struct {
	AllowedOrigins []string      // Origins allowed (e.g., ["https://app.aegis.dev"])
	AllowedMethods []string      // HTTP methods (default: GET, POST, PUT, DELETE, OPTIONS)
	AllowedHeaders []string      // Request headers (default: Authorization, Content-Type)
	MaxAge         int           // Preflight cache seconds (default: 86400)
	AllowCredentials bool        // Whether to send Access-Control-Allow-Credentials
}

// DefaultCORSConfig returns a restrictive config suitable for production.
func DefaultCORSConfig() CORSConfig {
	return CORSConfig{
		AllowedMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Authorization", "Content-Type", "X-Tenant-ID", "X-Request-ID"},
		MaxAge:         86400,
	}
}

// CORSMiddleware returns an http.Handler that adds CORS headers.
func CORSMiddleware(cfg CORSConfig, next http.Handler) http.Handler {
	if len(cfg.AllowedOrigins) == 0 {
		return next // no origins configured → no-op
	}

	allowedMethods := strings.Join(cfg.AllowedMethods, ", ")
	allowedHeaders := strings.Join(cfg.AllowedHeaders, ", ")
	maxAge := strconv.Itoa(cfg.MaxAge)

	originSet := make(map[string]bool, len(cfg.AllowedOrigins))
	for _, o := range cfg.AllowedOrigins {
		originSet[o] = true
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" || !originSet[origin] {
			// Not a CORS request or disallowed origin — pass through.
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", allowedMethods)
		w.Header().Set("Access-Control-Allow-Headers", allowedHeaders)
		w.Header().Set("Access-Control-Max-Age", maxAge)
		if cfg.AllowCredentials {
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}

		// Handle preflight OPTIONS request.
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
