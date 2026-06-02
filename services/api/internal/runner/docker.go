package runner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// DockerRunner spawns each agent invocation as its own short-lived
// container from AGENT_IMAGE via the Docker engine. It gives
// docker-compose / single-host deployments the same "API spawns the agent
// from a configurable image" model the Kubernetes runner provides in
// production, without requiring a cluster.
//
// SECURITY: this runner talks to the Docker engine over DOCKER_HOST / the
// mounted Docker socket. Mounting the socket grants the API root-equivalent
// access to the host — it is a local / single-host convenience. Production
// should use the kubernetes runner.
type DockerRunner struct {
	client dockerAPI
	config Config
}

// dockerAPI is the subset of the Docker SDK client the runner uses. It is
// an interface so unit tests can drive the full Run / RunSync / Cancel
// logic against a fake engine; the real *client.Client satisfies it.
type dockerAPI interface {
	ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error)
	ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error
	ContainerWait(ctx context.Context, containerID string, condition container.WaitCondition) (<-chan container.WaitResponse, <-chan error)
	ContainerLogs(ctx context.Context, containerID string, options container.LogsOptions) (io.ReadCloser, error)
	ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
	ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
	ImagePull(ctx context.Context, refStr string, options image.PullOptions) (io.ReadCloser, error)
	Ping(ctx context.Context) (dockertypes.Ping, error)
}

// dockerPingTimeout bounds the startup reachability probe so a missing /
// wedged Docker socket fails fast with a clear error instead of hanging
// the API boot.
const dockerPingTimeout = 10 * time.Second

// dockerStopGraceSeconds is how long ContainerStop waits after SIGTERM
// before the daemon escalates to SIGKILL. Sized to the agent's
// terminal-tail budget (persist + final status write) so a cancelled
// agent can save partial results before being forcibly killed — the
// Docker analogue of the K8s pod termination grace period. ContainerStop
// returns as soon as the agent exits, so this is only an upper bound. A
// var (not const) so tests can shorten it.
var dockerStopGraceSeconds = int(agentTerminalTailHeadroom.Seconds())

// dockerAgentLabel marks every container this runner creates, so Cancel
// and operators can discover agent containers (`docker ps --filter
// label=app=decisionbox-agent`).
const dockerAgentLabel = "decisionbox-agent"

// NewDockerRunner builds a DockerRunner from the configured DOCKER_HOST /
// socket and verifies the engine is reachable up front.
func NewDockerRunner(cfg Config) (*DockerRunner, error) {
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker runner: failed to create Docker client: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(context.Background(), dockerPingTimeout)
	defer cancel()
	if _, err := cli.Ping(pingCtx); err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("docker runner: cannot reach the Docker engine (is the socket mounted at /var/run/docker.sock or DOCKER_HOST set?): %w", err)
	}

	apilog.WithFields(apilog.Fields{
		"image":   cfg.AgentImage,
		"network": cfg.AgentDockerNetwork,
	}).Info("Runner mode: docker")

	return &DockerRunner{client: cli, config: cfg}, nil
}

// containerSpec is the variable per-invocation part of a container: the
// agent CLI args (the container command) and discovery labels. The image,
// env, and network come from the runner config and are shared across
// invocations.
type containerSpec struct {
	// image overrides r.config.AgentImage when non-empty. Production code
	// always leaves it empty; the integration tests set it to run a
	// throwaway image.
	image  string
	cmd    []string
	labels map[string]string
}

// buildEnv assembles the environment for an agent container: the base
// Mongo/scratch vars plus the curated passthrough set shared with the
// Kubernetes runner and the Docker-only cloud-credential / logging extras.
// See env.go.
func (r *DockerRunner) buildEnv() []string {
	var env []string
	for _, kv := range agentBaseEnv() {
		env = append(env, kv.Key+"="+kv.Value)
	}
	for _, kv := range collectForwardedEnv(agentForwardedEnvKeys, dockerAgentExtraEnvKeys) {
		env = append(env, kv.Key+"="+kv.Value)
	}
	return env
}

