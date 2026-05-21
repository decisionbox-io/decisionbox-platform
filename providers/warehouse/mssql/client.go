package mssql

import (
	"context"
	"database/sql"
)

// msClient abstracts *sql.DB for testing.
// The real implementation is *sql.DB opened via the microsoft/go-mssqldb driver.
type msClient interface {
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	// ExecContext runs a statement that doesn't return rows (used
	// by ValidateSQL's SET NOEXEC ON/OFF compile-only batch).
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
	PingContext(ctx context.Context) error
	Close() error
}

// Compile-time check that *sql.DB satisfies the interface.
var _ msClient = (*sql.DB)(nil)
