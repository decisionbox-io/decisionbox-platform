package mssql

import (
	"context"
	"database/sql"
)

// msClient abstracts *sql.DB for testing.
// The real implementation is *sql.DB opened via the microsoft/go-mssqldb driver.
type msClient interface {
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	PingContext(ctx context.Context) error
	Close() error
}

// Compile-time check that *sql.DB satisfies the interface.
var _ msClient = (*sql.DB)(nil)
