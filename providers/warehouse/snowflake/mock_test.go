package snowflake

import (
	"context"
	"database/sql"
	"fmt"
	"io"
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

// mockRows helps build mock *sql.Rows for testing.
// Since sql.Rows is a concrete type that can't be mocked directly,
// we test through the provider methods that produce QueryResult.

// mockRowsData holds test data for building expectations.
type mockRowsData struct {
	columns []string
	rows    [][]interface{}
}

// For tests that don't need actual sql.Rows, we test the helper
// functions (normalizeSnowflakeType, normalizeValue, parsePrivateKey)
// directly and test the provider through the factory registration.

// nullReadCloser is used for tests that need an io.ReadCloser.
type nullReadCloser struct{}

func (nullReadCloser) Read([]byte) (int, error) { return 0, io.EOF }
func (nullReadCloser) Close() error             { return nil }
