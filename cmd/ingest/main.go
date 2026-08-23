// Command ingest is the Aegis stateless ingress skeleton.
//
// PROMPT 1.2 scope: a compilable, health-check-only binary that proves the
// Go toolchain, module graph, and container image build end-to-end.
// PROMPT 4.1 replaces this file's handlers with HandleInitiate/HandleCommit
// backed by pgxpool, go-redis, franz-go, and echo — the stdlib mux here is
// intentionally dependency-free so `go build ./...` passes before go.sum
// exists.
package main

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// Readiness stays OK in the skeleton; PROMPT 4.1 gates it on pool pings.
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	addr := ":" + envOr("AEGIS_HTTP_PORT", "8080")
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	logger.Info("ingress skeleton listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server terminated", "err", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
