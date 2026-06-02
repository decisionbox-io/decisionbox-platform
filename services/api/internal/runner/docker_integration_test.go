//go:build integration_docker

// Docker runner integration tests. They drive the real Docker engine over
// the host socket (DOCKER_HOST / /var/run/docker.sock), spawning throwaway
// alpine containers to verify the container lifecycle, log streaming,
// cleanup, non-zero-exit mapping, and graceful cancel end to end.
//
// Kept under a dedicated build tag (not the package's `integration` tag)
// so it does NOT pull in the K3s testcontainer that integration_test.go's
// TestMain starts — this only needs a Docker daemon.
//
//	go test -tags=integration_docker ./internal/runner/
package runner

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dockerclient "github.com/docker/docker/client"
)

const testAlpineImage = "alpine:3.21"

func newRealDockerRunner(t *testing.T, cfg Config) *DockerRunner {
	t.Helper()
	if cfg.AgentImage == "" {
		cfg.AgentImage = testAlpineImage
	}
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("create docker client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := cli.Ping(ctx); err != nil {
		t.Skipf("docker engine not reachable, skipping: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return &DockerRunner{client: cli, config: cfg}
}

// countByRunID returns how many containers carry the given run-id label —
// 0 after a clean lifecycle / cancel.
func countByRunID(t *testing.T, r *DockerRunner, runID string) int {
	t.Helper()
	f := filters.NewArgs()
	f.Add("label", "app="+dockerAgentLabel)
	f.Add("label", "run-id="+runID)
	list, err := r.client.ContainerList(context.Background(), container.ListOptions{All: true, Filters: f})
	if err != nil {
		t.Fatalf("list by run-id: %v", err)
	}
	return len(list)
}

// cleanupByRunID force-removes any container left behind by a failed test.
func cleanupByRunID(t *testing.T, r *DockerRunner, runID string) {
	t.Helper()
	t.Cleanup(func() {
		f := filters.NewArgs()
		f.Add("label", "app="+dockerAgentLabel)
		f.Add("label", "run-id="+runID)
		list, err := r.client.ContainerList(context.Background(), container.ListOptions{All: true, Filters: f})
		if err != nil {
			return
		}
		for _, c := range list {
			_ = r.removeContainer(c.ID)
		}
	})
}

func TestInteg_DockerRunner_LifecycleAndLogStreaming(t *testing.T) {
	r := newRealDockerRunner(t, Config{})
	runID := "integ-ok"
	cleanupByRunID(t, r, runID)
	ctx := context.Background()

	id, err := r.createAndStart(ctx, containerSpec{
		image:  testAlpineImage,
		cmd:    []string{"sh", "-c", "echo stdout-line; echo err-line-1 >&2; echo err-line-2 >&2; exit 0"},
		labels: map[string]string{"run-id": runID},
	})
	if err != nil {
		t.Fatalf("createAndStart: %v", err)
	}

	var stdout bytes.Buffer
	var mu sync.Mutex
	var lines []string
	code, werr := r.streamWaitRemove(ctx, id, logHandlers{
		stdout:       &stdout,
		onStderrLine: func(l string) { mu.Lock(); lines = append(lines, l); mu.Unlock() },
		logPrefix:    "[integ] ",
	}, true)
	if werr != nil {
		t.Fatalf("streamWaitRemove: %v", werr)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "stdout-line") {
		t.Errorf("stdout = %q, want it to contain stdout-line", stdout.String())
	}
	mu.Lock()
	got := strings.Join(lines, "|")
	mu.Unlock()
	if got != "err-line-1|err-line-2" {
		t.Errorf("stderr lines = %q, want err-line-1|err-line-2", got)
	}

	// Container must be removed on completion.
	if n := countByRunID(t, r, runID); n != 0 {
		t.Errorf("expected container removed after completion, found %d", n)
	}
}

func TestInteg_DockerRunner_NonZeroExitSurfacesError(t *testing.T) {
	r := newRealDockerRunner(t, Config{})
	runID := "integ-fail"
	cleanupByRunID(t, r, runID)
	ctx := context.Background()

	id, err := r.createAndStart(ctx, containerSpec{
		image:  testAlpineImage,
		cmd:    []string{"sh", "-c", `echo '{"level":"fatal","error":"integ failure"}' >&2; exit 3`},
		labels: map[string]string{"run-id": runID},
	})
	if err != nil {
		t.Fatalf("createAndStart: %v", err)
	}

	code, werr := r.streamWaitRemove(ctx, id, logHandlers{logPrefix: "[integ-fail] "}, true)
	if werr == nil {
		t.Fatal("expected error on non-zero exit")
	}
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
	if !strings.Contains(werr.Error(), "integ failure") {
		t.Errorf("error = %q, want extracted FATAL message", werr.Error())
	}
	if n := countByRunID(t, r, runID); n != 0 {
		t.Errorf("expected container removed after failure, found %d", n)
	}
}

func TestInteg_DockerRunner_GracefulCancel(t *testing.T) {
	r := newRealDockerRunner(t, Config{})
	runID := "integ-cancel"
	cleanupByRunID(t, r, runID)
	ctx := context.Background()

	// Keep the SIGTERM grace short so the test is fast; the trap exits
	// promptly so ContainerStop returns well within it anyway.
	prev := dockerStopGraceSeconds
	dockerStopGraceSeconds = 10
	defer func() { dockerStopGraceSeconds = prev }()

	id, err := r.createAndStart(ctx, containerSpec{
		image:  testAlpineImage,
		cmd:    []string{"sh", "-c", "trap 'echo terminating >&2; exit 0' TERM; echo started >&2; while true; do sleep 1; done"},
		labels: map[string]string{"run-id": runID},
	})
	if err != nil {
		t.Fatalf("createAndStart: %v", err)
	}

	// Background watcher, mirroring Run's detached streamWaitRemove.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = r.streamWaitRemove(context.Background(), id, logHandlers{logPrefix: "[integ-cancel] "}, true)
	}()

	// Give the container a moment to start before cancelling.
	time.Sleep(500 * time.Millisecond)

	if err := r.Cancel(ctx, runID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("watcher did not finish after graceful cancel")
	}

	// Cancel's stop/remove is detached (so the grace can't block the handler)
	// and races the watcher's own removal; the container WILL be gone, just
	// possibly a moment after `done`. Poll until the daemon has reaped it.
	deadline := time.Now().Add(20 * time.Second)
	for {
		n := countByRunID(t, r, runID)
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected container removed after cancel, still found %d", n)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestInteg_DockerRunner_ImagePullFailureIsClear(t *testing.T) {
	r := newRealDockerRunner(t, Config{AgentImage: "decisionbox-nonexistent-image-xyz:doesnotexist"})
	runID := "integ-pull-fail"
	cleanupByRunID(t, r, runID)
	ctx := context.Background()

	_, err := r.createAndStart(ctx, containerSpec{
		cmd:    []string{"true"},
		labels: map[string]string{"run-id": runID},
	})
	if err == nil {
		t.Fatal("expected error for an image that cannot be pulled")
	}
	if !strings.Contains(err.Error(), "could not be pulled") {
		t.Errorf("error = %q, want a clear pull-failure message", err.Error())
	}
}
