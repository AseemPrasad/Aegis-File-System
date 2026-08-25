package ingress

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

// Config holds the ingress server configuration.
type Config struct {
	APIToken    string        // bearer token for API auth (AEGIS_API_TOKEN)
	EndpointID  string        // edge PoP ID for pre-signed URLs (AEGIS_ENDPOINT_ID)
	MaxBodySize int64         // max request body size in bytes (default 100MB)
	GracePeriod time.Duration // shutdown grace period
}

func (c *Config) applyDefaults() {
	if c.MaxBodySize <= 0 {
		c.MaxBodySize = 100 << 20 // 100 MB
	}
	if c.GracePeriod <= 0 {
		c.GracePeriod = 10 * time.Second
	}
}

// ---------------------------------------------------------------------------
// IngressServer
// ---------------------------------------------------------------------------

// IngressServer is the stateless ingestion engine.
type IngressServer struct {
	store   Store
	tokens  TokenSigner
	events  EventBus
	metrics *IngestMetrics
	cfg     Config
	logger  *slog.Logger

	sessionReaperStop context.CancelFunc
}

// TokenSigner abstracts the auth.TokenGenerator for pre-signed URL minting.
type TokenSigner interface {
	GeneratePreSignedURL(ctx context.Context, tenantID string, blockHash string, endpointID string) (string, error)
}

// NewIngressServer builds the server. All dependencies are required.
func NewIngressServer(
	store Store,
	tokens TokenSigner,
	events EventBus,
	metrics *IngestMetrics,
	cfg Config,
	lg *slog.Logger,
) *IngressServer {
	if lg == nil {
		lg = slog.Default()
	}
	cfg.applyDefaults()
	return &IngressServer{
		store:   store,
		tokens:  tokens,
		events:  events,
		metrics: metrics,
		cfg:     cfg,
		logger:  lg,
	}
}

// ---------------------------------------------------------------------------
// Routing
// ---------------------------------------------------------------------------

// SetupRoutes registers all handlers on the provided mux.
func (s *IngressServer) SetupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.HandleFunc("POST /api/v1/ingest/initiate", s.wrap(s.handleInitiate))
	mux.HandleFunc("POST /api/v1/ingest/commit", s.wrap(s.handleCommit))
}

// StartSessionReaper launches a background goroutine that periodically
// expires stale upload sessions. Returns a stop function.
func (s *IngressServer) StartSessionReaper(interval time.Duration) {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.sessionReaperStop = cancel
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				n, err := s.store.ExpireStaleSessions(ctx)
				if err != nil {
					s.logger.Error("session reaper failed", "err", err)
				} else if n > 0 {
					s.logger.Info("expired stale sessions", "count", n)
				}
			}
		}
	}()
}

// StopSessionReaper signals the reaper goroutine to exit.
func (s *IngressServer) StopSessionReaper() {
	if s.sessionReaperStop != nil {
		s.sessionReaperStop()
	}
}

// ---------------------------------------------------------------------------
// Middleware
// ---------------------------------------------------------------------------

// handlerFunc is the type for our handlers (returns error for status mapping).
type handlerFunc func(http.ResponseWriter, *http.Request) error

// wrap chains: panic recovery → auth → handler → error mapping.
func (s *IngressServer) wrap(fn handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// Panic recovery.
		defer func() {
			if rec := recover(); rec != nil {
				s.logger.Error("panic recovered", "err", rec, "path", r.URL.Path)
				s.writeError(w, http.StatusInternalServerError, "internal server error")
			}
		}()

		// Auth.
		if err := s.authenticate(r); err != nil {
			s.writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		// Max body size.
		r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxBodySize)

		// Handler.
		if err := fn(w, r); err != nil {
			status := HTTPStatus(err)
			s.writeError(w, status, err.Error())
		}
	}
}

func (s *IngressServer) authenticate(r *http.Request) error {
	if s.cfg.APIToken == "" {
		return nil // no token configured → open (dev mode)
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ErrUnauthorized
	}
	token := strings.TrimPrefix(auth, "Bearer ")
	if token != s.cfg.APIToken {
		return ErrUnauthorized
	}
	return nil
}

// ---------------------------------------------------------------------------
// Health handlers
// ---------------------------------------------------------------------------

func (s *IngressServer) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *IngressServer) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := s.store.Ping(ctx); err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "database unreachable")
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (s *IngressServer) writeJSON(w http.ResponseWriter, status int, v any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *IngressServer) writeError(w http.ResponseWriter, status int, msg string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// decodeJSON reads and decodes the request body into v.
func decodeJSON(r *http.Request, v any) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return errors.Join(ErrBadRequest, err)
	}
	if len(body) == 0 {
		return ErrBadRequest
	}
	return json.Unmarshal(body, v)
}

// statusWriter captures the written status code for logging.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

// ---------------------------------------------------------------------------
// Logging middleware (optional, called externally)
// ---------------------------------------------------------------------------

// LoggingMiddleware returns an http.Handler that logs every request.
func (s *IngressServer) LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		s.logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr,
		)
	})
}
