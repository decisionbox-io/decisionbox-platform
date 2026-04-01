// Package ecommerce implements the ecommerce domain pack for DecisionBox.
// It registers itself as "ecommerce" via init() so services can select it
// based on the project's domain field.
//
// This pack provides:
//   - AI Discovery: analysis areas, prompts, and profile schemas for ecommerce
//   - Categories: multi_category (multi-category online stores)
//
// Usage:
//
//	import _ "github.com/decisionbox-io/decisionbox/domain-packs/ecommerce/go"
//	// Then: domainpack.Get("ecommerce")
package ecommerce

import (
	"github.com/decisionbox-io/decisionbox/libs/go-common/domainpack"
)

func init() {
	domainpack.Register("ecommerce", NewPack())
}

// EcommercePack implements domainpack.Pack and domainpack.DiscoveryPack
// for the ecommerce domain.
type EcommercePack struct{}

// NewPack creates a new ecommerce domain pack.
func NewPack() *EcommercePack {
	return &EcommercePack{}
}

func (p *EcommercePack) Name() string { return "ecommerce" }
