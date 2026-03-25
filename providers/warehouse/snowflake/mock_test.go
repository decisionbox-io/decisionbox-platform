package snowflake

import (
	"context"
	"database/sql"
	"fmt"
)

// mockSFClient implements sfClient for unit testing.
type mockSFClient struct {
	queryFunc func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	pingErr   error
	closeErr  error

	lastQuery string
	lastArgs  []interface{}
}

func (m *mockSFClient) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	m.lastQuery = query
	m.lastArgs = args
	if m.queryFunc != nil {
		return m.queryFunc(ctx, query, args...)
	}
	return nil, fmt.Errorf("mock: no queryFunc configured")
}

func (m *mockSFClient) PingContext(ctx context.Context) error {
	return m.pingErr
}

func (m *mockSFClient) Close() error {
	return m.closeErr
}
