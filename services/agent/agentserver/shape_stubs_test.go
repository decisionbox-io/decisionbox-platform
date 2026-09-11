package agentserver

import (
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// Shape-declaring stub providers for this package's tests. Registration only
// needs a non-nil factory; nothing here is ever constructed.
//
// They live in their own file because more than one test reads them, and the
// registry is process-global: a package whose stubs are registered from
// whichever test file happened to need them first breaks the moment that file
// is the one that moves.

func shapeStubFactory(gowarehouse.ProviderConfig) (gowarehouse.Provider, error) { return nil, nil }

func init() {
	gowarehouse.RegisterWithMeta("test-tabular-source", shapeStubFactory, gowarehouse.ProviderMeta{
		Name: "Tabular test source",
	})
	gowarehouse.RegisterWithMeta("test-cube-source", shapeStubFactory, gowarehouse.ProviderMeta{
		Name:       "Cube test source",
		Capability: gowarehouse.Capability{Shape: gowarehouse.ShapeCube},
	})
}