// buildContainerConfig builds the container.Config for one invocation.
func (r *DockerRunner) buildContainerConfig(spec containerSpec) *container.Config {
	img := spec.image
	if img == "" {
		img = r.config.AgentImage
	}

	labels := map[string]string{"app": dockerAgentLabel}
	for k, v := range spec.labels {
		// Skip empty values so optional labels (e.g. run-id on a
		// validate-doc run) don't clutter the spec.
		if v != "" {
			labels[k] = v
		}
	}

	return &container.Config{
		Image:  img,
		Cmd:    spec.cmd,
		Env:    r.buildEnv(),
		Labels: labels,
		// TTY off so stdout and stderr arrive multiplexed and stdcopy can
		// demultiplex them (the agent logs to stderr; test-connection
		// results go to stdout).
		Tty: false,
	}
}

// buildNetworkingConfig attaches the agent container to AGENT_DOCKER_NETWORK
// when configured, so it can resolve `mongodb`, `qdrant`, and warehouse
// hosts by service name on the compose network. Returns nil (default
// network) when unset.
func (r *DockerRunner) buildNetworkingConfig() *network.NetworkingConfig {
	if r.config.AgentDockerNetwork == "" {
		return nil
	}
	return &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			r.config.AgentDockerNetwork: {},
		},
	}
}

// createAndStart creates and starts a container from the spec. If the
// image is not present locally it is pulled once and creation retried, so
// both registry images and locally-built ones work. Returns the container
// ID.
func (r *DockerRunner) createAndStart(ctx context.Context, spec containerSpec) (string, error) {
	cfg := r.buildContainerConfig(spec)
	netCfg := r.buildNetworkingConfig()

	resp, err := r.client.ContainerCreate(ctx, cfg, nil, netCfg, nil, "")
	if err != nil && cerrdefs.IsNotFound(err) {
		// Image missing locally — try to pull it, then retry create.
		if perr := r.pullImage(ctx, cfg.Image); perr != nil {
			return "", fmt.Errorf("agent image %q is not present locally and could not be pulled: %w", cfg.Image, perr)
		}
		resp, err = r.client.ContainerCreate(ctx, cfg, nil, netCfg, nil, "")
	}
	if err != nil {
		return "", fmt.Errorf("create agent container: %w", err)
	}

	if err := r.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		// Best-effort cleanup of the created-but-unstarted container.
		_ = r.removeContainer(context.Background(), resp.ID)
		return "", fmt.Errorf("start agent container: %w", err)
	}
	return resp.ID, nil
}

// pullImage pulls ref and drains the progress stream (the pull only
// completes once the response body is fully read).
func (r *DockerRunner) pullImage(ctx context.Context, ref string) error {
	rc, err := r.client.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("drain image pull stream: %w", err)
	}
	return nil
}

func (r *DockerRunner) removeContainer(ctx context.Context, id string) error {
	return r.client.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
}

// gracefulStop sends SIGTERM and waits up to dockerStopGraceSeconds before
// the daemon escalates to SIGKILL, on a detached ctx so a finished HTTP
// request can't cut the grace period short. A missing container is treated
// as already stopped.
func (r *DockerRunner) gracefulStop(id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(dockerStopGraceSeconds+5)*time.Second)
	defer cancel()
	timeout := dockerStopGraceSeconds
	err := r.client.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return err
	}
	return nil
}

// logHandlers configures how streamWaitRemove routes a container's output.
type logHandlers struct {
	// stdout, when non-nil, receives the container's demultiplexed stdout
	// (used by RunSync to capture the agent's test-connection result).
	stdout io.Writer
	// onStderrLine, when non-nil, is called with each complete stderr line
	// (used to fan the live tail to RunIndexSchema's OnLogLine callback).
	onStderrLine func(line string)
	// logPrefix prefixes each stderr line forwarded to the API's stderr.
	logPrefix string
}

