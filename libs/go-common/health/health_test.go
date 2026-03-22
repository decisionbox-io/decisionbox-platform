package health

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

type mockChecker struct {
	name string
	err  error
}

func (m mockChecker) Name() string                    { return m.name }
func (m mockChecker) Check(ctx context.Context) error { return m.err }

func TestLivenessHandler(t *testing.T) {
	h := NewHandler()
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	h.LivenessHandler()(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var resp response
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Status != "ok" {
		t.Errorf("status = %q, want %q", resp.Status, "ok")
	}
}

func TestReadinessAllHealthy(t *testing.T) {
	h := NewHandler(
		mockChecker{name: "mongodb", err: nil},
		mockChecker{name: "redis", err: nil},
	)

	req := httptest.NewRequest("GET", "/health/ready", nil)
	w := httptest.NewRecorder()

	h.ReadinessHandler()(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var resp response
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Status != "ok" {
		t.Errorf("status = %q, want %q", resp.Status, "ok")
	}
	if len(resp.Services) != 2 {
		t.Errorf("services count = %d, want 2", len(resp.Services))
	}
}

func TestReadinessDegraded(t *testing.T) {
	h := NewHandler(
		mockChecker{name: "mongodb", err: nil},
		mockChecker{name: "redis", err: fmt.Errorf("connection refused")},
	)

	req := httptest.NewRequest("GET", "/health/ready", nil)
	w := httptest.NewRecorder()

	h.ReadinessHandler()(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}

	var resp response
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Status != "degraded" {
		t.Errorf("status = %q, want %q", resp.Status, "degraded")
	}
	if resp.Services["redis"].Status != "error" {
		t.Errorf("redis status = %q, want %q", resp.Services["redis"].Status, "error")
	}
	if resp.Services["mongodb"].Status != "ok" {
		t.Errorf("mongodb status = %q, want %q", resp.Services["mongodb"].Status, "ok")
	}
}

func TestReadinessNoCheckers(t *testing.T) {
	h := NewHandler()

	req := httptest.NewRequest("GET", "/health/ready", nil)
	w := httptest.NewRecorder()

	h.ReadinessHandler()(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestReadinessSingleCheckerError(t *testing.T) {
	h := NewHandler(
		mockChecker{name: "mongodb", err: fmt.Errorf("connection timeout")},
	)

	req := httptest.NewRequest("GET", "/health/ready", nil)
	w := httptest.NewRecorder()

	h.ReadinessHandler()(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}

	var resp response
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Status != "degraded" {
		t.Errorf("status = %q, want %q", resp.Status, "degraded")
	}
	if resp.Services["mongodb"].Status != "error" {
		t.Errorf("mongodb status = %q, want %q", resp.Services["mongodb"].Status, "error")
	}
	if resp.Services["mongodb"].Error != "connection timeout" {
		t.Errorf("mongodb error = %q, want %q", resp.Services["mongodb"].Error, "connection timeout")
	}
}

func TestReadinessMultipleCheckersPartialFailure(t *testing.T) {
	h := NewHandler(
		mockChecker{name: "mongodb", err: nil},
		mockChecker{name: "redis", err: fmt.Errorf("connection refused")},
		mockChecker{name: "rabbitmq", err: nil},
	)

	req := httptest.NewRequest("GET", "/health/ready", nil)
	w := httptest.NewRecorder()

	h.ReadinessHandler()(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}

	var resp response
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Status != "degraded" {
		t.Errorf("status = %q, want %q", resp.Status, "degraded")
	}
	if len(resp.Services) != 3 {
		t.Errorf("services count = %d, want 3", len(resp.Services))
	}
	if resp.Services["mongodb"].Status != "ok" {
		t.Errorf("mongodb status = %q, want %q", resp.Services["mongodb"].Status, "ok")
	}
	if resp.Services["redis"].Status != "error" {
		t.Errorf("redis status = %q, want %q", resp.Services["redis"].Status, "error")
	}
	if resp.Services["redis"].Error != "connection refused" {
		t.Errorf("redis error = %q, want %q", resp.Services["redis"].Error, "connection refused")
	}
	if resp.Services["rabbitmq"].Status != "ok" {
		t.Errorf("rabbitmq status = %q, want %q", resp.Services["rabbitmq"].Status, "ok")
	}
}

func TestNewHandler_Checkers(t *testing.T) {
	c1 := mockChecker{name: "mongodb"}
	c2 := mockChecker{name: "redis"}
	h := NewHandler(c1, c2)

	checkers := h.Checkers()
	if len(checkers) != 2 {
		t.Errorf("Checkers() length = %d, want 2", len(checkers))
	}
	if checkers[0].Name() != "mongodb" {
		t.Errorf("checkers[0].Name() = %q, want %q", checkers[0].Name(), "mongodb")
	}
	if checkers[1].Name() != "redis" {
		t.Errorf("checkers[1].Name() = %q, want %q", checkers[1].Name(), "redis")
	}
}

func TestReadinessAllCheckersError(t *testing.T) {
	h := NewHandler(
		mockChecker{name: "mongodb", err: fmt.Errorf("timeout")},
		mockChecker{name: "redis", err: fmt.Errorf("connection refused")},
	)

	req := httptest.NewRequest("GET", "/health/ready", nil)
	w := httptest.NewRecorder()

	h.ReadinessHandler()(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}

	var resp response
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Status != "degraded" {
		t.Errorf("status = %q, want %q", resp.Status, "degraded")
	}
	if resp.Services["mongodb"].Status != "error" {
		t.Errorf("mongodb status = %q, want %q", resp.Services["mongodb"].Status, "error")
	}
	if resp.Services["redis"].Status != "error" {
		t.Errorf("redis status = %q, want %q", resp.Services["redis"].Status, "error")
	}
}

// mockPinger implements the Pinger interface for MongoChecker and RedisChecker.
type mockPinger struct {
	err error
}

func (m *mockPinger) Ping(ctx context.Context) error { return m.err }

// mockHealthChecker implements the HealthChecker interface for RabbitMQChecker.
type mockHealthChecker struct {
	err error
}

func (m *mockHealthChecker) HealthCheck() error { return m.err }

func TestMongoChecker_Name(t *testing.T) {
	c := MongoChecker{Pinger: &mockPinger{}}
	if c.Name() != "mongodb" {
		t.Errorf("Name() = %q, want %q", c.Name(), "mongodb")
	}
}

func TestMongoChecker_CheckSuccess(t *testing.T) {
	c := MongoChecker{Pinger: &mockPinger{err: nil}}
	if err := c.Check(context.Background()); err != nil {
		t.Errorf("Check() error: %v", err)
	}
}

func TestMongoChecker_CheckError(t *testing.T) {
	c := MongoChecker{Pinger: &mockPinger{err: fmt.Errorf("connection refused")}}
	if err := c.Check(context.Background()); err == nil {
		t.Error("Check() should return error")
	}
}

func TestRedisChecker_Name(t *testing.T) {
	c := RedisChecker{Pinger: &mockPinger{}}
	if c.Name() != "redis" {
		t.Errorf("Name() = %q, want %q", c.Name(), "redis")
	}
}

func TestRedisChecker_CheckSuccess(t *testing.T) {
	c := RedisChecker{Pinger: &mockPinger{err: nil}}
	if err := c.Check(context.Background()); err != nil {
		t.Errorf("Check() error: %v", err)
	}
}

func TestRedisChecker_CheckError(t *testing.T) {
	c := RedisChecker{Pinger: &mockPinger{err: fmt.Errorf("timeout")}}
	if err := c.Check(context.Background()); err == nil {
		t.Error("Check() should return error")
	}
}

func TestRabbitMQChecker_Name(t *testing.T) {
	c := RabbitMQChecker{HealthChecker: &mockHealthChecker{}}
	if c.Name() != "rabbitmq" {
		t.Errorf("Name() = %q, want %q", c.Name(), "rabbitmq")
	}
}

func TestRabbitMQChecker_CheckSuccess(t *testing.T) {
	c := RabbitMQChecker{HealthChecker: &mockHealthChecker{err: nil}}
	if err := c.Check(context.Background()); err != nil {
		t.Errorf("Check() error: %v", err)
	}
}

func TestRabbitMQChecker_CheckError(t *testing.T) {
	c := RabbitMQChecker{HealthChecker: &mockHealthChecker{err: fmt.Errorf("channel closed")}}
	if err := c.Check(context.Background()); err == nil {
		t.Error("Check() should return error")
	}
}
