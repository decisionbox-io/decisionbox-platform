package models

import (
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// AnyAnchors reports whether at least one of these datasources can carry a
// project by itself — a system of record rather than a system of observation.
//
// It is the question "is this project answerable at all?", asked of the
// datasource set rather than of any one source. A source that declares it
// cannot anchor is not defective: it is worth having, but only alongside
// something it can be correlated against. A project holding nothing else can
// produce analysis that restates what the source's own reporting UI already
// shows, and it can produce it confidently, which is why this is refused at
// the point of configuration rather than left to disappoint at the point of
// use.
//
// An empty set returns false, and callers must decide separately whether an
// empty set is a problem: a project with no datasources yet is normal — it is
// how every project starts — while a run over one is not.
//
// Resolution goes through warehouse.EffectiveAnchoring, so the provider's
// declaration is the ceiling and the per-datasource override may only demote.
func AnyAnchors(whs []WarehouseConfig) bool {
	for _, wh := range whs {
		if gowarehouse.EffectiveAnchoring(wh.Provider, wh.Anchoring) {
			return true
		}
	}
	return false
}
