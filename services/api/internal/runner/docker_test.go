package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// fakeDocker is an in-memory dockerAPI used to drive the full runner logic
// without a Docker daemon.
type fakeDocker struct {
	mu sync.Mutex

	// captured calls
	createCfgs  []*container.Config
	createNets  []*network.NetworkingConfig
	startedIDs  []string
	stoppedIDs  []string
	removedIDs  []string
	pulledRefs  []string
	listFilters []filters.Args

	// behaviour knobs
	pingErr    error
	createErrs []error // per-call ContainerCreate error; nil = success
	startErr   error
	logsStdout string
	logsStderr []string
	logsErr    error
	exitCode   int64
	waitErr    error
	hangWait   bool
	listResult []container.Summary

	createCount int
	nextID      int

	// removedCh fires after each ContainerRemove so background watchers
	// (Run) can be awaited deterministically.
	removedCh chan string
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{removedCh: make(chan string, 8)}
}

func (f *fakeDocker) ContainerCreate(_ context.Context, config *container.Config, _ *container.HostConfig, networkingConfig *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := f.createCount
	f.createCount++
	if idx < len(f.createErrs) && f.createErrs[idx] != nil {
		return container.CreateResponse{}, f.createErrs[idx]
	}
	f.createCfgs = append(f.createCfgs, config)
	f.createNets = append(f.createNets, networkingConfig)
	f.nextID++
	return container.CreateResponse{ID: fmt.Sprintf("cid-%d", f.nextID)}, nil
}

func (f *fakeDocker) ContainerStart(_ context.Context, containerID string, _ container.StartOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return f.startErr
	}
	f.startedIDs = append(f.startedIDs, containerID)
	return nil
}

func (f *fakeDocker) ContainerWait(_ context.Context, _ string, _ container.WaitCondition) (<-chan container.WaitResponse, <-chan error) {
	statusCh := make(chan container.WaitResponse, 1)
	errCh := make(chan error, 1)
	if f.hangWait {
		return statusCh, errCh // never delivered → caller blocks until ctx
	}
	if f.waitErr != nil {
		errCh <- f.waitErr
	} else {
		statusCh <- container.WaitResponse{StatusCode: f.exitCode}
	}
	return statusCh, errCh
}

func (f *fakeDocker) ContainerLogs(_ context.Context, _ string, _ container.LogsOptions) (io.ReadCloser, error) {
	if f.logsErr != nil {
		return nil, f.logsErr
	}
	var buf bytes.Buffer
	if f.logsStdout != "" {
		w := stdcopy.NewStdWriter(&buf, stdcopy.Stdout)
		_, _ = w.Write([]byte(f.logsStdout))
	}
	if len(f.logsStderr) > 0 {
		w := stdcopy.NewStdWriter(&buf, stdcopy.Stderr)
		for _, l := range f.logsStderr {
			_, _ = w.Write([]byte(l + "\n"))
		}
	}
	return io.NopCloser(&buf), nil
}

func (f *fakeDocker) ContainerStop(_ context.Context, containerID string, _ container.StopOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stoppedIDs = append(f.stoppedIDs, containerID)
	return nil
}

func (f *fakeDocker) ContainerRemove(_ context.Context, containerID string, _ container.RemoveOptions) error {
	f.mu.Lock()
	f.removedIDs = append(f.removedIDs, containerID)
	f.mu.Unlock()
	select {
	case f.removedCh <- containerID:
	default:
	}
	return nil
}

func (f *fakeDocker) ContainerList(_ context.Context, options container.ListOptions) ([]container.Summary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listFilters = append(f.listFilters, options.Filters)
	return f.listResult, nil
}

func (f *fakeDocker) ImagePull(_ context.Context, refStr string, _ image.PullOptions) (io.ReadCloser, error) {
	f.mu.Lock()
	f.pulledRefs = append(f.pulledRefs, refStr)
	f.mu.Unlock()
	return io.NopCloser(strings.NewReader(`{"status":"pulled"}`)), nil
}

func (f *fakeDocker) Ping(_ context.Context) (dockertypes.Ping, error) {
	return dockertypes.Ping{}, f.pingErr
}

// helpers ------------------------------------------------------------------

