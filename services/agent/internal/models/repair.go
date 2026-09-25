package models

import gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"

// InsightRepair and its outcomes live in libs/go-common/models for the reason the
// declared-claim types do: the API serves a stored insight through its own mirror
// and cannot import this package. See quantifier.go.
type InsightRepair = gomodels.InsightRepair

// Repair outcomes, re-exported so callers in this service keep one import.
const (
	RepairRepaired     = gomodels.RepairRepaired
	RepairClaimDropped = gomodels.RepairClaimDropped
	RepairUnrepaired   = gomodels.RepairUnrepaired
	RepairWithdrawn    = gomodels.RepairWithdrawn
)
