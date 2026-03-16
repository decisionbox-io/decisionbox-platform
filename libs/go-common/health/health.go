package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// Checker is implemented by each infrastructure dependency (MongoDB, Redis, RabbitMQ, Kafka).
type Checker interface {
	Name() string
	Check(ctx context.Context) error
}

type serviceStatus struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type response struct {
	Status   string                   `json:"status"`
	Services map[string]serviceStatus `json:"services,omitempty"`
}

type Handler struct {
	checkers []Checker
}

func NewHandler(checkers ...Checker) *Handler {
	return &Handler{checkers: checkers}
}

// Checkers returns the registered health checkers.
func (h *Handler) Checkers() []Checker {
	return h.checkers
}

// LivenessHandler returns 200 if the process is alive. No dependency checks.
func (h *Handler) LivenessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response{Status: "ok"})
	}
}

// ReadinessHandler checks all registered dependencies. Returns 200 if all healthy, 503 otherwise.
func (h *Handler) ReadinessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		services := make(map[string]serviceStatus, len(h.checkers))
		allHealthy := true
		var mu sync.Mutex
		var wg sync.WaitGroup

		for _, checker := range h.checkers {
			wg.Add(1)
			go func(c Checker) {
				defer wg.Done()
				s := serviceStatus{Status: "ok"}
				if err := c.Check(ctx); err != nil {
					s.Status = "error"
					s.Error = err.Error()
					mu.Lock()
					allHealthy = false
					mu.Unlock()
				}
				mu.Lock()
				services[c.Name()] = s
				mu.Unlock()
			}(checker)
		}
		wg.Wait()

		resp := response{Status: "ok", Services: services}
		if !allHealthy {
			resp.Status = "degraded"
			w.WriteHeader(http.StatusServiceUnavailable)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}
