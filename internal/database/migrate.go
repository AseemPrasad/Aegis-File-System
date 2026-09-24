package database

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

// MigrationRunner executes versioned SQL migrations using golang-migrate.
type MigrationRunner struct {
	dbURL  string
	migDir string
	logger *slog.Logger
}

func NewMigrationRunner(dbURL, migDir string, lg *slog.Logger) *MigrationRunner {
	if lg == nil {
		lg = slog.Default()
	}
	if migDir == "" {
		migDir = "file://db/migrations"
	}
	return &MigrationRunner{
		dbURL:  dbURL,
		migDir: migDir,
		logger: lg,
	}
}

// Up runs all unapplied forward SQL migrations.
func (m *MigrationRunner) Up(ctx context.Context) error {
	m.logger.Info("executing forward database migrations...")

	mig, err := migrate.New(m.migDir, m.dbURL)
	if err != nil {
		return fmt.Errorf("failed to create migration instance: %w", err)
	}
	defer mig.Close()

	if err := mig.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migration up failed: %w", err)
	}

	version, dirty, err := mig.Version()
	if err == nil {
		m.logger.Info("database migration up complete", "version", version, "dirty", dirty)
	}
	return nil
}

// Down rolls back the last applied migration.
func (m *MigrationRunner) Down(ctx context.Context) error {
	m.logger.Info("rolling back last database migration...")

	mig, err := migrate.New(m.migDir, m.dbURL)
	if err != nil {
		return fmt.Errorf("failed to create migration instance: %w", err)
	}
	defer mig.Close()

	if err := mig.Steps(-1); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migration down failed: %w", err)
	}

	return nil
}
