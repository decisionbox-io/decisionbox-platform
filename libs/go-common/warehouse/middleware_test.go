package warehouse

import (
	"context"
	"testing"
)

// resetMiddlewares clears global state for tests. Not safe for t.Parallel.
func resetMiddlewares() {
	middlewareMu.Lock()
	defer middlewareMu.Unlock()
	middlewares = nil
	middlewareNames = make(map[string]bool)
}

type mockProvider struct {
	Provider
	listTablesCalled bool
}

func (m *mockProvider) ListTables(_ context.Context) ([]string, error) {
	m.listTablesCalled = true
	return []string{"table1"}, nil
}

type wrappingProvider struct {
	Provider
	wrappedCalled bool
}

func (w *wrappingProvider) ListTables(ctx context.Context) ([]string, error) {
	w.wrappedCalled = true
	return w.Provider.ListTables(ctx)
}

func TestMiddleware(t *testing.T) {
	resetMiddlewares()

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

func TestApplyMiddleware_NoMiddlewares(t *testing.T) {
	resetMiddlewares()

	base := &mockProvider{}
	result := ApplyMiddleware(base)

	if result != base {
		t.Error("expected base provider returned unchanged when no middlewares")
	}
}

func TestApplyMiddleware_DeterministicOrder(t *testing.T) {
	resetMiddlewares()

	var order []string
	RegisterMiddleware("first", func(p Provider) Provider {
		order = append(order, "first")
		return p
	})
	RegisterMiddleware("second", func(p Provider) Provider {
		order = append(order, "second")
		return p
	})
	RegisterMiddleware("third", func(p Provider) Provider {
		order = append(order, "third")
		return p
	})

	ApplyMiddleware(&mockProvider{})

	if len(order) != 3 || order[0] != "first" || order[1] != "second" || order[2] != "third" {
		t.Errorf("expected [first, second, third], got %v", order)
	}
}

func TestRegisterMiddleware_PanicOnNil(t *testing.T) {
	resetMiddlewares()
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil middleware")
		}
	}()
	RegisterMiddleware("nil-mw", nil)
}

func TestRegisterMiddleware_PanicOnDuplicate(t *testing.T) {
	resetMiddlewares()
	RegisterMiddleware("dup", func(p Provider) Provider { return p })
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for duplicate registration")
		}
	}()
	RegisterMiddleware("dup", func(p Provider) Provider { return p })
}
