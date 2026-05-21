package mssql

import (
	"context"
	"database/sql"
	"fmt"
)

// mockMSClient implements msClient for unit testing.
type mockMSClient struct {
	queryFunc func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	execFunc  func(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
	pingErr   error
	closeErr  error

	lastQuery     string
	lastArgs      []interface{}
	lastExecQuery string
	lastExecArgs  []interface{}
}

func (m *mockMSClient) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	m.lastQuery = query
	m.lastArgs = args
	if m.queryFunc != nil {
		return m.queryFunc(ctx, query, args...)
	}
	return nil, fmt.Errorf("mock: no queryFunc configured")
}

func (m *mockMSClient) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	m.lastExecQuery = query
	m.lastExecArgs = args
	if m.execFunc != nil {
		return m.execFunc(ctx, query, args...)
	}
	return nil, nil
}

func (m *mockMSClient) PingContext(ctx context.Context) error {
	return m.pingErr
}

func (m *mockMSClient) Close() error {
	return m.closeErr
}
