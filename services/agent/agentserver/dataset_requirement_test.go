package agentserver

import (
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// stubProvider satisfies the registry's factory signature without doing
// anything — registration only needs a non-nil factory.
func stubFactory(gowarehouse.ProviderConfig) (gowarehouse.Provider, error) { return nil, nil }

func init() {
	gowarehouse.RegisterWithMeta("test-tabular-source", stubFactory, gowarehouse.ProviderMeta{
		Name: "Tabular test source",
	})
	gowarehouse.RegisterWithMeta("test-cube-source", stubFactory, gowarehouse.ProviderMeta{
		Name:       "Cube test source",
		Capability: gowarehouse.Capability{Shape: gowarehouse.ShapeCube},
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
		{name: "tabular source needs a dataset", provider: "test-tabular-source", want: true},
		{name: "cube source does not", provider: "test-cube-source", want: false},
		{name: "unregistered slug still needs one", provider: "not-registered", want: true},
		{name: "empty slug still needs one", provider: "", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := requiresDataset(tt.provider); got != tt.want {
				t.Errorf("requiresDataset(%q) = %v, want %v", tt.provider, got, tt.want)
			}
		})
	}
}
