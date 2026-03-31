package databricks

import (
	"context"
	"database/sql"
	"fmt"
)

// mockDBClient implements dbClient for unit testing.
type mockDBClient struct {
	queryFunc func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	pingErr   error
	closeErr  error

	lastQuery string
	lastArgs  []interface{}
}

func (m *mockDBClient) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	m.lastQuery = query
	m.lastArgs = args
	if m.queryFunc != nil {
		return m.queryFunc(ctx, query, args...)
	}
	return nil, fmt.Errorf("mock: no queryFunc configured")
}

func (m *mockDBClient) PingContext(ctx context.Context) error {
	return m.pingErr
}

func (m *mockDBClient) Close() error {
	return m.closeErr
}
