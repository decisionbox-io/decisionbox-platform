package warehouse

import "testing"

// The hash keys every schema cache in every deployment. Changing its byte
// layout invalidates all of them at once and forces a full re-index of every
// project — which is a decision, made by bumping the version prefix, and never
// something to discover from a support ticket.
//
// These digests were captured from the implementation as it stood before it
// moved here, so they pin the move as a move. A failure means either the
// layout changed or the move was not faithful; both need an explicit answer,
// and neither should be resolved by updating the constants.
func TestConfigHash_IsUnchangedByTheMove(t *testing.T) {
	tests := []struct {
		name                          string
		provider, projectID, location string
		datasets                      []string
		filterField, filterValue      string
		config                        map[string]string
		want                          string
	}{
		{
			name: "an empty config still hashes",
			want: "110d19fb9b536d97e3ada217a4a0bb7cf7ee6a1261df1df1068bc1f110b55d8d",
		},
		{
			name:     "every field, with unsorted datasets and config keys",
			provider: "bigquery", projectID: "p", location: "EU",
			datasets:    []string{"b", "a"},
			filterField: "tenant", filterValue: "t1",
			config: map[string]string{"z": "1", "a": "2"},
			want:   "22526735026996bd90eb4b0ced403cf39b006ad8197636ba189ca4d398f57496",
		},
		{
			name:     "a cross-project read is stamped",
			provider: "bigquery", projectID: "p",
			config: map[string]string{"data_project_id": "other"},
			want:   "26815f78d8e167d9509fa66a5c11ea7f0a258db82a967cc7115d19e521f14885",
		},
		{
			name:     "a data project equal to the project is not a cross-project read",
			provider: "bigquery", projectID: "p",
			config: map[string]string{"data_project_id": "p"},
			want:   "ea62b23401e40635bf7198f0ee9a67bdeeb0dfb94d72011e3f4da010c40af2b0",
		},
		{
			name:     "a source with no datasets at all",
			provider: "ga4",
			config:   map[string]string{"property_id": "properties/123"},
			want:     "0a2606a2a2e5a9cc644bd968dc31ba3aa351b017814c9d7fe84a9f2448b2c6c4",
		},
		{
			name:     "nil slices and maps",
			provider: "postgres",
			want:     "ff62b13fd3be92302f1f83a8865ef24e8a2ea69a46b5960320fa0a6956aa8730",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ConfigHash(tt.provider, tt.projectID, tt.location, tt.datasets, tt.filterField, tt.filterValue, tt.config)
			if got != tt.want {
				t.Errorf("ConfigHash = %s, want %s — every cached schema in every deployment is keyed by this", got, tt.want)
			}
		})
	}
}

// Canonicalisation, stated as behaviour rather than as a digest: the same
// configuration written two ways is one configuration, or an edit that
// reordered a list would silently re-index a project.
func TestConfigHash_IsIndependentOfOrder(t *testing.T) {
	a := ConfigHash("bq", "p", "EU", []string{"a", "b"}, "f", "v", map[string]string{"x": "1", "y": "2"})
	b := ConfigHash("bq", "p", "EU", []string{"b", "a"}, "f", "v", map[string]string{"y": "2", "x": "1"})
	if a != b {
		t.Errorf("order changed the hash: %s vs %s", a, b)
	}
}

// And the other direction: anything that could change what discovery returns
// has to change the hash, or a stale cache would be read as current.
func TestConfigHash_ChangesWithEveryInput(t *testing.T) {
	base := ConfigHash("bq", "p", "EU", []string{"a"}, "f", "v", map[string]string{"k": "1"})
	for name, got := range map[string]string{
		"provider":     ConfigHash("pg", "p", "EU", []string{"a"}, "f", "v", map[string]string{"k": "1"}),
		"project":      ConfigHash("bq", "q", "EU", []string{"a"}, "f", "v", map[string]string{"k": "1"}),
		"location":     ConfigHash("bq", "p", "US", []string{"a"}, "f", "v", map[string]string{"k": "1"}),
		"datasets":     ConfigHash("bq", "p", "EU", []string{"z"}, "f", "v", map[string]string{"k": "1"}),
		"filter field": ConfigHash("bq", "p", "EU", []string{"a"}, "g", "v", map[string]string{"k": "1"}),
		"filter value": ConfigHash("bq", "p", "EU", []string{"a"}, "f", "w", map[string]string{"k": "1"}),
		"config value": ConfigHash("bq", "p", "EU", []string{"a"}, "f", "v", map[string]string{"k": "2"}),
	} {
		if got == base {
			t.Errorf("changing the %s did not change the hash", name)
		}
	}
}
