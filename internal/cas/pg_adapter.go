package cas

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aegis-dev/aegis/internal/database"
)

// NewPgRegistryFromClient creates a PgRegistry backed by a DatabaseClient.
func NewPgRegistryFromClient(db *database.DatabaseClient, lg *slog.Logger) *PgRegistry {
	execFn := func(ctx context.Context, op, tenant, sql string, args ...any) (pgconn.CommandTag, error) {
		return db.ExecWithMetrics(ctx, database.OpWrite, op, tenant, sql, args...)
	}
	queryFn := func(ctx context.Context, op, tenant, sql string, args ...any) (pgx.Rows, error) {
		return db.QueryWithMetrics(ctx, database.OpMetadata, op, tenant, sql, args...)
	}
	txFn := func(ctx context.Context) (DBTx, error) {
		tx, err := db.BeginWriteTx(ctx)
		if err != nil {
			return nil, err
		}
		return &pgxTxAdapter{Tx: tx}, nil
	}
	return NewPgRegistry(txFn, execFn, queryFn, lg)
}

// pgxTxAdapter wraps pgx.Tx to satisfy our DBTx interface.
type pgxTxAdapter struct {
	pgx.Tx
}

func (a *pgxTxAdapter) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	return a.Tx.Exec(ctx, sql, arguments...)
}

func (a *pgxTxAdapter) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return a.Tx.QueryRow(ctx, sql, args...)
}

func (a *pgxTxAdapter) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return a.Tx.Query(ctx, sql, args...)
}
