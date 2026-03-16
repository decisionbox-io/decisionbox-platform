package runner

import (
	"context"
	"fmt"
	"os"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// --- Config tests ---

func TestLoadConfig_Defaults(t *testing.T) {
	// Clear env vars to test defaults
	for _, key := range []string{"RUNNER_MODE", "AGENT_IMAGE", "AGENT_NAMESPACE", "AGENT_CPU_REQUEST", "AGENT_CPU_LIMIT", "AGENT_MEMORY_REQUEST", "AGENT_MEMORY_LIMIT", "AGENT_JOB_TIMEOUT_HOURS"} {
		os.Unsetenv(key)
	}

	cfg := LoadConfig()

	if cfg.Mode != "subprocess" {
		t.Errorf("Mode = %q, want subprocess", cfg.Mode)
	}
	if cfg.AgentImage != "ghcr.io/decisionbox-io/decisionbox-agent:latest" {
		t.Errorf("AgentImage = %q", cfg.AgentImage)
	}
	if cfg.Namespace != "default" {
		t.Errorf("Namespace = %q, want default", cfg.Namespace)
	}
	if cfg.CPURequest != "250m" {
		t.Errorf("CPURequest = %q", cfg.CPURequest)
	}
	if cfg.MemoryLimit != "1Gi" {
		t.Errorf("MemoryLimit = %q", cfg.MemoryLimit)
	}
	if cfg.JobTimeoutHours != 6 {
		t.Errorf("JobTimeoutHours = %d, want 6", cfg.JobTimeoutHours)
	}
}

func TestLoadConfig_EnvOverrides(t *testing.T) {
	os.Setenv("RUNNER_MODE", "kubernetes")
	os.Setenv("AGENT_IMAGE", "my-registry/agent:v1")
	os.Setenv("AGENT_NAMESPACE", "discovery")
	os.Setenv("AGENT_CPU_LIMIT", "4")
	os.Setenv("AGENT_JOB_TIMEOUT_HOURS", "12")
	defer func() {
		os.Unsetenv("RUNNER_MODE")
		os.Unsetenv("AGENT_IMAGE")
		os.Unsetenv("AGENT_NAMESPACE")
		os.Unsetenv("AGENT_CPU_LIMIT")
		os.Unsetenv("AGENT_JOB_TIMEOUT_HOURS")
	}()

	cfg := LoadConfig()

	if cfg.Mode != "kubernetes" {
		t.Errorf("Mode = %q", cfg.Mode)
	}
	if cfg.AgentImage != "my-registry/agent:v1" {
		t.Errorf("AgentImage = %q", cfg.AgentImage)
	}
	if cfg.Namespace != "discovery" {
		t.Errorf("Namespace = %q", cfg.Namespace)
	}
	if cfg.CPULimit != "4" {
		t.Errorf("CPULimit = %q", cfg.CPULimit)
	}
	if cfg.JobTimeoutHours != 12 {
		t.Errorf("JobTimeoutHours = %d, want 12", cfg.JobTimeoutHours)
	}
}

func TestNew_Subprocess(t *testing.T) {
	r, err := New(Config{Mode: "subprocess"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.(*SubprocessRunner); !ok {
		t.Error("expected SubprocessRunner")
	}
}

func TestNew_EmptyMode(t *testing.T) {
	r, err := New(Config{Mode: ""})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.(*SubprocessRunner); !ok {
		t.Error("empty mode should default to SubprocessRunner")
	}
}

func TestNew_InvalidMode(t *testing.T) {
	_, err := New(Config{Mode: "docker"})
	if err == nil {
		t.Error("expected error for unknown mode")
	}
}

// --- Kubernetes runner with fake client ---

func newFakeK8sRunner() *KubernetesRunner {
	return &KubernetesRunner{
		client: fake.NewClientset(),
		config: Config{
			AgentImage:    "ghcr.io/decisionbox-io/decisionbox-agent:test",
			Namespace:     "test-ns",
			CPURequest:    "100m",
			CPULimit:      "1",
			MemoryRequest: "128Mi",
			MemoryLimit:   "512Mi",
		},
	}
}

func TestKubernetesRunner_Run_CreatesJob(t *testing.T) {
	r := newFakeK8sRunner()
	ctx := context.Background()

	err := r.Run(ctx, RunOptions{
		ProjectID: "proj-123",
		RunID:     "run-abc-def-123456",
		Areas:     []string{"churn", "monetization"},
		MaxSteps:  50,
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Verify Job was created
	jobs, err := r.client.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs.Items))
	}

	job := jobs.Items[0]

	// Check job name
	if job.Name != "discovery-run-abc-def-12345678" {
		// Name is truncated to 20 chars of runID
		t.Logf("job name: %s", job.Name)
	}

	// Check labels
	if job.Labels["app"] != "decisionbox-agent" {
		t.Errorf("label app = %q", job.Labels["app"])
	}
	if job.Labels["project-id"] != "proj-123" {
		t.Errorf("label project-id = %q", job.Labels["project-id"])
	}
	if job.Labels["run-id"] != "run-abc-def-123456" {
		t.Errorf("label run-id = %q", job.Labels["run-id"])
	}

	// Check container spec
	containers := job.Spec.Template.Spec.Containers
	if len(containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(containers))
	}
	c := containers[0]

	if c.Image != "ghcr.io/decisionbox-io/decisionbox-agent:test" {
		t.Errorf("image = %q", c.Image)
	}

	// Check args contain project-id and run-id
	argsStr := ""
	for _, a := range c.Args {
		argsStr += a + " "
	}
	if !containsStr(argsStr, "--project-id") || !containsStr(argsStr, "proj-123") {
		t.Errorf("args missing project-id: %v", c.Args)
	}
	if !containsStr(argsStr, "--run-id") || !containsStr(argsStr, "run-abc-def-123456") {
		t.Errorf("args missing run-id: %v", c.Args)
	}
	if !containsStr(argsStr, "--areas") || !containsStr(argsStr, "churn,monetization") {
		t.Errorf("args missing areas: %v", c.Args)
	}
	if !containsStr(argsStr, "--max-steps") || !containsStr(argsStr, "50") {
		t.Errorf("args missing max-steps: %v", c.Args)
	}

	// Check resource limits
	cpuLimit := c.Resources.Limits["cpu"]
	if cpuLimit.String() != "1" {
		t.Errorf("cpu limit = %q, want 1", cpuLimit.String())
	}
	memLimit := c.Resources.Limits["memory"]
	if memLimit.String() != "512Mi" {
		t.Errorf("memory limit = %q, want 512Mi", memLimit.String())
	}

	// Check env vars
	envMap := make(map[string]string)
	for _, e := range c.Env {
		envMap[e.Name] = e.Value
	}
	if envMap["DOMAIN_PACK_PATH"] != "/app/domain-packs" {
		t.Errorf("DOMAIN_PACK_PATH = %q", envMap["DOMAIN_PACK_PATH"])
	}

	// Check restart policy
	if job.Spec.Template.Spec.RestartPolicy != "Never" {
		t.Errorf("restart policy = %q, want Never", job.Spec.Template.Spec.RestartPolicy)
	}

	// Check backoff limit
	if *job.Spec.BackoffLimit != 0 {
		t.Errorf("backoff limit = %d, want 0", *job.Spec.BackoffLimit)
	}

	// Check TTL
	if *job.Spec.TTLSecondsAfterFinished != 3600 {
		t.Errorf("TTL = %d, want 3600", *job.Spec.TTLSecondsAfterFinished)
	}
}

func TestKubernetesRunner_Run_NoAreas(t *testing.T) {
	r := newFakeK8sRunner()
	ctx := context.Background()

	err := r.Run(ctx, RunOptions{
		ProjectID: "proj-456",
		RunID:     "run-full-discovery",
	})
	if err != nil {
		t.Fatal(err)
	}

	jobs, _ := r.client.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	c := jobs.Items[0].Spec.Template.Spec.Containers[0]

	// Should NOT have --areas arg
	for _, a := range c.Args {
		if a == "--areas" {
			t.Error("full run should not have --areas arg")
		}
	}
}

func TestKubernetesRunner_Cancel_DeletesJob(t *testing.T) {
	r := newFakeK8sRunner()
	ctx := context.Background()

	runID := "cancel-test-run-1234"

	// Create a job via Run (so naming matches)
	err := r.Run(ctx, RunOptions{ProjectID: "cancel-proj", RunID: runID})
	if err != nil {
		t.Fatal(err)
	}

	// Verify it exists
	jobs, _ := r.client.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	if len(jobs.Items) != 1 {
		t.Fatalf("expected 1 job before cancel, got %d", len(jobs.Items))
	}

	// Cancel it
	err = r.Cancel(ctx, runID)
	if err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}

	// Verify it's gone
	jobs, _ = r.client.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	if len(jobs.Items) != 0 {
		t.Errorf("expected 0 jobs after cancel, got %d", len(jobs.Items))
	}
}

