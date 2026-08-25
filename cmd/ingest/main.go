// Command ingest is the Aegis stateless ingress server.
//
// Environment variables:
//
//	AEGIS_HTTP_PORT        — listen address (default "8080")
//	AEGIS_API_TOKEN        — bearer token for API auth (empty = no auth)
//	AEGIS_ENDPOINT_ID      — edge PoP ID for pre-signed URLs (default "default")
//	AEGIS_DATABASE_DSN     — PostgreSQL primary DSN (required)
//	AEGIS_REDIS_ADDR       — Redis addr for nonces (default "localhost:6379")
//	AEGIS_REDIS_PASSWORD   — Redis password
//	AEGIS_SESSION_REAPER   — session reaper interval (default "60s")
//	AEGIS_LOG_LEVEL        — slog level: debug, info, warn, error (default "info")
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"

	"github.com/aegis-dev/aegis/internal/auth"
	"github.com/aegis-dev/aegis/internal/database"
	"github.com/aegis-dev/aegis/internal/ingress"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLogLevel(envOr("AEGIS_LOG_LEVEL", "info")),
	}))
	slog.SetDefault(logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// ------------------------------------------------------------------
	// Database
	// ------------------------------------------------------------------
	dsn := envOr("AEGIS_DATABASE_DSN", "")
	if dsn == "" {
		logger.Error("AEGIS_DATABASE_DSN is required")
		os.Exit(1)
	}
	dbCfg := database.Config{PrimaryDSN: dsn}
	dbClient, err := database.NewDatabaseClient(ctx, dbCfg, logger)
	if err != nil {
		logger.Error("database init failed", "err", err)
		os.Exit(1)
	}
	defer dbClient.Close()
	logger.Info("database connected", "dsn", maskDSN(dsn))

	// ------------------------------------------------------------------
	// Redis (nonces)
	// ------------------------------------------------------------------
	rdb := redis.NewClient(&redis.Options{
		Addr:     envOr("AEGIS_REDIS_ADDR", "localhost:6379"),
		Password: os.Getenv("AEGIS_REDIS_PASSWORD"),
		DB:       0,
	})
	defer rdb.Close()

	// ------------------------------------------------------------------
	// Auth (HMAC tokens for pre-signed URLs)
	// ------------------------------------------------------------------
	kms := auth.NewStaticKMS(time.Now)
	kms.Provision(uuid.Nil, 1, make([]byte, 32)) // bootstrap key for dev
	nonces := auth.NewRedisNonceStore(rdb)
	tokenGen := auth.NewTokenGenerator(kms, nonces,
		auth.WithBaseURL(envOr("AEGIS_BLOB_BASE_URL", "https://blob-storage")),
		auth.WithTTL(15*time.Minute),
	)
	signerAdapter := &tokenSignerAdapter{gen: tokenGen}

	// ------------------------------------------------------------------
	// Store (pgx adapter)
	// ------------------------------------------------------------------
	store := ingress.NewPgStore(dbClient, logger)

	// ------------------------------------------------------------------
	// Events (noop unless Kafka configured)
	// ------------------------------------------------------------------
	var events ingress.EventBus = &ingress.NoopBus{Logger: logger}

	// ------------------------------------------------------------------
	// Metrics
	// ------------------------------------------------------------------
	reg := prometheus.DefaultRegisterer
	metrics := ingress.NewIngestMetrics(reg)

	// ------------------------------------------------------------------
	// Ingress server
	// ------------------------------------------------------------------
	cfg := ingress.Config{
		APIToken:   envOr("AEGIS_API_TOKEN", ""),
		EndpointID: envOr("AEGIS_ENDPOINT_ID", "default"),
	}
	srv := ingress.NewIngressServer(store, signerAdapter, events, metrics, cfg, logger)

	// Start session reaper.
	reaperInterval, _ := time.ParseDuration(envOr("AEGIS_SESSION_REAPER", "60s"))
	srv.StartSessionReaper(reaperInterval)
	defer srv.StopSessionReaper()

	// ------------------------------------------------------------------
	// HTTP server
	// ------------------------------------------------------------------
	mux := http.NewServeMux()
	srv.SetupRoutes(mux)

	// Prometheus metrics endpoint.
	mux.Handle("GET /metrics", promhttp.Handler())

	httpSrv := &http.Server{
		Addr:              ":" + envOr("AEGIS_HTTP_PORT", "8080"),
		Handler:           srv.LoggingMiddleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Graceful shutdown.
	go func() {
		<-ctx.Done()
		logger.Info("shutting down", "grace", cfg.GracePeriod)
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.GracePeriod)
		defer shutdownCancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown error", "err", err)
		}
	}()

	logger.Info("ingress server listening", "addr", httpSrv.Addr)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server terminated", "err", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// tokenSignerAdapter bridges auth.TokenGenerator (uuid.UUID) → ingress.TokenSigner (string).
// ---------------------------------------------------------------------------

type tokenSignerAdapter struct {
	gen *auth.TokenGenerator
}

func (a *tokenSignerAdapter) GeneratePreSignedURL(ctx context.Context, tenantID, blockHash, endpointID string) (string, error) {
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return "", err
	}
	return a.gen.GeneratePreSignedURL(ctx, tid, blockHash, endpointID)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseLogLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func maskDSN(dsn string) string {
	if len(dsn) > 20 {
		return dsn[:10] + "***" + dsn[len(dsn)-7:]
	}
	return "***"
}
