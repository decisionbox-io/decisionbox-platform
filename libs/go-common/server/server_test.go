package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/health"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Port != 8080 {
		t.Errorf("Port = %d, want 8080", cfg.Port)
	}
	if cfg.ReadTimeout != 30*time.Second {
		t.Errorf("ReadTimeout = %v, want 30s", cfg.ReadTimeout)
	}
	if cfg.WriteTimeout != 30*time.Second {
		t.Errorf("WriteTimeout = %v, want 30s", cfg.WriteTimeout)
	}
	if cfg.IdleTimeout != 120*time.Second {
		t.Errorf("IdleTimeout = %v, want 120s", cfg.IdleTimeout)
	}
	if cfg.ShutdownTimeout != 30*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 30s", cfg.ShutdownTimeout)
	}
}

func TestDefaultConfig_OptionalFieldsAreZero(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.MaxHeaderBytes != 0 {
		t.Errorf("MaxHeaderBytes = %d, want 0", cfg.MaxHeaderBytes)
	}
	if cfg.TLSCertFile != "" {
		t.Errorf("TLSCertFile = %q, want empty", cfg.TLSCertFile)
	}
	if cfg.TLSKeyFile != "" {
		t.Errorf("TLSKeyFile = %q, want empty", cfg.TLSKeyFile)
	}
	if cfg.OnShutdown != nil {
		t.Error("OnShutdown should be nil by default")
	}
}

// freePort returns an available TCP port by binding to :0 and releasing it.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// waitForServer polls the given URL until it responds or the timeout expires.
func waitForServer(t *testing.T, url string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server at %s did not become ready within %v", url, timeout)
}

func TestRun_StartsAndShutdownsGracefully(t *testing.T) {
	port := freePort(t)
	cfg := DefaultConfig()
	cfg.Port = port
	cfg.ShutdownTimeout = 2 * time.Second

	shutdownCalled := false
	var mu sync.Mutex
	cfg.OnShutdown = func() {
		mu.Lock()
		shutdownCalled = true
		mu.Unlock()
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(cfg, handler, nil)
	}()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitForServer(t, baseURL+"/metrics", 3*time.Second)

	// Verify application handler responds
	resp, err := http.Get(baseURL + "/")
	if err != nil {
		t.Fatalf("GET / returned error: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET / status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if string(body) != "ok" {
		t.Errorf("GET / body = %q, want %q", string(body), "ok")
	}

	// Send SIGTERM to trigger graceful shutdown
	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("failed to find current process: %v", err)
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("failed to send SIGTERM: %v", err)
	}

	select {
	case runErr := <-errCh:
		if runErr != nil {
			t.Errorf("Run() returned error: %v", runErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after SIGTERM within 5s")
	}

	mu.Lock()
	if !shutdownCalled {
		t.Error("OnShutdown callback was not called")
	}
	mu.Unlock()
}

func TestRun_HealthEndpoints(t *testing.T) {
	port := freePort(t)
	cfg := DefaultConfig()
	cfg.Port = port
	cfg.ShutdownTimeout = 2 * time.Second

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	hh := health.NewHandler()

	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(cfg, handler, hh)
	}()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitForServer(t, baseURL+"/health", 3*time.Second)

	// Test liveness endpoint
	resp, err := http.Get(baseURL + "/health")
	if err != nil {
		t.Fatalf("GET /health returned error: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /health status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var healthResp map[string]interface{}
	if err := json.Unmarshal(body, &healthResp); err != nil {
		t.Fatalf("failed to parse /health response: %v", err)
	}
	if healthResp["status"] != "ok" {
		t.Errorf("health status = %q, want %q", healthResp["status"], "ok")
	}

	// Test readiness endpoint
	resp, err = http.Get(baseURL + "/health/ready")
	if err != nil {
		t.Fatalf("GET /health/ready returned error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /health/ready status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	// Shutdown
	p, _ := os.FindProcess(os.Getpid())
	p.Signal(syscall.SIGTERM)
	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after SIGTERM")
	}
}

func TestRun_MetricsEndpoint(t *testing.T) {
	port := freePort(t)
	cfg := DefaultConfig()
	cfg.Port = port
	cfg.ShutdownTimeout = 2 * time.Second

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(cfg, handler, nil)
	}()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitForServer(t, baseURL+"/metrics", 3*time.Second)

	resp, err := http.Get(baseURL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics returned error: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /metrics status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	// Prometheus metrics output should contain standard Go metrics
	if !strings.Contains(string(body), "go_") {
		t.Error("GET /metrics response does not contain expected Prometheus metrics")
	}

	// Shutdown
	p, _ := os.FindProcess(os.Getpid())
	p.Signal(syscall.SIGTERM)
	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after SIGTERM")
	}
}