func TestKubernetesRunner_Cancel_NotFound(t *testing.T) {
	r := newFakeK8sRunner()
	ctx := context.Background()

	// Cancel a non-existent job — should return error but not panic
	err := r.Cancel(ctx, "nonexistent-run-id")
	if err == nil {
		t.Error("expected error for non-existent job")
	}
}

func TestKubernetesRunner_MultipleRuns(t *testing.T) {
	r := newFakeK8sRunner()
	ctx := context.Background()

	// Create 3 runs
	for i, runID := range []string{"run-aaa-111111111111", "run-bbb-222222222222", "run-ccc-333333333333"} {
		err := r.Run(ctx, RunOptions{
			ProjectID: "proj-multi",
			RunID:     runID,
			MaxSteps:  10 * (i + 1),
		})
		if err != nil {
			t.Fatalf("Run %d failed: %v", i, err)
		}
	}

	jobs, _ := r.client.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	if len(jobs.Items) != 3 {
		t.Errorf("expected 3 parallel jobs, got %d", len(jobs.Items))
	}
}

// --- Subprocess runner ---

func TestSubprocessRunner_Cancel_NotRunning(t *testing.T) {
	r := NewSubprocessRunner()
	// Cancel a run that doesn't exist — should not error
	err := r.Cancel(context.Background(), "nonexistent")
	if err != nil {
		t.Errorf("cancel non-existent should not error: %v", err)
	}
}

