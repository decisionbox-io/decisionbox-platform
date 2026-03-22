// Package testutil provides shared test mocks and helpers for the agent.
package testutil

import (
	"context"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// MockLLMProvider implements llm.Provider for testing.
type MockLLMProvider struct {
	// Responses maps prompt substrings to responses.
	// If a message contains the key, the corresponding response is returned.
	Responses map[string]*gollm.ChatResponse

	// DefaultResponse is returned when no matching response is found.
	DefaultResponse *gollm.ChatResponse

	// Calls records all calls for verification.
	Calls []MockLLMCall

	// Error if set, all calls return this error.
	Error error
}

// MockLLMCall records a single LLM call.
type MockLLMCall struct {
	Request gollm.ChatRequest
}

func NewMockLLMProvider() *MockLLMProvider {
	return &MockLLMProvider{
		Responses: make(map[string]*gollm.ChatResponse),
		DefaultResponse: &gollm.ChatResponse{
			Content:    `{"done": true, "summary": "mock complete"}`,
			Model:      "mock-model",
			StopReason: "end_turn",
			Usage:      gollm.Usage{InputTokens: 100, OutputTokens: 50},
		},
		Calls: make([]MockLLMCall, 0),
	}
}

func (m *MockLLMProvider) Validate(ctx context.Context) error {
	return m.Error
}

func (m *MockLLMProvider) Chat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	m.Calls = append(m.Calls, MockLLMCall{Request: req})

	if m.Error != nil {
		return nil, m.Error
	}

	// Check messages for matching response
	for _, msg := range req.Messages {
		for key, resp := range m.Responses {
			if len(msg.Content) >= len(key) {
				for i := 0; i <= len(msg.Content)-len(key); i++ {
					if msg.Content[i:i+len(key)] == key {
						return resp, nil
					}
				}
			}
		}
	}

	return m.DefaultResponse, nil
}

// MockWarehouseProvider implements warehouse.Provider for testing.
type MockWarehouseProvider struct {
	Dataset      string
	Tables       []string
	TableSchemas map[string]*gowarehouse.TableSchema
	QueryResults map[string]*gowarehouse.QueryResult // query substring -> result
	DefaultResult *gowarehouse.QueryResult

	Calls      []MockWarehouseCall
	QueryError error
}

// MockWarehouseCall records a single warehouse call.
type MockWarehouseCall struct {
	Method string
	Query  string
}

func NewMockWarehouseProvider(dataset string) *MockWarehouseProvider {
	return &MockWarehouseProvider{
		Dataset:      dataset,
		Tables:       []string{"sessions", "events", "users"},
		TableSchemas: make(map[string]*gowarehouse.TableSchema),
		QueryResults: make(map[string]*gowarehouse.QueryResult),
		DefaultResult: &gowarehouse.QueryResult{
			Columns: []string{"count"},
			Rows:    []map[string]interface{}{{"count": int64(100)}},
		},
		Calls: make([]MockWarehouseCall, 0),
	}
}

func (m *MockWarehouseProvider) Query(ctx context.Context, query string, params map[string]interface{}) (*gowarehouse.QueryResult, error) {
	m.Calls = append(m.Calls, MockWarehouseCall{Method: "Query", Query: query})
	if m.QueryError != nil {
		return nil, m.QueryError
	}
	for key, result := range m.QueryResults {
		if len(query) >= len(key) {
			for i := 0; i <= len(query)-len(key); i++ {
				if query[i:i+len(key)] == key {
					return result, nil
				}
			}
		}
	}
	return m.DefaultResult, nil
}

func (m *MockWarehouseProvider) ListTables(ctx context.Context) ([]string, error) {
	m.Calls = append(m.Calls, MockWarehouseCall{Method: "ListTables"})
	return m.Tables, nil
}

func (m *MockWarehouseProvider) GetTableSchema(ctx context.Context, table string) (*gowarehouse.TableSchema, error) {
	m.Calls = append(m.Calls, MockWarehouseCall{Method: "GetTableSchema", Query: table})
	if schema, ok := m.TableSchemas[table]; ok {
		return schema, nil
	}
	return &gowarehouse.TableSchema{
		Name:     table,
		RowCount: 1000,
		Columns: []gowarehouse.ColumnSchema{
			{Name: "user_id", Type: "STRING", Nullable: false},
			{Name: "created_at", Type: "TIMESTAMP", Nullable: false},
			{Name: "app_id", Type: "STRING", Nullable: false},
		},
	}, nil
}

func (m *MockWarehouseProvider) GetDataset() string { return m.Dataset }

func (m *MockWarehouseProvider) SQLDialect() string { return "Mock SQL" }

func (m *MockWarehouseProvider) SQLFixPrompt() string {
	return "Fix this {{ORIGINAL_SQL}} query. Error: {{ERROR_MESSAGE}}"
}

func (m *MockWarehouseProvider) ListTablesInDataset(ctx context.Context, dataset string) ([]string, error) {
	return m.ListTables(ctx)
}
func (m *MockWarehouseProvider) GetTableSchemaInDataset(ctx context.Context, dataset, table string) (*gowarehouse.TableSchema, error) {
	return m.GetTableSchema(ctx, table)
}
func (m *MockWarehouseProvider) ValidateReadOnly(ctx context.Context) error { return nil }
func (m *MockWarehouseProvider) HealthCheck(ctx context.Context) error { return nil }
func (m *MockWarehouseProvider) Close() error                         { return nil }

// MockSQLFixer implements queryexec.SQLFixer for testing.
type MockSQLFixer struct {
	FixedQuery string
	Error      error
	Calls      int
}

func (m *MockSQLFixer) FixSQL(ctx context.Context, query string, errMsg string, attempt int) (string, error) {
	m.Calls++
	if m.Error != nil {
		return "", m.Error
	}
	if m.FixedQuery != "" {
		return m.FixedQuery, nil
	}
	return "SELECT fixed FROM `dataset.table` WHERE app_id = 'test'", nil
}
