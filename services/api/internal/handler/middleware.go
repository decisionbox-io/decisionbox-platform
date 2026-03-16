package handler

import (
	"net/http"
	"time"

	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
)

// LoggingMiddleware logs every HTTP request with method, path, status, and duration.
func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}

		next.ServeHTTP(sw, r)

		duration := time.Since(start)

		fields := apilog.Fields{
			"method":   r.Method,
			"path":     r.URL.Path,
			"status":   sw.status,
			"duration": duration.String(),
		}

		switch {
		case sw.status >= 500:
			apilog.WithFields(fields).Error("Request failed")
		case sw.status >= 400:
			apilog.WithFields(fields).Warn("Request error")
		default:
			apilog.WithFields(fields).Debug("Request completed")
		}
	})
}

// statusWriter wraps ResponseWriter to capture the status code.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