// streamWaitRemove streams the container's logs, waits for it to exit (or
// for ctx to be cancelled), removes the container, and reports the
// outcome:
//
//   - ctx cancelled → SIGTERM the container (so the agent runs its
//     terminal tail), remove it, return ctx.Err().
//   - non-zero exit → return an error carrying the agent's last
//     FATAL/ERROR message (extracted from stderr).
//   - clean exit    → return (0, nil).
func (r *DockerRunner) streamWaitRemove(ctx context.Context, id string, h logHandlers) (int64, error) {
	statusCh, errCh := r.client.ContainerWait(ctx, id, container.WaitConditionNotRunning)

	tail := newTailBuffer()
	logsDone := make(chan struct{})
	logCtx, logCancel := context.WithCancel(ctx)
	defer logCancel()
	go func() {
		defer close(logsDone)
		r.streamLogs(logCtx, id, h, tail)
	}()

	cancelAndRemove := func() {
		_ = r.gracefulStop(id)
		logCancel()
		<-logsDone
		_ = r.removeContainer(context.Background(), id)
	}

	select {
	case <-ctx.Done():
		cancelAndRemove()
		return 0, ctx.Err()
	case werr := <-errCh:
		if ctx.Err() != nil {
			cancelAndRemove()
			return 0, ctx.Err()
		}
		logCancel()
		<-logsDone
		_ = r.removeContainer(context.Background(), id)
		return 0, fmt.Errorf("wait for agent container: %w", werr)
	case status := <-statusCh:
		<-logsDone // let the log stream flush to EOF (container stopped)
		_ = r.removeContainer(context.Background(), id)
		if status.StatusCode != 0 {
			msg := extractErrorMessage(tail.String(), fmt.Errorf("exit status %d", status.StatusCode))
			return status.StatusCode, fmt.Errorf("%s", msg)
		}
		return 0, nil
	}
}

// streamLogs follows the container logs, demultiplexes stdout/stderr, and
// routes them per the handlers. It returns when the stream ends (container
// stopped) or ctx is cancelled.
func (r *DockerRunner) streamLogs(ctx context.Context, id string, h logHandlers, tail *tailBuffer) {
	reader, err := r.client.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
	})
	if err != nil {
		if ctx.Err() == nil {
			apilog.WithFields(apilog.Fields{
				"container": id, "error": err.Error(),
			}).Warn("Failed to stream agent container logs")
		}
		return
	}
	defer func() { _ = reader.Close() }()

	stderrW := &lineWriter{onLine: func(line []byte) {
		_, _ = io.WriteString(os.Stderr, h.logPrefix)
		_, _ = os.Stderr.Write(line)
		_, _ = os.Stderr.Write([]byte{'\n'})
		tail.append(line)
		if h.onStderrLine != nil {
			// Copy out of the writer's buffer — line aliases it.
			h.onStderrLine(string(line))
		}
	}}
	stdoutW := h.stdout
	if stdoutW == nil {
		stdoutW = io.Discard
	}

	// StdCopy reads the multiplexed stream serially, so the two writers are
	// never invoked concurrently — the shared tail buffer needs no lock.
	if _, err := stdcopy.StdCopy(stdoutW, stderrW, reader); err != nil && ctx.Err() == nil {
		apilog.WithFields(apilog.Fields{
			"container": id, "error": err.Error(),
		}).Debug("Agent container log stream ended with error")
	}
	stderrW.flush()
}

// Run spawns a discovery agent container and watches it in the background.
func (r *DockerRunner) Run(ctx context.Context, opts RunOptions) error {
	args := []string{
		"--project-id", opts.ProjectID,
		"--run-id", opts.RunID,
	}
	if len(opts.Areas) > 0 {
		args = append(args, "--areas", strings.Join(opts.Areas, ","))
	}
	if opts.MaxSteps > 0 {
		args = append(args, "--max-steps", strconv.Itoa(opts.MaxSteps))
	}
	// MinSteps forwards as-is: zero means "no floor, disabled". Mirrors the
	// subprocess / Kubernetes runners.
	if opts.MinSteps > 0 {
		args = append(args, "--min-steps", strconv.Itoa(opts.MinSteps))
	}

	id, err := r.createAndStart(ctx, containerSpec{
		cmd: args,
		labels: map[string]string{
			"run-id":     opts.RunID,
			"project-id": opts.ProjectID,
		},
	})
	if err != nil {
		apilog.WithFields(apilog.Fields{
			"run_id": opts.RunID, "error": err.Error(),
		}).Error("Failed to start agent container")
		return err
	}

	apilog.WithFields(apilog.Fields{
		"run_id":     opts.RunID,
		"project_id": opts.ProjectID,
		"container":  id,
		"image":      r.config.AgentImage,
		"areas":      opts.Areas,
		"max_steps":  opts.MaxSteps,
	}).Info("Agent container started")

	// Watch the run to completion in the background. watchRun owns its own
	// context.Background() so the run outlives the HTTP request (same
	// rationale as the K8s watcher).
	go r.watchRun(id, opts) //nolint:gosec // intentional: the run outlives the request context (same as the K8s watcher)

	return nil
}

