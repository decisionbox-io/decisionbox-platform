package mssql

import (
	"context"
	"database/sql"
	"fmt"
)

// mockMSClient implements msClient for unit testing.
type mockMSClient struct {
	queryFunc func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	pingErr   error
	closeErr  error

	lastQuery string
	lastArgs  []interface{}
}

func (m *mockMSClient) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	m.lastQuery = query
	m.lastArgs = args
	if m.queryFunc != nil {
		return m.queryFunc(ctx, query, args...)
	}
	return nil, fmt.Errorf("mock: no queryFunc configured")
}

func (m *mockMSClient) PingContext(ctx context.Context) error {
	return m.pingErr
}

func (m *mockMSClient) Close() error {
	return m.closeErr
}
