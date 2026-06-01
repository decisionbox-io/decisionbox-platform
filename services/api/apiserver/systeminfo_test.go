package apiserver

import (
	"context"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/systeminfo"
	goversion "github.com/decisionbox-io/decisionbox/libs/go-common/version"
)

func TestRegisterAlwaysOnComponents(t *testing.T) {
	systeminfo.ResetForTest()
	t.Cleanup(systeminfo.ResetForTest)

	registerAlwaysOnComponents()

	got := systeminfo.Collect(context.Background())
	byName := map[string]systeminfo.Descriptor{}
	for _, d := range got {
		byName[d.Name] = d
	}

	api, ok := byName["API"]
	if !ok || api.Kind != systeminfo.KindService {
		t.Fatalf("API service not registered: %+v", got)
	}

	// Validation jobs always starts → registered here.
	w, ok := byName["Validation jobs"]
	if !ok {
		t.Fatalf("Validation jobs worker not registered: %+v", got)
	}
	if w.Kind != systeminfo.KindWorker || w.RunsIn != "API" || w.Note == "" {
		t.Errorf("Validation jobs descriptor wrong: %+v", w)
	}
	if w.Version != api.Version {
		t.Errorf("worker version = %q, want API version %q", w.Version, api.Version)
	}

	// Schema indexing is Qdrant-gated — must NOT be registered here
	// (it is added from Run() only when the worker starts).
	if _, present := byName["Schema indexing"]; present {
		t.Errorf("Schema indexing must not be registered unconditionally: %+v", got)
	}
}

func TestRegisterSchemaIndexComponent(t *testing.T) {
	systeminfo.ResetForTest()
	t.Cleanup(systeminfo.ResetForTest)

	registerSchemaIndexComponent()

	got := systeminfo.Collect(context.Background())
	if len(got) != 1 {
		t.Fatalf("want 1 descriptor, got %d: %+v", len(got), got)
	}
	w := got[0]
	if w.Name != "Schema indexing" || w.Kind != systeminfo.KindWorker || w.RunsIn != "API" || w.Note == "" {
		t.Fatalf("Schema indexing descriptor wrong: %+v", w)
	}
	if w.Version != goversion.Version {
		t.Errorf("version = %q, want API version %q", w.Version, goversion.Version)
	}
}

func TestRegisterAgentComponent_KubernetesMode(t *testing.T) {
	systeminfo.ResetForTest()
	t.Cleanup(systeminfo.ResetForTest)

	registerAgentComponent("kubernetes", "ghcr.io/decisionbox-io/decisionbox-agent:v1.2.3")

	got := systeminfo.Collect(context.Background())
	if len(got) != 1 {
		t.Fatalf("want 1 descriptor, got %d: %+v", len(got), got)
	}
	agent := got[0]
	if agent.Name != "Agent" || agent.Kind != systeminfo.KindService {
		t.Fatalf("unexpected agent descriptor: %+v", agent)
	}
	// K8s mode reports the configured image tag.
	if agent.Version != "v1.2.3" {
		t.Errorf("version = %q, want v1.2.3 (image tag)", agent.Version)
	}
	if agent.Note == "" {
		t.Errorf("agent must carry a clarifying note about being a Job")
	}
}

func TestRegisterAgentComponent_SubprocessMode(t *testing.T) {
	systeminfo.ResetForTest()
	t.Cleanup(systeminfo.ResetForTest)

	// Subprocess mode (default) ignores AGENT_IMAGE — the bundled binary
	// runs, so the version must be the API's own stamped build, not the
	// image tag.
	registerAgentComponent("subprocess", "ghcr.io/decisionbox-io/decisionbox-agent:v1.2.3")

	got := systeminfo.Collect(context.Background())
	if len(got) != 1 {
		t.Fatalf("want 1 descriptor, got %d: %+v", len(got), got)
	}
	agent := got[0]
	if agent.Version != goversion.Version {
		t.Errorf("version = %q, want bundled binary version %q (not the image tag)", agent.Version, goversion.Version)
	}
	if agent.Version == "v1.2.3" {
		t.Errorf("subprocess mode must not report the AGENT_IMAGE tag")
	}
	if agent.Note == "" {
		t.Errorf("agent must carry a clarifying note about the bundled binary")
	}
}

func TestRegisterAgentComponent_EmptyModeDefaultsToSubprocess(t *testing.T) {
	systeminfo.ResetForTest()
	t.Cleanup(systeminfo.ResetForTest)

	// runner.LoadConfig defaults RUNNER_MODE to "subprocess", but guard
	// the empty-string case too (treated as non-kubernetes → bundled binary).
	registerAgentComponent("", "ghcr.io/decisionbox-io/decisionbox-agent:v9.9.9")

	got := systeminfo.Collect(context.Background())
	if len(got) != 1 || got[0].Version != goversion.Version {
		t.Fatalf("empty mode should report bundled binary version, got: %+v", got)
	}
}

func TestImageTag(t *testing.T) {
	cases := []struct {
		ref  string
		want string
	}{
		{"ghcr.io/decisionbox-io/decisionbox-agent:latest", "latest"},
		{"ghcr.io/decisionbox-io/decisionbox-agent:v0.10.0", "v0.10.0"},
		{"decisionbox-agent:1.2.3", "1.2.3"},
		{"decisionbox-agent", "unknown"}, // no tag
		{"localhost:5000/decisionbox-agent", "unknown"}, // colon is a registry port, not a tag
		{"localhost:5000/decisionbox-agent:dev", "dev"},
		{"ghcr.io/decisionbox-io/decisionbox-agent@sha256:abc123", "unknown"}, // digest, no tag
		{"ghcr.io/decisionbox-io/decisionbox-agent:1.0@sha256:abc123", "1.0"}, // tag + digest
		{"agent:", "unknown"}, // trailing colon, empty tag
		{"", "unknown"},
	}
	for _, c := range cases {
		if got := imageTag(c.ref); got != c.want {
			t.Errorf("imageTag(%q) = %q, want %q", c.ref, got, c.want)
		}
	}
}