func newDockerRunner(client dockerAPI, cfg Config) *DockerRunner {
	if cfg.AgentImage == "" {
		cfg.AgentImage = "ghcr.io/decisionbox-io/decisionbox-agent:test"
	}
	return &DockerRunner{client: client, config: cfg}
}

func (f *fakeDocker) lastCreate(t *testing.T) *container.Config {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.createCfgs) == 0 {
		t.Fatal("no container created")
	}
	return f.createCfgs[len(f.createCfgs)-1]
}

func argsContain(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func hasArg(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func envValue(env []string, key string) (string, bool) {
	for _, e := range env {
		if strings.HasPrefix(e, key+"=") {
			return strings.TrimPrefix(e, key+"="), true
		}
	}
	return "", false
}

// --- Run -----------------------------------------------------------------

func TestDockerRunner_Run_BuildsContainerSpec(t *testing.T) {
	f := newFakeDocker()
	f.exitCode = 0
	r := newDockerRunner(f, Config{AgentDockerNetwork: "dbx-net"})

	err := r.Run(context.Background(), RunOptions{
		ProjectID: "proj-1",
		RunID:     "run-123",
		Areas:     []string{"churn", "monetization"},
		MaxSteps:  50,
		MinSteps:  30,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	cfg := f.lastCreate(t)
	if cfg.Image != "ghcr.io/decisionbox-io/decisionbox-agent:test" {
		t.Errorf("image = %q", cfg.Image)
	}
	if cfg.Tty {
		t.Error("Tty must be false so stdout/stderr arrive multiplexed")
	}
	if !argsContain(cfg.Cmd, "--project-id", "proj-1") {
		t.Errorf("cmd missing --project-id: %v", cfg.Cmd)
	}
	if !argsContain(cfg.Cmd, "--run-id", "run-123") {
		t.Errorf("cmd missing --run-id: %v", cfg.Cmd)
	}
	if !argsContain(cfg.Cmd, "--areas", "churn,monetization") {
		t.Errorf("cmd missing --areas: %v", cfg.Cmd)
	}
	if !argsContain(cfg.Cmd, "--max-steps", "50") {
		t.Errorf("cmd missing --max-steps: %v", cfg.Cmd)
	}
	if !argsContain(cfg.Cmd, "--min-steps", "30") {
		t.Errorf("cmd missing --min-steps: %v", cfg.Cmd)
	}
	if cfg.Labels["app"] != dockerAgentLabel {
		t.Errorf("label app = %q", cfg.Labels["app"])
	}
	if cfg.Labels["run-id"] != "run-123" {
		t.Errorf("label run-id = %q", cfg.Labels["run-id"])
	}
	if cfg.Labels["project-id"] != "proj-1" {
		t.Errorf("label project-id = %q", cfg.Labels["project-id"])
	}

	// Network attachment.
	netCfg := f.createNets[len(f.createNets)-1]
	if netCfg == nil || netCfg.EndpointsConfig["dbx-net"] == nil {
		t.Errorf("expected container attached to dbx-net, got %+v", netCfg)
	}

	// Container started.
	if len(f.startedIDs) != 1 {
		t.Errorf("expected 1 started container, got %d", len(f.startedIDs))
	}

	// Background watcher should remove the container on completion.
	waitRemoved(t, f)
}

func TestDockerRunner_Run_OmitsOptionalArgs(t *testing.T) {
	f := newFakeDocker()
	r := newDockerRunner(f, Config{})

	if err := r.Run(context.Background(), RunOptions{ProjectID: "p", RunID: "run-no-opts"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	cfg := f.lastCreate(t)
	if hasArg(cfg.Cmd, "--areas") {
		t.Errorf("no areas → must omit --areas: %v", cfg.Cmd)
	}
	if hasArg(cfg.Cmd, "--max-steps") {
		t.Errorf("MaxSteps 0 → must omit --max-steps: %v", cfg.Cmd)
	}
	if hasArg(cfg.Cmd, "--min-steps") {
		t.Errorf("MinSteps 0 → must omit --min-steps: %v", cfg.Cmd)
	}
	waitRemoved(t, f)
}

func TestDockerRunner_Run_NonZeroExitCallsOnFailure(t *testing.T) {
	f := newFakeDocker()
	f.exitCode = 1
	f.logsStderr = []string{
		`2026-03-13T20:23:21.485Z	INFO	starting`,
		`2026-03-13T20:23:47.479Z	FATAL	Discovery failed	{"error": "authentication_error - invalid x-api-key"}`,
	}
	r := newDockerRunner(f, Config{})

	failMsg := make(chan string, 1)
	err := r.Run(context.Background(), RunOptions{
		ProjectID: "p", RunID: "run-fail",
		OnFailure: func(_ string, msg string) { failMsg <- msg },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	select {
	case msg := <-failMsg:
		if msg != "authentication_error - invalid x-api-key" {
			t.Errorf("OnFailure msg = %q", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnFailure was not called on non-zero exit")
	}
	waitRemoved(t, f)
}

func TestDockerRunner_Run_CleanExitNoOnFailure(t *testing.T) {
	f := newFakeDocker()
	f.exitCode = 0
	r := newDockerRunner(f, Config{})

	called := make(chan struct{}, 1)
	if err := r.Run(context.Background(), RunOptions{
		ProjectID: "p", RunID: "run-ok",
		OnFailure: func(_, _ string) { called <- struct{}{} },
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitRemoved(t, f) // wait for the background watcher to finish
	select {
	case <-called:
		t.Error("OnFailure must not be called on clean exit")
	default:
	}
}

func TestDockerRunner_Run_PullsImageWhenMissing(t *testing.T) {
	f := newFakeDocker()
	// First create returns NotFound (image missing); retry after pull succeeds.
	f.createErrs = []error{errdefs.NotFound(errors.New("No such image: agent:test"))}
	r := newDockerRunner(f, Config{AgentImage: "agent:test"})

	if err := r.Run(context.Background(), RunOptions{ProjectID: "p", RunID: "run-pull"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(f.pulledRefs) != 1 || f.pulledRefs[0] != "agent:test" {
		t.Errorf("expected image pull of agent:test, got %v", f.pulledRefs)
	}
	if f.createCount != 2 {
		t.Errorf("expected create retried after pull (2 calls), got %d", f.createCount)
	}
	waitRemoved(t, f)
}

func TestDockerRunner_Run_CreateErrorSurfaced(t *testing.T) {
	f := newFakeDocker()
	f.createErrs = []error{errors.New("boom")}
	r := newDockerRunner(f, Config{})

	err := r.Run(context.Background(), RunOptions{ProjectID: "p", RunID: "run-create-err"})
	if err == nil {
		t.Fatal("expected error from Run when create fails")
	}
	if !strings.Contains(err.Error(), "create agent container") {
		t.Errorf("error = %q, want it to mention create", err.Error())
	}
}

func TestDockerRunner_Run_StartErrorCleansUp(t *testing.T) {
	f := newFakeDocker()
	f.startErr = errors.New("no start")
	r := newDockerRunner(f, Config{})

	err := r.Run(context.Background(), RunOptions{ProjectID: "p", RunID: "run-start-err"})
	if err == nil {
		t.Fatal("expected error from Run when start fails")
	}
	if !strings.Contains(err.Error(), "start agent container") {
		t.Errorf("error = %q, want it to mention start", err.Error())
	}
	// The created-but-unstarted container must be removed.
	f.mu.Lock()
	removed := len(f.removedIDs)
	f.mu.Unlock()
	if removed != 1 {
		t.Errorf("expected created container removed after start failure, removed=%d", removed)
	}
}

// --- RunSync -------------------------------------------------------------

func TestDockerRunner_RunSync_CapturesStdout(t *testing.T) {
	f := newFakeDocker()
	f.exitCode = 0
	f.logsStdout = "connection ok\n"
	r := newDockerRunner(f, Config{})

	res, err := r.RunSync(context.Background(), RunSyncOptions{
		ProjectID: "proj-x",
		Args:      []string{"--test-connection", "warehouse"},
	})
	if err != nil {
		t.Fatalf("RunSync: %v", err)
	}
	if string(res.Output) != "connection ok\n" {
		t.Errorf("Output = %q, want stdout passthrough", string(res.Output))
	}

	cfg := f.lastCreate(t)
	if !argsContain(cfg.Cmd, "--project-id", "proj-x") {
		t.Errorf("cmd missing --project-id: %v", cfg.Cmd)
	}
	if !argsContain(cfg.Cmd, "--test-connection", "warehouse") {
		t.Errorf("cmd missing --test-connection warehouse: %v", cfg.Cmd)
	}
	if cfg.Labels["type"] != "test-connection" {
		t.Errorf("label type = %q, want test-connection", cfg.Labels["type"])
	}
	f.mu.Lock()
	removed := len(f.removedIDs)
	f.mu.Unlock()
	if removed != 1 {
		t.Errorf("RunSync must remove the container, removed=%d", removed)
	}
}

func TestDockerRunner_RunSync_NonZeroExitReturnsError(t *testing.T) {
	f := newFakeDocker()
	f.exitCode = 1
	f.logsStderr = []string{`{"level":"fatal","error":"llm: invalid key"}`}
	r := newDockerRunner(f, Config{})

	res, err := r.RunSync(context.Background(), RunSyncOptions{ProjectID: "p", Args: []string{"--test-connection", "llm"}})
	if err == nil {
		t.Fatal("expected error on non-zero exit")
	}
	if res == nil || res.Error == "" {
		t.Fatalf("expected RunSyncResult with Error set, got %+v", res)
	}
	if res.Error != "llm: invalid key" {
		t.Errorf("Error = %q, want extracted message", res.Error)
	}
}

func TestDockerRunner_RunSync_ContextCancelGracefulStop(t *testing.T) {
	f := newFakeDocker()
	f.hangWait = true
	r := newDockerRunner(f, Config{})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := r.RunSync(ctx, RunSyncOptions{ProjectID: "p", Args: []string{"--test-connection", "warehouse"}})
	if err == nil {
		t.Fatal("expected error when ctx is cancelled")
	}
	assertStoppedAndRemoved(t, f)
}

// --- RunIndexSchema ------------------------------------------------------

func TestDockerRunner_RunIndexSchema_StreamsLogLines(t *testing.T) {
	f := newFakeDocker()
	f.exitCode = 0
	f.logsStderr = []string{"indexing table a", "indexing table b", "done"}
	r := newDockerRunner(f, Config{})

	var mu sync.Mutex
	var lines []string
	err := r.RunIndexSchema(context.Background(), IndexSchemaOptions{
		ProjectID: "proj-idx",
		RunID:     "idx-run-1",
		OnLogLine: func(l string) {
			mu.Lock()
			lines = append(lines, l)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("RunIndexSchema: %v", err)
	}

	cfg := f.lastCreate(t)
	if !argsContain(cfg.Cmd, "--mode", "index-schema") {
		t.Errorf("cmd missing --mode index-schema: %v", cfg.Cmd)
	}
	if !argsContain(cfg.Cmd, "--project-id", "proj-idx") {
		t.Errorf("cmd missing --project-id: %v", cfg.Cmd)
	}
	if !argsContain(cfg.Cmd, "--run-id", "idx-run-1") {
		t.Errorf("cmd missing --run-id: %v", cfg.Cmd)
	}
	mu.Lock()
	got := strings.Join(lines, "|")
	mu.Unlock()
	if got != "indexing table a|indexing table b|done" {
		t.Errorf("OnLogLine received %q, want all 3 stderr lines", got)
	}
}

func TestDockerRunner_RunIndexSchema_NonZeroExitErrors(t *testing.T) {
	f := newFakeDocker()
	f.exitCode = 2
	f.logsStderr = []string{`{"level":"fatal","error":"index failed"}`}
	r := newDockerRunner(f, Config{})

	err := r.RunIndexSchema(context.Background(), IndexSchemaOptions{ProjectID: "p"})
	if err == nil {
		t.Fatal("expected error on non-zero exit")
	}
	if !strings.Contains(err.Error(), "index failed") {
		t.Errorf("error = %q, want extracted message", err.Error())
	}
}

func TestDockerRunner_RunIndexSchema_OmitsRunIDWhenEmpty(t *testing.T) {
	f := newFakeDocker()
	f.exitCode = 0
	r := newDockerRunner(f, Config{})

	if err := r.RunIndexSchema(context.Background(), IndexSchemaOptions{ProjectID: "p"}); err != nil {
		t.Fatalf("RunIndexSchema: %v", err)
	}
	cfg := f.lastCreate(t)
	if hasArg(cfg.Cmd, "--run-id") {
		t.Errorf("empty RunID → must omit --run-id: %v", cfg.Cmd)
	}
}

func TestDockerRunner_RunIndexSchema_ContextCancelGracefulStop(t *testing.T) {
	f := newFakeDocker()
	f.hangWait = true
	r := newDockerRunner(f, Config{})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	err := r.RunIndexSchema(ctx, IndexSchemaOptions{ProjectID: "p", RunID: "x"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	assertStoppedAndRemoved(t, f)
}

// --- RunValidateDoc ------------------------------------------------------

func TestDockerRunner_RunValidateDoc_RejectsEmptyJobID(t *testing.T) {
	f := newFakeDocker()
	r := newDockerRunner(f, Config{})
	if err := r.RunValidateDoc(context.Background(), ValidateDocOptions{}); err == nil {
		t.Error("RunValidateDoc with empty JobID should error")
	}
}

func TestDockerRunner_RunValidateDoc_BuildsSpec(t *testing.T) {
	f := newFakeDocker()
	f.exitCode = 0
	r := newDockerRunner(f, Config{})

	err := r.RunValidateDoc(context.Background(), ValidateDocOptions{
		JobID:       "job-abc",
		ProjectID:   "proj-1",
		DiscoveryID: "disc-1",
		DocKind:     "insight",
		DocID:       "ins-1",
	})
	if err != nil {
		t.Fatalf("RunValidateDoc: %v", err)
	}
	cfg := f.lastCreate(t)
	if !argsContain(cfg.Cmd, "--mode", "validate-doc") {
		t.Errorf("cmd missing --mode validate-doc: %v", cfg.Cmd)
	}
	if !argsContain(cfg.Cmd, "--job-id", "job-abc") {
		t.Errorf("cmd missing --job-id: %v", cfg.Cmd)
	}
	if cfg.Labels["type"] != "validate-doc" || cfg.Labels["job-id"] != "job-abc" {
		t.Errorf("unexpected labels: %+v", cfg.Labels)
	}
}

func TestDockerRunner_RunValidateDoc_ContextCancelGracefulStop(t *testing.T) {
	f := newFakeDocker()
	f.hangWait = true
	r := newDockerRunner(f, Config{})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	err := r.RunValidateDoc(ctx, ValidateDocOptions{JobID: "job-cancel"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	assertStoppedAndRemoved(t, f)
}

// --- Cancel --------------------------------------------------------------

func TestDockerRunner_Cancel_StopsMatchingContainer(t *testing.T) {
	f := newFakeDocker()
	f.listResult = []container.Summary{{ID: "cid-running"}}
	r := newDockerRunner(f, Config{})

	if err := r.Cancel(context.Background(), "run-cancel"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if len(f.stoppedIDs) != 1 || f.stoppedIDs[0] != "cid-running" {
		t.Errorf("expected cid-running stopped, got %v", f.stoppedIDs)
	}
	// Cancel must filter by both the app label and the run-id label.
	if len(f.listFilters) != 1 {
		t.Fatalf("expected 1 list call, got %d", len(f.listFilters))
	}
	labels := f.listFilters[0].Get("label")
	if !contains(labels, "run-id=run-cancel") || !contains(labels, "app="+dockerAgentLabel) {
		t.Errorf("list filter labels = %v, want run-id + app", labels)
	}
}

func TestDockerRunner_Cancel_NoContainerIsNoop(t *testing.T) {
	f := newFakeDocker()
	f.listResult = nil // nothing running
	r := newDockerRunner(f, Config{})

	if err := r.Cancel(context.Background(), "run-gone"); err != nil {
		t.Errorf("Cancel of a finished run should be a no-op, got %v", err)
	}
	if len(f.stoppedIDs) != 0 {
		t.Errorf("expected no stops, got %v", f.stoppedIDs)
	}
}

func TestDockerRunner_Cancel_ListErrorSurfaced(t *testing.T) {
	f := newFakeDockerListErr()
	r := newDockerRunner(f, Config{})
	if err := r.Cancel(context.Background(), "run-x"); err == nil {
		t.Error("expected error when ContainerList fails")
	}
}

// --- env / config builders ----------------------------------------------

func TestDockerRunner_BuildEnv_ForwardsSetVarsOnly(t *testing.T) {
	t.Setenv("MONGODB_URI", "mongodb://mongodb:27017")
	t.Setenv("MONGODB_DB", "decisionbox")
	t.Setenv("SECRET_PROVIDER", "mongodb")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIA-test")
	t.Setenv("ENV", "prod")
	// Ensure an unset forwarded var stays absent.
	t.Setenv("QDRANT_URL", "")

	r := newDockerRunner(newFakeDocker(), Config{})
	env := r.buildEnv()

	if v, ok := envValue(env, "MONGODB_URI"); !ok || v != "mongodb://mongodb:27017" {
		t.Errorf("MONGODB_URI = %q, ok=%v", v, ok)
	}
	if v, ok := envValue(env, "SECRET_PROVIDER"); !ok || v != "mongodb" {
		t.Errorf("SECRET_PROVIDER = %q, ok=%v", v, ok)
	}
	if v, ok := envValue(env, "AWS_ACCESS_KEY_ID"); !ok || v != "AKIA-test" {
		t.Errorf("AWS_ACCESS_KEY_ID = %q, ok=%v", v, ok)
	}
	if v, ok := envValue(env, "ENV"); !ok || v != "prod" {
		t.Errorf("ENV = %q, ok=%v", v, ok)
	}
	// Writable scratch dirs always present.
	if v, _ := envValue(env, "HOME"); v != "/tmp" {
		t.Errorf("HOME = %q, want /tmp", v)
	}
	// Unset var must be absent.
	if _, ok := envValue(env, "QDRANT_URL"); ok {
		t.Error("unset QDRANT_URL must not be forwarded")
	}
}

func TestDockerRunner_BuildContainerConfig_ImageOverrideAndLabelFilter(t *testing.T) {
	r := newDockerRunner(newFakeDocker(), Config{AgentImage: "default:img"})

	cfg := r.buildContainerConfig(containerSpec{
		image:  "override:img",
		cmd:    []string{"--mode", "x"},
		labels: map[string]string{"run-id": "r1", "discovery-id": ""},
	})
	if cfg.Image != "override:img" {
		t.Errorf("image = %q, want override:img", cfg.Image)
	}
	if cfg.Labels["app"] != dockerAgentLabel {
		t.Errorf("missing app label: %+v", cfg.Labels)
	}
	if cfg.Labels["run-id"] != "r1" {
		t.Errorf("run-id label = %q", cfg.Labels["run-id"])
	}
	if _, ok := cfg.Labels["discovery-id"]; ok {
		t.Error("empty label values must be dropped")
	}
}

func TestDockerRunner_BuildNetworkingConfig(t *testing.T) {
	r := newDockerRunner(newFakeDocker(), Config{})
	if cfg := r.buildNetworkingConfig(); cfg != nil {
		t.Errorf("empty network → nil networking config, got %+v", cfg)
	}

	r2 := newDockerRunner(newFakeDocker(), Config{AgentDockerNetwork: "dbx"})
	cfg := r2.buildNetworkingConfig()
	if cfg == nil || cfg.EndpointsConfig["dbx"] == nil {
		t.Errorf("expected endpoint config for dbx, got %+v", cfg)
	}
}

// helpers for assertions --------------------------------------------------

func waitRemoved(t *testing.T, f *fakeDocker) {
	t.Helper()
	select {
	case <-f.removedCh:
	case <-time.After(5 * time.Second):
		t.Fatal("container was not removed within 5s (background watcher stuck)")
	}
}

func assertStoppedAndRemoved(t *testing.T, f *fakeDocker) {
	t.Helper()
	// Allow the cancel path's detached removal to land.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		stopped, removed := len(f.stoppedIDs), len(f.removedIDs)
		f.mu.Unlock()
		if stopped >= 1 && removed >= 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t.Fatalf("expected container stopped and removed on cancel, stopped=%d removed=%d", len(f.stoppedIDs), len(f.removedIDs))
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// fakeDockerListErr returns an error from ContainerList.
type fakeDockerListErr struct{ *fakeDocker }

func newFakeDockerListErr() *fakeDockerListErr { return &fakeDockerListErr{newFakeDocker()} }

func (f *fakeDockerListErr) ContainerList(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
	return nil, errors.New("daemon unreachable")
}
