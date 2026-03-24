package snowflake

import (
	"context"
	"database/sql"
)

// sfClient abstracts *sql.DB for testing.
// The real implementation is *sql.DB opened via the gosnowflake driver.
type sfClient interface {
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	PingContext(ctx context.Context) error
	Close() error
}

// Compile-time check that *sql.DB satisfies the interface.
var _ sfClient = (*sql.DB)(nil)
