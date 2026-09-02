package queryexec

import (
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// Capability-declaring stub providers for this package's tests.
//
// Registered from init() rather than from a test body, because the registry is
// process-global and refuses a duplicate slug by panicking — a registration
// inside a test passes once and takes the package down under `go test -count=2`
// or any repeat run. Nothing here is ever constructed; registration only needs
// a non-nil factory.

func capabilityStubFactory(gowarehouse.ProviderConfig) (gowarehouse.Provider, error) {
	return nil, nil
}

func init() {
	// A source whose queries are its own request format. Declaring a
	// QueryLanguage in the capability descriptor is how a provider says that,
	// and it is the statement the tenant-filter guard reads.
	gowarehouse.RegisterWithMeta("queryexec-native-probe", capabilityStubFactory, gowarehouse.ProviderMeta{
		Name:       "Native probe",
		Capability: gowarehouse.Capability{QueryLanguage: "Probe Request (JSON)"},
	})
	// A SQL warehouse: a dialect, and no query language of its own.
	gowarehouse.RegisterWithMeta("queryexec-sql-probe", capabilityStubFactory, gowarehouse.ProviderMeta{
		Name:    "SQL probe",
		Dialect: "postgresql",
	})
}
