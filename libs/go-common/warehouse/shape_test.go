package warehouse

import (
	"testing"
)

// shapeStubFactory satisfies the registry's factory signature without doing
// anything — registration only needs a non-nil factory.
func shapeStubFactory(ProviderConfig) (Provider, error) { return nil, nil }

func init() {
	RegisterWithMeta("shape-test-tabular", shapeStubFactory, ProviderMeta{
		Name: "Tabular test source",
	})
	RegisterWithMeta("shape-test-cube", shapeStubFactory, ProviderMeta{
		Name:       "Cube test source",
		Capability: Capability{Shape: ShapeCube},
	})
}

// TestRequiresDataset decides whether a datasource can be configured at all.
// A cube-shaped source has no datasets, so demanding one makes it
// unconfigurable — or forces an operator to invent a value that means nothing
// and then wonder why it is ignored.
//
// The default matters as much as the cube case: an unregistered slug must
// still require a dataset, because that is what every provider needed before
// shape existed and a wrong default here would silently drop the check for
// real warehouses.
func TestRequiresDataset(t *testing.T) {
	tests := []struct {
		name, provider string
		want           bool
	}{
		{name: "tabular source needs a dataset", provider: "shape-test-tabular", want: true},
		{name: "cube source does not", provider: "shape-test-cube", want: false},
		{name: "unregistered slug still needs one", provider: "not-registered", want: true},
		{name: "empty slug still needs one", provider: "", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RequiresDataset(tt.provider); got != tt.want {
				t.Errorf("RequiresDataset(%q) = %v, want %v", tt.provider, got, tt.want)
			}
		})
	}
}
