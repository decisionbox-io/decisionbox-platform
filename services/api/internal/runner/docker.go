package runner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
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

	// cancelled records run IDs the operator explicitly cancelled, so the
	// background watchRun reports the resulting container exit as a
	// cancellation (silent) rather than a failure — mirroring the K8s
	// watcher staying quiet after a Job delete. Without it, Cancel's
	// SIGTERM/remove would surface through OnFailure and trigger the
	// handler's failure side-effects (e.g. policy "failure" confirmation)
	// for a user-cancelled run.
	mu        sync.Mutex
	cancelled map[string]struct{}
}

// markCancelled records that runID was explicitly cancelled (lazily
// initialising the set so a hand-built DockerRunner is safe).
func (r *DockerRunner) markCancelled(runID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancelled == nil {
		r.cancelled = make(map[string]struct{})
	}
	r.cancelled[runID] = struct{}{}
}

// consumeCancelled reports whether runID was explicitly cancelled, clearing
// the record so it does not leak.
func (r *DockerRunner) consumeCancelled(runID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.cancelled[runID]; ok {
		delete(r.cancelled, runID)
		return true
	}
	return false
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
// before the daemon escalates to SIGKILL — the Docker analogue of the K8s
// pod termination grace period. Sized to the agent's terminal-tail budget
// (persist + final status write) so that, once the agent honours SIGTERM
// (tracked in #270), a cancelled run has room to save partial results
// before being forcibly killed. ContainerStop returns as soon as the
// agent exits, so this is only an upper bound. A var (not const) so tests
// can shorten it.
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

	r := &DockerRunner{client: cli, config: cfg, cancelled: make(map[string]struct{})}
	// Reap agent containers orphaned by a prior API lifecycle (crash /
	// restart) before serving any run — Docker has no daemon-side Job TTL
	// like K8s, so otherwise they linger.
	//
	// Guarded by a process-level Once: apiserver.Run constructs a
	// DockerRunner twice (one for the in-process workers, one for the
	// discovery routes), and reaping on the second construction could
	// remove a container the first runner's worker has already launched.
	// The first construction happens before any runner can launch a
	// container, so a single sweep there is both sufficient and safe.
	orphanReconcileOnce.Do(func() {
		reapCtx, reapCancel := context.WithTimeout(context.Background(), dockerPingTimeout)
		defer reapCancel()
		r.reconcileOrphans(reapCtx)
	})

	return r, nil
}

// orphanReconcileOnce ensures the startup orphan sweep runs at most once
// per process, regardless of how many DockerRunner instances are built.
var orphanReconcileOnce sync.Once

// reconcileOrphans force-removes agent containers left over from a previous
// API lifecycle. Docker, unlike K8s, has no per-container deadline / TTL, so
// an API crash or restart would otherwise leave a sibling container running
// past its budget (or stopped but unremoved) and unreachable via the cancel
// endpoint once startup marks its run failed. Safe under docker mode's
// single-host / single-API assumption: every app=decisionbox-agent
// container belongs to this API. Best-effort — a sweep failure must not
// block startup.
func (r *DockerRunner) reconcileOrphans(ctx context.Context) {
	f := filters.NewArgs()
	f.Add("label", "app="+dockerAgentLabel)
	list, err := r.client.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		apilog.WithError(err).Warn("docker runner: failed to list orphaned agent containers on startup")
		return
	}
	var reaped int
	for _, c := range list {
		if rmErr := r.removeContainer(c.ID); !removalRaceBenign(rmErr) {
			apilog.WithFields(apilog.Fields{
				"container": c.ID, "error": rmErr.Error(),
			}).Warn("docker runner: failed to remove orphaned agent container on startup")
			continue
		}
		reaped++
	}
	if reaped > 0 {
		apilog.WithField("count", reaped).Info("docker runner: reaped orphaned agent containers from a prior lifecycle")
	}
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
	if isImageNotFound(err, cfg.Image) {
		// The image (not some other resource) is missing locally — pull it
		// once, then retry create.
		if perr := r.pullImage(ctx, cfg.Image); perr != nil {
			return "", fmt.Errorf("agent image %q is not present locally and could not be pulled: %w", cfg.Image, perr)
		}
		resp, err = r.client.ContainerCreate(ctx, cfg, nil, netCfg, nil, "")
	}
	if err != nil {
		// A NotFound here that is NOT the image is most commonly a missing
		// AGENT_DOCKER_NETWORK — wrapping the daemon's own message surfaces
		// that instead of masking it as a pull failure.
		return "", fmt.Errorf("create agent container: %w", err)
	}

	if err := r.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		// Best-effort cleanup of the created-but-unstarted container.
		_ = r.removeContainer(resp.ID)
		return "", fmt.Errorf("start agent container: %w", err)
	}
	return resp.ID, nil
}

