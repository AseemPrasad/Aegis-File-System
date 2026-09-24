package database_test

import (
	"testing"

	"github.com/aegis-dev/aegis/internal/database"
)

func TestMigrationRunnerInstantiator(t *testing.T) {
	runner := database.NewMigrationRunner("postgres://user:pass@localhost:5432/db", "file://db/migrations", nil)
	if runner == nil {
		t.Error("expected non-nil MigrationRunner")
	}
}
