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

// init registers the components the API process knows about itself: the
// API service (its own ldflags-stamped version) and the two background
// workers that run in-process inside it. The Agent is registered
// separately from Run() because its version comes from the configured
// agent image, which is runtime config (see registerAgentComponent).
//
// Registering here mirrors the codebase's other init()-time registries;
// out-of-tree builds add their own components the same way without
// touching the endpoint or the dashboard.
func init() {
	registerInProcessComponents()
}

// registerInProcessComponents records the API and its two in-process
// workers. Split out of init() so it is exercisable in isolation.
func registerInProcessComponents() {
	systeminfo.Register(systeminfo.Descriptor{
		Name:      "API",
		Kind:      systeminfo.KindService,
		Version:   goversion.Version,
		Commit:    goversion.Commit,
		BuildDate: goversion.BuildDate,
	})
	systeminfo.Register(systeminfo.Descriptor{
		Name:      "Schema indexing",
		Kind:      systeminfo.KindWorker,
		RunsIn:    "API",
		Version:   goversion.Version,
		Commit:    goversion.Commit,
		BuildDate: goversion.BuildDate,
		Note:      workerSharesImageNote,
	})
	systeminfo.Register(systeminfo.Descriptor{
		Name:      "Validation jobs",
		Kind:      systeminfo.KindWorker,
		RunsIn:    "API",
		Version:   goversion.Version,
		Commit:    goversion.Commit,
		BuildDate: goversion.BuildDate,
		Note:      workerSharesImageNote,
	})
}

// registerAgentComponent records the agent in the system inventory using
// the image the API is configured to launch. The agent is not a live
// service the API can introspect — it runs as a subprocess (dev) or a
// Kubernetes Job (prod) — so its reported version is the tag of the
// configured image, with a note making that explicit.
func registerAgentComponent(agentImage string) {
	systeminfo.Register(systeminfo.Descriptor{
		Name:    "Agent",
		Kind:    systeminfo.KindService,
		Version: imageTag(agentImage),
		Note: fmt.Sprintf(
			"agent image the API is configured to launch (%s); runs as a subprocess (dev) or Kubernetes Job (prod), not a live service",
			agentImage,
		),
	})
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
