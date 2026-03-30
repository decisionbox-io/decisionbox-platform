package postgres

import (
	"context"
	"database/sql"
)

// pgClient abstracts *sql.DB for testing.
// The real implementation is *sql.DB opened via the lib/pq driver.
type pgClient interface {
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	PingContext(ctx context.Context) error
	Close() error
}

// Compile-time check that *sql.DB satisfies the interface.
var _ pgClient = (*sql.DB)(nil)
