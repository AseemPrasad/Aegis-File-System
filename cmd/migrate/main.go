package main

import (
	"context"
	"flag"
	"log/slog"
	"os"

	"github.com/aegis-dev/aegis/internal/database"
)

func main() {
	var (
		dbURL  = flag.String("db-url", getEnv("DATABASE_URL", "postgres://aegis_migrator:aegis_secret@localhost:5432/aegis?sslmode=disable"), "Database Connection URL")
		migDir = flag.String("mig-dir", getEnv("MIGRATIONS_DIR", "file://db/migrations"), "Migrations directory URL")
		action = flag.String("action", "up", "Migration action: 'up' or 'down'")
	)
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	runner := database.NewMigrationRunner(*dbURL, *migDir, logger)

	ctx := context.Background()

	if *action == "down" {
		if err := runner.Down(ctx); err != nil {
			logger.Error("migration rollback failed", "err", err)
			os.Exit(1)
		}
	} else {
		if err := runner.Up(ctx); err != nil {
			logger.Error("migration up failed", "err", err)
			os.Exit(1)
		}
	}

	logger.Info("database migration task completed successfully")
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