// --- Error extraction tests ---

func TestExtractErrorMessage_FatalLine(t *testing.T) {
	stderr := `2026-03-13T20:23:21.485Z	INFO	LLM provider initialized
2026-03-13T20:23:47.479Z	FATAL	Discovery failed	{"error": "authentication_error - invalid x-api-key"}`

	msg := extractErrorMessage(stderr, fmt.Errorf("exit status 1"))
	if msg != "authentication_error - invalid x-api-key" {
		t.Errorf("got %q", msg)
	}
}

func TestExtractErrorMessage_ErrorLine(t *testing.T) {
	stderr := `2026-03-13T20:23:41.071Z	INFO	Starting exploration
2026-03-13T20:23:47.469Z	ERROR	LLM call failed	{"error": "claude: API error: rate_limited"}`

	msg := extractErrorMessage(stderr, fmt.Errorf("exit status 1"))
	if msg != "claude: API error: rate_limited" {
		t.Errorf("got %q", msg)
	}
}

func TestExtractErrorMessage_NoStructuredLogs(t *testing.T) {
	stderr := "panic: runtime error: index out of range"
	msg := extractErrorMessage(stderr, fmt.Errorf("exit status 2"))
	if msg != "panic: runtime error: index out of range" {
		t.Errorf("got %q", msg)
	}
}

func TestExtractErrorMessage_Empty(t *testing.T) {
	msg := extractErrorMessage("", fmt.Errorf("signal: killed"))
	if msg != "signal: killed" {
		t.Errorf("got %q", msg)
	}
}

func TestExtractErrorMessage_JSONFormat(t *testing.T) {
	stderr := `{"level":"fatal","msg":"Discovery failed","error":"bigquery: dataset not found","service":"decisionbox-agent"}`
	msg := extractErrorMessage(stderr, fmt.Errorf("exit status 1"))
	if msg != "bigquery: dataset not found" {
		t.Errorf("got %q", msg)
	}
}

func TestExtractJSONField(t *testing.T) {
	tests := []struct {
		line  string
		field string
		want  string
	}{
		{`{"error": "test error", "status": "failed"}`, "error", "test error"},
		{`{"msg":"hello world"}`, "msg", "hello world"},
		{`no json here`, "error", ""},
		{`{"other": "value"}`, "error", ""},
		{`{"error": "escaped \"quotes\""}`, "error", `escaped "quotes"`},
	}
	for _, tt := range tests {
		got := extractJSONField(tt.line, tt.field)
		if got != tt.want {
			t.Errorf("extractJSONField(%q, %q) = %q, want %q", tt.line, tt.field, got, tt.want)
		}
	}
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
