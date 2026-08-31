package warehouse

import "testing"

// anchoringProbe registers a provider with a given CanAnchor value under a
// unique slug, so these tests exercise the real registry rather than a stub.
func anchoringProbe(t *testing.T, slug string, canAnchor bool) string {
	t.Helper()
	RegisterWithMeta(slug, func(ProviderConfig) (Provider, error) {
		return &mockWarehouseProvider{}, nil
	}, ProviderMeta{
		Name:       slug,
		Dialect:    "Probe SQL",
		Capability: Capability{CanAnchor: Anchoring(canAnchor)},
	})
	return slug
}

func TestEffectiveAnchoring(t *testing.T) {
	anchor := anchoringProbe(t, "probe_anchor_yes", true)
	enrich := anchoringProbe(t, "probe_anchor_no", false)

	tests := []struct {
		name     string
		provider string
		override *bool
		want     bool
	}{
		{"anchoring provider, no override", anchor, nil, true},
		{"anchoring provider demoted", anchor, Anchoring(false), false},
		{"anchoring provider explicitly kept", anchor, Anchoring(true), true},

		// The ceiling: a provider that cannot anchor stays non-anchoring
		// whatever the datasource says. Promotion would assert something about
		// the data that is not true.
		{"non-anchoring provider, no override", enrich, nil, false},
		{"non-anchoring provider cannot be promoted", enrich, Anchoring(true), false},
		{"non-anchoring provider demoted stays demoted", enrich, Anchoring(false), false},

		// Unregistered providers resolve to anchoring — see the doc comment.
		{"unregistered provider", "probe_never_registered", nil, true},
		{"unregistered provider demoted", "probe_never_registered", Anchoring(false), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveAnchoring(tc.provider, tc.override); got != tc.want {
				t.Errorf("EffectiveAnchoring(%q, %v) = %v, want %v", tc.provider, tc.override, got, tc.want)
			}
		})
	}
}

// TestAnchoringOverrideAllowed pins that promotion is refused rather than
// silently ignored. Storing a value that does nothing would leave the user
// believing they had changed something.
func TestAnchoringOverrideAllowed(t *testing.T) {
	anchor := anchoringProbe(t, "probe_allow_yes", true)
	enrich := anchoringProbe(t, "probe_allow_no", false)

	if !AnchoringOverrideAllowed(anchor, true) {
		t.Error("keeping an anchoring provider anchoring must be allowed")
	}
	if !AnchoringOverrideAllowed(anchor, false) {
		t.Error("demoting an anchoring provider must be allowed")
	}
	if AnchoringOverrideAllowed(enrich, true) {
		t.Error("promoting a non-anchoring provider must be refused")
	}
	if !AnchoringOverrideAllowed(enrich, false) {
		t.Error("demotion is always legal, including for a provider that already cannot anchor")
	}
	if !AnchoringOverrideAllowed("probe_never_registered", true) {
		t.Error("an unregistered provider must not be treated as non-anchoring")
	}
}
