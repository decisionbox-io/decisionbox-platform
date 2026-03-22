package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler(t *testing.T) {
	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()

	Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "go_goroutines") {
		t.Error("metrics output should contain default Go metrics")
	}
}

func TestHTTPMiddleware(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	handler := HTTPMiddleware(inner)

	req := httptest.NewRequest("GET", "/api/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != "ok" {
		t.Errorf("body = %q, want %q", w.Body.String(), "ok")
	}
}

func TestHTTPMiddlewareSkipsHealthAndMetrics(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	handler := HTTPMiddleware(inner)

	paths := []string{"/metrics", "/health", "/health/ready"}
	for _, path := range paths {
		called = false
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if !called {
			t.Errorf("handler not called for %s", path)
		}
	}
}

func TestResponseWriterCapturesStatus(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	handler := HTTPMiddleware(inner)

	req := httptest.NewRequest("GET", "/api/missing", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestNewCounter_NoPanic(t *testing.T) {
	c := NewCounter("test_counter_unit", "A test counter", "label1")
	if c == nil {
		t.Error("NewCounter returned nil")
	}
	// Verify it can be used without panic
	c.WithLabelValues("val1").Inc()
}

func TestNewHistogram_NoPanic(t *testing.T) {
	h := NewHistogram("test_histogram_unit", "A test histogram", "label1")
	if h == nil {
		t.Error("NewHistogram returned nil")
	}
	h.WithLabelValues("val1").Observe(0.5)
}

func TestNewGauge_NoPanic(t *testing.T) {
	g := NewGauge("test_gauge_unit", "A test gauge", "label1")
	if g == nil {
		t.Error("NewGauge returned nil")
	}
	g.WithLabelValues("val1").Set(42)
}

func TestHTTPMiddleware_StatusCodes(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		path       string
	}{
		{"200 OK", http.StatusOK, "/api/v1/projects"},
		{"201 Created", http.StatusCreated, "/api/v1/projects/create"},
		{"400 Bad Request", http.StatusBadRequest, "/api/v1/bad"},
		{"404 Not Found", http.StatusNotFound, "/api/v1/notfound"},
		{"500 Internal Server Error", http.StatusInternalServerError, "/api/v1/error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
			})

			handler := HTTPMiddleware(inner)
			req := httptest.NewRequest("GET", tt.path, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != tt.statusCode {
				t.Errorf("status = %d, want %d", w.Code, tt.statusCode)
			}
		})
	}
}

func TestHTTPMiddleware_DefaultStatusOK(t *testing.T) {
	// When inner handler does not call WriteHeader, status should default to 200
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("no explicit status"))
	})

	handler := HTTPMiddleware(inner)
	req := httptest.NewRequest("GET", "/api/v1/implicit", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestHTTPMiddleware_POST(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":"123"}`))
	})

	handler := HTTPMiddleware(inner)
	req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(`{"name":"test"}`))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", w.Code)
	}
}
