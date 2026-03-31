package databricks

import (
	"context"
	"database/sql"
)

// dbClient abstracts *sql.DB for testing.
// The real implementation is *sql.DB opened via the databricks-sql-go driver.
type dbClient interface {
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	PingContext(ctx context.Context) error
	Close() error
}

// Compile-time check that *sql.DB satisfies the interface.
var _ dbClient = (*sql.DB)(nil)
