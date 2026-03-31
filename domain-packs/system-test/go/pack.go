// Package systemtest implements the system-test domain pack for DecisionBox.
// It registers itself as "system-test" via init() ONLY when the
// DECISIONBOX_ENABLE_SYSTEM_TEST environment variable is set to "true".
//
// This is a diagnostic domain pack — not an industry pack. It instructs
// the agent to systematically validate warehouse connectivity, schema
// discovery, data type mapping, and SQL dialect support.
//
// Usage:
//
//	import _ "github.com/decisionbox-io/decisionbox/domain-packs/system-test/go"
//	// Set DECISIONBOX_ENABLE_SYSTEM_TEST=true
//	// Then: domainpack.Get("system-test")
package systemtest

import (
	"os"

	"github.com/decisionbox-io/decisionbox/libs/go-common/domainpack"
)

func init() {
	if os.Getenv("DECISIONBOX_ENABLE_SYSTEM_TEST") == "true" {
		domainpack.Register("system-test", NewPack())
	}
}

// SystemTestPack implements domainpack.Pack and domainpack.DiscoveryPack
// for warehouse validation and data profiling.
type SystemTestPack struct{}

// NewPack creates a new system-test domain pack.
func NewPack() *SystemTestPack {
	return &SystemTestPack{}
}

func (p *SystemTestPack) Name() string { return "system-test" }