// isImageNotFound reports whether a ContainerCreate error means the image
// itself is missing (so a pull is worth trying), as opposed to another
// NotFound resource. The daemon returns a NotFound-typed error for both a
// missing image AND a missing network (e.g. a misspelled
// AGENT_DOCKER_NETWORK), so we additionally disambiguate on the message:
// missing images surface as "No such image: <ref>". Anything else (network
// not found, …) is left to bubble up unwrapped, not retried as a pull.
func isImageNotFound(err error, imageRef string) bool {
	if err == nil || !cerrdefs.IsNotFound(err) {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "No such image") || strings.Contains(msg, imageRef)
}

// pullImage pulls ref and drains the progress stream (the pull only
// completes once the response body is fully read).
//
// The pull carries no registry credentials (empty PullOptions): docker
// mode is a single-host convenience targeting public or locally-built
// agent images. A private-registry AGENT_IMAGE must be pre-pulled (or
// otherwise present on the daemon) — the create-then-pull path surfaces a
// clear "could not be pulled" error otherwise.
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

// removeContainer force-removes a container on its own detached, bounded
// context. It is detached (not the caller's ctx) so removal still runs when
// the request/run context is already cancelled, and bounded so a
// slow/unresponsive daemon can't block the caller — and leak its goroutine —
// indefinitely.
func (r *DockerRunner) removeContainer(id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), dockerPingTimeout)
	defer cancel()
	return r.client.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
}