// watchRun streams + waits on a backgrounded discovery container and routes
// a non-zero exit to OnFailure. It runs detached from any request context
// because the run outlives the HTTP request that started it.
func (r *DockerRunner) watchRun(id string, opts RunOptions) {
	_, werr := r.streamWaitRemove(context.Background(), id, logHandlers{
		logPrefix: fmt.Sprintf("[agent %s] ", opts.RunID),
	})
	if werr != nil {
		errMsg := werr.Error()
		apilog.WithFields(apilog.Fields{
			"run_id": opts.RunID, "error": errMsg,
		}).Warn("Agent container exited with error")
		if opts.OnFailure != nil {
			opts.OnFailure(opts.RunID, errMsg)
		}
		return
	}
	apilog.WithField("run_id", opts.RunID).Info("Agent container completed")
}

// RunSync runs a synchronous agent invocation (e.g. --test-connection) and
// returns its stdout.
func (r *DockerRunner) RunSync(ctx context.Context, opts RunSyncOptions) (*RunSyncResult, error) {
	args := append([]string{"--project-id", opts.ProjectID}, opts.Args...)

	id, err := r.createAndStart(ctx, containerSpec{
		cmd: args,
		labels: map[string]string{
			"type":       "test-connection",
			"project-id": opts.ProjectID,
		},
	})
	if err != nil {
		return nil, err
	}

	var stdout bytes.Buffer
	_, werr := r.streamWaitRemove(ctx, id, logHandlers{
		stdout:    &stdout,
		logPrefix: fmt.Sprintf("[test %s] ", opts.ProjectID),
	})
	if werr != nil {
		return &RunSyncResult{Output: stdout.Bytes(), Error: werr.Error()}, werr
	}
	return &RunSyncResult{Output: stdout.Bytes()}, nil
}

