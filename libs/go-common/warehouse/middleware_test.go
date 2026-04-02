package warehouse

import (
	"context"
	"testing"
)

// mockProvider for testing middleware
type mockProvider struct {
	Provider
	listTablesCalled bool
}

func (m *mockProvider) ListTables(ctx context.Context) ([]string, error) {
	m.listTablesCalled = true
	return []string{"table1"}, nil
}

// wrappingProvider for testing middleware
type wrappingProvider struct {
	Provider
	wrappedCalled bool
}

func (w *wrappingProvider) ListTables(ctx context.Context) ([]string, error) {
	w.wrappedCalled = true
	return w.Provider.ListTables(ctx)
}

func TestMiddleware(t *testing.T) {
	// Reset middlewares for test
	middlewareMu.Lock()
	middlewares = make(map[string]Middleware)
	middlewareMu.Unlock()

	base := &mockProvider{}

	RegisterMiddleware("test", func(p Provider) Provider {
		return &wrappingProvider{Provider: p}
	})

	governed := ApplyMiddleware(base)

	res, err := governed.ListTables(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(res) != 1 || res[0] != "table1" {
		t.Errorf("expected table1, got %v", res)
	}

	wrapper := governed.(*wrappingProvider)
	if !wrapper.wrappedCalled {
		t.Error("middleware wrapper was not called")
	}

	if !base.listTablesCalled {
		t.Error("base provider was not called")
	}
}
