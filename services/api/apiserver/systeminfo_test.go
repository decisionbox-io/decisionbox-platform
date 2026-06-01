package apiserver

import (
	"context"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/systeminfo"
)

func TestRegisterInProcessComponents(t *testing.T) {
	systeminfo.ResetForTest()
	t.Cleanup(systeminfo.ResetForTest)

	registerInProcessComponents()

	got := systeminfo.Collect(context.Background())
	byName := map[string]systeminfo.Descriptor{}
	for _, d := range got {
		byName[d.Name] = d
	}

	api, ok := byName["API"]
	if !ok || api.Kind != systeminfo.KindService {
		t.Fatalf("API service not registered: %+v", got)
	}

	for _, name := range []string{"Schema indexing", "Validation jobs"} {
		w, ok := byName[name]
		if !ok {
			t.Fatalf("%s worker not registered: %+v", name, got)
		}
		if w.Kind != systeminfo.KindWorker {
			t.Errorf("%s kind = %q, want worker", name, w.Kind)
		}
		if w.RunsIn != "API" {
			t.Errorf("%s runs_in = %q, want API", name, w.RunsIn)
		}
		if w.Note == "" {
			t.Errorf("%s must carry an explanatory note (not independently versioned)", name)
		}
		// A worker shares its parent image version.
		if w.Version != api.Version {
			t.Errorf("%s version = %q, want API version %q", name, w.Version, api.Version)
		}
	}
}

func TestRegisterAgentComponent(t *testing.T) {
	systeminfo.ResetForTest()
	t.Cleanup(systeminfo.ResetForTest)

	registerAgentComponent("ghcr.io/decisionbox-io/decisionbox-agent:v1.2.3")

	got := systeminfo.Collect(context.Background())
	if len(got) != 1 {
		t.Fatalf("want 1 descriptor, got %d: %+v", len(got), got)
	}
	agent := got[0]
	if agent.Name != "Agent" || agent.Kind != systeminfo.KindService {
		t.Fatalf("unexpected agent descriptor: %+v", agent)
	}
	if agent.Version != "v1.2.3" {
		t.Errorf("version = %q, want v1.2.3", agent.Version)
	}
	if agent.Note == "" {
		t.Errorf("agent must carry a clarifying note about being a Job/subprocess")
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