// RunIndexSchema runs the agent in --mode index-schema and blocks until it
// exits, fanning stderr lines to opts.OnLogLine for the live tail.
func (r *DockerRunner) RunIndexSchema(ctx context.Context, opts IndexSchemaOptions) error {
	args := []string{
		"--mode", "index-schema",
		"--project-id", opts.ProjectID,
	}
	if opts.RunID != "" {
		args = append(args, "--run-id", opts.RunID)
	}

	id, err := r.createAndStart(ctx, containerSpec{
		cmd: args,
		labels: map[string]string{
			"type":       "index-schema",
			"project-id": opts.ProjectID,
			"run-id":     opts.RunID,
		},
	})
	if err != nil {
		return fmt.Errorf("agent --mode index-schema: %w", err)
	}

	apilog.WithFields(apilog.Fields{
		"project_id": opts.ProjectID, "run_id": opts.RunID, "container": id,
	}).Info("Agent index-schema container starting")

	_, werr := r.streamWaitRemove(ctx, id, logHandlers{
		onStderrLine: opts.OnLogLine,
		logPrefix:    fmt.Sprintf("[agent %s] ", opts.RunID),
	})
	if werr == nil {
		apilog.WithFields(apilog.Fields{
			"project_id": opts.ProjectID, "run_id": opts.RunID,
		}).Info("Agent index-schema container completed")
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("agent --mode index-schema: %w", werr)
}

// RunValidateDoc runs the agent in --mode=validate-doc for one insight /
// recommendation and blocks until it exits or ctx is cancelled. On
// cancellation the container is SIGTERM'd and removed so the agent records
// its terminal status before exit (mirrors the K8s foreground-delete).
func (r *DockerRunner) RunValidateDoc(ctx context.Context, opts ValidateDocOptions) error {
	if opts.JobID == "" {
		return fmt.Errorf("validate-doc: job_id is required")
	}
	args := []string{"--mode", "validate-doc", "--job-id", opts.JobID}

	id, err := r.createAndStart(ctx, containerSpec{
		cmd: args,
		labels: map[string]string{
			"type":         "validate-doc",
			"job-id":       opts.JobID,
			"project-id":   opts.ProjectID,
			"discovery-id": opts.DiscoveryID,
			"doc-kind":     opts.DocKind,
		},
	})
	if err != nil {
		return fmt.Errorf("agent --mode validate-doc: %w", err)
	}

	apilog.WithFields(apilog.Fields{
		"job_id": opts.JobID, "project_id": opts.ProjectID, "container": id,
	}).Info("Agent validate-doc container starting")

	_, werr := r.streamWaitRemove(ctx, id, logHandlers{
		logPrefix: fmt.Sprintf("[validate-doc %s] ", opts.JobID),
	})
	if werr == nil {
		apilog.WithField("job_id", opts.JobID).Info("Agent validate-doc container completed")
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("agent --mode validate-doc: %w", werr)
}

// Cancel gracefully stops the discovery container(s) for runID (SIGTERM →
// grace → SIGKILL). The background streamWaitRemove watcher observes the
// exit and removes the container, so the agent gets its terminal tail
// first. A run with no live container is a no-op (already finished).
func (r *DockerRunner) Cancel(ctx context.Context, runID string) error {
	f := filters.NewArgs()
	f.Add("label", "app="+dockerAgentLabel)
	f.Add("label", "run-id="+runID)

	list, err := r.client.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return fmt.Errorf("list agent containers for cancel: %w", err)
	}
	if len(list) == 0 {
		return nil // not running (already finished or never started)
	}

	var firstErr error
	for _, c := range list {
		apilog.WithFields(apilog.Fields{
			"run_id": runID, "container": c.ID,
		}).Info("Stopping agent container (discovery cancelled)")
		if stopErr := r.gracefulStop(c.ID); stopErr != nil {
			apilog.WithFields(apilog.Fields{
				"run_id": runID, "container": c.ID, "error": stopErr.Error(),
			}).Warn("Failed to stop agent container on cancel")
			if firstErr == nil {
				firstErr = fmt.Errorf("stop agent container: %w", stopErr)
			}
		}
	}
	return firstErr
}

// tailBuffer keeps a rolling, size-capped tail of stderr so a non-zero
// exit can still surface the agent's last error after the live stream is
// gone. Ring-style: when an append would exceed the cap, the buffer is
// reset first (drops old context, keeps the most recent lines).
type tailBuffer struct {
	buf bytes.Buffer
	cap int
}

func newTailBuffer() *tailBuffer {
	t := &tailBuffer{cap: 64 * 1024}
	t.buf.Grow(t.cap)
	return t
}

func (t *tailBuffer) append(line []byte) {
	if t.buf.Len()+len(line)+1 > t.cap {
		t.buf.Reset()
	}
	t.buf.Write(line)
	t.buf.WriteByte('\n')
}

func (t *tailBuffer) String() string { return t.buf.String() }

// lineWriter is an io.Writer that splits its input on '\n' and invokes
// onLine for each complete line. The byte slice passed to onLine aliases
// the writer's internal buffer and is only valid for the duration of the
// call — copy it if you need to retain it.
type lineWriter struct {
	buf    []byte
	onLine func(line []byte)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.onLine(w.buf[:i])
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

// flush emits any trailing bytes not terminated by a newline (e.g. the
// agent's final line when the container exits without a trailing '\n').
func (w *lineWriter) flush() {
	if len(w.buf) > 0 {
		w.onLine(w.buf)
		w.buf = nil
	}
}