// removalRaceBenign reports whether a removal error just means the container
// is already gone or already being removed by a concurrent remover (the Run
// watcher and Cancel both remove on a cancel, by design — see Cancel). Both
// outcomes are the desired end state, so they are not real failures.
func removalRaceBenign(err error) bool {
	return err == nil || cerrdefs.IsNotFound(err) || cerrdefs.IsConflict(err)
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

// stopAndRemove gracefully stops then removes a container. Used as the
// background cleanup for a caller-cancelled run so the caller doesn't block
// on the stop grace. Both steps run on their own detached, bounded contexts.
func (r *DockerRunner) stopAndRemove(id string) {
	_ = r.gracefulStop(id)
	if err := r.removeContainer(id); !removalRaceBenign(err) {
		apilog.WithFields(apilog.Fields{
			"container": id, "error": err.Error(),
		}).Warn("Failed to remove agent container after cancellation")
	}
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
//   - ctx cancelled → SIGTERM the container (graceful, with the grace
//     window before SIGKILL), remove it, return ctx.Err().
//   - non-zero exit → return an error carrying the agent's last
//     FATAL/ERROR message (extracted from stderr).
//   - clean exit    → return (0, nil).
//
// waitForCleanup controls the ctx-cancel path. When true (the background
// watchRun, which reports failure on its wall-clock timeout) the container
// is stopped + removed SYNCHRONOUSLY before returning, so the caller can't
// mark the run failed while the agent is still running and could race a
// terminal-status write. When false (synchronous callers whose request /
// worker ctx was cancelled — they just unwind and don't report failure)
// cleanup runs in the background so the caller returns promptly instead of
// blocking on the SIGTERM grace period.
func (r *DockerRunner) streamWaitRemove(ctx context.Context, id string, h logHandlers, waitForCleanup bool) (int64, error) {
	statusCh, errCh := r.client.ContainerWait(ctx, id, container.WaitConditionNotRunning)

	tail := newTailBuffer()
	logsDone := make(chan struct{})
	logCtx, logCancel := context.WithCancel(ctx)
	defer logCancel()
	go func() {
		defer close(logsDone)
		r.streamLogs(logCtx, id, h, tail)
	}()

	cancelCleanup := func() {
		if waitForCleanup {
			// Synchronous: wait for the container to actually stop (logs
			// stream during the grace) and be removed before returning, so
			// the caller doesn't report failure with the agent still live.
			_ = r.gracefulStop(id)
			logCancel()
			<-logsDone
			_ = r.removeContainer(id)
			return
		}
		// Background: return promptly; the agent still gets its grace before
		// SIGKILL, and a container left by an early process exit is reaped on
		// next startup.
		logCancel() // stop streaming; the log goroutine unwinds on its own
		go r.stopAndRemove(id)
	}

	select {
	case <-ctx.Done():
		cancelCleanup()
		return 0, ctx.Err()
	case werr := <-errCh:
		if ctx.Err() != nil {
			cancelCleanup()
			return 0, ctx.Err()
		}
		logCancel()
		<-logsDone
		_ = r.removeContainer(id)
		return 0, fmt.Errorf("wait for agent container: %w", werr)
	case status := <-statusCh:
		<-logsDone // let the log stream flush to EOF (container stopped)
		_ = r.removeContainer(id)
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
	// context (detached from the request) so the run outlives the HTTP
	// request (same rationale as the K8s watcher).
	go r.watchRun(id, opts) //nolint:gosec // intentional: the run outlives the request context (same as the K8s watcher)

	return nil
}

// dockerRunWatchTimeout is the wall-clock budget for a backgrounded
// discovery container, from AGENT_JOB_TIMEOUT_HOURS. It is the Docker
// analogue of the K8s Job's ActiveDeadlineSeconds: when it elapses the
// container is force-stopped and the run is failed, so a wedged agent
// cannot run forever. LoadConfig clamps AGENT_JOB_TIMEOUT_HOURS to a
// positive value, so in production the cap is always on; the <= 0 guard
// only protects against a non-positive value passed via a hand-built
// Config (it disables the cap rather than instantly cancelling). A var
// (not const) so tests can shorten it from hours to milliseconds.
var dockerRunWatchTimeout = func(jobTimeoutHours int) time.Duration {
	if jobTimeoutHours <= 0 {
		return 0
	}
	return time.Duration(jobTimeoutHours) * time.Hour
}

// watchRun streams + waits on a backgrounded discovery container and routes
// a non-zero exit (or an exceeded wall-clock budget) to OnFailure. It runs
// detached from any request context because the run outlives the HTTP
// request that started it.
func (r *DockerRunner) watchRun(id string, opts RunOptions) {
	ctx := context.Background()
	cancel := context.CancelFunc(func() {})
	if budget := dockerRunWatchTimeout(r.config.JobTimeoutHours); budget > 0 {
		ctx, cancel = context.WithTimeout(ctx, budget)
	}
	defer cancel()

	// waitForCleanup=true: this watcher reports failure on its wall-clock
	// timeout, so the container must be fully stopped + removed before we do
	// (no racing the agent's terminal-status write).
	_, werr := r.streamWaitRemove(ctx, id, logHandlers{
		logPrefix: fmt.Sprintf("[agent %s] ", opts.RunID),
	}, true)

	// An operator Cancel marked this run before stopping its container, so
	// the resulting exit is a cancellation, not a failure — stay silent
	// (the handler's cancel path owns the terminal status). Mirrors the K8s
	// watcher going quiet after a Job delete. Checked first so it wins over
	// both the exit-error and wall-clock paths.
	if r.consumeCancelled(opts.RunID) {
		apilog.WithField("run_id", opts.RunID).Info("Agent container cancelled; not reporting as a failure")
		return
	}

	if werr == nil {
		apilog.WithField("run_id", opts.RunID).Info("Agent container completed")
		return
	}

	// Wall-clock budget exceeded: streamWaitRemove already force-stopped and
	// removed the container; report the run as failed (matches the K8s
	// ActiveDeadlineSeconds → failed-condition behaviour).
	if ctx.Err() != nil {
		msg := fmt.Sprintf("agent container exceeded the AGENT_JOB_TIMEOUT_HOURS wall-clock budget (%dh)", r.config.JobTimeoutHours)
		apilog.WithFields(apilog.Fields{
			"run_id": opts.RunID, "timeout_hours": r.config.JobTimeoutHours,
		}).Warn(msg)
		if opts.OnFailure != nil {
			opts.OnFailure(opts.RunID, msg)
		}
		return
	}

	errMsg := werr.Error()
	apilog.WithFields(apilog.Fields{
		"run_id": opts.RunID, "error": errMsg,
	}).Warn("Agent container exited with error")
	if opts.OnFailure != nil {
		opts.OnFailure(opts.RunID, errMsg)
	}
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
	// waitForCleanup=false: a cancelled RunSync caller (e.g. the 90s
	// test-connection deadline) unwinds promptly; cleanup runs in the
	// background.
	_, werr := r.streamWaitRemove(ctx, id, logHandlers{
		stdout:    &stdout,
		logPrefix: fmt.Sprintf("[test %s] ", opts.ProjectID),
	}, false)
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

	// waitForCleanup=false: a cancelled caller (worker shutdown) unwinds
	// promptly via ctx.Err(); cleanup runs in the background.
	_, werr := r.streamWaitRemove(ctx, id, logHandlers{
		onStderrLine: opts.OnLogLine,
		logPrefix:    fmt.Sprintf("[agent %s] ", opts.RunID),
	}, false)
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
// cancellation the container is SIGTERM'd (the same signal the K8s
// foreground-delete sends) and removed.
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

	// waitForCleanup=false: a cancelled caller (worker shutdown) unwinds
	// promptly via ctx.Err(); cleanup runs in the background.
	_, werr := r.streamWaitRemove(ctx, id, logHandlers{
		logPrefix: fmt.Sprintf("[validate-doc %s] ", opts.JobID),
	}, false)
	if werr == nil {
		apilog.WithField("job_id", opts.JobID).Info("Agent validate-doc container completed")
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("agent --mode validate-doc: %w", werr)
}

// Cancel stops + removes the discovery container(s) for runID with the
// graceful SIGTERM → grace → SIGKILL sequence (the same termination a K8s
// pod gets on cancel). (Persisting partial results on SIGTERM additionally
// needs agent-side signal handling — see #270.) A run with no live
// container is a no-op (already finished).
//
// The stop+remove runs in the BACKGROUND: an agent image that traps/delays
// SIGTERM could otherwise hold the calling handler for the full grace
// (~dockerStopGraceSeconds), which exceeds the HTTP server WriteTimeout and
// would tie up the request goroutine. markCancelled (set synchronously
// before returning) lets the watcher treat the resulting exit as a
// cancellation rather than a failure, and the handler's own runRepo.Cancel
// is the authoritative terminal-status write — so returning before the
// container is fully gone is safe. Cancel does the removal itself (not only
// via the Run watcher), so a run is still cleaned up when the watcher is
// gone after an API restart/crash; double-removes are idempotent (NotFound).
func (r *DockerRunner) Cancel(ctx context.Context, runID string) error {
	f := filters.NewArgs()
	f.Add("label", "app="+dockerAgentLabel)
	f.Add("label", "run-id="+runID)

	// Default (All:false) lists only running containers — exactly what we
	// want to stop. An already-exited container the background watcher is
	// still cleaning up is not our concern (and stopping it would be a
	// wasted no-op).
	list, err := r.client.ContainerList(ctx, container.ListOptions{Filters: f})
	if err != nil {
		return fmt.Errorf("list agent containers for cancel: %w", err)
	}
	if len(list) == 0 {
		return nil // not running (already finished or never started)
	}

	// Mark BEFORE stopping so the background watchRun (which observes the
	// resulting exit) treats it as a cancellation, not a failure.
	r.markCancelled(runID)

	for _, c := range list {
		apilog.WithFields(apilog.Fields{
			"run_id": runID, "container": c.ID,
		}).Info("Stopping agent container (discovery cancelled)")
		// Detached so the grace period can't block the request handler.
		go r.stopAndRemove(c.ID)
	}
	return nil
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

// maxLogLineBytes caps how much a single un-terminated log line may buffer
// before lineWriter force-emits it, so a pathological newline-less stream
// can't grow the buffer without bound (mirrors the subprocess runner's
// scanner max-token limit). 1 MiB comfortably holds the agent's largest
// zap JSON lines.
const maxLogLineBytes = 1 << 20

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
			// No newline yet. Bound memory: if the pending line exceeds the
			// cap, emit what we have and reset rather than buffering forever.
			if len(w.buf) >= maxLogLineBytes {
				w.onLine(w.buf)
				w.buf = w.buf[:0]
			}
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
