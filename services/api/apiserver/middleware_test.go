package apiserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func resetGlobalMiddlewares() {
	globalMu.Lock()
	defer globalMu.Unlock()
	globalMiddlewares = nil
	globalNames = make(map[string]bool)
}

func TestGlobalMiddleware(t *testing.T) {
	resetGlobalMiddlewares()

	middlewareCalled := false
	RegisterGlobalMiddleware("test", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			middlewareCalled = true
			next.ServeHTTP(w, r)
		})
	})

	handlerCalled := false
	baseHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	wrapped := ApplyGlobalMiddlewares(baseHandler)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	if !middlewareCalled {
		t.Error("global middleware was not called")
	}
	if !handlerCalled {
		t.Error("base handler was not called")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestApplyGlobalMiddlewares_NoMiddlewares(t *testing.T) {
	resetGlobalMiddlewares()

	base := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrapped := ApplyGlobalMiddlewares(base)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestApplyGlobalMiddlewares_Order(t *testing.T) {
	resetGlobalMiddlewares()

	var order []string
	RegisterGlobalMiddleware("outer", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			order = append(order, "outer-before")
			next.ServeHTTP(w, r)
			order = append(order, "outer-after")
		})
	})
	RegisterGlobalMiddleware("inner", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			order = append(order, "inner-before")
			next.ServeHTTP(w, r)
			order = append(order, "inner-after")
		})
	})

	base := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		order = append(order, "handler")
	})

	wrapped := ApplyGlobalMiddlewares(base)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	wrapped.ServeHTTP(httptest.NewRecorder(), req)

	expected := []string{"outer-before", "inner-before", "handler", "inner-after", "outer-after"}
	if len(order) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, order)
	}
	for i, v := range expected {
		if order[i] != v {
			t.Errorf("position %d: expected %s, got %s", i, v, order[i])
		}
	}
}

func TestRegisterGlobalMiddleware_PanicOnNil(t *testing.T) {
	resetGlobalMiddlewares()
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil middleware")
		}
	}()
	RegisterGlobalMiddleware("nil-mw", nil)
}

func TestRegisterGlobalMiddleware_PanicOnDuplicate(t *testing.T) {
	resetGlobalMiddlewares()
	RegisterGlobalMiddleware("dup", func(next http.Handler) http.Handler { return next })
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for duplicate registration")
		}
	}()
	RegisterGlobalMiddleware("dup", func(next http.Handler) http.Handler { return next })
}
