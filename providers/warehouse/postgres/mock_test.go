package postgres

import (
	"context"
	"database/sql"
	"fmt"
)

// mockPGClient implements pgClient for unit testing.
type mockPGClient struct {
	queryFunc func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	pingErr   error
	closeErr  error

	lastQuery string
	lastArgs  []interface{}
}

func (m *mockPGClient) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	m.lastQuery = query
	m.lastArgs = args
	if m.queryFunc != nil {
		return m.queryFunc(ctx, query, args...)
	}
	return nil, fmt.Errorf("mock: no queryFunc configured")
}

func (m *mockPGClient) PingContext(ctx context.Context) error {
	return m.pingErr
}

func (m *mockPGClient) Close() error {
	return m.closeErr
}
