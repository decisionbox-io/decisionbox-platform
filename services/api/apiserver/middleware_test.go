package apiserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGlobalMiddleware(t *testing.T) {
	// Reset globalMiddlewares for test
	globalMiddlewares = nil

	middlewareCalled := false
	RegisterGlobalMiddleware(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			middlewareCalled = true
			next.ServeHTTP(w, r)
		})
	})

	handlerCalled := false
	baseHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	wrapped := ApplyGlobalMiddlewares(baseHandler)

	srv := httptest.NewServer(wrapped)
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if !middlewareCalled {
		t.Error("global middleware was not called")
	}

	if !handlerCalled {
		t.Error("base handler was not called")
	}

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status OK, got %d", resp.StatusCode)
	}
}
