package apiserver

import (
	"fmt"
	"strings"

	"github.com/decisionbox-io/decisionbox/libs/go-common/systeminfo"
	goversion "github.com/decisionbox-io/decisionbox/libs/go-common/version"
)

// workerSharesImageNote explains that an in-process worker is not a
// separately-versioned artifact — it ships inside the API image and
// reports that image's version. Without this a reader could mistake the
// worker for an independently-released component.
const workerSharesImageNote = "runs in-process inside the API service; shares its image version"

// init registers the components that are always present when the API
// process runs: the API service itself (its ldflags-stamped version) and
// the validation-jobs worker (started unconditionally). Components that
// start conditionally are registered from Run() when they actually start:
// the schema-index worker (Qdrant-gated, registerSchemaIndexComponent)
// and the Agent (registerAgentComponent, version depends on runner mode).
// This keeps GET /api/v1/system honest — it never lists a worker that was
// not started.
//
// Registering here mirrors the codebase's other init()-time registries;
// out-of-tree builds add their own components the same way without
// touching the endpoint or the dashboard.
func init() {
	registerAlwaysOnComponents()
}

// registerAlwaysOnComponents records the API and the validation-jobs
// worker, both present whenever the API runs. Split out of init() so it
// is exercisable in isolation.
func registerAlwaysOnComponents() {
	systeminfo.Register(systeminfo.Descriptor{
		Name:      "API",
		Kind:      systeminfo.KindService,
		Version:   goversion.Version,
		Commit:    goversion.Commit,
		BuildDate: goversion.BuildDate,
	})
	systeminfo.Register(workerDescriptor("Validation jobs"))
}

// registerSchemaIndexComponent records the schema-index worker. Called
// from Run() only when the worker actually starts (Qdrant configured),
// so a Qdrant-less deployment does not falsely report it as running.
func registerSchemaIndexComponent() {
	systeminfo.Register(workerDescriptor("Schema indexing"))
}

// workerDescriptor builds the descriptor for an in-process API worker —
// it shares the API image's version and carries the explanatory note.
func workerDescriptor(name string) systeminfo.Descriptor {
	return systeminfo.Descriptor{
		Name:      name,
		Kind:      systeminfo.KindWorker,
		RunsIn:    "API",
		Version:   goversion.Version,
		Commit:    goversion.Commit,
		BuildDate: goversion.BuildDate,
		Note:      workerSharesImageNote,
	}
}

// runnerModeKubernetes / runnerModeDocker mirror the runner package's
// RUNNER_MODE values that spawn the agent from AGENT_IMAGE. In any other
// mode (subprocess, the default) the runner ignores AGENT_IMAGE and runs
// the agent binary bundled in the API image.
const (
	runnerModeKubernetes = "kubernetes"
	runnerModeDocker     = "docker"
)

// registerAgentComponent records the agent in the system inventory. The
// agent is never a live service the API can introspect, so what it
// reports depends on how the API launches it:
//
//   - kubernetes mode: each run is a Job created from AGENT_IMAGE, so the
//     version is that image's tag.
//   - docker mode: each run is a container created from AGENT_IMAGE, so the
//     version is that image's tag (same as kubernetes).
//   - subprocess mode (default): the API execs the agent binary bundled
//     in its own image — built with the same version stamp — so AGENT_IMAGE
//     is unused and the accurate version is the API's own build metadata.
func registerAgentComponent(mode, agentImage string) {
	if launchedFromImage(mode) {
		runtime := "Kubernetes Job"
		if strings.EqualFold(mode, runnerModeDocker) {
			runtime = "Docker container"
		}
		systeminfo.Register(systeminfo.Descriptor{
			Name:    "Agent",
			Kind:    systeminfo.KindService,
			Version: imageTag(agentImage),
			Note: fmt.Sprintf(
				"agent image the API launches as a %s (%s); not a live service",
				runtime, agentImage,
			),
		})
		return
	}
	systeminfo.Register(systeminfo.Descriptor{
		Name:      "Agent",
		Kind:      systeminfo.KindService,
		Version:   goversion.Version,
		Commit:    goversion.Commit,
		BuildDate: goversion.BuildDate,
		Note:      "agent binary bundled in the API image, run as a subprocess; shares the API image version",
	})
}

// launchedFromImage reports whether the runner mode spawns the agent from
// AGENT_IMAGE (docker / kubernetes) rather than the bundled binary
// (subprocess / default).
func launchedFromImage(mode string) bool {
	return strings.EqualFold(mode, runnerModeKubernetes) || strings.EqualFold(mode, runnerModeDocker)
}

// imageTag extracts the tag from a container image reference, returning
// "unknown" when the reference carries no tag. It is digest- and
// registry-port-aware: the final ":" only delimits a tag when it appears
// after the last "/" (otherwise it is a registry port), and a "@sha256:"
// digest is stripped first.
func imageTag(ref string) string {
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		ref = ref[:at]
	}
	colon := strings.LastIndex(ref, ":")
	slash := strings.LastIndex(ref, "/")
	if colon > slash && colon < len(ref)-1 {
		return ref[colon+1:]
	}
	return "unknown"
}
